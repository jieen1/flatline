package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"flatline/internal/adapters/claudecode"
	"flatline/internal/adapters/codex"
	"flatline/internal/assets"
	"flatline/internal/canonical"
)

// These records are synthetic fixtures. They exercise the native wire-shape
// reader without copying any real user transcript into the repository.
func TestDiscoverClaudeFixtureKeepsExplicitAssetEvidence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture-claude.jsonl")
	fixture := []map[string]any{
		{"type": "user", "sessionId": "cc-fixture", "timestamp": "2026-08-20T09:00:00Z", "cwd": root, "version": "2.15.0"},
		{"type": "assistant", "sessionId": "cc-fixture", "timestamp": "2026-08-20T09:01:00Z", "cwd": root, "version": "2.15.0", "message": map[string]any{
			"id": "message-1", "role": "assistant", "model": "fixture-model", "content": []any{
				map[string]any{"type": "tool_use", "name": "Skill", "input": map[string]any{"skill": "billing"}},
				map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": filepath.Join(root, "AGENTS.md")}},
				map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": filepath.Join(root, "AGENTS.md")}},
			},
		}},
	}
	writeJSONLines(t, path, fixture)

	sessions, report, err := Discover(Config{ClaudeRoot: root, ProjectRoot: root, Assets: []assets.Asset{
		{ID: "skill:project:billing", Kind: assets.KindSkill, Name: "claude:skills:billing", Scope: assets.ScopeProject, SourcePath: stringPtr(filepath.Join(root, ".claude/skills/billing/SKILL.md"))},
		{ID: "agents_md:project:agents", Kind: assets.KindAgentsMD, Name: "agents", Scope: assets.ScopeProject, SourcePath: stringPtr(filepath.Join(root, "AGENTS.md"))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionsNormalized != 1 || report.AssetEvidenceFound != 2 || len(sessions) != 1 {
		t.Fatalf("report=%+v sessions=%d", report, len(sessions))
	}
	if len(sessions[0].Input.TaskTags) != 0 {
		t.Fatalf("task tags=%v, want unrecorded task shape", sessions[0].Input.TaskTags)
	}
	meta, events, err := claudecode.New().Parse(sessions[0].Input.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if meta.SourceSessionID != "cc-fixture" || len(events) != 6 {
		t.Fatalf("meta=%+v events=%d", meta, len(events))
	}
	if events[2].AssetID != "skill:project:billing" || events[2].ObservationLevel != canonical.LevelInvoked {
		t.Fatalf("skill event=%+v", events[2])
	}
	if events[4].AssetID != "agents_md:project:agents" || events[4].ObservationLevel != canonical.LevelLoaded {
		t.Fatalf("read event=%+v", events[4])
	}
}

func TestDiscoverCodexFixtureMatchesOnlyProjectFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout-fixture.jsonl")
	fixture := []map[string]any{
		{"timestamp": "2026-08-20T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "codex-fixture", "cwd": root, "cli_version": "0.48.0"}},
		{"timestamp": "2026-08-20T10:01:00Z", "type": "turn_context", "payload": map[string]any{"cwd": root, "model": "fixture-model"}},
		{"timestamp": "2026-08-20T10:02:00Z", "type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "call-1", "input": `const cmd = "sed -n '1,80p' ` + filepath.Join(root, ".claude/rules/style.md") + `";`}},
	}
	writeJSONLines(t, path, fixture)
	rulePath := filepath.Join(root, ".claude/rules/style.md")
	sessions, report, err := Discover(Config{CodexRoot: root, ProjectRoot: root, Assets: []assets.Asset{{ID: "rule:project:style", Kind: assets.KindRule, Name: "claude:rules:style", Scope: assets.ScopeProject, SourcePath: &rulePath}}})
	if err != nil {
		t.Fatal(err)
	}
	if report.SessionsNormalized != 1 || report.AssetEvidenceFound != 1 || len(sessions) != 1 {
		t.Fatalf("report=%+v sessions=%d", report, len(sessions))
	}
	meta, events, err := codex.New().Parse(sessions[0].Input.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if meta.SourceSessionID != "codex-fixture" || len(events) != 3 || events[2].AssetID != "rule:project:style" || events[2].ObservationLevel != canonical.LevelLoaded {
		t.Fatalf("meta=%+v events=%+v", meta, events)
	}
}

func TestDiscoverFullLocalModeIncludesForeignProjectsAndSubagents(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "project", "session.jsonl")
	subagentPath := filepath.Join(root, "project", "subagents", "agent.jsonl")
	if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(subagentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONLines(t, mainPath, []map[string]any{{
		"type": "user", "sessionId": "foreign-project", "timestamp": "2026-08-20T09:00:00Z", "cwd": filepath.Join(root, "another-project"),
	}})
	writeJSONLines(t, subagentPath, []map[string]any{{
		"type": "user", "sessionId": "subagent-session", "timestamp": "2026-08-20T09:01:00Z", "cwd": filepath.Join(root, "another-project"),
	}})

	sessions, report, err := Discover(Config{ClaudeRoot: root, IncludeSubagents: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesSeen != 2 || report.FilesRead != 2 || report.SessionsNormalized != 2 || len(sessions) != 2 {
		t.Fatalf("report=%+v sessions=%d", report, len(sessions))
	}

	known := report.FileStamps
	_, secondReport, err := Discover(Config{ClaudeRoot: root, IncludeSubagents: true, KnownFiles: known})
	if err != nil {
		t.Fatal(err)
	}
	if secondReport.FilesSkipped != 2 || secondReport.FilesRead != 0 {
		t.Fatalf("second report=%+v", secondReport)
	}
}

func TestDiscoverNativeTranscriptKeepsSourceBackedTitleAndToolLifecycle(t *testing.T) {
	root := t.TempDir()
	claudePath := filepath.Join(root, "claude.jsonl")
	writeJSONLines(t, claudePath, []map[string]any{
		{"type": "ai-title", "aiTitle": "检查迁移监控页面", "sessionId": "claude-native"},
		{"type": "user", "sessionId": "claude-native", "timestamp": "2026-08-20T09:00:00Z", "cwd": root, "message": map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "text", "text": "检查迁移监控页面并核对真实会话数据"}},
		}},
		{"type": "assistant", "sessionId": "claude-native", "timestamp": "2026-08-20T09:00:01Z", "cwd": root, "message": map[string]any{
			"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": filepath.Join(root, "AGENTS.md")}}},
		}},
		{"type": "user", "sessionId": "claude-native", "timestamp": "2026-08-20T09:00:02Z", "cwd": root, "message": map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "read-1", "content": "AGENTS.md contents"}},
		}},
	})
	assetPath := filepath.Join(root, "AGENTS.md")
	sessions, _, err := Discover(Config{ClaudeRoot: root, ProjectRoot: root, Assets: []assets.Asset{{ID: "agents_md:project:agents", Kind: assets.KindAgentsMD, Name: "agents", Scope: assets.ScopeProject, SourcePath: &assetPath}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions=%d, want one", len(sessions))
	}
	meta, events, err := claudecode.New().Parse(sessions[0].Input.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "检查迁移监控页面" || meta.TaskText != "检查迁移监控页面并核对真实会话数据" {
		t.Fatalf("metadata=%+v", meta)
	}
	if len(sessions[0].Input.TaskTags) == 0 {
		t.Fatal("native task shape is empty for a recorded user task")
	}
	if len(sessions[0].Input.OpportunityAssetIDs) != 1 || sessions[0].Input.OpportunityAssetIDs[0] != "agents_md:project:agents" {
		t.Fatalf("opportunity assets=%v, want the exact referenced asset", sessions[0].Input.OpportunityAssetIDs)
	}
	if !hasEventType(events, canonical.EventTypeTranscriptMessage) || !hasEventType(events, canonical.EventTypeTranscriptToolCall) || !hasEventType(events, canonical.EventTypeTranscriptResult) {
		t.Fatalf("transcript event types missing: %+v", events)
	}
	if !hasAsset(events, "agents_md:project:agents") {
		t.Fatalf("asset evidence missing: %+v", events)
	}
}

func TestDiscoverNativeClaudeTranscriptPreservesExplicitToolError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "claude-error.jsonl")
	writeJSONLines(t, path, []map[string]any{
		{"type": "user", "sessionId": "claude-error", "timestamp": "2026-08-20T09:00:00Z", "cwd": root, "message": map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "text", "text": "运行检查"}},
		}},
		{"type": "assistant", "sessionId": "claude-error", "timestamp": "2026-08-20T09:00:01Z", "cwd": root, "message": map[string]any{
			"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "false"}}},
		}},
		{"type": "user", "sessionId": "claude-error", "timestamp": "2026-08-20T09:00:02Z", "cwd": root, "message": map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool-1", "is_error": true, "content": "permission denied"}},
		}},
	})
	sessions, _, err := Discover(Config{ClaudeRoot: root, ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions=%d, want one", len(sessions))
	}
	_, events, err := claudecode.New().Parse(sessions[0].Input.Raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType == canonical.EventTypeTranscriptResult && event.Payload["is_error"] == true {
			return
		}
	}
	t.Fatalf("explicit tool error was not preserved: %#v", events)
}

func TestDiscoverCodexNativeTranscriptKeepsMessagesAndFunctionResults(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	writeJSONLines(t, path, []map[string]any{
		{"timestamp": "2026-08-20T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "codex-native", "cwd": root, "cli_version": "0.48.0"}},
		{"timestamp": "2026-08-20T10:00:01Z", "type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "核对会话详情和资产参与率"}}}},
		{"timestamp": "2026-08-20T10:00:02Z", "type": "response_item", "payload": map[string]any{"type": "function_call", "name": "exec_command", "call_id": "call-native", "arguments": `{"cmd":"ls"}`}},
		{"timestamp": "2026-08-20T10:00:03Z", "type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": "call-native", "output": "file list"}},
	})
	sessions, _, err := Discover(Config{CodexRoot: root, ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions=%d, want one", len(sessions))
	}
	meta, events, err := codex.New().Parse(sessions[0].Input.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "核对会话详情和资产参与率" || meta.TaskText != "核对会话详情和资产参与率" {
		t.Fatalf("metadata=%+v", meta)
	}
	if len(sessions[0].Input.TaskTags) == 0 {
		t.Fatal("native task shape is empty for a recorded Codex task")
	}
	if len(sessions[0].Input.OpportunityAssetIDs) != 0 {
		t.Fatalf("opportunity assets=%v, want none without an exact asset reference", sessions[0].Input.OpportunityAssetIDs)
	}
	if !hasEventType(events, canonical.EventTypeTranscriptMessage) || !hasEventType(events, canonical.EventTypeTranscriptToolCall) || !hasEventType(events, canonical.EventTypeTranscriptResult) {
		t.Fatalf("transcript event types missing: %+v", events)
	}
}

func TestDiscoverNativeCodexTranscriptPreservesNonZeroExitCode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout-error.jsonl")
	writeJSONLines(t, path, []map[string]any{
		{"timestamp": "2026-08-20T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "codex-error", "cwd": root}},
		{"timestamp": "2026-08-20T10:00:01Z", "type": "response_item", "payload": map[string]any{"type": "function_call", "name": "exec_command", "call_id": "call-error", "arguments": `{"cmd":"false"}`}},
		{"timestamp": "2026-08-20T10:00:02Z", "type": "response_item", "payload": map[string]any{"type": "function_call_output", "call_id": "call-error", "output": "Exit code 7\npermission denied"}},
	})
	sessions, _, err := Discover(Config{CodexRoot: root, ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions=%d, want one", len(sessions))
	}
	_, events, err := codex.New().Parse(sessions[0].Input.Raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.EventType == canonical.EventTypeTranscriptResult && event.Payload["exit_code"] == 7 {
			return
		}
	}
	t.Fatalf("non-zero exit code was not preserved: %#v", events)
}

func TestNativeTaskShapeUsesExactUniqueBasenameOnly(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "AGENTS.md")
	second := filepath.Join(root, "nested", "AGENTS.md")
	index := newAssetIndex([]assets.Asset{
		{ID: "agents:first", Kind: assets.KindAgentsMD, Name: "first", Scope: assets.ScopeProject, SourcePath: &first},
		{ID: "agents:second", Kind: assets.KindAgentsMD, Name: "second", Scope: assets.ScopeProject, SourcePath: &second},
	}, "")
	if got := index.invocationsInText("Read AGENTS.md"); len(got) != 0 {
		t.Fatalf("ambiguous basename evidence=%v, want none", got)
	}
	if got := index.invocationsInText("Read " + second); len(got) != 1 || got[0].AssetID != "agents:second" {
		t.Fatalf("exact path evidence=%v", got)
	}
	if got := nativeTaskTags("继续实施并验证页面交互", root); len(got) == 0 {
		t.Fatal("expected deterministic tags for meaningful task text")
	}
	if got := nativeTaskTags("", root); got != nil {
		t.Fatalf("empty task tags=%v, want nil", got)
	}
}

func hasEventType(events []canonical.Event, eventType string) bool {
	for _, event := range events {
		if event.EventType == eventType {
			return true
		}
	}
	return false
}

func hasAsset(events []canonical.Event, assetID string) bool {
	for _, event := range events {
		if event.AssetID == assetID {
			return true
		}
	}
	return false
}

func writeJSONLines(t *testing.T, path string, records []map[string]any) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func stringPtr(value string) *string { return &value }
