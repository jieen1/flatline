package api

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/eventstore"
	"flatline/internal/storage"
)

const adherenceSignature = "tool_error|Edit|<tool_use_error>file has not been read yet. read it first before writing to it.</tool_use_error>"

// adherenceFixtureDB spreads one mechanism's friction over three of the last
// four weeks — 2 hits, a silent week, 3 hits — with one session per hit plus a
// friction-free session in the silent week, so the curve has both a real zero
// and real denominators.
func adherenceFixtureDB(t *testing.T) (*storage.DB, time.Time) {
	t.Helper()
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	now := time.Now().UTC()
	monday := now.AddDate(0, 0, -((int(now.Weekday()) + 6) % 7)).Truncate(24 * time.Hour)

	session := func(id string, at time.Time) string {
		end := at.Add(time.Hour)
		stored, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
			SourceSessionID: id, StartedAt: &at, EndedAt: &end, CWD: "/synthetic/project-adherence"})
		if err != nil {
			t.Fatalf("ingest %s: %v", id, err)
		}
		if err := store.RecomputeSessionStats(ctx, stored); err != nil {
			t.Fatalf("stats %s: %v", id, err)
		}
		return stored
	}
	friction := func(sessionID, eventID string, at time.Time) {
		stamp := at.Format(time.RFC3339Nano)
		exec(t, db, `INSERT INTO friction_records
			(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category,
			 classifier_version, signature, payload_json, locator_json, occurred_at, created_at)
			VALUES (?, ?, 'tool_error', 'transcript_tool_result', 'invoked', 'Edit', 'tool_error',
			        'friction/test', ?, '{}', '{}', ?, ?)`,
			sessionID, eventID, adherenceSignature, stamp, stamp)
	}
	// Week -3: two hits in two sessions. Week -2: one clean session, zero
	// hits. Week -1: three hits in one session. This week: nothing.
	week3 := monday.AddDate(0, 0, -21).Add(10 * time.Hour)
	week2 := monday.AddDate(0, 0, -14).Add(10 * time.Hour)
	week1 := monday.AddDate(0, 0, -7).Add(10 * time.Hour)
	s1 := session("adh-w3-a", week3)
	s2 := session("adh-w3-b", week3.Add(time.Hour))
	session("adh-w2-clean", week2)
	s4 := session("adh-w1", week1)
	friction(s1, "adh-f1", week3)
	friction(s2, "adh-f2", week3.Add(time.Hour))
	friction(s4, "adh-f3", week1)
	friction(s4, "adh-f4", week1.Add(time.Minute))
	friction(s4, "adh-f5", week1.Add(2*time.Minute))
	exec(t, db, `UPDATE session_stats SET is_empty = 0`)
	return db, monday
}

type weekPointResponse struct {
	Week         string `json:"week"`
	Count        int    `json:"count"`
	SessionCount int    `json:"session_count"`
	WeekSessions int    `json:"week_sessions"`
}

func TestFrictionSignatureWeeklyCurve(t *testing.T) {
	db, monday := adherenceFixtureDB(t)
	handler := NewServerWithDB(db).Handler()
	var detail struct {
		Signature string              `json:"signature"`
		Weeks     []weekPointResponse `json:"weeks"`
		Note      string              `json:"note"`
		NoteEN    string              `json:"note_en"`
		Complete  bool                `json:"complete"`
	}
	getJSON(t, handler, "/api/v1/friction/weekly?signature="+url.QueryEscape(adherenceSignature), &detail)
	if !detail.Complete || detail.Signature != adherenceSignature {
		t.Fatalf("weekly header = %+v", detail)
	}
	if detail.Note == "" || detail.NoteEN == "" {
		t.Error("the curve carries no criterion sentence")
	}
	weeks := detail.Weeks
	if len(weeks) != 12 {
		t.Fatalf("weeks = %d, want a continuous 12-week axis", len(weeks))
	}
	byWeek := map[string]weekPointResponse{}
	for i, w := range weeks {
		if i > 0 && !(weeks[i-1].Week < w.Week) {
			t.Fatalf("weeks out of order: %s then %s", weeks[i-1].Week, w.Week)
		}
		byWeek[w.Week] = w
	}
	label := func(offsetDays int) string { return monday.AddDate(0, 0, offsetDays).Format("2006-01-02") }
	w3, w2, w1 := byWeek[label(-21)], byWeek[label(-14)], byWeek[label(-7)]
	if w3.Count != 2 || w3.SessionCount != 2 || w3.WeekSessions != 2 {
		t.Errorf("week -3 = %+v, want 2 hits / 2 sessions / 2 running", w3)
	}
	// The silent week is present with a zero count and its real denominator —
	// a gap in the axis would hide exactly the reading the curve exists for.
	if w2.Count != 0 || w2.WeekSessions != 1 {
		t.Errorf("week -2 = %+v, want 0 hits over 1 running session", w2)
	}
	if w1.Count != 3 || w1.SessionCount != 1 {
		t.Errorf("week -1 = %+v, want 3 hits in 1 session", w1)
	}
}

func TestAssetAdherenceCurvesFollowTheMentionedMechanisms(t *testing.T) {
	db, monday := adherenceFixtureDB(t)
	dir := t.TempDir()
	mention := filepath.Join(dir, "workflow.md")
	if err := os.WriteFile(mention, []byte("# Workflow\n\n- Read before you edit: read the file first.\n"), 0o644); err != nil {
		t.Fatalf("write rule: %v", err)
	}
	silent := filepath.Join(dir, "style.md")
	if err := os.WriteFile(silent, []byte("# Style\n\n- 小步提交。\n"), 0o644); err != nil {
		t.Fatalf("write rule: %v", err)
	}
	stamp := monday.AddDate(0, 0, -30).Format(time.RFC3339Nano)
	exec(t, db, `INSERT INTO assets (id, kind, scope, name, source_path, first_seen_at)
		VALUES ('rule:user:workflow', 'rule', 'user', 'workflow', ?, ?),
		       ('rule:user:style', 'rule', 'user', 'style', ?, ?)`, mention, stamp, silent, stamp)
	handler := NewServerWithDB(db).Handler()

	var page struct {
		Mechanisms []struct {
			Mechanism  string              `json:"mechanism"`
			Kind       string              `json:"kind"`
			Keywords   []string            `json:"keywords_mentioned"`
			Signatures int                 `json:"signatures"`
			Weeks      []weekPointResponse `json:"weeks"`
		} `json:"mechanisms"`
		Note string `json:"note"`
	}
	getJSON(t, handler, "/api/v1/assets/rule:user:workflow/adherence", &page)
	if len(page.Mechanisms) != 1 {
		t.Fatalf("mechanisms = %+v, want exactly the read-before-edit mechanism", page.Mechanisms)
	}
	m := page.Mechanisms[0]
	if m.Kind != "harness_rule" || len(m.Keywords) == 0 || m.Signatures != 1 {
		t.Errorf("mechanism = %+v", m)
	}
	total := 0
	for _, w := range m.Weeks {
		total += w.Count
	}
	if total != 5 {
		t.Errorf("curve total = %d, want the fixture's 5 hits", total)
	}
	if page.Note == "" {
		t.Error("no criterion sentence on the adherence answer")
	}

	var quiet struct {
		Mechanisms []any `json:"mechanisms"`
	}
	getJSON(t, handler, "/api/v1/assets/rule:user:style/adherence", &quiet)
	if len(quiet.Mechanisms) != 0 {
		t.Errorf("a rule mentioning no mechanism reports %d curves, want none", len(quiet.Mechanisms))
	}
}

// The rescue count (P17-3) is the archive moat stated as a fact: sessions
// whose source transcript the harness has deleted while the event store still
// holds their history. A file that exists is not rescued; a session with no
// recorded file at all is unknown, not rescued.
func TestStatsCountRescuedTranscripts(t *testing.T) {
	db, monday := adherenceFixtureDB(t)
	dir := t.TempDir()
	kept := filepath.Join(dir, "kept.jsonl")
	if err := os.WriteFile(kept, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write kept transcript: %v", err)
	}
	stamp := monday.Format(time.RFC3339Nano)
	var keptSession, goneSession string
	if err := db.QueryRowContext(context.Background(),
		`SELECT id FROM sessions WHERE source_session_id = 'adh-w3-a'`).Scan(&keptSession); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(),
		`SELECT id FROM sessions WHERE source_session_id = 'adh-w1'`).Scan(&goneSession); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `INSERT INTO native_files (path, size, mtime_ns, session_id, last_read_at) VALUES
		(?, 10, 1, ?, ?),
		('/nonexistent/rescued-one.jsonl', 10, 1, ?, ?),
		('/nonexistent/rescued-two.jsonl', 10, 1, ?, ?)`,
		kept, keptSession, stamp, goneSession, stamp, goneSession, stamp)

	handler := NewServerWithDB(db).Handler()
	var stats struct {
		Rescued struct {
			Sessions int    `json:"sessions"`
			Files    int    `json:"files"`
			Note     string `json:"note"`
			NoteEN   string `json:"note_en"`
		} `json:"rescued_transcripts"`
	}
	getJSON(t, handler, "/api/v1/stats", &stats)
	if stats.Rescued.Files != 2 || stats.Rescued.Sessions != 1 {
		t.Fatalf("rescued = %+v, want 2 gone files across 1 session; the kept file stays out", stats.Rescued)
	}
	if stats.Rescued.Note == "" || stats.Rescued.NoteEN == "" {
		t.Error("the rescue count carries no criterion sentence")
	}
}
