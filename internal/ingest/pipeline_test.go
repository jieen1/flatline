package ingest

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/adapters/claudecode"
	"flatline/internal/assets"
	"flatline/internal/storage"
)

func testPipeline(t *testing.T) (*Pipeline, *storage.DB) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "ingest.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registry := adapters.NewRegistry()
	if err := registry.Register(claudecode.New()); err != nil {
		t.Fatalf("register Claude Code adapter: %v", err)
	}
	return NewPipeline(db, registry), db
}

func rawFixture(t *testing.T) []byte {
	return rawFixtureWithSession(t, "ingest-fixture-1")
}

func rawFixtureWithSession(t *testing.T, sessionID string) []byte {
	return rawFixtureWithAsset(t, sessionID, "skill:synthetic:fixture")
}

func rawFixtureWithAsset(t *testing.T, sessionID, assetID string) []byte {
	t.Helper()
	fixture := map[string]any{
		"session": map[string]any{
			"id": sessionID, "started_at": "2026-08-20T09:00:00Z", "ended_at": "2026-08-20T09:03:00Z",
			"harness_version": "2.14.0", "model": "synthetic-model", "cwd": "/synthetic/project",
		},
		"messages": []any{
			map[string]any{"id": "message-1", "timestamp": "2026-08-20T09:01:00Z", "role": "assistant", "asset_invocations": []any{
				map[string]any{"asset_id": assetID},
			}},
		},
	}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return data
}

func TestPipelineResolvesLatestVersionForStableAssetReplay(t *testing.T) {
	pipeline, db := testPipeline(t)
	first := SessionInput{Raw: adapters.RawSession{Source: adapters.SourceClaudeCode, RawJSON: rawFixtureWithSession(t, "version-seed")}, Assets: []AssetObservation{fixtureAsset()}}
	if _, err := pipeline.Ingest(context.Background(), first); err != nil {
		t.Fatalf("seed Ingest: %v", err)
	}
	replay := SessionInput{Raw: adapters.RawSession{Source: adapters.SourceClaudeCode, RawJSON: rawFixtureWithAsset(t, "version-replay", "skill:project:fixture")}}
	report, err := pipeline.Ingest(context.Background(), replay)
	if err != nil {
		t.Fatalf("stable replay Ingest: %v", err)
	}
	if report.UnresolvedAssetVersions != 0 || report.ParticipationsInserted != 1 {
		t.Fatalf("replay report = %+v, want latest version resolution", report)
	}
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM participations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("participations = %d, want 2", count)
	}
}

func fixtureAsset() AssetObservation {
	observed := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	return AssetObservation{
		SourceAssetID: "skill:synthetic:fixture",
		Asset: assets.AssetInput{
			Kind: assets.KindSkill, Scope: assets.ScopeProject, Name: "fixture",
			SourcePath: "/synthetic/project/.flatline/fixture/SKILL.md", FirstSeenAt: observed,
		},
		Content:          []byte("synthetic fixture asset content\n"),
		ObservationLevel: "invoked",
		ObservedAt:       observed,
		ContentRef:       "fixture:asset:skill-project-fixture:v1",
	}
}

func TestPipelineIngestsFactsAndP3DerivationsIdempotently(t *testing.T) {
	pipeline, db := testPipeline(t)
	input := SessionInput{
		Raw:      adapters.RawSession{Source: adapters.SourceClaudeCode, RawJSON: rawFixture(t), SourcePath: "fixture:claudecode:ingest-1"},
		TaskTags: []string{"SQL migrations", "deploy"},
		Assets:   []AssetObservation{fixtureAsset()},
	}

	report, err := pipeline.Ingest(context.Background(), input)
	if err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	if report.SessionID != "claude_code:ingest-fixture-1" || report.EventsInserted != 2 || report.OpportunitiesInserted != 1 || report.ParticipationsInserted != 1 || report.AssetVersionsCreated != 1 {
		t.Fatalf("first report = %+v", report)
	}
	if !report.ShapeRecorded || report.UnresolvedAssetVersions != 0 {
		t.Fatalf("first report shape/version = %+v", report)
	}

	replay, err := pipeline.Ingest(context.Background(), input)
	if err != nil {
		t.Fatalf("replay Ingest: %v", err)
	}
	if replay.EventsInserted != 0 || replay.OpportunitiesInserted != 0 || replay.ParticipationsInserted != 0 || replay.AssetVersionsCreated != 0 {
		t.Fatalf("replay report = %+v, want zero inserts", replay)
	}

	var facts, opportunities, participations, versions, bundles int
	for query, target := range map[string]*int{
		"SELECT COUNT(*) FROM events":            &facts,
		"SELECT COUNT(*) FROM opportunities":     &opportunities,
		"SELECT COUNT(*) FROM participations":    &participations,
		"SELECT COUNT(*) FROM asset_versions":    &versions,
		"SELECT COUNT(*) FROM effective_bundles": &bundles,
	} {
		if err := db.QueryRowContext(context.Background(), query).Scan(target); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
	}
	if facts != 2 || opportunities != 1 || participations != 1 || versions != 1 || bundles != 1 {
		t.Fatalf("stored counts = facts %d opportunities %d participations %d versions %d bundles %d", facts, opportunities, participations, versions, bundles)
	}
}

func TestPipelineKeepsMissingShapeExplicit(t *testing.T) {
	pipeline, db := testPipeline(t)
	report, err := pipeline.Ingest(context.Background(), SessionInput{
		Raw:    adapters.RawSession{Source: adapters.SourceClaudeCode, RawJSON: rawFixture(t)},
		Assets: []AssetObservation{fixtureAsset()},
	})
	if err != nil {
		t.Fatalf("Ingest without task shape: %v", err)
	}
	if report.ShapeRecorded || report.ShapeReason == "" {
		t.Fatalf("shape report = %+v, want explicit missing shape", report)
	}
	var opportunities int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM opportunities`).Scan(&opportunities); err != nil {
		t.Fatalf("opportunity count: %v", err)
	}
	if opportunities != 0 {
		t.Fatalf("opportunities = %d, want 0 for missing shape", opportunities)
	}
}

func TestPipelineRejectsUnmappedAssetBeforeWritingFacts(t *testing.T) {
	pipeline, db := testPipeline(t)
	input := SessionInput{
		Raw: adapters.RawSession{Source: adapters.SourceClaudeCode, RawJSON: rawFixture(t)},
		Assets: []AssetObservation{{
			SourceAssetID: "different-source-id", Asset: fixtureAsset().Asset,
			Content: []byte("synthetic fixture asset content\n"), ObservationLevel: "invoked", ObservedAt: fixtureAsset().ObservedAt,
		}},
	}
	if _, err := pipeline.Ingest(context.Background(), input); err == nil {
		t.Fatal("unmapped asset Ingest = nil, want error")
	}
	var sessions, events, assetsCount int
	for query, target := range map[string]*int{
		"SELECT COUNT(*) FROM sessions": &sessions,
		"SELECT COUNT(*) FROM events":   &events,
		"SELECT COUNT(*) FROM assets":   &assetsCount,
	} {
		if err := db.QueryRowContext(context.Background(), query).Scan(target); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
	}
	if sessions != 0 || events != 0 || assetsCount != 0 {
		t.Fatalf("partial writes after rejected mapping: sessions=%d events=%d assets=%d", sessions, events, assetsCount)
	}
}
