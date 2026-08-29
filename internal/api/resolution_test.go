package api

import (
	"context"
	"net/url"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
	"flatline/internal/eventstore"
	"flatline/internal/friction"
	"flatline/internal/storage"
)

const resolutionSignature = "timeout|Bash|command timed out after #m #s"

// resolutionFixtureDB is the mining shape (P18-1): the same signature in
// three sessions — two where it ended and work went on (with one action in
// common right after), one where the session died on the failure itself.
func resolutionFixtureDB(t *testing.T) (*storage.DB, map[string]string) {
	t.Helper()
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	base := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	ids := make(map[string]string, 3)
	session := func(key string, start time.Time) string {
		end := start.Add(2 * time.Hour)
		id, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
			SourceSessionID: "res-" + key, StartedAt: &start, EndedAt: &end, CWD: "/synthetic/project-res"})
		if err != nil {
			t.Fatalf("ingest %s: %v", key, err)
		}
		if err := store.RecomputeSessionStats(ctx, id); err != nil {
			t.Fatalf("stats %s: %v", key, err)
		}
		ids[key] = id
		return id
	}
	hit := func(id, eventID string, at time.Time) {
		stamp := at.Format(time.RFC3339Nano)
		exec(t, db, `INSERT INTO friction_records
			(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category,
			 classifier_version, signature, payload_json, locator_json, occurred_at, created_at)
			VALUES (?, ?, 'tool_error', 'transcript_tool_result', 'invoked', 'Bash', 'timeout',
			        'friction/test', ?, '{}', '{}', ?, ?)`, id, eventID, resolutionSignature, stamp, stamp)
	}
	activity := func(id, eventID string, at time.Time) {
		events := []canonical.Event{frictionTestCallEvent(id, eventID, at, map[string]any{
			"tool_name": "Bash", "tool_use_id": eventID, "tool_input": `{"command":"echo on"}`})}
		if _, err := store.IngestEvents(ctx, id, events); err != nil {
			t.Fatalf("ingest activity %s: %v", eventID, err)
		}
	}
	command := func(id string, eventID int, cmd, program string, at time.Time) {
		exec(t, db, `INSERT INTO session_commands (session_id, event_id, ordinal, tool_name, program, command, exit_code, is_error, expected_exit, occurred_at)
			VALUES (?, ?, 0, 'Bash', ?, ?, NULL, NULL, 0, ?)`, id, eventID, program, cmd, at.Format(time.RFC3339Nano))
	}

	one := session("one", base)
	hit(one, "res-1a", base.Add(10*time.Minute))
	hit(one, "res-1b", base.Add(20*time.Minute))
	command(one, 9101, `pkill -9 -f leakchild.py`, "pkill", base.Add(21*time.Minute))
	command(one, 9102, `docker ps -a`, "docker", base.Add(22*time.Minute))
	activity(one, "res-1-after", base.Add(25*time.Minute))

	two := session("two", base.Add(time.Hour))
	hit(two, "res-2a", base.Add(65*time.Minute))
	command(two, 9201, `pkill -9 -f leakchild.py`, "pkill", base.Add(66*time.Minute))
	exec(t, db, `INSERT INTO session_files (session_id, event_id, path, action, tool_name, occurred_at)
		VALUES (?, 9202, '/synthetic/project-res/conftest.py', 'read', 'Read', ?)`,
		two, base.Add(67*time.Minute).Format(time.RFC3339Nano))
	activity(two, "res-2-after", base.Add(70*time.Minute))

	// The third session's last recorded moment IS the failure: nothing ended
	// there, so it stays out of the ended count and out of the aftermath.
	three := session("three", base.Add(2*time.Hour))
	activity(three, "res-3-before", base.Add(125*time.Minute))
	hit(three, "res-3a", base.Add(130*time.Minute))

	exec(t, db, `UPDATE session_stats SET is_empty = 0`)
	return db, ids
}

type resolutionActionRow struct {
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Sessions int    `json:"sessions"`
	Count    int    `json:"count"`
}

type resolutionResponseBody struct {
	Signature     string                `json:"signature"`
	TotalSessions int                   `json:"total_sessions"`
	EndedSessions int                   `json:"ended_sessions"`
	Actions       []resolutionActionRow `json:"actions"`
	Sample        *struct {
		SessionID string `json:"session_id"`
		LastHitAt string `json:"last_hit_at"`
		Actions   []struct {
			Kind    string `json:"kind"`
			Detail  string `json:"detail"`
			EventID int64  `json:"event_id"`
		} `json:"actions"`
	} `json:"sample"`
	Note     string `json:"note"`
	NoteEN   string `json:"note_en"`
	Complete bool   `json:"complete"`
}

func TestResolutionMinesWhatEndedASignature(t *testing.T) {
	db, ids := resolutionFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var body resolutionResponseBody
	getJSON(t, handler, "/api/v1/friction/resolution?signature="+url.QueryEscape(resolutionSignature), &body)
	if !body.Complete || body.Note == "" || body.NoteEN == "" {
		t.Fatalf("header = %+v, want the mining rule stated", body)
	}
	if body.TotalSessions != 3 || body.EndedSessions != 2 {
		t.Fatalf("sessions = %d/%d, want 2 of 3 ended (the die-on-failure session stays out)", body.EndedSessions, body.TotalSessions)
	}
	if len(body.Actions) == 0 {
		t.Fatal("no aftermath actions mined")
	}
	top := body.Actions[0]
	wantLabel := friction.NormalizeLine("pkill -9 -f leakchild.py")
	if top.Label != wantLabel || top.Sessions != 2 || top.Kind != "command" {
		t.Errorf("top action = %+v, want %q corroborated by 2 sessions", top, wantLabel)
	}
	// The failing tool here is Bash, so bare file actions stay out of the
	// ranked list: "then it read a file" is true after almost anything —
	// measured on real data, read:Read topped 9 of 12 signatures as noise.
	// The file action still appears in the sample sequence.
	for _, action := range body.Actions {
		if action.Kind == "file" {
			t.Errorf("ranked actions carry a bare file action %+v for a Bash signature", action)
		}
	}
	if body.Sample == nil || (body.Sample.SessionID != ids["one"] && body.Sample.SessionID != ids["two"]) {
		t.Fatalf("sample = %+v, want one of the ended sessions", body.Sample)
	}
	if len(body.Sample.Actions) == 0 || body.Sample.Actions[0].EventID == 0 {
		t.Errorf("sample actions = %+v, want drillable event ids", body.Sample.Actions)
	}
}

// When the failing tool is itself a file tool, the file aftermath is the
// story: read-then-edit is exactly how a read-before-edit refusal ends.
func TestResolutionRanksFileActionsForFileToolSignatures(t *testing.T) {
	db, _ := resolutionFixtureDB(t)
	editSig := "tool_error|Edit|<tool_use_error>file has not been read yet. read it first…"
	ctx := context.Background()
	var sid string
	if err := db.QueryRowContext(ctx, `SELECT id FROM sessions WHERE source_session_id = 'res-one'`).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 20, 10, 30, 0, 0, time.UTC)
	stamp := at.Format(time.RFC3339Nano)
	exec(t, db, `INSERT INTO friction_records
		(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category,
		 classifier_version, signature, payload_json, locator_json, occurred_at, created_at)
		VALUES (?, 'res-edit-1', 'tool_error', 'transcript_tool_result', 'invoked', 'Edit', 'tool_error',
		        'friction/test', ?, '{}', '{}', ?, ?)`, sid, editSig, stamp, stamp)
	exec(t, db, `INSERT INTO session_files (session_id, event_id, path, action, tool_name, occurred_at)
		VALUES (?, 9301, '/synthetic/project-res/x.go', 'read', 'Read', ?),
		       (?, 9302, '/synthetic/project-res/x.go', 'edit', 'Edit', ?)`,
		sid, at.Add(time.Minute).Format(time.RFC3339Nano), sid, at.Add(2*time.Minute).Format(time.RFC3339Nano))
	store := eventstore.New(db)
	events := []canonical.Event{frictionTestCallEvent(sid, "res-edit-after", at.Add(3*time.Minute), map[string]any{
		"tool_name": "Bash", "tool_use_id": "res-edit-after", "tool_input": `{"command":"echo on"}`})}
	if _, err := store.IngestEvents(ctx, sid, events); err != nil {
		t.Fatalf("ingest activity: %v", err)
	}

	handler := NewServerWithDB(db).Handler()
	var body resolutionResponseBody
	getJSON(t, handler, "/api/v1/friction/resolution?signature="+url.QueryEscape(editSig), &body)
	if len(body.Actions) == 0 || body.Actions[0].Kind != "file" || body.Actions[0].Label != "read:Read" {
		t.Fatalf("actions = %+v, want read:Read ranked first for an Edit-tool signature", body.Actions)
	}
}

func TestResolutionOfAnUnseenSignatureIsZeroNotAnError(t *testing.T) {
	db, _ := resolutionFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var body resolutionResponseBody
	getJSON(t, handler, "/api/v1/friction/resolution?signature="+url.QueryEscape("tool_error|X|never seen"), &body)
	if body.TotalSessions != 0 || body.EndedSessions != 0 || len(body.Actions) != 0 || body.Sample != nil {
		t.Fatalf("unseen signature = %+v, want empty facts", body)
	}
}
