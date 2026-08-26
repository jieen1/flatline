package eventstore

import (
	"context"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
)

// Codex writes an exec script as `const cmds = [[label, cmd, cwd], ...]`. One
// tool call therefore runs several commands, and until session_commands had an
// ordinal the unique key (session_id, event_id) silently dropped every command
// after the first.

func TestCodexExecScriptRecordsEveryCommandInTheCall(t *testing.T) {
	db := supersedeTestDB(t)
	store := New(db)
	ctx := context.Background()
	at := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	sessionID, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
		SourceSessionID: "commands-1", StartedAt: &at, CWD: "/synthetic/project",
	})
	if err != nil {
		t.Fatalf("ingest session: %v", err)
	}
	// The shape Codex actually writes: a small script whose cmds array holds
	// one [label, command, cwd] tuple per command it is about to run.
	script := "const cmds = [\n" +
		"  [\"fmt\", \"gofmt -l .\", \".\"],\n" +
		"  [\"vet\", \"go vet ./...\", \".\"],\n" +
		"  [\"test\", \"go test ./...\", \".\"],\n" +
		"];\n"
	events := []canonical.Event{
		{SourceEventID: "exec-call", SessionID: sessionID, EventType: canonical.EventTypeTranscriptToolCall,
			ObservationLevel: canonical.LevelUnknown, OccurredAt: &at,
			Payload: map[string]any{"tool_name": "exec", "tool_use_id": "exec-1", "tool_input": script},
			Locator: canonical.Locator{Source: "codex", SessionID: sessionID, RawRef: "turn-1"}},
		{SourceEventID: "exec-result", SessionID: sessionID, EventType: canonical.EventTypeTranscriptResult,
			ObservationLevel: canonical.LevelUnknown, OccurredAt: &at,
			Payload: map[string]any{"tool_use_id": "exec-1", "tool_output": "FAIL", "exit_code": 1},
			Locator: canonical.Locator{Source: "codex", SessionID: sessionID, RawRef: "turn-2"}},
	}
	if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
		t.Fatalf("ingest events: %v", err)
	}
	if err := store.RecomputeSessionProjections(ctx, sessionID); err != nil {
		t.Fatalf("project: %v", err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT ordinal, command, exit_code FROM session_commands WHERE session_id = ? ORDER BY ordinal`, sessionID)
	if err != nil {
		t.Fatalf("read commands: %v", err)
	}
	defer rows.Close()
	type row struct {
		ordinal  int
		command  string
		exitCode *int64
	}
	var got []row
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.ordinal, &item.command, &item.exitCode); err != nil {
			t.Fatalf("scan command: %v", err)
		}
		got = append(got, item)
	}
	if len(got) != 3 {
		t.Fatalf("recorded %d commands, want all 3 of the call: %+v", len(got), got)
	}
	for index, want := range []string{"gofmt -l .", "go vet ./...", "go test ./..."} {
		if got[index].ordinal != index || got[index].command != want {
			t.Fatalf("command %d = %+v, want ordinal %d %q", index, got[index], index, want)
		}
	}
	// The call reports one status, and a shell reports the last statement's.
	// The earlier commands have no recorded outcome, which is not the same as
	// having succeeded.
	if got[0].exitCode != nil || got[1].exitCode != nil {
		t.Fatalf("an outcome was attributed to a command the source did not report one for: %+v", got)
	}
	if got[2].exitCode == nil || *got[2].exitCode != 1 {
		t.Fatalf("last command exit code = %v, want 1", got[2].exitCode)
	}

	var commandCount int
	if err := db.QueryRowContext(ctx,
		`SELECT command_count FROM session_stats WHERE session_id = ?`, sessionID).Scan(&commandCount); err != nil {
		t.Fatalf("read command count: %v", err)
	}
	if commandCount != 3 {
		t.Fatalf("session_stats.command_count = %d, want 3", commandCount)
	}
}
