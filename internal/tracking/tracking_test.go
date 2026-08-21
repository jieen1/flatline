package tracking

import (
	"context"
	"testing"
	"time"

	"flatline/internal/canonical"
	"flatline/internal/storage"
)

// All test data is synthetic: sessions, assets, versions and events are
// fabricated in a temporary SQLite database. No real user data is read.

func testTracker(t *testing.T) (*Tracker, *storage.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir()+"/tracking.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db), db
}

func seedSession(t *testing.T, db *storage.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sessions (id, source, source_session_id) VALUES (?, 'claude_code', ?)`, id, id); err != nil {
		t.Fatal(err)
	}
}

func seedAsset(t *testing.T, db *storage.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO assets (id, kind, name, first_seen_at) VALUES (?, 'skill', ?, '2026-01-01T00:00:00Z')`, id, id); err != nil {
		t.Fatal(err)
	}
}

func seedAssetVersion(t *testing.T, db *storage.DB, assetID string, version int) int64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO asset_versions (asset_id, version, content_hash, observation_level, observed_at)
		VALUES (?, ?, ?, 'invoked', '2026-01-01T00:00:00Z')`, assetID, version, "hash-"+assetID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func day(day int) time.Time {
	return time.Date(2026, 8, day, 0, 0, 0, 0, time.UTC)
}

func TestClassifyShapeDeterministic(t *testing.T) {
	cases := []struct {
		name  string
		tags  []string
		class string
	}{
		{"order independent", []string{"sql", "migration"}, "shape/1:migration|sql"},
		{"dedup", []string{"sql", "SQL", "sql"}, "shape/1:sql"},
		{"normalization", []string{"Run SQL Migrations", "run-sql-migrations"}, "shape/1:run-sql-migrations"},
		{"empty tags", []string{"", "  ", "---"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, basis, err := ClassifyShape(tc.tags)
			if err != nil {
				t.Fatal(err)
			}
			if class != tc.class {
				t.Fatalf("class = %q, want %q", class, tc.class)
			}
			if tc.class != "" && basis == "" {
				t.Fatal("basis must be non-empty for a classified shape")
			}
			// Determinism: same input, same output.
			again, _, err := ClassifyShape(tc.tags)
			if err != nil || again != class {
				t.Fatalf("second classification = %q, %v", again, err)
			}
		})
	}
}

func TestRecordSessionShapeIdempotent(t *testing.T) {
	ctx := context.Background()
	tracker, db := testTracker(t)
	seedSession(t, db, "claude_code:s1")
	seedAsset(t, db, "skill:user:sql-migrations")

	shape := SessionShape{
		SessionID:  "claude_code:s1",
		Tags:       []string{"sql", "migration"},
		AssetIDs:   []string{"skill:user:sql-migrations"},
		DetectedAt: day(1),
	}
	class, inserted, err := tracker.RecordSessionShape(ctx, shape)
	if err != nil {
		t.Fatal(err)
	}
	if class != "shape/1:migration|sql" {
		t.Fatalf("class = %q", class)
	}
	if inserted != 1 {
		t.Fatalf("first insert = %d, want 1", inserted)
	}
	// Idempotent replay: same input, no new rows.
	if _, inserted, err := tracker.RecordSessionShape(ctx, shape); err != nil || inserted != 0 {
		t.Fatalf("replay insert = %d, %v", inserted, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM opportunities WHERE session_id = 'claude_code:s1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("opportunity rows = %d, want 1", count)
	}
}

func TestRecordSessionShapeRejects(t *testing.T) {
	ctx := context.Background()
	tracker, db := testTracker(t)
	seedSession(t, db, "claude_code:s1")
	seedAsset(t, db, "skill:user:sql-migrations")

	if _, _, err := tracker.RecordSessionShape(ctx, SessionShape{
		SessionID: "claude_code:s1", Tags: []string{"sql"}, AssetIDs: nil,
	}); err == nil {
		t.Fatal("missing asset list must fail")
	}
	if _, _, err := tracker.RecordSessionShape(ctx, SessionShape{
		SessionID: "claude_code:unknown", Tags: []string{"sql"}, AssetIDs: []string{"skill:user:sql-migrations"},
	}); err == nil {
		t.Fatal("unknown session must fail")
	}
	if _, _, err := tracker.RecordSessionShape(ctx, SessionShape{
		SessionID: "claude_code:s1", Tags: []string{"sql"}, AssetIDs: []string{"skill:user:missing"},
	}); err == nil {
		t.Fatal("unknown asset must fail")
	}
	// No tags: no shape class, never recorded as an empty class.
	if _, _, err := tracker.RecordSessionShape(ctx, SessionShape{
		SessionID: "claude_code:s1", Tags: nil, AssetIDs: []string{"skill:user:sql-migrations"},
	}); err == nil {
		t.Fatal("tagless session must fail, not record an empty shape")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM opportunities`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("opportunities after rejected inputs = %d, want 0", count)
	}

	if _, _, err := tracker.RecordSessionShape(ctx, SessionShape{
		SessionID: "claude_code:s1", Tags: []string{"sql"},
		AssetIDs: []string{"skill:user:sql-migrations"}, DetectedAt: time.Time{},
	}); err == nil {
		t.Fatal("zero detected_at must be rejected")
	}
	if _, _, err := tracker.RecordSessionShape(ctx, SessionShape{
		SessionID: "claude_code:s1", Tags: []string{"sql"},
		AssetIDs: []string{"skill:user:sql-migrations"}, DetectedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local),
	}); err == nil {
		t.Fatal("non-UTC detected_at must be rejected")
	}
}

func TestRecordParticipationValidationAndIdempotency(t *testing.T) {
	ctx := context.Background()
	tracker, db := testTracker(t)
	seedSession(t, db, "claude_code:s1")
	seedAsset(t, db, "skill:user:sql-migrations")
	versionID := seedAssetVersion(t, db, "skill:user:sql-migrations", 1)

	// Non-canonical signal and level are rejected.
	if _, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: versionID, SessionID: "claude_code:s1",
		Signal: canonical.ParticipationSignal("followed-up"), Level: canonical.LevelInvoked,
	}); err == nil {
		t.Fatal("non-canonical signal must be rejected")
	}
	if _, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: versionID, SessionID: "claude_code:s1",
		Signal: canonical.SignalInvoked, Level: canonical.ObservationLevel("followed"),
	}); err == nil {
		t.Fatal("followed is a signal, never an observation level")
	}
	if _, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: 0, SessionID: "claude_code:s1",
		Signal: canonical.SignalInvoked, Level: canonical.LevelInvoked,
	}); err == nil {
		t.Fatal("missing asset version must be rejected")
	}

	// Signal and level are orthogonal: followed with unknown level is valid.
	inserted, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: versionID, SessionID: "claude_code:s1",
		Signal: canonical.SignalFollowed, Level: canonical.LevelUnknown,
	})
	if err != nil || !inserted {
		t.Fatalf("followed/unknown participation = %v, %v", inserted, err)
	}
	// Idempotent replay.
	if inserted, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: versionID, SessionID: "claude_code:s1",
		Signal: canonical.SignalFollowed, Level: canonical.LevelUnknown,
	}); err != nil || inserted {
		t.Fatalf("replay = %v, %v", inserted, err)
	}
	// Same session+version, different signal: a distinct row.
	if inserted, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: versionID, SessionID: "claude_code:s1",
		Signal: canonical.SignalInvoked, Level: canonical.LevelInvoked,
	}); err != nil || !inserted {
		t.Fatalf("second signal = %v, %v", inserted, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM participations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("participation rows = %d, want 2", count)
	}
}

func TestBaselineExplainableNumeratorDenominator(t *testing.T) {
	ctx := context.Background()
	tracker, db := testTracker(t)
	seedAsset(t, db, "skill:user:sql-migrations")
	versionID := seedAssetVersion(t, db, "skill:user:sql-migrations", 1)

	// 10 opportunity sessions in the window; 7 participate.
	for i := 1; i <= 10; i++ {
		sessionID := "claude_code:base-" + string(rune('a'+i-1))
		seedSession(t, db, sessionID)
		if _, _, err := tracker.RecordSessionShape(ctx, SessionShape{
			SessionID: sessionID, Tags: []string{"sql", "migration"},
			AssetIDs: []string{"skill:user:sql-migrations"}, DetectedAt: day(i),
		}); err != nil {
			t.Fatal(err)
		}
		if i <= 7 {
			if _, err := tracker.RecordParticipation(ctx, ParticipationInput{
				AssetVersionID: versionID, SessionID: sessionID,
				Signal: canonical.SignalInvoked, Level: canonical.LevelInvoked,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	baseline, err := tracker.Baseline(ctx, "skill:user:sql-migrations", "shape/1:migration|sql", day(1), day(11))
	if err != nil {
		t.Fatal(err)
	}
	if baseline.OpportunitySessions != 10 || baseline.ParticipatingSessions != 7 {
		t.Fatalf("baseline = %d/%d, want 7/10", baseline.ParticipatingSessions, baseline.OpportunitySessions)
	}
	if baseline.Rate == nil || *baseline.Rate != 0.7 {
		t.Fatalf("rate = %v, want 0.7", baseline.Rate)
	}
	if baseline.ShapeRuleVersion != ShapeRuleVersion || baseline.TrackerVersion != TrackerVersion {
		t.Fatalf("baseline missing rule/tracker version: %#v", baseline)
	}

	// Numerator and denominator are independently queryable and agree.
	denominator, err := tracker.CountOpportunities(ctx, "skill:user:sql-migrations", "shape/1:migration|sql", day(1), day(11))
	if err != nil || denominator != 10 {
		t.Fatalf("denominator = %d, %v", denominator, err)
	}
	numerator, err := tracker.CountParticipatingSessions(ctx, "skill:user:sql-migrations", "shape/1:migration|sql", day(1), day(11))
	if err != nil || numerator != 7 {
		t.Fatalf("numerator = %d, %v", numerator, err)
	}

	// A window with no opportunities: rate is nil, not 0 (缺失 ≠ 零).
	empty, err := tracker.Baseline(ctx, "skill:user:sql-migrations", "shape/1:migration|sql", day(20), day(21))
	if err != nil {
		t.Fatal(err)
	}
	if empty.OpportunitySessions != 0 || empty.ParticipatingSessions != 0 {
		t.Fatalf("empty window = %d/%d, want 0/0", empty.ParticipatingSessions, empty.OpportunitySessions)
	}
	if empty.Rate != nil {
		t.Fatalf("empty window rate = %v, want nil (no baseline, not zero)", *empty.Rate)
	}
}

func TestBaselineWindowIsHalfOpenAndShapeScoped(t *testing.T) {
	ctx := context.Background()
	tracker, db := testTracker(t)
	seedAsset(t, db, "skill:user:sql-migrations")
	versionID := seedAssetVersion(t, db, "skill:user:sql-migrations", 1)

	// Day 10: sql shape, participates. Day 11: different shape, participates.
	seedSession(t, db, "claude_code:w10")
	seedSession(t, db, "claude_code:w11")
	if _, _, err := tracker.RecordSessionShape(ctx, SessionShape{
		SessionID: "claude_code:w10", Tags: []string{"sql"},
		AssetIDs: []string{"skill:user:sql-migrations"}, DetectedAt: day(10),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tracker.RecordSessionShape(ctx, SessionShape{
		SessionID: "claude_code:w11", Tags: []string{"deploy"},
		AssetIDs: []string{"skill:user:sql-migrations"}, DetectedAt: day(11),
	}); err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{"claude_code:w10", "claude_code:w11"} {
		if _, err := tracker.RecordParticipation(ctx, ParticipationInput{
			AssetVersionID: versionID, SessionID: sessionID,
			Signal: canonical.SignalInvoked, Level: canonical.LevelInvoked,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// [day10, day11) includes day 10 only; the day-11 session is excluded
	// both by the half-open window and by shape class.
	baseline, err := tracker.Baseline(ctx, "skill:user:sql-migrations", "shape/1:sql", day(10), day(11))
	if err != nil {
		t.Fatal(err)
	}
	if baseline.OpportunitySessions != 1 || baseline.ParticipatingSessions != 1 {
		t.Fatalf("windowed baseline = %d/%d, want 1/1", baseline.ParticipatingSessions, baseline.OpportunitySessions)
	}
	// The deploy shape has its own denominator.
	deploy, err := tracker.Baseline(ctx, "skill:user:sql-migrations", "shape/1:deploy", day(10), day(12))
	if err != nil {
		t.Fatal(err)
	}
	if deploy.OpportunitySessions != 1 || deploy.ParticipatingSessions != 1 {
		t.Fatalf("deploy baseline = %d/%d, want 1/1", deploy.ParticipatingSessions, deploy.OpportunitySessions)
	}
}

func TestParticipationRequiresOpportunitySessionMatch(t *testing.T) {
	ctx := context.Background()
	tracker, db := testTracker(t)
	seedSession(t, db, "claude_code:s1")
	seedSession(t, db, "claude_code:s2")
	seedAsset(t, db, "skill:user:sql-migrations")
	versionID := seedAssetVersion(t, db, "skill:user:sql-migrations", 1)

	class, _, err := tracker.RecordSessionShape(ctx, SessionShape{
		SessionID: "claude_code:s1", Tags: []string{"sql"},
		AssetIDs: []string{"skill:user:sql-migrations"}, DetectedAt: day(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	var opportunityID int64
	if err := db.QueryRow(`SELECT id FROM opportunities WHERE session_id = 'claude_code:s1'`).Scan(&opportunityID); err != nil {
		t.Fatal(err)
	}
	// Opportunity from another session must be rejected.
	if _, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: versionID, SessionID: "claude_code:s2",
		OpportunityID: &opportunityID,
		Signal:        canonical.SignalInvoked, Level: canonical.LevelInvoked,
	}); err == nil {
		t.Fatal("opportunity from a different session must be rejected")
	}
	// Matching opportunity is accepted.
	if _, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: versionID, SessionID: "claude_code:s1",
		OpportunityID: &opportunityID,
		Signal:        canonical.SignalInvoked, Level: canonical.LevelInvoked,
	}); err != nil {
		t.Fatal(err)
	}
	_ = class
}

func TestParticipationRequiresOpportunityAssetMatch(t *testing.T) {
	ctx := context.Background()
	tracker, db := testTracker(t)
	seedSession(t, db, "claude_code:s1")
	seedAsset(t, db, "skill:user:a")
	seedAsset(t, db, "skill:user:b")
	versionA := seedAssetVersion(t, db, "skill:user:a", 1)
	versionB := seedAssetVersion(t, db, "skill:user:b", 1)
	if _, _, err := tracker.RecordSessionShape(ctx, SessionShape{
		SessionID: "claude_code:s1", Tags: []string{"sql"},
		AssetIDs: []string{"skill:user:a", "skill:user:b"}, DetectedAt: day(1),
	}); err != nil {
		t.Fatal(err)
	}
	var opportunityB int64
	if err := db.QueryRow(`SELECT id FROM opportunities WHERE asset_id = 'skill:user:b'`).Scan(&opportunityB); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: versionA, SessionID: "claude_code:s1", OpportunityID: &opportunityB,
		Signal: canonical.SignalInvoked, Level: canonical.LevelInvoked,
	}); err == nil {
		t.Fatal("participation for asset A with asset B opportunity must be rejected")
	}
	if _, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: versionB, SessionID: "claude_code:s1", OpportunityID: &opportunityB,
		Signal: canonical.SignalInvoked, Level: canonical.LevelInvoked,
	}); err != nil {
		t.Fatalf("matching asset participation: %v", err)
	}
}

func TestParticipationRejectsNonUTCOccurredAt(t *testing.T) {
	ctx := context.Background()
	tracker, db := testTracker(t)
	seedSession(t, db, "claude_code:s1")
	seedAsset(t, db, "skill:user:a")
	versionID := seedAssetVersion(t, db, "skill:user:a", 1)
	when := time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)
	if _, err := tracker.RecordParticipation(ctx, ParticipationInput{
		AssetVersionID: versionID, SessionID: "claude_code:s1", Signal: canonical.SignalInvoked,
		Level: canonical.LevelInvoked, OccurredAt: &when,
	}); err == nil {
		t.Fatal("non-UTC occurred_at must be rejected")
	}
}
