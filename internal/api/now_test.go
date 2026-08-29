package api

import (
	"context"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
	"flatline/internal/storage"
)

// nowFixtureDB is a live fleet next to a finished one: a main session whose
// transcript was written seconds ago, one live child and one already-quiet
// child under it, and a main session whose transcript went quiet an hour ago.
func nowFixtureDB(t *testing.T) (*storage.DB, map[string]string) {
	t.Helper()
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	at := time.Now().UTC().Add(-2 * time.Hour)

	ids := make(map[string]string, 4)
	add := func(key, sourceID, title string) {
		id, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
			SourceSessionID: sourceID, StartedAt: &at, CWD: "/synthetic/project-now", Title: title,
		})
		if err != nil {
			t.Fatalf("ingest %s: %v", sourceID, err)
		}
		if err := store.RecomputeSessionStats(ctx, id); err != nil {
			t.Fatalf("stats %s: %v", sourceID, err)
		}
		ids[key] = id
	}
	add("live", "now-live", "还在跑的舰队")
	add("livechild", "now-live-child", "")
	add("quietchild", "now-quiet-child", "")
	add("done", "now-done", "早就收工的会话")

	exec(t, db, `UPDATE session_stats SET is_empty = 0`)
	exec(t, db, `UPDATE sessions SET thread_kind = 'main' WHERE id IN (?, ?)`, ids["live"], ids["done"])
	exec(t, db, `UPDATE sessions SET thread_kind = 'subagent', parent_session_id = ?, agent_role = 'dev-7' WHERE id = ?`, ids["live"], ids["livechild"])
	exec(t, db, `UPDATE sessions SET thread_kind = 'subagent', parent_session_id = ?, agent_role = 'qa-7' WHERE id = ?`, ids["live"], ids["quietchild"])

	file := func(id, path string, age time.Duration) {
		exec(t, db, `INSERT INTO native_files (path, size, mtime_ns, session_id, last_read_at)
			VALUES (?, 100, ?, ?, ?)`, path, time.Now().Add(-age).UnixNano(), id, time.Now().UTC().Format(time.RFC3339Nano))
	}
	file(ids["live"], "/synthetic/now/live.jsonl", 30*time.Second)
	file(ids["livechild"], "/synthetic/now/live-child.jsonl", 90*time.Second)
	file(ids["quietchild"], "/synthetic/now/quiet-child.jsonl", time.Hour)
	file(ids["done"], "/synthetic/now/done.jsonl", time.Hour)

	// The live child is hitting the same failure over and over: six identical
	// signatures inside the last hour. The live parent has two stray friction
	// records with different signatures — activity, not a loop.
	friction := func(id, eventID, signature string, age time.Duration) {
		at := time.Now().UTC().Add(-age).Format(time.RFC3339Nano)
		exec(t, db, `INSERT INTO friction_records
			(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category,
			 classifier_version, signature, payload_json, locator_json, occurred_at, created_at)
			VALUES (?, ?, 'tool_error', 'transcript_tool_result', 'invoked', 'Bash', 'tool_error',
			        'friction/test', ?, '{}', '{}', ?, ?)`, id, eventID, signature, at, at)
	}
	for index := 0; index < 6; index++ {
		friction(ids["livechild"], "now-loop-"+string(rune('a'+index)),
			"tool_error|Bash|error: project config not found", time.Duration(50-index*7)*time.Minute)
	}
	friction(ids["live"], "now-one-a", "tool_error|Bash|bash exit 1", 20*time.Minute)
	friction(ids["live"], "now-one-b", "tool_error|Edit|string to replace not found", 5*time.Minute)
	return db, ids
}

type nowLoopResponse struct {
	Signature  string `json:"signature"`
	SampleLine string `json:"sample_line"`
	Count      int    `json:"count"`
	FirstAt    string `json:"first_at"`
	LastAt     string `json:"last_at"`
}

type nowRowResponse struct {
	ID             string           `json:"id"`
	ThreadKind     *string          `json:"thread_kind"`
	DisplayTitle   *string          `json:"display_title"`
	InProgress     bool             `json:"in_progress"`
	LiveChildren   int              `json:"live_children"`
	FrictionLastAt *string          `json:"friction_last_at"`
	Loop           *nowLoopResponse `json:"loop"`
}

type nowResponse struct {
	Sessions []nowRowResponse `json:"sessions"`
	Count    int              `json:"count"`
	Note     string           `json:"note"`
	NoteEN   string           `json:"note_en"`
	Complete bool             `json:"complete"`
}

func TestNowListsOnlyLiveTranscriptsAndCountsLiveChildren(t *testing.T) {
	db, ids := nowFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var now nowResponse
	getJSON(t, handler, "/api/v1/now", &now)
	if !now.Complete || now.Note == "" || now.NoteEN == "" {
		t.Fatalf("now header = %+v, want the criterion sentence stated", now)
	}
	if now.Count != 2 || len(now.Sessions) != 2 {
		t.Fatalf("now sessions = %+v, want the live parent and its live child only", now.Sessions)
	}
	byID := map[string]nowRowResponse{}
	for _, row := range now.Sessions {
		if !row.InProgress {
			t.Errorf("row %s is listed but not in progress", row.ID)
		}
		byID[row.ID] = row
	}
	parent, ok := byID[ids["live"]]
	if !ok {
		t.Fatalf("live parent missing from %+v", now.Sessions)
	}
	if parent.LiveChildren != 1 {
		t.Errorf("live parent live_children = %d, want 1 (the quiet child stays out)", parent.LiveChildren)
	}
	if _, listed := byID[ids["done"]]; listed {
		t.Error("the hour-quiet session is listed as live")
	}
	if _, listed := byID[ids["quietchild"]]; listed {
		t.Error("the hour-quiet child is listed as live")
	}
}

// A live session repeating the same failure is the one thing a monitor must
// say out loud. The claim is factual — this signature recurred N times inside
// the stated window — and two different failures are activity, not a loop.
func TestNowNamesARepeatingFailureOnALiveSession(t *testing.T) {
	db, ids := nowFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var now nowResponse
	getJSON(t, handler, "/api/v1/now", &now)
	byID := map[string]nowRowResponse{}
	for _, row := range now.Sessions {
		byID[row.ID] = row
	}
	child := byID[ids["livechild"]]
	if child.Loop == nil {
		t.Fatalf("live child = %+v, want its six-hit signature named", child)
	}
	if child.Loop.Count != 6 || child.Loop.SampleLine != "error: project config not found" {
		t.Errorf("loop = %+v, want count 6 with the sample line", child.Loop)
	}
	if child.Loop.FirstAt == "" || child.Loop.LastAt == "" {
		t.Errorf("loop = %+v, want both bounds stated", child.Loop)
	}
	parent := byID[ids["live"]]
	if parent.Loop != nil {
		t.Errorf("parent loop = %+v, want none: two different signatures are not a loop", parent.Loop)
	}
	if parent.FrictionLastAt == nil {
		t.Error("parent friction_last_at is null, want the five-minute-old record's time")
	}
}

// The live view is state, not history: a browser holding a cached copy would
// keep showing a run that already ended, so the response must refuse the
// version-keyed cache the read endpoints use.
func TestNowIsNeverServedFromCache(t *testing.T) {
	db, _ := nowFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var payload nowResponse
	first := getJSON(t, handler, "/api/v1/now", &payload)
	if etag := first.Header().Get("ETag"); etag != "" {
		t.Errorf("now carries ETag %q; live state must not be revalidated into staleness", etag)
	}
	if cache := first.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("now Cache-Control = %q, want no-store", cache)
	}
}
