package baseline

import (
	"context"
	"database/sql"
	"testing"
)

func TestResolveUsesLatestVersionAtSessionStart(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	seedAsset(t, db, "skill:user:demo", "skill", "demo")
	seedVersion(t, db, "skill:user:demo", 1, "sha256:v1", "2026-08-01T00:00:00Z")
	version2 := seedVersion(t, db, "skill:user:demo", 2, "sha256:v2", "2026-08-10T00:00:00Z")
	seedVersion(t, db, "skill:user:demo", 3, "sha256:v3", "2026-08-30T00:00:00Z")
	seedSession(t, db, "claude_code:s1", "claude_code", "2026-08-15T12:00:00Z")

	resolver := NewResolver(db)
	bundle, err := resolver.Resolve(ctx, "claude_code:s1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	entry, ok := bundle.EntryFor("skill:user:demo")
	if !ok {
		t.Fatal("bundle has no entry for demo asset")
	}
	if entry.AssetVersionID != version2 || entry.Version != 2 || entry.ContentHash != "sha256:v2" {
		t.Fatalf("effective entry = %+v, want version 2 (id %d)", entry, version2)
	}

	loaded, err := resolver.Load(ctx, "claude_code:s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Entries) != 1 {
		t.Fatalf("loaded entries = %d, want 1", len(loaded.Entries))
	}
	loadedEntry, _ := loaded.EntryFor("skill:user:demo")
	if loadedEntry.AssetVersionID != version2 {
		t.Fatalf("loaded effective version id = %d, want %d", loadedEntry.AssetVersionID, version2)
	}
}

func TestResolveAsOfOmitsVersionsNotYetObserved(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	seedAsset(t, db, "skill:user:future", "skill", "future")
	seedVersion(t, db, "skill:user:future", 1, "sha256:future", "2026-08-20T00:00:00Z")
	seedSession(t, db, "codex:s1", "codex", "2026-08-01T00:00:00Z")

	bundle, err := NewResolver(db).ResolveAsOf(ctx, "codex:s1", ts("2026-08-10T00:00:00Z"))
	if err != nil {
		t.Fatalf("ResolveAsOf: %v", err)
	}
	if len(bundle.Entries) != 0 {
		t.Fatalf("entries = %d, want 0 before first observation", len(bundle.Entries))
	}
}

func TestResolveRequiresRecordedSessionStart(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "claude_code:no-start", "claude_code", "")
	if _, err := NewResolver(db).Resolve(context.Background(), "claude_code:no-start"); err == nil {
		t.Fatal("Resolve without started_at: expected error")
	}
}

func TestLoadUnknownBundleReturnsNoRows(t *testing.T) {
	db := testDB(t)
	if _, err := NewResolver(db).Load(context.Background(), "claude_code:missing"); err != sql.ErrNoRows {
		t.Fatalf("Load unknown error = %v, want sql.ErrNoRows", err)
	}
}

func TestBaselineQueryExposesNumeratorDenominatorAndSessions(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	assetID := "skill:user:demo"
	seedAsset(t, db, assetID, "skill", "demo")
	versionID := seedVersion(t, db, assetID, 1, "sha256:demo", "2026-07-01T00:00:00Z")

	for i, sessionID := range []string{"claude_code:a", "claude_code:b", "claude_code:c"} {
		seedSession(t, db, sessionID, "claude_code", "2026-08-01T00:00:00Z")
		day := []string{"2026-08-01T00:00:00Z", "2026-08-02T00:00:00Z", "2026-08-03T00:00:00Z"}[i]
		seedOpportunity(t, db, sessionID, "shape/1:sql", assetID, day)
	}
	seedParticipation(t, db, versionID, "claude_code:a", "invoked", "invoked", "2026-08-01T00:01:00Z")
	seedParticipation(t, db, versionID, "claude_code:c", "followed", "unknown", "2026-08-03T00:01:00Z")

	result, err := NewQuery(db).Compute(ctx, assetID, "shape/1:sql", ts("2026-08-01T00:00:00Z"), ts("2026-08-04T00:00:00Z"))
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if result.OpportunitySessions != 3 || result.ParticipatingSessions != 2 {
		t.Fatalf("counts = %d/%d, want 2/3", result.ParticipatingSessions, result.OpportunitySessions)
	}
	if result.Rate == nil || *result.Rate != 2.0/3.0 {
		t.Fatalf("rate = %v, want 2/3", result.Rate)
	}
	if len(result.OpportunitySessionIDs) != 3 || len(result.ParticipatingSessionIDs) != 2 {
		t.Fatalf("session ids = %v/%v, want 3/2", result.ParticipatingSessionIDs, result.OpportunitySessionIDs)
	}
	if result.ShapeRuleVersion != "shape/1" || result.BaselineVersion != BaselineVersion {
		t.Fatalf("replay metadata = %#v", result)
	}

	empty, err := NewQuery(db).Compute(ctx, assetID, "shape/1:missing", ts("2026-08-01T00:00:00Z"), ts("2026-08-04T00:00:00Z"))
	if err != nil {
		t.Fatalf("empty Compute: %v", err)
	}
	if empty.Rate != nil || empty.OpportunitySessions != 0 || empty.ParticipatingSessions != 0 {
		t.Fatalf("empty baseline = %#v, want nil rate and 0/0", empty)
	}
}
