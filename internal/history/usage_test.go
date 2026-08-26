package history

import (
	"path/filepath"
	"strings"
	"testing"

	"flatline/internal/eventstore"
)

// Every record below is a synthetic fixture. It reproduces the wire shape the
// two harnesses write without copying any real user transcript.

// claudeUsageBlock is one assistant record's usage, in Claude Code's shape.
func claudeUsageBlock(input, cacheRead, cacheWrite, output, thinking int) map[string]any {
	return map[string]any{
		"input_tokens": input, "cache_read_input_tokens": cacheRead,
		"cache_creation_input_tokens": cacheWrite, "output_tokens": output,
		"output_tokens_details": map[string]any{"thinking_tokens": thinking},
	}
}

func TestClaudeUsageIsCountedOncePerMessage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "usage-claude.jsonl")
	// The harness writes the same message id out again on every follow-up
	// record, and a streaming model's usage block grows as it writes: counting
	// each record multiplies the session's tokens, and keeping the first
	// undercounts it. The last record for a message id is its final count.
	partial := claudeUsageBlock(1000, 200, 50, 30, 2)
	usage := claudeUsageBlock(1000, 200, 50, 300, 20)
	writeJSONLines(t, path, []map[string]any{
		{"type": "user", "sessionId": "usage-1", "timestamp": "2026-08-20T09:00:00Z", "cwd": root,
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "please do the thing"}}}},
		{"type": "assistant", "sessionId": "usage-1", "timestamp": "2026-08-20T09:00:10Z", "cwd": root,
			"message": map[string]any{"id": "msg-1", "role": "assistant", "model": "fixture-model",
				"stop_reason": "tool_use", "usage": partial, "content": []any{
					map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Write",
						"input": map[string]any{"file_path": filepath.Join(root, "a.txt"), "content": "one\ntwo\nthree"}}}}},
		{"type": "assistant", "sessionId": "usage-1", "timestamp": "2026-08-20T09:00:20Z", "cwd": root,
			"message": map[string]any{"id": "msg-1", "role": "assistant", "model": "fixture-model",
				"stop_reason": "end_turn", "usage": usage, "content": []any{
					map[string]any{"type": "text", "text": "done"}}}},
		{"type": "assistant", "sessionId": "usage-1", "timestamp": "2026-08-20T09:00:30Z", "cwd": root,
			"message": map[string]any{"id": "msg-2", "role": "assistant", "model": "fixture-model",
				"stop_reason": "end_turn", "usage": claudeUsageBlock(500, 0, 0, 100, 0), "content": []any{
					map[string]any{"type": "text", "text": "and again"}}}},
	})

	measured := measureClaude(t, path)
	if measured.Source != eventstore.UsageSourceClaude {
		t.Fatalf("source = %q", measured.Source)
	}
	// msg-1 counted once, at its final numbers (1000+200+50+300), plus msg-2
	// (500+100). The partial record msg-1 opened with is superseded.
	assertMeasured(t, "total_tokens", measured.TotalTokens, 2150)
	assertMeasured(t, "input_tokens", measured.InputTokens, 1500)
	assertMeasured(t, "output_tokens", measured.OutputTokens, 400)
	assertMeasured(t, "cached_input_tokens", measured.CachedInputTokens, 200)
	assertMeasured(t, "cache_write_tokens", measured.CacheWriteTokens, 50)
	assertMeasured(t, "reasoning_tokens", measured.ReasoningTokens, 20)
	// Two message ids closed a turn; the repeated record of msg-1 is not a
	// second turn.
	assertMeasured(t, "assistant_turns", measured.AssistantTurns, 2)
	assertMeasured(t, "user_turns", measured.UserTurns, 1)
	if len(measured.ByModel) != 1 || measured.ByModel[0].Model != "fixture-model" {
		t.Fatalf("by_model = %+v", measured.ByModel)
	}
	assertMeasured(t, "by_model total", measured.ByModel[0].TotalTokens, 2150)
}

func TestClaudeChangeCountsSurviveATruncatedPayload(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "big-write.jsonl")
	// 20 KB of content: well past the 8192-rune bound the stored payload is
	// cut to, so the measurement has to come from the parse, not the payload.
	big := strings.Repeat("a line of content\n", 1200)
	lines := int64(strings.Count(big, "\n") + 1)
	writeJSONLines(t, path, []map[string]any{
		{"type": "assistant", "sessionId": "big-1", "timestamp": "2026-08-20T09:00:00Z", "cwd": root,
			"message": map[string]any{"id": "msg-1", "role": "assistant", "model": "fixture-model",
				"stop_reason": "tool_use", "content": []any{
					map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Write",
						"input": map[string]any{"file_path": filepath.Join(root, "big.txt"), "content": big}}}}},
	})
	if len(big) < 20000 {
		t.Fatalf("fixture is only %d bytes; it has to exceed the payload bound", len(big))
	}
	measured := measureClaude(t, path)
	assertMeasured(t, "lines_added", measured.LinesAdded, lines)
	assertMeasured(t, "files_changed", measured.FilesChanged, 1)
	// No token record at all: the tokens are unrecorded, not zero.
	if measured.Source != eventstore.UsageSourceUnrecorded {
		t.Fatalf("source = %q, want unrecorded", measured.Source)
	}
	if measured.TotalTokens != nil {
		t.Fatalf("total_tokens = %d, want null for a transcript with no usage record", *measured.TotalTokens)
	}
}

func TestCodexTotalsAreTheLastRunningTotalNotTheSum(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "usage-codex.jsonl")
	total := func(input, cached, output, reasoning, all int) map[string]any {
		return map[string]any{"input_tokens": input, "cached_input_tokens": cached,
			"cache_write_input_tokens": 0, "output_tokens": output,
			"reasoning_output_tokens": reasoning, "total_tokens": all}
	}
	writeJSONLines(t, path, []map[string]any{
		{"timestamp": "2026-08-20T09:00:00Z", "type": "session_meta",
			"payload": map[string]any{"id": "codex-usage-1", "cwd": root, "cli_version": "1.0.0"}},
		{"timestamp": "2026-08-20T09:00:01Z", "type": "turn_context",
			"payload": map[string]any{"cwd": root, "model": "codex-fixture"}},
		{"timestamp": "2026-08-20T09:00:05Z", "type": "event_msg", "payload": map[string]any{
			"type": "token_count", "info": map[string]any{
				"total_token_usage":    total(100, 10, 20, 5, 120),
				"last_token_usage":     total(100, 10, 20, 5, 120),
				"model_context_window": 237500}}},
		{"timestamp": "2026-08-20T09:00:20Z", "type": "event_msg", "payload": map[string]any{
			"type": "token_count", "info": map[string]any{
				"total_token_usage":    total(400, 40, 60, 15, 460),
				"last_token_usage":     total(300, 30, 40, 10, 340),
				"model_context_window": 237500}}},
	})

	measured := measureCodex(t, path)
	if measured.Source != eventstore.UsageSourceCodex {
		t.Fatalf("source = %q", measured.Source)
	}
	// The running total is cumulative: the session's tokens come from the last
	// record, not from 120 + 460.
	//
	// Codex counts cached input inside input_tokens, and the stored
	// input_tokens is the input that was not served from cache: 400 - 40. The
	// total is not taken from Codex at all — it is recomputed from the stored
	// components (360 + 40 + 0 + 60), which lands on Codex's own 460.
	assertMeasured(t, "input_tokens", measured.InputTokens, 360)
	assertMeasured(t, "cached_input_tokens", measured.CachedInputTokens, 40)
	assertMeasured(t, "output_tokens", measured.OutputTokens, 60)
	assertMeasured(t, "total_tokens", measured.TotalTokens, 460)
	assertMeasured(t, "context_window", measured.ContextWindow, 237500)
	assertMeasured(t, "assistant_turns", measured.AssistantTurns, 1)
	// The model split comes from the per-turn block, which does add up.
	if len(measured.ByModel) != 1 {
		t.Fatalf("by_model = %+v", measured.ByModel)
	}
	assertMeasured(t, "by_model total", measured.ByModel[0].TotalTokens, 460)
}

func TestCodexPatchIsMeasuredFromTheFullEscapedInput(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "patch-codex.jsonl")
	// Codex writes the patch inside an exec script, so the body arrives as a
	// string literal with its newlines escaped and is far longer than the
	// bound the stored payload is cut to.
	filler := strings.Repeat(`+another added line\n`, 1200)
	script := `const patch = "*** Begin Patch\n` +
		`*** Update File: /repo/a.py\n` +
		`@@\n` +
		` context line\n` +
		`-removed one\n` +
		`-removed two\n` +
		filler +
		`*** Add File: /repo/b.py\n` +
		`+brand new\n` +
		`*** End Patch"`
	writeJSONLines(t, path, []map[string]any{
		{"timestamp": "2026-08-20T09:00:00Z", "type": "session_meta",
			"payload": map[string]any{"id": "codex-patch-1", "cwd": root, "cli_version": "1.0.0"}},
		{"timestamp": "2026-08-20T09:00:02Z", "type": "response_item", "payload": map[string]any{
			"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "exec", "input": script}},
	})
	if len(script) < 20000 {
		t.Fatalf("fixture is only %d bytes; it has to exceed the payload bound", len(script))
	}
	measured := measureCodex(t, path)
	assertMeasured(t, "lines_added", measured.LinesAdded, 1201)
	assertMeasured(t, "lines_removed", measured.LinesRemoved, 2)
	assertMeasured(t, "files_changed", measured.FilesChanged, 2)
}

func TestActiveTimeSkipsIdleGaps(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "idle.jsonl")
	writeJSONLines(t, path, []map[string]any{
		{"type": "user", "sessionId": "idle-1", "timestamp": "2026-08-20T09:00:00Z", "cwd": root,
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "first"}}}},
		// 60 seconds later: inside the idle bound, so it counts.
		{"type": "assistant", "sessionId": "idle-1", "timestamp": "2026-08-20T09:01:00Z", "cwd": root,
			"message": map[string]any{"id": "m1", "role": "assistant", "stop_reason": "end_turn",
				"content": []any{map[string]any{"type": "text", "text": "second"}}}},
		// Two hours later: idle, so the gap is not counted.
		{"type": "user", "sessionId": "idle-1", "timestamp": "2026-08-20T11:01:00Z", "cwd": root,
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "third"}}}},
		{"type": "assistant", "sessionId": "idle-1", "timestamp": "2026-08-20T11:01:30Z", "cwd": root,
			"message": map[string]any{"id": "m2", "role": "assistant", "stop_reason": "end_turn",
				"content": []any{map[string]any{"type": "text", "text": "fourth"}}}},
	})
	measured := measureClaude(t, path)
	assertMeasured(t, "active_ms", measured.ActiveMS, 90_000)
}

func measureClaude(t *testing.T, path string) *eventstore.SessionUsage {
	t.Helper()
	session, _, ok, warning := readClaude(path, assetIndex{}, "")
	if !ok {
		t.Fatalf("readClaude did not produce a session (%s)", warning)
	}
	return requireUsage(t, session)
}

func measureCodex(t *testing.T, path string) *eventstore.SessionUsage {
	t.Helper()
	session, _, ok, warning := readCodex(path, assetIndex{}, "")
	if !ok {
		t.Fatalf("readCodex did not produce a session (%s)", warning)
	}
	return requireUsage(t, session)
}

func requireUsage(t *testing.T, session Session) *eventstore.SessionUsage {
	t.Helper()
	if session.Input.Usage == nil {
		t.Fatal("no measurement was produced")
	}
	if session.Input.ParserVersion != ParserVersion {
		t.Fatalf("parser version = %q, want %q", session.Input.ParserVersion, ParserVersion)
	}
	return session.Input.Usage
}

func assertMeasured(t *testing.T, name string, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is null, want %d", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %d, want %d", name, *got, want)
	}
}
