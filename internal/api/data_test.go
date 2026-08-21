package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/assets"
	"flatline/internal/canonical"
	"flatline/internal/detectors"
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

func TestDataAPIListsAndDrillsIntoAssetEvidence(t *testing.T) {
	db := testAPIDB(t)
	handler := NewServerWithDB(db).Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("assets status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var list struct {
		Assets []struct {
			ID           string `json:"id"`
			StateStatus  string `json:"state_status"`
			CurrentState *struct {
				State string `json:"state"`
			} `json:"current_state"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode assets: %v", err)
	}
	if len(list.Assets) != 1 || list.Assets[0].ID != "skill:project:fixture" || list.Assets[0].StateStatus != "evaluated" || list.Assets[0].CurrentState == nil || list.Assets[0].CurrentState.State != "dormant" {
		t.Fatalf("asset list = %+v", list.Assets)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/assets/skill:project:fixture", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset detail status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Asset struct {
			ID string `json:"id"`
		} `json:"asset"`
		Versions        []json.RawMessage `json:"versions"`
		Opportunities   []json.RawMessage `json:"opportunities"`
		Participations  []json.RawMessage `json:"participations"`
		RelatedSessions []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			TaskText string `json:"task_text"`
		} `json:"related_sessions"`
		Transitions []json.RawMessage `json:"transitions"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Asset.ID != "skill:project:fixture" || len(detail.Versions) != 1 || len(detail.Opportunities) != 1 || len(detail.Participations) != 1 || len(detail.RelatedSessions) != 1 || detail.RelatedSessions[0].ID != "claude_code:api-fixture" || detail.RelatedSessions[0].Title != "API 会话标题" || detail.RelatedSessions[0].TaskText != "检查 API 资产证据" || len(detail.Transitions) != 1 {
		t.Fatalf("asset detail = asset=%q versions=%d opportunities=%d participations=%d related_sessions=%+v transitions=%d", detail.Asset.ID, len(detail.Versions), len(detail.Opportunities), len(detail.Participations), detail.RelatedSessions, len(detail.Transitions))
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var sessionList struct {
		Sessions []struct {
			Title           string `json:"title"`
			TaskText        string `json:"task_text"`
			TranscriptCount int    `json:"transcript_count"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&sessionList); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(sessionList.Sessions) != 1 || sessionList.Sessions[0].Title != "API 会话标题" || sessionList.Sessions[0].TaskText != "检查 API 资产证据" || sessionList.Sessions[0].TranscriptCount != 1 {
		t.Fatalf("sessions = %+v", sessionList.Sessions)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions?summary=1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session summary status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var sessionSummary struct {
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&sessionSummary); err != nil {
		t.Fatalf("decode session summary: %v", err)
	}
	if len(sessionSummary.Sessions) != 1 {
		t.Fatalf("session summary count = %d, want 1", len(sessionSummary.Sessions))
	}
	if _, ok := sessionSummary.Sessions[0]["event_count"]; ok {
		t.Fatal("session summary unexpectedly contains event_count")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/claude_code:api-fixture", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session detail status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var sessionDetail struct {
		Session struct {
			ID              string `json:"id"`
			Title           string `json:"title"`
			TaskText        string `json:"task_text"`
			TranscriptCount int    `json:"transcript_count"`
		} `json:"session"`
		Events []json.RawMessage `json:"events"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&sessionDetail); err != nil {
		t.Fatalf("decode session detail: %v", err)
	}
	if sessionDetail.Session.ID != "claude_code:api-fixture" || sessionDetail.Session.Title != "API 会话标题" || sessionDetail.Session.TaskText != "检查 API 资产证据" || sessionDetail.Session.TranscriptCount != 1 || len(sessionDetail.Events) != 1 {
		t.Fatalf("session detail = %+v events=%d", sessionDetail.Session, len(sessionDetail.Events))
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/claude_code:api-fixture?events=page&limit=1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("paged session detail status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var pagedSession struct {
		Events        []map[string]json.RawMessage `json:"events"`
		EventOffset   int                          `json:"event_offset"`
		EventLimit    int                          `json:"event_limit"`
		EventTotal    int                          `json:"event_total"`
		EventsHasMore bool                         `json:"events_has_more"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&pagedSession); err != nil {
		t.Fatalf("decode paged session detail: %v", err)
	}
	if len(pagedSession.Events) != 1 || pagedSession.EventOffset != 0 || pagedSession.EventLimit != 1 || pagedSession.EventTotal != 1 || pagedSession.EventsHasMore {
		t.Fatalf("paged session = events=%d offset=%d limit=%d total=%d has_more=%v", len(pagedSession.Events), pagedSession.EventOffset, pagedSession.EventLimit, pagedSession.EventTotal, pagedSession.EventsHasMore)
	}
	if _, ok := pagedSession.Events[0]["payload_truncated"]; ok {
		t.Fatal("small paged event unexpectedly marked payload_truncated")
	}

	var eventID int64
	if err := db.QueryRowContext(context.Background(), `SELECT id FROM events WHERE session_id = ? ORDER BY id LIMIT 1`, "claude_code:api-fixture").Scan(&eventID); err != nil {
		t.Fatalf("query fixture event id: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/claude_code:api-fixture/events/"+strconv.FormatInt(eventID, 10), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session event status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var eventDetail struct {
		Event map[string]json.RawMessage `json:"event"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&eventDetail); err != nil {
		t.Fatalf("decode session event: %v", err)
	}
	if string(eventDetail.Event["id"]) != strconv.FormatInt(eventID, 10) {
		t.Fatalf("session event id = %s, want %d", eventDetail.Event["id"], eventID)
	}
}

func TestSessionDetailExposesSessionWideFrictionProjection(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	store := eventstore.New(db)
	sessionID := "claude_code:api-fixture"
	failureAt := time.Date(2026, 7, 1, 12, 0, 2, 0, time.UTC)
	event := canonical.Event{
		SourceEventID: "api-fixture-tool-error", SessionID: sessionID, EventType: canonical.EventTypeTranscriptResult,
		ObservationLevel: canonical.LevelUnknown, Payload: map[string]any{"role": "tool", "tool_output": "permission denied", "is_error": true},
		Locator: canonical.Locator{Source: string(adapters.SourceClaudeCode), SessionID: sessionID, RawRef: "fixture:tool-result"}, OccurredAt: &failureAt,
	}
	if _, err := store.IngestEvents(ctx, sessionID, []canonical.Event{event}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IngestFriction(ctx, sessionID, []canonical.Event{event}); err != nil {
		t.Fatal(err)
	}
	handler := NewServerWithDB(db).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/claude_code:api-fixture?events=page&limit=1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("session detail status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	var friction map[string]json.RawMessage
	if err := json.Unmarshal(response["friction"], &friction); err != nil {
		t.Fatal(err)
	}
	var count int
	var complete bool
	var records []map[string]json.RawMessage
	var events []json.RawMessage
	if err := json.Unmarshal(friction["count"], &count); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(friction["complete"], &complete); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(friction["records"], &records); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(response["events"], &events); err != nil {
		t.Fatal(err)
	}
	var sourceEventID string
	var isError bool
	if len(records) == 1 {
		_ = json.Unmarshal(records[0]["source_event_id"], &sourceEventID)
		_ = json.Unmarshal(records[0]["is_error"], &isError)
	}
	if count != 1 || !complete || len(records) != 1 || sourceEventID != event.SourceEventID || !isError || len(events) != 1 {
		t.Fatalf("friction count=%d complete=%v records=%v events=%d", count, complete, records, len(events))
	}
}

func TestDataAPIWallProjectionOmitsDetailEvidenceAndBoundsSharedMarkers(t *testing.T) {
	db := testAPIDB(t)
	handler := NewServerWithDB(db).Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets?view=wall", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("wall assets status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var wall struct {
		Assets []struct {
			CurrentState map[string]json.RawMessage `json:"current_state"`
			Facts        struct {
				ChangeMarkers []json.RawMessage `json:"change_markers"`
			} `json:"facts"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&wall); err != nil {
		t.Fatalf("decode wall assets: %v", err)
	}
	if len(wall.Assets) != 1 {
		t.Fatalf("wall assets = %d, want 1", len(wall.Assets))
	}
	if _, ok := wall.Assets[0].CurrentState["evidence"]; ok {
		t.Fatal("wall current_state unexpectedly contains detail evidence")
	}
	if _, ok := wall.Assets[0].CurrentState["baseline"]; ok {
		t.Fatal("wall current_state unexpectedly contains detail baseline")
	}
}

func TestCompactEnvironmentMarkersPreservesRange(t *testing.T) {
	markers := make([]changeMarker, 40)
	for index := range markers {
		markers[index] = changeMarker{At: time.Unix(int64(index), 0).UTC(), Kind: "environment"}
	}
	compact := compactEnvironmentMarkers(markers, 16)
	if len(compact) != 16 {
		t.Fatalf("compact marker count = %d, want 16", len(compact))
	}
	if !compact[0].At.Equal(markers[0].At) || !compact[len(compact)-1].At.Equal(markers[len(markers)-1].At) {
		t.Fatalf("compact marker range = %v..%v, want %v..%v", compact[0].At, compact[len(compact)-1].At, markers[0].At, markers[len(markers)-1].At)
	}
	for index := 1; index < len(compact); index++ {
		if compact[index].At.Before(compact[index-1].At) {
			t.Fatalf("compact markers out of order at %d", index)
		}
	}
}

func TestDataAPIRelatedSessionsIncludeOpportunityWithoutParticipation(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	assetID := "skill:project:fixture"
	startedAt := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (id, source, source_session_id, title, task_text, started_at)
		VALUES (?, ?, ?, ?, ?, ?)`, "codex:opportunity-only", adapters.SourceCodex, "opportunity-only", "未记录参与的真实任务", "检查 fixture 资产", startedAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert opportunity-only session: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO opportunities (session_id, shape_class, shape_rule_version, asset_id, detector_version, detected_at)
		VALUES (?, ?, ?, ?, ?, ?)`, "codex:opportunity-only", "same-task-shape", "shape/fixture", assetID, "tracker/fixture", startedAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert opportunity-only record: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+assetID, nil)
	rec := httptest.NewRecorder()
	NewServerWithDB(db).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset detail status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Opportunities []struct {
			SessionID string `json:"session_id"`
		} `json:"opportunities"`
		Participations []struct {
			SessionID string `json:"session_id"`
		} `json:"participations"`
		RelatedSessions []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			TaskText string `json:"task_text"`
		} `json:"related_sessions"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode asset detail: %v", err)
	}
	if len(detail.Opportunities) != 2 || len(detail.Participations) != 1 {
		t.Fatalf("evidence counts = opportunities=%d participations=%d", len(detail.Opportunities), len(detail.Participations))
	}
	var found bool
	for _, session := range detail.RelatedSessions {
		if session.ID == "codex:opportunity-only" {
			found = session.Title == "未记录参与的真实任务" && session.TaskText == "检查 fixture 资产"
		}
	}
	if !found {
		t.Fatalf("related sessions = %+v, want opportunity-only session with task metadata", detail.RelatedSessions)
	}
}

func TestDataAPISourcePreviewReturnsExactCurrentContentHash(t *testing.T) {
	db := testAPIDB(t)
	content := []byte("# API source fixture\n")
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}
	if _, err := db.Exec(`UPDATE assets SET source_path = ? WHERE id = ?`, path, "skill:project:fixture"); err != nil {
		t.Fatalf("update source path: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/skill:project:fixture/source", nil)
	rec := httptest.NewRecorder()
	NewServerWithDB(db).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("source status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var source struct {
		Content     string `json:"content"`
		ContentHash string `json:"content_hash"`
		Truncated   bool   `json:"truncated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&source); err != nil {
		t.Fatalf("decode source: %v", err)
	}
	sum := sha256.Sum256(content)
	wantHash := "sha256:" + hex.EncodeToString(sum[:])
	if source.Content != string(content) || source.ContentHash != wantHash || source.Truncated {
		t.Fatalf("source = %+v, want content and hash %q", source, wantHash)
	}
}

func TestDataAPISourcePreviewHashesFullFileWhenPreviewIsTruncated(t *testing.T) {
	db := testAPIDB(t)
	content := append(bytes.Repeat([]byte("x"), (1<<20)+17), '\n')
	path := filepath.Join(t.TempDir(), "large-SKILL.md")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write large source fixture: %v", err)
	}
	if _, err := db.Exec(`UPDATE assets SET source_path = ? WHERE id = ?`, path, "skill:project:fixture"); err != nil {
		t.Fatalf("update source path: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/skill:project:fixture/source", nil)
	rec := httptest.NewRecorder()
	NewServerWithDB(db).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("source status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var source struct {
		Content     string `json:"content"`
		ContentHash string `json:"content_hash"`
		Truncated   bool   `json:"truncated"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&source); err != nil {
		t.Fatalf("decode source: %v", err)
	}
	sum := sha256.Sum256(content)
	wantHash := "sha256:" + hex.EncodeToString(sum[:])
	if len(source.Content) != 1<<20 || source.ContentHash != wantHash || !source.Truncated {
		t.Fatalf("source length=%d hash=%q truncated=%v, want length=%d hash=%q truncated=true", len(source.Content), source.ContentHash, source.Truncated, 1<<20, wantHash)
	}
}

func TestDataAPIHonorsAssetListLimit(t *testing.T) {
	db := testAPIDB(t)
	if _, err := assets.New(db).Register(context.Background(), assets.AssetInput{
		Kind:        assets.KindRule,
		Scope:       assets.ScopeProject,
		Name:        "fixture-two",
		FirstSeenAt: time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("register second fixture: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets?limit=1", nil)
	rec := httptest.NewRecorder()
	NewServerWithDB(db).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("limited assets status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var list struct {
		Assets []json.RawMessage `json:"assets"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode limited assets: %v", err)
	}
	if len(list.Assets) != 1 {
		t.Fatalf("limited assets = %d, want 1", len(list.Assets))
	}
}

func TestDataAPIAssetSummaryOmitsPerAssetFacts(t *testing.T) {
	db := testAPIDB(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets?summary=1", nil)
	rec := httptest.NewRecorder()
	NewServerWithDB(db).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset summary status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Assets []map[string]json.RawMessage `json:"assets"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode asset summary: %v", err)
	}
	if len(body.Assets) != 1 {
		t.Fatalf("asset summary count = %d, want 1", len(body.Assets))
	}
	if _, ok := body.Assets[0]["facts"]; ok {
		t.Fatalf("asset summary unexpectedly includes per-asset facts: %s", body.Assets[0]["facts"])
	}
	if string(body.Assets[0]["id"]) != `"skill:project:fixture"` || string(body.Assets[0]["state_status"]) != `"evaluated"` {
		t.Fatalf("asset summary identity/state = id=%s state=%s", body.Assets[0]["id"], body.Assets[0]["state_status"])
	}
}

func TestDataAPISurfacesFactsStatsAndCleanupCandidates(t *testing.T) {
	db := testAPIDB(t)
	handler := NewServerWithDB(db).Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("facts assets status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var list struct {
		Assets []struct {
			Facts struct {
				VersionCount       int   `json:"version_count"`
				SessionCount       int   `json:"session_count"`
				ParticipationCount int   `json:"participation_count"`
				Sparkline          []any `json:"sparkline"`
			} `json:"facts"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Assets) != 1 || list.Assets[0].Facts.VersionCount != 1 || list.Assets[0].Facts.SessionCount != 1 || list.Assets[0].Facts.ParticipationCount != 1 || len(list.Assets[0].Facts.Sparkline) != 1 {
		t.Fatalf("facts = %+v", list.Assets)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var stats struct {
		AssetCount         int            `json:"asset_count"`
		SessionCount       int            `json:"session_count"`
		ParticipationCount int            `json:"participation_count"`
		SourceCounts       map[string]int `json:"source_counts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if stats.AssetCount != 1 || stats.SessionCount != 1 || stats.ParticipationCount != 1 || stats.SourceCounts["claude_code"] != 1 {
		t.Fatalf("stats = %+v", stats)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/cleanup", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"candidates"`)) {
		t.Fatalf("cleanup status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDataAPIUsesExplicitNotFoundAndEmptyCollections(t *testing.T) {
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	handler := NewServerWithDB(db).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/missing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing detail status = %d, want 404", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty assets status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode empty assets: %v", err)
	}
	assetsValue, ok := body["assets"].([]any)
	if !ok || assetsValue == nil {
		t.Fatalf("empty assets = %#v, want explicit empty array", body["assets"])
	}
}

func TestDataAPIExposesFunnelNotificationsAndTimelineProjection(t *testing.T) {
	db := testAPIDB(t)
	handler := NewServerWithDB(db).Handler()
	ctx := context.Background()
	assetID := "skill:project:fixture"
	firstSeen := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+assetID, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Funnel struct {
			Current struct {
				OpportunityCount *int `json:"opportunity_count"`
				Steps            []struct {
					Signal string `json:"signal"`
				} `json:"steps"`
			} `json:"current"`
		} `json:"funnel"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode funnel: %v", err)
	}
	if detail.Funnel.Current.OpportunityCount == nil || *detail.Funnel.Current.OpportunityCount != 1 || len(detail.Funnel.Current.Steps) != 5 {
		t.Fatalf("funnel = %+v", detail.Funnel)
	}

	repo := vital.NewRepository(db, vital.NewMachine(vital.DefaultConfig()))
	if _, err := repo.Apply(ctx, vital.Assessment{AssetID: assetID, At: firstSeen.Add(time.Hour), HasOpportunity: true, HasBaseline: true, ParticipationObserved: true}); err != nil {
		t.Fatalf("healthy transition: %v", err)
	}
	if _, err := repo.Apply(ctx, vital.Assessment{AssetID: assetID, At: firstSeen.Add(2 * time.Hour), HasOpportunity: true, HasBaseline: true, Silent: detectors.Verdict{Triggered: true, Observable: true, Summary: "fixture entered silent", Rule: "fixture rule"}}); err != nil {
		t.Fatalf("silent transition: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("notifications status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var notifications struct {
		Items []struct {
			Kind    string `json:"kind"`
			Summary string `json:"summary"`
		} `json:"notifications"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&notifications); err != nil {
		t.Fatalf("decode notifications: %v", err)
	}
	if len(notifications.Items) != 1 || notifications.Items[0].Kind != "silent" || notifications.Items[0].Summary != "fixture entered silent" {
		t.Fatalf("notifications = %+v", notifications.Items)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/timeline", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"clusters"`)) {
		t.Fatalf("timeline status=%d body=%s", rec.Code, rec.Body.String())
	}
	var timeline struct {
		Items []struct {
			Kind  string `json:"kind"`
			State string `json:"state"`
		} `json:"timeline"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&timeline); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	silentTransitionFound := false
	for _, item := range timeline.Items {
		if item.Kind == "state_transition" && item.State == "silent" {
			silentTransitionFound = true
		}
	}
	if !silentTransitionFound {
		t.Fatal("timeline does not expose the fixture silent state transition")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+assetID+"/source", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing source status = %d, want 404", rec.Code)
	}
}

func TestDataAPIFunnelUsesLatestRecordedTaskShape(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	assetID := "skill:project:fixture"
	latest := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (id, source, source_session_id, started_at)
		VALUES (?, ?, ?, ?)`, "claude_code:api-fixture-latest-shape", adapters.SourceClaudeCode, "api-fixture-latest-shape", latest.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert latest-shape session: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO opportunities (session_id, shape_class, shape_rule_version, asset_id, detector_version, detected_at)
		VALUES (?, ?, ?, ?, ?, ?)`, "claude_code:api-fixture-latest-shape", "different-task-shape", "shape/fixture", assetID, "tracker/fixture", latest.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert latest-shape opportunity: %v", err)
	}

	handler := NewServerWithDB(db).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+assetID, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Funnel struct {
			Current struct {
				OpportunityCount *int `json:"opportunity_count"`
				Steps            []struct {
					Signal    string `json:"signal"`
					Numerator *int   `json:"numerator"`
				} `json:"steps"`
			} `json:"current"`
		} `json:"funnel"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode funnel: %v", err)
	}
	if detail.Funnel.Current.OpportunityCount == nil || *detail.Funnel.Current.OpportunityCount != 1 {
		t.Fatalf("current funnel denominator = %+v, want latest shape only", detail.Funnel.Current)
	}
	for _, step := range detail.Funnel.Current.Steps {
		if step.Signal == "invoked" && step.Numerator != nil {
			t.Fatalf("invoked numerator = %v, old-shape participation leaked into latest shape", *step.Numerator)
		}
	}
}

func TestDispositionAPIRequiresConfirmationAndSupportsLogicalArchiveRestore(t *testing.T) {
	db := testAPIDB(t)
	handler := NewServerWithDB(db).Handler()
	ctx := context.Background()
	var instanceID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM state_transitions WHERE asset_id = ? ORDER BY id DESC LIMIT 1`, "skill:project:fixture").Scan(&instanceID); err != nil {
		t.Fatalf("state instance: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/skill:project:fixture/dispositions", bytes.NewBufferString(`{"action":"ignore","state_instance_id":`+strconv.FormatInt(instanceID, 10)+`,"confirmed":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed disposition status = %d, body=%s", rec.Code, rec.Body.String())
	}

	body := `{"action":"prune","state_instance_id":` + strconv.FormatInt(instanceID, 10) + `,"confirmed":true,"reason":"synthetic cleanup","rollback":{"source_path":"/synthetic/fixture/SKILL.md","strategy":"restore archived_at to NULL","reversible":true}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/assets/skill:project:fixture/dispositions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("confirmed disposition status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode disposition: %v", err)
	}
	if created.Action != "prune" {
		t.Fatalf("created disposition = %+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/assets/skill:project:fixture/dispositions", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"action":"prune"`)) {
		t.Fatalf("disposition list status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/assets/skill:project:fixture/restore", bytes.NewBufferString(`{"confirmed":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM vital_states WHERE asset_id = ? AND ended_at IS NULL`, "skill:project:fixture").Scan(&state); err != nil {
		t.Fatalf("restored state: %v", err)
	}
	if state != "dormant" {
		t.Fatalf("restored state = %q, want dormant", state)
	}
}
