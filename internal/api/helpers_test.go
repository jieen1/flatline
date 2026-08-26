package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func testAPIDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	firstSeen := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	registry := assets.New(db)
	assetID, err := registry.Register(ctx, assets.AssetInput{Kind: assets.KindSkill, Scope: assets.ScopeProject, Name: "fixture", SourcePath: "/synthetic/fixture/SKILL.md", FirstSeenAt: firstSeen})
	if err != nil {
		t.Fatalf("register asset: %v", err)
	}
	if _, err := registry.RecordVersion(ctx, assets.VersionInput{AssetID: assetID, Content: []byte("synthetic api fixture\n"), ObservationLevel: canonical.LevelInvoked, ObservedAt: firstSeen, ContentRef: "fixture:asset:v1"}); err != nil {
		t.Fatalf("record version: %v", err)
	}
	store := eventstore.New(db)
	sessionID, err := store.IngestSession(ctx, adapters.SourceClaudeCode, adapters.SessionMeta{SourceSessionID: "api-fixture", StartedAt: &firstSeen, Model: "synthetic-model", Title: "API 会话标题", TaskText: "检查 API 资产证据"})
	if err != nil {
		t.Fatalf("ingest session: %v", err)
	}
	if _, err := store.IngestEvents(ctx, sessionID, []canonical.Event{{
		SourceEventID: "api-fixture-transcript", SessionID: sessionID, EventType: canonical.EventTypeTranscriptMessage,
		ObservationLevel: canonical.LevelUnknown, Payload: map[string]any{"role": "user", "text": "检查 API 资产证据"},
		Locator: canonical.Locator{Source: string(adapters.SourceClaudeCode), SessionID: sessionID, RawRef: "fixture:message"}, OccurredAt: &firstSeen,
	}}); err != nil {
		t.Fatalf("ingest transcript event: %v", err)
	}
	if err := store.ReplaceRuleTags(ctx, sessionID, []string{"analysis", "workspace-fixture"}); err != nil {
		t.Fatalf("replace rule tags: %v", err)
	}
	if err := store.RecomputeSessionStats(ctx, sessionID); err != nil {
		t.Fatalf("recompute session stats: %v", err)
	}
	tracker := tracking.New(db)
	if _, _, err := tracker.RecordSessionShape(ctx, tracking.SessionShape{SessionID: sessionID, Tags: []string{"fixture"}, AssetIDs: []string{assetID}, DetectedAt: firstSeen}); err != nil {
		t.Fatalf("record opportunity: %v", err)
	}
	var versionID, opportunityID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM asset_versions WHERE asset_id = ?`, assetID).Scan(&versionID); err != nil {
		t.Fatalf("version id: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT id FROM opportunities WHERE asset_id = ?`, assetID).Scan(&opportunityID); err != nil {
		t.Fatalf("opportunity id: %v", err)
	}
	if _, err := tracker.RecordParticipation(ctx, tracking.ParticipationInput{AssetVersionID: versionID, SessionID: sessionID, OpportunityID: &opportunityID, Signal: canonical.SignalInvoked, Level: canonical.LevelInvoked, OccurredAt: &firstSeen}); err != nil {
		t.Fatalf("record participation: %v", err)
	}
	if _, err := vital.NewRepository(db, vital.NewMachine(vital.DefaultConfig())).Apply(ctx, vital.Assessment{AssetID: assetID, At: firstSeen, HasOpportunity: true, HasBaseline: true, ParticipationObserved: true}); err != nil {
		t.Fatalf("record vital state: %v", err)
	}
	return db
}

func timePtr(value time.Time) *time.Time { return &value }

func getJSON(t *testing.T, handler http.Handler, path string, target any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, body=%s", path, rec.Code, rec.Body.String())
	}
	if target != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return rec
}

func exec(t *testing.T, db *storage.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func urlEscape(value string) string {
	return strings.ReplaceAll(value, "/", "%2F")
}

func strPtr(value string) *string { return &value }

func int64Ptr(value int64) *int64 { return &value }
