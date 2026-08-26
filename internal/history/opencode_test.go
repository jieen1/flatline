package history

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// writeOpenCodeFixture builds a small opencode database with the columns the
// reader actually reads. It is synthesized here rather than copied from a real
// machine: no fixture in this repository may contain real session content.
func writeOpenCodeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	schema := `
	CREATE TABLE session (
		id TEXT PRIMARY KEY, project_id TEXT NOT NULL, workspace_id TEXT, parent_id TEXT,
		slug TEXT NOT NULL, directory TEXT NOT NULL, path TEXT, title TEXT NOT NULL,
		version TEXT NOT NULL, share_url TEXT, summary_additions INTEGER, summary_deletions INTEGER,
		summary_files INTEGER, summary_diffs TEXT, metadata TEXT, cost REAL DEFAULT 0 NOT NULL,
		tokens_input INTEGER DEFAULT 0 NOT NULL, tokens_output INTEGER DEFAULT 0 NOT NULL,
		tokens_reasoning INTEGER DEFAULT 0 NOT NULL, tokens_cache_read INTEGER DEFAULT 0 NOT NULL,
		tokens_cache_write INTEGER DEFAULT 0 NOT NULL, revert TEXT, permission TEXT, agent TEXT,
		model TEXT, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
		time_compacting INTEGER, time_archived INTEGER);
	CREATE TABLE message (
		id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL,
		time_updated INTEGER NOT NULL, data TEXT NOT NULL);
	CREATE TABLE part (
		id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
		time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}

	sessions := []struct {
		id, parent, title, agent string
		additions                any
		created, updated         int64
	}{
		{"ses_parent", "", "Wire the taskboard", "build", int64(12), 1786103452000, 1786103999000},
		{"ses_child", "ses_parent", "New session - 2026-08-07T11:50:52.159Z", "explore", nil, 1786104000000, 1786104500000},
	}
	for _, item := range sessions {
		var parent any
		if item.parent != "" {
			parent = item.parent
		}
		if _, err := db.Exec(`
			INSERT INTO session (id, project_id, parent_id, slug, directory, title, version,
				summary_additions, summary_deletions, summary_files, cost,
				tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write,
				agent, model, time_created, time_updated)
			VALUES (?, 'proj', ?, 'slug', '/home/dev/demo', ?, '1.18.18', ?, 3, 2, 0.25,
				1000, 200, 40, 500, 10, ?, '{"id":"demo-model","providerID":"demo"}', ?, ?)`,
			item.id, parent, item.title, item.additions, item.agent, item.created, item.updated); err != nil {
			t.Fatalf("insert session: %v", err)
		}
	}

	type partRow struct {
		id, messageID, sessionID string
		created                  int64
		data                     map[string]any
	}
	messages := []struct {
		id, sessionID string
		created       int64
		data          map[string]any
		parts         []partRow
	}{
		{"msg_1", "ses_parent", 1786103452000, map[string]any{"role": "user", "time": map[string]any{"created": 1786103452000}},
			[]partRow{{"prt_1", "msg_1", "ses_parent", 1786103452000, map[string]any{"type": "text", "text": "Add a taskboard page"}}}},
		{"msg_2", "ses_parent", 1786103460000, map[string]any{"role": "assistant", "time": map[string]any{"created": 1786103460000}},
			[]partRow{
				{"prt_2", "msg_2", "ses_parent", 1786103460000, map[string]any{"type": "reasoning", "text": "thinking"}},
				{"prt_3", "msg_2", "ses_parent", 1786103461000, map[string]any{"type": "text", "text": "Running the tests first."}},
				{"prt_4", "msg_2", "ses_parent", 1786103462000, map[string]any{
					"type": "tool", "tool": "bash", "callID": "call_ok",
					"state": map[string]any{"status": "completed", "input": map[string]any{"command": "go test ./..."},
						"output": "ok", "metadata": map[string]any{"exit": 0},
						"time": map[string]any{"start": 1786103462000, "end": 1786103463000}}}},
				{"prt_5", "msg_2", "ses_parent", 1786103464000, map[string]any{
					"type": "tool", "tool": "bash", "callID": "call_bad",
					"state": map[string]any{"status": "error", "input": map[string]any{"command": "go build"},
						"error": "build failed", "metadata": map[string]any{"exit": 2, "interrupted": false},
						"time": map[string]any{"start": 1786103464000, "end": 1786103465000}}}},
				{"prt_6", "msg_2", "ses_parent", 1786103466000, map[string]any{
					"type": "tool", "tool": "bash", "callID": "call_running",
					"state": map[string]any{"status": "running", "input": map[string]any{"command": "sleep 100"},
						"time": map[string]any{"start": 1786103466000}}}},
			}},
		{"msg_3", "ses_child", 1786104000000, map[string]any{"role": "user", "time": map[string]any{"created": 1786104000000}},
			[]partRow{{"prt_7", "msg_3", "ses_child", 1786104000000, map[string]any{"type": "text", "text": "Explore the repo"}}}},
	}
	for _, message := range messages {
		encoded, _ := json.Marshal(message.data)
		if _, err := db.Exec(`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?)`,
			message.id, message.sessionID, message.created, message.created, string(encoded)); err != nil {
			t.Fatalf("insert message: %v", err)
		}
		for _, part := range message.parts {
			encoded, _ := json.Marshal(part.data)
			if _, err := db.Exec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?,?)`,
				part.id, part.messageID, part.sessionID, part.created, part.created, string(encoded)); err != nil {
				t.Fatalf("insert part: %v", err)
			}
		}
	}
	return path
}

func decodeSession(t *testing.T, session Session) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(session.Input.Raw.RawJSON, &document); err != nil {
		t.Fatalf("decode normalized json: %v", err)
	}
	return document
}

func TestOpenCodeReaderNormalizesSessions(t *testing.T) {
	path := writeOpenCodeFixture(t)
	sessions, report, err := Discover(Config{OpenCodeDB: path})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	if report.SessionsFound != 2 || report.FilesRead != 2 {
		t.Fatalf("report sessions=%d files_read=%d", report.SessionsFound, report.FilesRead)
	}

	byID := map[string]Session{}
	for _, session := range sessions {
		byID[session.Input.Raw.SessionID] = session
	}

	parent := decodeSession(t, byID["ses_parent"])
	meta := parent["session"].(map[string]any)
	if meta["cwd"] != "/home/dev/demo" || meta["model"] != "demo-model" || meta["harness_version"] != "1.18.18" {
		t.Fatalf("session metadata = %#v", meta)
	}
	if meta["title"] != "Wire the taskboard" {
		t.Fatalf("title = %v", meta["title"])
	}
	if meta["thread_kind"] != "main" || meta["parent_session_id"] != "" {
		t.Fatalf("parent thread = %v / %v", meta["thread_kind"], meta["parent_session_id"])
	}
	if meta["agent_role"] != "build" || meta["originator"] != "opencode" {
		t.Fatalf("agent role/originator = %v / %v", meta["agent_role"], meta["originator"])
	}

	child := decodeSession(t, byID["ses_child"])
	childMeta := child["session"].(map[string]any)
	if childMeta["thread_kind"] != "subagent" || childMeta["parent_session_id"] != "opencode:ses_parent" {
		t.Fatalf("child hierarchy = %v / %v", childMeta["thread_kind"], childMeta["parent_session_id"])
	}
	// The placeholder title opencode writes before naming a session is not
	// evidence, so the reader falls back to the recorded task text.
	if childMeta["title"] != "Explore the repo" {
		t.Fatalf("placeholder title leaked: %v", childMeta["title"])
	}
}

func TestOpenCodeReaderPairsToolsAndRecordsFailure(t *testing.T) {
	path := writeOpenCodeFixture(t)
	sessions, _, err := Discover(Config{OpenCodeDB: path})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var parent Session
	for _, session := range sessions {
		if session.Input.Raw.SessionID == "ses_parent" {
			parent = session
		}
	}
	document := decodeSession(t, parent)
	messages := document["messages"].([]any)

	calls, results := map[string]map[string]any{}, map[string]map[string]any{}
	for _, item := range messages {
		message := item.(map[string]any)
		callID, _ := message["call_id"].(string)
		switch message["kind"] {
		case "tool_call":
			calls[callID] = message
		case "tool_result":
			results[callID] = message
		}
	}
	// Every tool_call and tool_result carries call_id, which is what §13's
	// pairing projection joins on.
	if len(calls) != 3 {
		t.Fatalf("tool calls = %d, want 3", len(calls))
	}
	if _, ok := calls["call_ok"]; !ok {
		t.Fatal("call_ok missing its tool_call")
	}
	if len(results) != 2 {
		t.Fatalf("tool results = %d, want 2 (the running tool has none)", len(results))
	}
	if _, ok := results["call_running"]; ok {
		t.Fatal("a running tool must not get a result: that would invent an outcome")
	}

	ok := results["call_ok"]
	if ok["is_error"] != false {
		t.Fatalf("completed tool is_error = %v, want false", ok["is_error"])
	}
	if ok["exit_code"] != float64(0) {
		t.Fatalf("completed tool exit_code = %v, want 0", ok["exit_code"])
	}
	bad := results["call_bad"]
	if bad["is_error"] != true {
		t.Fatalf("failed tool is_error = %v, want true", bad["is_error"])
	}
	if bad["exit_code"] != float64(2) {
		t.Fatalf("failed tool exit_code = %v, want 2", bad["exit_code"])
	}
	if bad["tool_output"] != "build failed" {
		t.Fatalf("failed tool output = %v", bad["tool_output"])
	}
}

func TestOpenCodeReaderRecordsUsageFromSessionRow(t *testing.T) {
	path := writeOpenCodeFixture(t)
	sessions, _, err := Discover(Config{OpenCodeDB: path})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, session := range sessions {
		usage := session.Input.Usage
		if usage == nil {
			t.Fatalf("%s has no usage", session.Input.Raw.SessionID)
		}
		if usage.Source != UsageSourceOpenCode {
			t.Fatalf("usage source = %q", usage.Source)
		}
		if usage.InputTokens == nil || *usage.InputTokens != 1000 {
			t.Fatalf("input tokens = %v", usage.InputTokens)
		}
		// 1000 uncached input + 500 cache read + 10 cache write + 200 output.
		// The 40 reasoning tokens are part of the output opencode already
		// counted, so adding them again would count them twice.
		if usage.TotalTokens == nil || *usage.TotalTokens != 1710 {
			t.Fatalf("total tokens = %v", *usage.TotalTokens)
		}
		if usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 10 {
			t.Fatalf("cache write tokens = %v", usage.CacheWriteTokens)
		}
		if len(usage.ByModel) != 1 || usage.ByModel[0].Model != "demo-model" {
			t.Fatalf("by model = %#v", usage.ByModel)
		}
	}

	byID := map[string]Session{}
	for _, session := range sessions {
		byID[session.Input.Raw.SessionID] = session
	}
	// summary_additions is NULL on the child row. NULL means opencode has not
	// summarized the diff yet, so lines_added stays unrecorded rather than 0.
	if byID["ses_child"].Input.Usage.LinesAdded != nil {
		t.Fatalf("unrecorded lines_added became %v", *byID["ses_child"].Input.Usage.LinesAdded)
	}
	if got := byID["ses_parent"].Input.Usage.LinesAdded; got == nil || *got != 12 {
		t.Fatalf("recorded lines_added = %v", got)
	}
}

func TestOpenCodeReaderSkipsUnchangedRows(t *testing.T) {
	path := writeOpenCodeFixture(t)
	_, first, err := Discover(Config{OpenCodeDB: path})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	sessions, second, err := Discover(Config{OpenCodeDB: path, KnownFiles: first.FileStamps})
	if err != nil {
		t.Fatalf("Discover again: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("unchanged rows were re-read: %d sessions", len(sessions))
	}
	if second.FilesSkipped != 2 {
		t.Fatalf("files_skipped = %d, want 2", second.FilesSkipped)
	}
	// The fingerprint is the row's own time_updated under a pseudo-path, so a
	// SQLite source needs no file to stat.
	if _, ok := first.FileStamps[path+"#ses_parent"]; !ok {
		t.Fatalf("fingerprint pseudo-path missing from %v", first.FileStamps)
	}
}

func TestOpenCodeSourceStatus(t *testing.T) {
	path := writeOpenCodeFixture(t)
	hermes := t.TempDir()
	if err := os.MkdirAll(filepath.Join(hermes, "sessions"), 0o755); err != nil {
		t.Fatalf("create hermes fixture: %v", err)
	}
	if _, _, err := Discover(Config{OpenCodeDB: path, HermesRoot: hermes}); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	byKind := map[string]SourceStatus{}
	for _, status := range SourceStatuses() {
		byKind[status.Kind] = status
	}
	opencode := byKind["opencode"]
	if opencode.Status != StatusOK || opencode.Sessions == nil || *opencode.Sessions != 2 {
		t.Fatalf("opencode status = %#v", opencode)
	}
	if byKind["hermes"].Status != StatusNoSessions {
		t.Fatalf("hermes status = %#v", byKind["hermes"])
	}
	if byKind["claude_code"].Status != StatusNotFound {
		t.Fatalf("absent claude root should be not_found, got %#v", byKind["claude_code"])
	}
}
