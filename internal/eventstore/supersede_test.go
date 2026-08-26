package eventstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
	"flatline/internal/storage"
)

// A stricter parser rule withdraws the evidence the looser one wrote, without
// deleting or editing a single stored row. These tests hold that line: the
// event is still there, it is only marked as no longer produced, and the
// derived rows follow it in both directions.

func supersedeTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "supersede.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func supersedeFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	db := supersedeTestDB(t)
	store := New(db)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	sessionID, err := store.IngestSession(ctx, adapters.SourceCodex, adapters.SessionMeta{
		SourceSessionID: "supersede-1", StartedAt: &at, CWD: "/synthetic/project",
	})
	if err != nil {
		t.Fatalf("ingest session: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO assets (id, kind, scope, name, source_path, first_seen_at)
		VALUES ('asset:one', 'skill', 'project', 'one', '/synthetic/project/one/SKILL.md', ?)`,
		at.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	signal := canonical.SignalLoaded
	events := []canonical.Event{
		{SourceEventID: "keep", SessionID: sessionID, EventType: canonical.EventTypeAssetInvoked,
			AssetID: "asset:one", ParticipationSignal: &signal, ObservationLevel: canonical.LevelLoaded,
			Payload: map[string]any{}, Locator: canonical.Locator{Source: "codex", SessionID: sessionID, RawRef: "turn-1"},
			OccurredAt: &at},
		{SourceEventID: "withdraw", SessionID: sessionID, EventType: canonical.EventTypeAssetInvoked,
			AssetID: "asset:one", ParticipationSignal: &signal, ObservationLevel: canonical.LevelLoaded,
			Payload: map[string]any{}, Locator: canonical.Locator{Source: "codex", SessionID: sessionID, RawRef: "turn-2"},
			OccurredAt: &at},
	}
	if _, err := store.IngestEvents(ctx, sessionID, events); err != nil {
		t.Fatalf("ingest events: %v", err)
	}
	return store, sessionID, "asset:one"
}

func TestSupersedeMarksTheWithdrawnEventAndKeepsTheRow(t *testing.T) {
	store, sessionID, _ := supersedeFixture(t)
	ctx := context.Background()

	report, err := store.SupersedeAssetEvidence(ctx, sessionID, map[string]struct{}{"keep": {}})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if report.Events != 1 || report.Restored != 0 {
		t.Fatalf("report = %+v, want one event superseded", report)
	}

	// The row is still there, and its payload and locator are untouched: this
	// is a withdrawal of a reading, not a deletion of a record.
	var total, superseded int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(CASE WHEN superseded_at IS NOT NULL THEN 1 ELSE 0 END)
		FROM events WHERE session_id = ? AND event_type = ?`,
		sessionID, canonical.EventTypeAssetInvoked).Scan(&total, &superseded); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 || superseded != 1 {
		t.Fatalf("events total = %d, superseded = %d, want 2 and 1", total, superseded)
	}

	live, withdrawn, err := store.SupersededAssetEvidenceCount(ctx)
	if err != nil {
		t.Fatalf("count asset evidence: %v", err)
	}
	if live != 1 || withdrawn != 1 {
		t.Fatalf("live = %d, superseded = %d, want 1 and 1", live, withdrawn)
	}
}

func TestSupersedeIsReversibleAndLeavesDerivedRowsAloneWhileEvidenceRemains(t *testing.T) {
	store, sessionID, assetID := supersedeFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO opportunities (session_id, shape_class, shape_rule_version, asset_id, detector_version, detected_at)
		VALUES (?, 'fixture', 'shape/1', ?, 'tracker/1', ?)`,
		sessionID, assetID, at.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert opportunity: %v", err)
	}

	// One event withdrawn, one still standing: the opportunity is still
	// supported and must not be withdrawn with it.
	if _, err := store.SupersedeAssetEvidence(ctx, sessionID, map[string]struct{}{"keep": {}}); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if got := supersededOpportunities(t, store, sessionID); got != 0 {
		t.Fatalf("%d opportunities superseded while an event still stands", got)
	}

	// Now nothing produces the asset any more, so what was derived from it
	// goes with it.
	if _, err := store.SupersedeAssetEvidence(ctx, sessionID, map[string]struct{}{}); err != nil {
		t.Fatalf("supersede all: %v", err)
	}
	if got := supersededOpportunities(t, store, sessionID); got != 1 {
		t.Fatalf("%d opportunities superseded after all evidence was withdrawn, want 1", got)
	}

	// A later parse that produces the evidence again restores both.
	report, err := store.SupersedeAssetEvidence(ctx, sessionID, map[string]struct{}{"keep": {}, "withdraw": {}})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if report.Restored != 2 {
		t.Fatalf("restored = %d, want 2", report.Restored)
	}
	if got := supersededOpportunities(t, store, sessionID); got != 0 {
		t.Fatalf("%d opportunities still superseded after the evidence came back", got)
	}
}

func supersededOpportunities(t *testing.T, store *Store, sessionID string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM opportunities WHERE session_id = ? AND superseded_at IS NOT NULL`,
		sessionID).Scan(&count); err != nil {
		t.Fatalf("count opportunities: %v", err)
	}
	return count
}

func TestProjectKeyFoldsOnlyTheHarnessWorktreeShape(t *testing.T) {
	for _, item := range []struct{ cwd, key, worktree string }{
		{"/home/bot/project/qsr/.claude/worktrees/agent-a4f5", "/home/bot/project/qsr", "agent-a4f5"},
		{"/home/bot/project/qsr/.claude/worktrees/agent-a4f5/sub/dir", "/home/bot/project/qsr", "agent-a4f5"},
		// A directory that only looks like a worktree by name is left alone:
		// guessing at a naming convention would merge two real projects.
		{"/home/bot/project/qsr-wt-deps", "/home/bot/project/qsr-wt-deps", ""},
		{"/home/bot/project/qsr", "/home/bot/project/qsr", ""},
		{"", "", ""},
	} {
		key, worktree := ProjectKeyOf(item.cwd)
		if key != item.key || worktree != item.worktree {
			t.Errorf("ProjectKeyOf(%q) = (%q, %q), want (%q, %q)", item.cwd, key, worktree, item.key, item.worktree)
		}
	}
}

func TestDataVersionSurvivesAReopen(t *testing.T) {
	db := supersedeTestDB(t)
	store := New(db)
	ctx := context.Background()
	if version, err := store.LoadDataVersion(ctx); err != nil || version != 0 {
		t.Fatalf("fresh database data version = %d (%v), want 0", version, err)
	}
	if err := store.SaveDataVersion(ctx, 42); err != nil {
		t.Fatalf("save: %v", err)
	}
	// A second Store over the same database is what a restarted daemon reads
	// with. It must not begin again at 1.
	if version, err := New(db).LoadDataVersion(ctx); err != nil || version != 42 {
		t.Fatalf("reloaded data version = %d (%v), want 42", version, err)
	}
}

// A session's span only ever grows. Reading a transcript that is still being
// written and then reading it again must widen the session, not freeze it at
// the first reading: every measurement taken from the later read — active
// time above all — is measured over the wider span.
func TestSessionSpanGrowsWithTheTranscript(t *testing.T) {
	db := supersedeTestDB(t)
	store := New(db)
	ctx := context.Background()
	start := time.Date(2026, 8, 22, 17, 38, 1, 0, time.UTC)
	firstEnd := start.Add(3*time.Minute + 40*time.Second)
	laterEnd := start.Add(50 * time.Minute)

	for _, end := range []time.Time{firstEnd, laterEnd, firstEnd} {
		if _, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
			SourceSessionID: "span-1", StartedAt: &start, EndedAt: &end, CWD: "/synthetic/project",
		}); err != nil {
			t.Fatalf("ingest: %v", err)
		}
	}
	var ended string
	if err := db.QueryRowContext(ctx, `SELECT ended_at FROM sessions WHERE source_session_id = 'span-1'`).Scan(&ended); err != nil {
		t.Fatalf("read ended_at: %v", err)
	}
	if ended != laterEnd.Format(time.RFC3339Nano) {
		t.Fatalf("ended_at = %q, want the latest reading %q", ended, laterEnd.Format(time.RFC3339Nano))
	}

	// An earlier start is taken too: a parser that learns to read a record the
	// previous one skipped can find the session began sooner.
	earlier := start.Add(-5 * time.Minute)
	if _, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
		SourceSessionID: "span-1", StartedAt: &earlier, EndedAt: &firstEnd, CWD: "/synthetic/project",
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	var started string
	if err := db.QueryRowContext(ctx, `SELECT started_at FROM sessions WHERE source_session_id = 'span-1'`).Scan(&started); err != nil {
		t.Fatalf("read started_at: %v", err)
	}
	if started != earlier.Format(time.RFC3339Nano) {
		t.Fatalf("started_at = %q, want the earliest reading %q", started, earlier.Format(time.RFC3339Nano))
	}
}

// Claude Code symlinks a subagent's transcript into a second parent's
// directory, so the same file is on disk under two paths and the daemon
// measures it twice. One transcript is one transcript: the roll-up counts it
// once, identified by its file name rather than by where it sits.
func TestOneTranscriptUnderTwoPathsIsMeasuredOnce(t *testing.T) {
	db := supersedeTestDB(t)
	store := New(db)
	ctx := context.Background()
	at := time.Date(2026, 8, 2, 9, 55, 0, 0, time.UTC)
	end := at.Add(49 * time.Minute)
	sessionID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
		SourceSessionID: "a86083b9", StartedAt: &at, EndedAt: &end, CWD: "/synthetic/project",
	})
	if err != nil {
		t.Fatalf("ingest session: %v", err)
	}
	measured := func() *SessionUsage {
		usage := &SessionUsage{Source: UsageSourceClaude, InputTokens: known64(10),
			OutputTokens: known64(5), ActiveMS: known64(2_940_000), AssistantTurns: known64(1)}
		usage.RecomputeTotal()
		return usage
	}
	for _, path := range []string{
		"/home/bot/.claude/projects/p/one/subagents/agent-a86083b9.jsonl",
		"/home/bot/.claude/projects/p/two/subagents/agent-a86083b9.jsonl",
	} {
		if err := store.RecordFileUsage(ctx, path, sessionID, measured(), ParserVersionForTest); err != nil {
			t.Fatalf("record usage %s: %v", path, err)
		}
	}

	var total, active, turns int64
	if err := db.QueryRowContext(ctx,
		`SELECT total_tokens, active_ms, assistant_turns FROM session_usage WHERE session_id = ?`,
		sessionID).Scan(&total, &active, &turns); err != nil {
		t.Fatalf("read session usage: %v", err)
	}
	if total != 15 || active != 2_940_000 || turns != 1 {
		t.Fatalf("session usage = total %d / active %d / turns %d, want the one transcript counted once", total, active, turns)
	}
	var duration int64
	if err := store.RecomputeSessionStats(ctx, sessionID); err != nil {
		t.Fatalf("stats: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT duration_ms FROM session_stats WHERE session_id = ?`, sessionID).Scan(&duration); err != nil {
		t.Fatalf("read duration: %v", err)
	}
	if active > duration {
		t.Fatalf("active_ms %d exceeds duration_ms %d", active, duration)
	}
}

// ParserVersionForTest is the stamp these tests write; the value itself is not
// under test.
const ParserVersionForTest = "parser/test"

func known64(value int64) *int64 { return &value }

// An opportunity produced only by a path reference in the task text has no
// asset_invoked event behind it, so the evidence channel cannot reach it. This
// is the channel that can: it compares the stored rows against the whole set
// the current rules produce.
func TestSupersedeStaleOpportunitiesWithdrawsWhatTheRulesNoLongerProduce(t *testing.T) {
	store, sessionID, assetID := supersedeFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO assets (id, kind, scope, name, first_seen_at)
		VALUES ('asset:two', 'skill', 'project', 'two', ?)`, at); err != nil {
		t.Fatalf("insert second asset: %v", err)
	}
	for _, id := range []string{assetID, "asset:two"} {
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO opportunities (session_id, shape_class, shape_rule_version, asset_id, detector_version, detected_at)
			VALUES (?, 'shape/1:analysis', 'shape/1', ?, 'tracker/1', ?)`, sessionID, id, at); err != nil {
			t.Fatalf("insert opportunity %s: %v", id, err)
		}
	}

	withdrawn, err := store.SupersedeStaleOpportunities(ctx, sessionID,
		[]OpportunityKey{{ShapeClass: "shape/1:analysis", AssetID: assetID}})
	if err != nil {
		t.Fatalf("supersede opportunities: %v", err)
	}
	if withdrawn != 1 {
		t.Fatalf("withdrawn = %d, want 1", withdrawn)
	}

	var total, live int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(CASE WHEN superseded_at IS NULL THEN 1 ELSE 0 END)
		FROM opportunities WHERE session_id = ?`, sessionID).Scan(&total, &live); err != nil {
		t.Fatalf("count opportunities: %v", err)
	}
	if total != 2 || live != 1 {
		t.Fatalf("opportunities total = %d, live = %d, want 2 and 1: the row stays, only the reading is withdrawn", total, live)
	}

	// A shape class the rules no longer record is withdrawn too: the row is
	// keyed by (shape class, asset), not by asset alone.
	if withdrawn, err = store.SupersedeStaleOpportunities(ctx, sessionID,
		[]OpportunityKey{{ShapeClass: "shape/1:implementation", AssetID: assetID}}); err != nil {
		t.Fatalf("supersede after shape change: %v", err)
	}
	if withdrawn != 1 {
		t.Fatalf("withdrawn after shape change = %d, want 1", withdrawn)
	}
}
