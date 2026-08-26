package eventstore

import (
	"context"
	"testing"

	"flatline/internal/adapters"
)

func TestIngestSessionFillsMissingMetadataWithoutOverwritingFacts(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	if _, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{SourceSessionID: "metadata-replay"}); err != nil {
		t.Fatalf("initial session: %v", err)
	}
	if _, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
		SourceSessionID: "metadata-replay", StartedAt: testTime(1), EndedAt: testTime(2),
		HarnessVersion: "2.14", Model: "synthetic-model", CWD: "/synthetic/project",
		Title: "真实会话标题", TaskText: "核对真实 transcript",
	}); err != nil {
		t.Fatalf("metadata replay: %v", err)
	}
	if _, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
		SourceSessionID: "metadata-replay", StartedAt: testTime(3), EndedAt: testTime(4),
		HarnessVersion: "2.15", Model: "other-model", CWD: "/other/project",
	}); err != nil {
		t.Fatalf("conflicting replay: %v", err)
	}
	var started, ended, harness, model, cwd, title, taskText string
	if err := store.db.QueryRowContext(ctx, `
		SELECT started_at, ended_at, harness_version, model, cwd, title, task_text
		FROM sessions WHERE id = ?`, "claude_code:metadata-replay").Scan(&started, &ended, &harness, &model, &cwd, &title, &taskText); err != nil {
		t.Fatalf("load session: %v", err)
	}
	// Identity facts keep their first recorded value; the span is the one thing
	// that follows the newest reading, because a transcript that is still being
	// written has more records in it every time it is read.
	if harness != "2.14" || model != "synthetic-model" || cwd != "/synthetic/project" || title != "真实会话标题" || taskText != "核对真实 transcript" {
		t.Fatalf("session metadata = %q %q %q %q %q, want first recorded values", harness, model, cwd, title, taskText)
	}
	if started != testTime(1).Format("2006-01-02T15:04:05Z07:00") {
		t.Fatalf("started_at = %q, want the earliest reading", started)
	}
	if ended != testTime(4).Format("2006-01-02T15:04:05Z07:00") {
		t.Fatalf("ended_at = %q, want the latest reading", ended)
	}
}
