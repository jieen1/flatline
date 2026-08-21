package eventstore

import (
	"context"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
	"flatline/internal/storage"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := storage.Open(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func testTime(hour int) *time.Time {
	value := time.Date(2026, 8, 20, hour, 0, 0, 0, time.UTC)
	return &value
}

func testEvent(session, id, raw string, at *time.Time) canonical.Event {
	line := 0
	return canonical.Event{
		SourceEventID:    id,
		SessionID:        session,
		EventType:        canonical.EventTypeSessionStarted,
		ObservationLevel: canonical.LevelInferred,
		Payload:          map[string]any{"synthetic": true},
		Locator:          canonical.Locator{Source: "claude_code", SessionID: session, Line: &line, RawRef: raw},
		OccurredAt:       at,
		AdapterVersion:   "test/1",
	}
}

func TestIngestSessionAndEventsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	id, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{SourceSessionID: "s1", StartedAt: testTime(1)})
	if err != nil {
		t.Fatal(err)
	}
	if id != "claude_code:s1" {
		t.Fatalf("session id = %q", id)
	}
	event := testEvent(id, "evt-1", "message-1", testTime(1))
	if got, err := store.IngestEvents(ctx, id, []canonical.Event{event}); err != nil || got != 1 {
		t.Fatalf("first ingest = %d, %v", got, err)
	}
	if got, err := store.IngestEvents(ctx, id, []canonical.Event{event}); err != nil || got != 0 {
		t.Fatalf("repeat ingest = %d, %v", got, err)
	}
	rows, err := store.EventsForSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("event rows = %d, want 1", len(rows))
	}
	if rows[0].SourceEventID != "evt-1" {
		t.Fatalf("source event id = %q", rows[0].SourceEventID)
	}
}

func TestIngestFrictionProjectsOnlyExplicitToolFailures(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	sessionID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{SourceSessionID: "friction"})
	if err != nil {
		t.Fatal(err)
	}
	failure := testEvent(sessionID, "tool-result-error", "result-error", testTime(1))
	failure.EventType = canonical.EventTypeTranscriptResult
	failure.ObservationLevel = canonical.LevelUnknown
	failure.Payload = map[string]any{"tool_output": "permission denied", "is_error": true}
	exitFailure := testEvent(sessionID, "tool-result-exit", "result-exit", testTime(2))
	exitFailure.EventType = canonical.EventTypeTranscriptResult
	exitFailure.Payload = map[string]any{"tool_output": "Exit code 7\ncommand failed", "exit_code": 7}
	ordinaryErrorText := testEvent(sessionID, "tool-result-text", "result-text", testTime(3))
	ordinaryErrorText.EventType = canonical.EventTypeTranscriptResult
	ordinaryErrorText.Payload = map[string]any{"tool_output": "the word error is part of this documentation"}
	if _, err := store.IngestEvents(ctx, sessionID, []canonical.Event{failure, exitFailure, ordinaryErrorText}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.IngestFriction(ctx, sessionID, []canonical.Event{failure, exitFailure, ordinaryErrorText}); err != nil || got != 2 {
		t.Fatalf("first friction projection = %d, %v; want 2", got, err)
	}
	if got, err := store.IngestFriction(ctx, sessionID, []canonical.Event{failure, exitFailure, ordinaryErrorText}); err != nil || got != 0 {
		t.Fatalf("repeat friction projection = %d, %v; want 0", got, err)
	}
	records, err := store.FrictionRecordsForSession(ctx, sessionID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].FrictionKind != FrictionKindToolError || records[1].ExitCode == nil || *records[1].ExitCode != 7 {
		t.Fatalf("friction records = %#v", records)
	}
}

func TestEventByLocator(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	id, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{SourceSessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	event := testEvent(id, "evt-1", "turn-1", nil)
	event.Locator.Source = "codex"
	if _, err := store.IngestEvents(ctx, id, []canonical.Event{event}); err != nil {
		t.Fatal(err)
	}
	got, err := store.EventByLocator(ctx, event.Locator)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventType != event.EventType || got.Locator.RawRef != "turn-1" {
		t.Fatalf("round trip = %#v", got)
	}
}

func TestInvalidEventDoesNotPartiallyInsert(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	id, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{SourceSessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	valid := testEvent(id, "evt-1", "one", nil)
	invalid := valid
	invalid.SourceEventID = ""
	if _, err := store.IngestEvents(ctx, id, []canonical.Event{valid, invalid}); err == nil {
		t.Fatal("invalid event should fail")
	}
	rows, err := store.EventsForSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("partial rows = %d", len(rows))
	}
}

func TestEnvironmentChangedAnchors(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	first, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
		SourceSessionID: "s1", StartedAt: testTime(1), HarnessVersion: "2.13", Model: "opus",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
		SourceSessionID: "s2", StartedAt: testTime(2), HarnessVersion: "2.14", Model: "sonnet",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.DetectEnvironmentChanges(ctx, first); err != nil || got != 0 {
		t.Fatalf("first anchors = %d, %v", got, err)
	}
	if got, err := store.DetectEnvironmentChanges(ctx, second); err != nil || got != 2 {
		t.Fatalf("second anchors = %d, %v", got, err)
	}
	if got, err := store.DetectEnvironmentChanges(ctx, second); err != nil || got != 0 {
		t.Fatalf("repeat anchors = %d, %v", got, err)
	}
	rows, err := store.EventsForSession(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("anchor rows = %d", len(rows))
	}
	for _, row := range rows {
		if row.EventType != canonical.EventTypeEnvironmentChanged || row.ObservationLevel != canonical.LevelInferred {
			t.Fatalf("unexpected anchor: %#v", row)
		}
	}
}

func TestEnvironmentChangeRequiresRecordedValues(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	_, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{SourceSessionID: "s1", StartedAt: testTime(1)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{SourceSessionID: "s2", StartedAt: testTime(2), Model: "gpt"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.DetectEnvironmentChanges(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("anchors with missing previous values = %d", got)
	}
}
