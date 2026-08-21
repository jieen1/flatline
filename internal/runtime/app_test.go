package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/assets"
	"flatline/internal/canonical"
	"flatline/internal/eventstore"
	"flatline/internal/storage"
	"flatline/internal/tracking"
	"flatline/internal/vital"
)

func TestEvaluateAllReachesSilentFromPersistedP3Facts(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	assetRegistry := assets.New(db)
	assetID, err := assetRegistry.Register(ctx, assets.AssetInput{Kind: assets.KindSkill, Scope: assets.ScopeProject, Name: "runtime-fixture", FirstSeenAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), SourcePath: "/synthetic/runtime/SKILL.md"})
	if err != nil {
		t.Fatalf("register asset: %v", err)
	}
	version, err := assetRegistry.RecordVersion(ctx, assets.VersionInput{AssetID: assetID, Content: []byte("synthetic runtime content\n"), ObservationLevel: canonical.LevelInvoked, ObservedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("record version: %v", err)
	}
	store := eventstore.New(db)
	tracker := tracking.New(db)
	for i := 0; i < 18; i++ {
		at := time.Date(2026, 7, 1+i, 12, 0, 0, 0, time.UTC)
		sessionID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{SourceSessionID: "runtime-" + string(rune('a'+i)), StartedAt: &at, HarnessVersion: "synthetic", Model: "synthetic"})
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
		if _, _, err := tracker.RecordSessionShape(ctx, tracking.SessionShape{SessionID: sessionID, Tags: []string{"sql", "migration"}, AssetIDs: []string{assetID}, DetectedAt: at}); err != nil {
			t.Fatalf("opportunity %d: %v", i, err)
		}
		if i < 10 {
			var opportunityID int64
			if err := db.QueryRowContext(ctx, `SELECT id FROM opportunities WHERE session_id = ? AND asset_id = ?`, sessionID, assetID).Scan(&opportunityID); err != nil {
				t.Fatalf("opportunity id %d: %v", i, err)
			}
			if _, err := tracker.RecordParticipation(ctx, tracking.ParticipationInput{AssetVersionID: version.ID, SessionID: sessionID, OpportunityID: &opportunityID, Signal: canonical.SignalInvoked, Level: canonical.LevelInvoked, OccurredAt: &at}); err != nil {
				t.Fatalf("participation %d: %v", i, err)
			}
		}
	}

	app := New(db, adapters.NewRegistry(), vital.DefaultConfig())
	decisions, err := app.EvaluateAll(ctx, time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("EvaluateAll: %v", err)
	}
	if len(decisions) != 1 || decisions[0].State != vital.StateSilent || !decisions[0].Alert {
		t.Fatalf("decisions = %+v, want one silent alert", decisions)
	}
	current, err := app.States().Current(ctx, assetID)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if current == nil || current.State != vital.StateSilent {
		t.Fatalf("current = %+v, want silent", current)
	}
}

func TestEvaluateAllKeepsNoOpportunitySeparateFromUnknown(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "runtime-empty.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	registry := assets.New(db)
	if _, err := registry.Register(ctx, assets.AssetInput{Kind: assets.KindSkill, Scope: assets.ScopeProject, Name: "empty", FirstSeenAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("register asset: %v", err)
	}
	app := New(db, adapters.NewRegistry(), vital.DefaultConfig())
	decisions, err := app.EvaluateAll(ctx, time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("EvaluateAll: %v", err)
	}
	if len(decisions) != 1 || decisions[0].State != vital.StateNoOpportunity {
		t.Fatalf("decisions = %+v, want no-opportunity state", decisions)
	}
}

func TestEvaluateAllUsesLatestRecordedTaskShape(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "runtime-shape.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	when := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	registry := assets.New(db)
	assetID, err := registry.Register(ctx, assets.AssetInput{
		Kind:        assets.KindSkill,
		Scope:       assets.ScopeProject,
		Name:        "latest-shape",
		FirstSeenAt: when.Add(-20 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("register asset: %v", err)
	}
	version, err := registry.RecordVersion(ctx, assets.VersionInput{
		AssetID:          assetID,
		Content:          []byte("synthetic latest-shape fixture\n"),
		ObservationLevel: canonical.LevelLoaded,
		ObservedAt:       when.Add(-20 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("record version: %v", err)
	}

	store := eventstore.New(db)
	tracker := tracking.New(db)
	record := func(index int, tags []string, at time.Time, participate bool) {
		t.Helper()
		sessionID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{
			SourceSessionID: fmt.Sprintf("shape-%02d", index),
			StartedAt:       &at,
		})
		if err != nil {
			t.Fatalf("session %d: %v", index, err)
		}
		if _, inserted, err := tracker.RecordSessionShape(ctx, tracking.SessionShape{SessionID: sessionID, Tags: tags, AssetIDs: []string{assetID}, DetectedAt: at}); err != nil || inserted != 1 {
			t.Fatalf("opportunity %d: inserted=%d err=%v", index, inserted, err)
		}
		if !participate {
			return
		}
		var opportunityID int64
		if err := db.QueryRowContext(ctx, `SELECT id FROM opportunities WHERE session_id = ? AND asset_id = ?`, sessionID, assetID).Scan(&opportunityID); err != nil {
			t.Fatalf("opportunity id %d: %v", index, err)
		}
		if _, err := tracker.RecordParticipation(ctx, tracking.ParticipationInput{
			AssetVersionID: version.ID,
			SessionID:      sessionID,
			OpportunityID:  &opportunityID,
			Signal:         canonical.SignalInvoked,
			Level:          canonical.LevelInvoked,
			OccurredAt:     &at,
		}); err != nil {
			t.Fatalf("participation %d: %v", index, err)
		}
	}

	oldTags := []string{"database", "migration"}
	newTags := []string{"frontend", "layout"}
	for i := 0; i < 10; i++ {
		record(i, oldTags, when.Add(-20*24*time.Hour+time.Duration(i)*24*time.Hour), true)
	}
	for i := 0; i < 8; i++ {
		record(10+i, newTags, when.Add(-8*24*time.Hour+time.Duration(i)*24*time.Hour), false)
	}

	app := New(db, adapters.NewRegistry(), vital.DefaultConfig())
	decisions, err := app.EvaluateAll(ctx, when)
	if err != nil {
		t.Fatalf("EvaluateAll: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("decisions = %+v, want one decision", decisions)
	}
	if decisions[0].State == vital.StateSilent {
		t.Fatalf("decision = %+v, old task shape incorrectly supplied the baseline", decisions[0])
	}
}

func TestEvaluateAllPersistsTemporalAlignmentOnTransition(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "runtime-alignment.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	when := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	registry := assets.New(db)
	assetID, err := registry.Register(ctx, assets.AssetInput{Kind: assets.KindSkill, Scope: assets.ScopeProject, Name: "alignment", FirstSeenAt: when})
	if err != nil {
		t.Fatalf("register asset: %v", err)
	}
	if _, err := registry.RecordVersion(ctx, assets.VersionInput{AssetID: assetID, Content: []byte("synthetic alignment fixture\n"), ObservationLevel: canonical.LevelLoaded, ObservedAt: when}); err != nil {
		t.Fatalf("record version: %v", err)
	}
	app := New(db, adapters.NewRegistry(), vital.DefaultConfig())
	if _, err := app.EvaluateAll(ctx, when); err != nil {
		t.Fatalf("EvaluateAll: %v", err)
	}
	var alignment string
	if err := db.QueryRowContext(ctx, `SELECT alignment_json FROM state_transitions WHERE asset_id = ?`, assetID).Scan(&alignment); err != nil {
		t.Fatalf("load alignment: %v", err)
	}
	if !strings.Contains(alignment, `"asset_version"`) {
		t.Fatalf("alignment = %s, want asset version anchor", alignment)
	}
}

func TestResurrectionRequiresExplicitFollowedEvidence(t *testing.T) {
	started := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	current := &vital.CurrentState{State: vital.StateAwaitingResurrection, StartedAt: started}
	opportunities := []opportunityFact{{ID: 1, SessionID: "claude_code:session", DetectedAt: started.Add(time.Hour), Participated: true, ParticipationKnown: true}}
	resurrected, failed := resurrectionOutcome(current, opportunities, vital.DefaultConfig())
	if resurrected || failed {
		t.Fatalf("invocation-only outcome = resurrected=%v failed=%v, want verification pending", resurrected, failed)
	}
	opportunities[0].Followed = true
	resurrected, failed = resurrectionOutcome(current, opportunities, vital.DefaultConfig())
	if !resurrected || failed {
		t.Fatalf("followed outcome = resurrected=%v failed=%v, want verification passed", resurrected, failed)
	}
}

func TestBypassVerdictUsesExactCanonicalEvidence(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "bypass.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	assetID, err := assets.New(db).Register(ctx, assets.AssetInput{Kind: assets.KindSkill, Scope: assets.ScopeProject, Name: "bypass-fixture", FirstSeenAt: time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("register asset: %v", err)
	}
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	store := eventstore.New(db)
	sessionID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{SourceSessionID: "bypass", StartedAt: &at})
	if err != nil {
		t.Fatalf("ingest session: %v", err)
	}
	violationAt := at.Add(time.Minute)
	if _, err := store.IngestEvents(ctx, sessionID, []canonical.Event{
		{SourceEventID: "invoke", SessionID: sessionID, EventType: canonical.EventTypeAssetInvoked, AssetID: assetID, ObservationLevel: canonical.LevelInvoked, Payload: map[string]any{}, Locator: canonical.Locator{Source: "claude_code", SessionID: sessionID, RawRef: "invoke"}, OccurredAt: &at},
		{SourceEventID: "violate", SessionID: sessionID, EventType: canonical.EventTypeAssetViolation, AssetID: assetID, ObservationLevel: canonical.LevelInvoked, Payload: map[string]any{"violated": true}, Locator: canonical.Locator{Source: "claude_code", SessionID: sessionID, RawRef: "violate"}, OccurredAt: &violationAt},
	}); err != nil {
		t.Fatalf("ingest evidence: %v", err)
	}
	verdict, err := New(db, adapters.NewRegistry(), vital.DefaultConfig()).bypassVerdict(ctx, assetID, violationAt)
	if err != nil {
		t.Fatalf("bypass verdict: %v", err)
	}
	if !verdict.Triggered || !verdict.Observable || verdict.ReasonCode != "invoked_then_violated" {
		t.Fatalf("bypass verdict = %+v, want exact triggered verdict", verdict)
	}
}
