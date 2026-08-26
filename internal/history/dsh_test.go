package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// writeDSHFixture synthesizes one dsh session file and compresses it the way
// dsh does. Nothing here is copied from a real transcript.
func writeDSHFixture(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "--home-dev-demo--", "session-demo-0001")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create fixture dir: %v", err)
	}
	records := []map[string]any{
		{"type": "session", "id": "session-demo-0001", "cwd": "/home/dev/demo",
			"createdAt": 1786628583011, "version": 0, "agentPreset": "standard", "delegationDepth": 0},
		{"type": "session/title", "seq": 1, "time": 1786628583020,
			"data": map[string]any{"title": "Ship the taskboard", "source": map[string]any{"kind": "fallback"}}},
		{"type": "request/context", "seq": 2, "time": 1786628583030,
			"data": map[string]any{"provider": "demo", "model": "demo-model", "contextWindow": 1000000}},
		{"type": "user/message", "seq": 3, "time": 1786628583040,
			"data": map[string]any{"role": "user", "id": "u1",
				"content": []any{map[string]any{"type": "text", "text": "Ship the taskboard"}}}},
		{"type": "assistant/message", "seq": 4, "time": 1786628584000,
			"data": map[string]any{"turn": 1, "step": 1,
				"message": map[string]any{"role": "assistant", "id": "a1", "content": []any{
					map[string]any{"type": "reasoning", "text": "internal"},
					map[string]any{"type": "text", "text": "Checking the tests."},
					map[string]any{"type": "tool-call", "id": "call_ok", "name": "bash"},
				}},
				"usage": map[string]any{"inputTokens": 1200, "outputTokens": 300, "cacheReadTokens": 500}}},
		{"type": "tool/call", "seq": 5, "time": 1786628584100,
			"data": map[string]any{"turn": 1, "step": 1, "callId": "call_ok", "name": "bash",
				"arguments": `{"command":"go test ./..."}`}},
		{"type": "tool/result", "seq": 6, "time": 1786628584200,
			"data": map[string]any{"turn": 1, "step": 1,
				"message": map[string]any{"role": "user", "id": "r1",
					"source": map[string]any{"kind": "tool", "callId": "call_ok"},
					"content": []any{map[string]any{"type": "tool-result", "toolCallId": "call_ok",
						"isError": false,
						"content": []any{map[string]any{"type": "text", "text": "ok"}}}}}}},
		{"type": "tool/call", "seq": 7, "time": 1786628585000,
			"data": map[string]any{"turn": 1, "step": 2, "callId": "call_bad", "name": "read",
				"arguments": `{"filePath":"/missing"}`}},
		{"type": "tool/result", "seq": 8, "time": 1786628585100,
			"data": map[string]any{"turn": 1, "step": 2,
				"error": map[string]any{"name": "FsError", "code": "FS_STALE_VERSION"},
				"message": map[string]any{"role": "user", "id": "r2",
					"source": map[string]any{"kind": "tool", "callId": "call_bad"},
					"content": []any{map[string]any{"type": "tool-result", "toolCallId": "call_bad",
						"isError": true,
						"content": []any{map[string]any{"type": "text", "text": "stale version"}}}}}}},
		{"type": "assistant/message", "seq": 9, "time": 1786628586000,
			"data": map[string]any{"turn": 1, "step": 3,
				"message": map[string]any{"role": "assistant", "id": "a2", "content": []any{
					map[string]any{"type": "text", "text": "Done."}}},
				"usage": map[string]any{"inputTokens": 800, "outputTokens": 120, "cacheReadTokens": 0}}},
		{"type": "turn/end", "seq": 10, "time": 1786628587000,
			"data": map[string]any{"turn": 1, "reason": map[string]any{"kind": "aborted",
				"reason": map[string]any{"kind": "user"}}}},
	}

	var builder strings.Builder
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("encode record: %v", err)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}

	path := filepath.Join(dir, dshSessionFile)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer file.Close()
	encoder, err := zstd.NewWriter(file)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	if _, err := encoder.Write([]byte(builder.String())); err != nil {
		t.Fatalf("compress fixture: %v", err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatalf("close encoder: %v", err)
	}
	return path
}

func TestDSHReaderNormalizesSession(t *testing.T) {
	root := t.TempDir()
	writeDSHFixture(t, root)
	sessions, report, err := Discover(Config{DSHRoot: root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if report.SessionsFound != 1 {
		t.Fatalf("sessions found = %d", report.SessionsFound)
	}
	document := decodeSession(t, sessions[0])
	meta := document["session"].(map[string]any)
	if meta["id"] != "session-demo-0001" || meta["cwd"] != "/home/dev/demo" {
		t.Fatalf("session metadata = %#v", meta)
	}
	if meta["model"] != "demo-model" {
		t.Fatalf("model = %v", meta["model"])
	}
	if meta["title"] != "Ship the taskboard" {
		t.Fatalf("title = %v", meta["title"])
	}
	// dsh records the record-schema version, not a harness version, so
	// harness_version must stay empty rather than carry a misleading "0".
	if meta["harness_version"] != "" {
		t.Fatalf("harness_version = %v, want unrecorded", meta["harness_version"])
	}
	if meta["thread_kind"] != "main" || meta["parent_session_id"] != "" {
		t.Fatalf("thread = %v / %v", meta["thread_kind"], meta["parent_session_id"])
	}
	if meta["agent_role"] != "standard" || meta["originator"] != "dsh" {
		t.Fatalf("agent/originator = %v / %v", meta["agent_role"], meta["originator"])
	}
}

func TestDSHReaderPairsToolsAndRecordsFailure(t *testing.T) {
	root := t.TempDir()
	writeDSHFixture(t, root)
	sessions, _, err := Discover(Config{DSHRoot: root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	document := decodeSession(t, sessions[0])
	messages := document["messages"].([]any)

	calls, results := map[string]map[string]any{}, map[string]map[string]any{}
	kinds := map[string]int{}
	abortReasons := []string{}
	for _, item := range messages {
		message := item.(map[string]any)
		kind, _ := message["kind"].(string)
		kinds[kind]++
		callID, _ := message["call_id"].(string)
		switch message["kind"] {
		case "tool_call":
			calls[callID] = message
		case "tool_result":
			results[callID] = message
		}
		if reason, ok := message["abort_reason"].(string); ok && reason != "" {
			abortReasons = append(abortReasons, reason)
		}
	}
	if len(calls) != 2 || len(results) != 2 {
		t.Fatalf("calls=%d results=%d, want 2/2", len(calls), len(results))
	}
	if calls["call_ok"]["tool_name"] != "bash" {
		t.Fatalf("tool name = %v", calls["call_ok"]["tool_name"])
	}
	if results["call_ok"]["is_error"] != false {
		t.Fatalf("successful result is_error = %v", results["call_ok"]["is_error"])
	}
	if results["call_bad"]["is_error"] != true {
		t.Fatalf("failed result is_error = %v", results["call_bad"]["is_error"])
	}
	// dsh records no process exit status, so exit_code stays unrecorded rather
	// than being filled with a 0 that would read as success.
	if _, ok := results["call_bad"]["exit_code"]; ok {
		t.Fatalf("exit_code must be unrecorded for dsh, got %v", results["call_bad"]["exit_code"])
	}
	// The tool-call block inside assistant/message repeats the tool/call
	// record; counting both would double every tool call.
	// 1 user + 2 assistant text + 1 system record for the aborted turn.
	if kinds["message"] != 4 {
		t.Fatalf("transcript messages = %d, want 4 (1 user, 2 assistant, 1 abort)", kinds["message"])
	}
	if len(abortReasons) != 1 || abortReasons[0] != "aborted" {
		t.Fatalf("abort reasons = %v", abortReasons)
	}
}

func TestDSHReaderSumsMessageUsage(t *testing.T) {
	root := t.TempDir()
	writeDSHFixture(t, root)
	sessions, _, err := Discover(Config{DSHRoot: root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	usage := sessions[0].Input.Usage
	if usage == nil {
		t.Fatal("no usage recorded")
	}
	if usage.Source != UsageSourceDSH {
		t.Fatalf("usage source = %q", usage.Source)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 2000 {
		t.Fatalf("input tokens = %v, want 2000", usage.InputTokens)
	}
	if usage.OutputTokens == nil || *usage.OutputTokens != 420 {
		t.Fatalf("output tokens = %v, want 420", usage.OutputTokens)
	}
	if usage.CachedInputTokens == nil || *usage.CachedInputTokens != 500 {
		t.Fatalf("cached tokens = %v, want 500", usage.CachedInputTokens)
	}
	if usage.ContextWindow == nil || *usage.ContextWindow != 1000000 {
		t.Fatalf("context window = %v", usage.ContextWindow)
	}
	if usage.AssistantTurns == nil || *usage.AssistantTurns != 2 {
		t.Fatalf("assistant turns = %v", usage.AssistantTurns)
	}
	// dsh reports no cost and no reasoning tokens anywhere.
	if usage.ReasoningTokens != nil {
		t.Fatalf("reasoning tokens invented: %v", *usage.ReasoningTokens)
	}
}

func TestDSHReaderSkipsUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	path := writeDSHFixture(t, root)
	_, first, err := Discover(Config{DSHRoot: root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if _, ok := first.FileStamps[path]; !ok {
		t.Fatalf("no fingerprint for %s", path)
	}
	sessions, second, err := Discover(Config{DSHRoot: root, KnownFiles: first.FileStamps})
	if err != nil {
		t.Fatalf("Discover again: %v", err)
	}
	if len(sessions) != 0 || second.FilesSkipped != 1 {
		t.Fatalf("unchanged file re-read: sessions=%d skipped=%d", len(sessions), second.FilesSkipped)
	}
}
