package vital

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flatline/internal/assets"
	"flatline/internal/canonical"
	"flatline/internal/detectors"
	"flatline/internal/storage"
)

func testVitalRepository(t *testing.T) (*Repository, *storage.DB) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "vital.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registry := assets.New(db)
	firstSeen := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if _, err := registry.Register(context.Background(), assets.AssetInput{
		Kind: assets.KindSkill, Scope: assets.ScopeProject, Name: "fixture",
		SourcePath: "/synthetic/fixture/SKILL.md", FirstSeenAt: firstSeen,
	}); err != nil {
		t.Fatalf("register asset: %v", err)
	}
	machine := NewMachine(DefaultConfig())
	return NewRepository(db, machine), db
}

func repositoryAssessment(day int) Assessment {
	return Assessment{
		AssetID:               "skill:project:fixture",
		At:                    time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC),
		HasOpportunity:        true,
		HasBaseline:           true,
		ParticipationObserved: true,
		Silent:                detectors.Verdict{Detector: detectors.SilentDetector, Observable: true, Summary: "synthetic fixture: not silent", Rule: "fixture rule"},
		Degraded:              detectors.Verdict{Detector: detectors.DegradedDetector, Observable: true, Summary: "synthetic fixture: not degraded", Rule: "fixture rule"},
		Broken:                detectors.ReferenceVerdict{Verdict: detectors.Verdict{Detector: detectors.ReferenceDetector, Observable: true, Summary: "synthetic fixture: references present", Rule: "fixture rule"}},
		Bypassed:              detectors.Verdict{Detector: detectors.BypassDetector, Observable: true, Summary: "synthetic fixture: not bypassed", Rule: "fixture rule"},
		Dormant:               detectors.Verdict{Detector: detectors.DormantDetector, Observable: true, Summary: "synthetic fixture: not dormant", Rule: "fixture rule"},
	}
}

func repositoryIntPointer(value int) *int { return &value }

func TestRepositoryPersistsOneOpenStateAndReplayIsIdempotent(t *testing.T) {
	repository, db := testVitalRepository(t)
	ctx := context.Background()

	// ADR-26: the fixture assessment carries participation, so the first
	// evaluation lands on healthy directly — and a first-ever state never
	// alerts, because nothing was transitioned from.
	decision, err := repository.Apply(ctx, repositoryAssessment(1))
	if err != nil {
		t.Fatalf("initial Apply: %v", err)
	}
	if decision.State != StateHealthy || !decision.Transition || decision.Alert {
		t.Fatalf("initial decision = %+v, want a healthy start without an alert", decision)
	}

	silent := repositoryAssessment(2)
	silent.Silent = detectors.Verdict{Detector: detectors.SilentDetector, Triggered: true, Observable: true,
		Summary: "synthetic fixture: went silent", Rule: "fixture rule"}
	decision, err = repository.Apply(ctx, silent)
	if err != nil {
		t.Fatalf("silent Apply: %v", err)
	}
	if decision.State != StateSilent || !decision.Transition {
		t.Fatalf("silent decision = %+v, want silent transition", decision)
	}

	decision, err = repository.Apply(ctx, repositoryAssessment(3))
	if err != nil {
		t.Fatalf("healthy Apply: %v", err)
	}
	if decision.State != StateHealthy || !decision.Transition {
		t.Fatalf("healthy decision = %+v, want healthy transition", decision)
	}

	// Re-evaluating the same state updates evidence but does not create a
	// duplicate alert/transition.
	decision, err = repository.Apply(ctx, repositoryAssessment(3))
	if err != nil {
		t.Fatalf("same-state replay: %v", err)
	}
	if decision.Transition || decision.Alert {
		t.Fatalf("same-state decision = %+v, want no transition", decision)
	}

	var open, transitions int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vital_states WHERE asset_id = ? AND ended_at IS NULL`, "skill:project:fixture").Scan(&open); err != nil {
		t.Fatalf("open state count: %v", err)
	}
	if open != 1 {
		t.Fatalf("open state count = %d, want 1", open)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM state_transitions WHERE asset_id = ?`, "skill:project:fixture").Scan(&transitions); err != nil {
		t.Fatalf("transition count: %v", err)
	}
	if transitions != 3 {
		t.Fatalf("transition count = %d, want 3", transitions)
	}
	var evidence string
	if err := db.QueryRowContext(ctx, `SELECT evidence_json FROM state_transitions WHERE asset_id = ? ORDER BY id DESC LIMIT 1`, "skill:project:fixture").Scan(&evidence); err != nil {
		t.Fatalf("transition evidence: %v", err)
	}
	if !strings.Contains(evidence, `"decision"`) || !strings.Contains(evidence, `"rule"`) {
		t.Fatalf("transition evidence = %s, want persisted decision envelope", evidence)
	}
}

func TestRepositoryPersistsBrokenOverlayAndRecovery(t *testing.T) {
	repository, db := testVitalRepository(t)
	ctx := context.Background()
	if _, err := repository.Apply(ctx, repositoryAssessment(1)); err != nil {
		t.Fatalf("initial Apply: %v", err)
	}
	if _, err := repository.Apply(ctx, repositoryAssessment(2)); err != nil {
		t.Fatalf("healthy Apply: %v", err)
	}

	broken := repositoryAssessment(3)
	broken.Broken = detectors.ReferenceVerdict{Verdict: detectors.Verdict{Detector: detectors.ReferenceDetector, Triggered: true, Observable: true, Summary: "synthetic fixture: one reference is missing", Rule: "1 of 1 checked references is missing", Evidence: detectors.Evidence{Numerator: repositoryIntPointer(1), Denominator: repositoryIntPointer(1), ObservationLevels: []canonical.ObservationLevel{canonical.LevelInvoked}}}, Failed: 1, Checked: 1}
	decision, err := repository.Apply(ctx, broken)
	if err != nil {
		t.Fatalf("broken Apply: %v", err)
	}
	if decision.State != StateHealthy || !decision.BrokenOverlay || !decision.Alert {
		t.Fatalf("broken decision = %+v, want healthy + overlay", decision)
	}

	decision, err = repository.Apply(ctx, broken)
	if err != nil {
		t.Fatalf("repeated broken Apply: %v", err)
	}
	if decision.Alert || decision.Transition {
		t.Fatalf("repeated broken decision = %+v, want no duplicate transition", decision)
	}

	recovered := repositoryAssessment(4)
	decision, err = repository.Apply(ctx, recovered)
	if err != nil {
		t.Fatalf("recovery Apply: %v", err)
	}
	if decision.BrokenOverlay || !decision.Alert {
		t.Fatalf("recovery decision = %+v, want overlay recovery alert", decision)
	}

	var ended int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vital_states WHERE asset_id = ? AND ended_at IS NOT NULL`, "skill:project:fixture").Scan(&ended); err != nil {
		t.Fatalf("ended state count: %v", err)
	}
	// ADR-26: the first evaluation lands on healthy, so the second same-state
	// apply opens no row — the history is healthy → healthy+overlay →
	// healthy, two rows ended by their successors.
	if ended != 2 {
		t.Fatalf("ended state count = %d, want 2", ended)
	}
}

func TestRepositoryUnknownAssetReturnsForeignKeyError(t *testing.T) {
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "unknown.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	repository := NewRepository(db, NewMachine(DefaultConfig()))
	_, err = repository.Apply(context.Background(), Assessment{AssetID: "skill:project:missing", At: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)})
	if err == nil {
		t.Fatal("Apply missing asset = nil, want error")
	}
	if err == sql.ErrNoRows {
		t.Fatal("Apply missing asset returned bare sql.ErrNoRows")
	}
}
