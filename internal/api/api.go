// Package api assembles the local HTTP API. P1 exposes only the health
// check; all endpoints are served on loopback only (ADR-1/ADR-2).
package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"flatline/internal/assets"
	"flatline/internal/storage"
	"flatline/internal/vital"
	"flatline/internal/web"
)

// HealthChecker reports whether the backing store is reachable.
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// Server is the local API handler set.
type Server struct {
	health HealthChecker
	db     *storage.DB
	vital  *vital.Repository
}

// NewServer builds the API handler. health may be nil, in which case /healthz
// reports the process is up but the store is not wired.
func NewServer(health HealthChecker) *Server {
	server := &Server{health: health}
	if db, ok := health.(*storage.DB); ok {
		server.db = db
		server.vital = vital.NewRepository(db, vital.NewMachine(vital.DefaultConfig()))
	}
	return server
}

// NewServerWithDB enables the read API in addition to /healthz. NewServer is
// kept source-compatible for the health-only unit tests and accepts a
// *storage.DB as well.
func NewServerWithDB(db *storage.DB) *Server {
	return &Server{health: db, db: db, vital: vital.NewRepository(db, vital.NewMachine(vital.DefaultConfig()))}
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("HEAD /healthz", s.handleHealthz)
	if s.db != nil {
		mux.HandleFunc("GET /api/v1/assets", s.handleAssets)
		mux.HandleFunc("GET /api/v1/assets/{id}", s.handleAsset)
		mux.HandleFunc("GET /api/v1/assets/{id}/transitions", s.handleAssetTransitions)
		mux.HandleFunc("GET /api/v1/assets/{id}/opportunities", s.handleAssetOpportunities)
		mux.HandleFunc("GET /api/v1/assets/{id}/participations", s.handleAssetParticipations)
		mux.HandleFunc("GET /api/v1/assets/{id}/dispositions", s.handleAssetDispositions)
		mux.HandleFunc("GET /api/v1/assets/{id}/references", s.handleAssetReferences)
		mux.HandleFunc("GET /api/v1/assets/{id}/source", s.handleAssetSource)
		mux.HandleFunc("POST /api/v1/assets/{id}/dispositions", s.handleCreateDisposition)
		mux.HandleFunc("POST /api/v1/assets/{id}/restore", s.handleRestoreAsset)
		mux.HandleFunc("GET /api/v1/sessions", s.handleSessions)
		mux.HandleFunc("GET /api/v1/sessions/{id}", s.handleSession)
		mux.HandleFunc("GET /api/v1/sessions/{id}/events/{event_id}", s.handleSessionEvent)
		mux.HandleFunc("GET /api/v1/timeline", s.handleTimeline)
		mux.HandleFunc("GET /api/v1/notifications", s.handleNotifications)
		mux.HandleFunc("GET /api/v1/stats", s.handleStats)
		mux.HandleFunc("GET /api/v1/cleanup", s.handleCleanup)
		mux.HandleFunc("POST /api/v1/cleanup", s.handleBatchCleanup)
		mux.HandleFunc("GET /api/v1/ingest/status", s.handleIngestStatus)
		mux.Handle("/", web.Handler())
	}
	return mux
}

type healthResponse struct {
	Status string `json:"status"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{Status: "ok"}
	code := http.StatusOK

	if s.health != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.health.Ping(ctx); err != nil {
			resp = healthResponse{Status: "degraded"}
			code = http.StatusServiceUnavailable
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(resp)
}

type assetListResponse struct {
	Assets []assetListItem `json:"assets"`
}

type assetSummaryResponse struct {
	Assets []assetSummaryItem `json:"assets"`
}

type assetSummaryState struct {
	State         vital.State `json:"state"`
	BrokenOverlay bool        `json:"broken_overlay"`
}

// assetSummaryItem is the small projection needed to render the persistent
// shell and cross-page links. It deliberately omits per-asset facts and
// sparklines; the wall and cleanup screens request the full projection only
// when they actually need those measurements.
type assetSummaryItem struct {
	ID           string             `json:"id"`
	Kind         assets.Kind        `json:"kind"`
	Name         string             `json:"name"`
	Scope        assets.Scope       `json:"scope"`
	SourcePath   *string            `json:"source_path,omitempty"`
	FirstSeenAt  time.Time          `json:"first_seen_at"`
	LastSeenAt   *time.Time         `json:"last_seen_at,omitempty"`
	ArchivedAt   *time.Time         `json:"archived_at,omitempty"`
	StateStatus  string             `json:"state_status"`
	CurrentState *assetSummaryState `json:"current_state"`
}

type assetListItem struct {
	ID           string         `json:"id"`
	Kind         assets.Kind    `json:"kind"`
	Name         string         `json:"name"`
	Scope        assets.Scope   `json:"scope"`
	SourcePath   *string        `json:"source_path,omitempty"`
	FirstSeenAt  time.Time      `json:"first_seen_at"`
	LastSeenAt   *time.Time     `json:"last_seen_at,omitempty"`
	ArchivedAt   *time.Time     `json:"archived_at,omitempty"`
	StateStatus  string         `json:"state_status"`
	CurrentState *stateResponse `json:"current_state"`
	Facts        assetFacts     `json:"facts"`
}

type sparkPoint struct {
	At    time.Time `json:"at"`
	Value *int      `json:"value"`
}

type changeMarker struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
}

// assetFacts contains only persisted counts and timestamps. An empty
// collection means that no corresponding fact was recorded; it is not
// converted into a synthetic opportunity or participation.
type assetFacts struct {
	VersionCount        int            `json:"version_count"`
	SessionCount        int            `json:"session_count"`
	OpportunityCount    int            `json:"opportunity_count"`
	ParticipationCount  int            `json:"participation_count"`
	SourceBytes         *int64         `json:"source_bytes,omitempty"`
	BaselineNumerator   *int           `json:"baseline_participation_numerator,omitempty"`
	BaselineDenominator *int           `json:"baseline_participation_denominator,omitempty"`
	CurrentNumerator    *int           `json:"current_participation_numerator,omitempty"`
	CurrentDenominator  *int           `json:"current_participation_denominator,omitempty"`
	LastParticipationAt *time.Time     `json:"last_participation_at,omitempty"`
	ObservationLevels   []string       `json:"observation_levels"`
	Sparkline           []sparkPoint   `json:"sparkline"`
	ChangeMarkers       []changeMarker `json:"change_markers"`
}

type funnelStepResponse struct {
	Signal            string   `json:"signal"`
	Numerator         *int     `json:"numerator,omitempty"`
	Denominator       *int     `json:"denominator,omitempty"`
	ObservationLevels []string `json:"observation_levels"`
}

type funnelWindowResponse struct {
	Name             string               `json:"name"`
	Basis            string               `json:"basis"`
	OpportunityCount *int                 `json:"opportunity_count,omitempty"`
	Steps            []funnelStepResponse `json:"steps"`
}

type funnelResponse struct {
	Current  funnelWindowResponse  `json:"current"`
	Baseline *funnelWindowResponse `json:"baseline,omitempty"`
	Note     string                `json:"note"`
}

type stateResponse struct {
	InstanceID       int64           `json:"instance_id"`
	State            vital.State     `json:"state"`
	BrokenOverlay    bool            `json:"broken_overlay"`
	Evidence         json.RawMessage `json:"evidence,omitempty"`
	Baseline         json.RawMessage `json:"baseline,omitempty"`
	DetectorVersion  string          `json:"detector_version"`
	SchemaVersion    string          `json:"schema_version"`
	ThresholdVersion string          `json:"threshold_version"`
	StartedAt        time.Time       `json:"started_at"`
}

type assetDetailResponse struct {
	Asset           assetListItem            `json:"asset"`
	Description     *string                  `json:"description,omitempty"`
	Versions        []versionResponse        `json:"versions"`
	CurrentState    *stateResponse           `json:"current_state"`
	StateStatus     string                   `json:"state_status"`
	Funnel          funnelResponse           `json:"funnel"`
	Transitions     []vital.Transition       `json:"transitions"`
	Opportunities   []opportunityResponse    `json:"opportunities"`
	Participations  []participationResponse  `json:"participations"`
	RelatedSessions []sessionResponse        `json:"related_sessions"`
	Dispositions    []vital.Disposition      `json:"dispositions"`
	ReferenceChecks []referenceCheckResponse `json:"reference_checks"`
}

type versionResponse struct {
	ID               int64     `json:"id"`
	AssetID          string    `json:"asset_id"`
	Version          int       `json:"version"`
	ContentHash      string    `json:"content_hash"`
	ContentRef       *string   `json:"content_ref,omitempty"`
	ObservationLevel string    `json:"observation_level"`
	ObservedAt       time.Time `json:"observed_at"`
}

type opportunityResponse struct {
	ID                 int64     `json:"id"`
	SessionID          string    `json:"session_id"`
	ShapeClass         string    `json:"shape_class"`
	ShapeRuleVersion   string    `json:"shape_rule_version"`
	AssetID            string    `json:"asset_id"`
	DetectorVersion    string    `json:"detector_version"`
	DetectedAt         time.Time `json:"detected_at"`
	Participated       *bool     `json:"participated"`
	ParticipationKnown *bool     `json:"participation_known"`
}

type participationResponse struct {
	ID               int64           `json:"id"`
	AssetVersionID   int64           `json:"asset_version_id"`
	SessionID        string          `json:"session_id"`
	OpportunityID    *int64          `json:"opportunity_id,omitempty"`
	Signal           string          `json:"participation_signal"`
	ObservationLevel string          `json:"observation_level"`
	OccurredAt       *time.Time      `json:"occurred_at,omitempty"`
	Locator          json.RawMessage `json:"locator"`
}

type referenceItemResponse struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Exists *bool  `json:"exists"`
	Detail string `json:"detail,omitempty"`
}

type referenceCheckResponse struct {
	ID             int64                   `json:"id"`
	AssetVersionID int64                   `json:"asset_version_id"`
	CheckedAt      time.Time               `json:"checked_at"`
	OverallStatus  string                  `json:"overall_status"`
	CheckerVersion string                  `json:"checker_version"`
	Items          []referenceItemResponse `json:"items"`
}

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("view") == "wall" {
		items, err := s.listWallAssets(r.Context())
		if err != nil {
			http.Error(w, "data query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if limit := queryLimit(r, len(items)); limit < len(items) {
			items = items[:limit]
		}
		writeJSON(w, http.StatusOK, assetListResponse{Assets: items})
		return
	}
	if r.URL.Query().Get("summary") == "1" {
		items, err := s.listAssetSummaries(r.Context())
		if err != nil {
			http.Error(w, "data query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if limit := queryLimit(r, len(items)); limit < len(items) {
			items = items[:limit]
		}
		writeJSON(w, http.StatusOK, assetSummaryResponse{Assets: items})
		return
	}
	items, err := s.listAssets(r.Context())
	if err != nil {
		http.Error(w, "data query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if limit := queryLimit(r, len(items)); limit < len(items) {
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, assetListResponse{Assets: items})
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	asset, err := assets.New(s.db).Get(r.Context(), id)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, "asset query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	versions, err := assets.New(s.db).Versions(r.Context(), id)
	if err != nil {
		http.Error(w, "version query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	current, status, err := s.currentState(r.Context(), id)
	if err != nil {
		http.Error(w, "state query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	facts, err := s.assetFacts(r.Context(), id)
	if err != nil {
		http.Error(w, "facts query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	transitions, err := s.vital.Transitions(r.Context(), id, queryLimit(r, 500))
	if err != nil {
		http.Error(w, "transition query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	opportunities, err := s.assetOpportunities(r.Context(), id, queryLimit(r, 500))
	if err != nil {
		http.Error(w, "opportunity query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	participations, err := s.assetParticipations(r.Context(), id, queryLimit(r, 500))
	if err != nil {
		http.Error(w, "participation query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	dispositions, err := s.dispositions(r.Context(), id, queryLimit(r, 500))
	if err != nil {
		http.Error(w, "disposition query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	referenceChecks, err := s.referenceChecks(r.Context(), id, queryLimit(r, 100))
	if err != nil {
		http.Error(w, "reference query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	funnel, err := s.assetFunnel(r.Context(), id)
	if err != nil {
		http.Error(w, "funnel query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	relatedSessions, err := s.assetRelatedSessions(r.Context(), id, queryLimit(r, 500))
	if err != nil {
		http.Error(w, "related session query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, assetDetailResponse{
		Asset:       assetListItem{ID: asset.ID, Kind: asset.Kind, Name: asset.Name, Scope: asset.Scope, SourcePath: asset.SourcePath, FirstSeenAt: asset.FirstSeenAt, LastSeenAt: asset.LastSeenAt, ArchivedAt: asset.ArchivedAt, StateStatus: status, CurrentState: current, Facts: facts},
		Description: asset.Description, Versions: versionResponses(versions), CurrentState: current, StateStatus: status,
		Funnel:      funnel,
		Transitions: transitions, Opportunities: opportunities, Participations: participations, RelatedSessions: relatedSessions, Dispositions: dispositions, ReferenceChecks: referenceChecks,
	})
}

func (s *Server) handleAssetTransitions(w http.ResponseWriter, r *http.Request) {
	if _, err := assets.New(s.db).Get(r.Context(), r.PathValue("id")); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	transitions, err := s.vital.Transitions(r.Context(), r.PathValue("id"), queryLimit(r, 500))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"transitions": transitions})
}

func (s *Server) handleAssetOpportunities(w http.ResponseWriter, r *http.Request) {
	items, err := s.assetOpportunities(r.Context(), r.PathValue("id"), queryLimit(r, 500))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"opportunities": items})
}

func (s *Server) handleAssetParticipations(w http.ResponseWriter, r *http.Request) {
	items, err := s.assetParticipations(r.Context(), r.PathValue("id"), queryLimit(r, 500))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"participations": items})
}

func (s *Server) handleAssetDispositions(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAsset(r.Context(), r.PathValue("id")); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items, err := s.dispositions(r.Context(), r.PathValue("id"), queryLimit(r, 500))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dispositions": items})
}

func (s *Server) handleAssetReferences(w http.ResponseWriter, r *http.Request) {
	if err := s.requireAsset(r.Context(), r.PathValue("id")); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items, err := s.referenceChecks(r.Context(), r.PathValue("id"), queryLimit(r, 100))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reference_checks": items})
}

type assetSourceResponse struct {
	AssetID     string `json:"asset_id"`
	SourcePath  string `json:"source_path"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
	Truncated   bool   `json:"truncated"`
}

// handleAssetSource is a bounded, read-only source preview for diagnosis. It
// never accepts source content and never exposes a write path to the browser.
func (s *Server) handleAssetSource(w http.ResponseWriter, r *http.Request) {
	asset, err := assets.New(s.db).Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, "asset query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if asset.SourcePath == nil || strings.TrimSpace(*asset.SourcePath) == "" {
		http.Error(w, "asset source path is not recorded", http.StatusNotFound)
		return
	}
	file, err := os.Open(*asset.SourcePath)
	if err != nil {
		http.Error(w, "asset source cannot be read: "+err.Error(), http.StatusNotFound)
		return
	}
	defer file.Close()
	const maxSourcePreview = 1 << 20
	hasher := sha256.New()
	preview := make([]byte, 0, maxSourcePreview)
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			_, _ = hasher.Write(buffer[:read])
			total += int64(read)
			if len(preview) < maxSourcePreview {
				keep := read
				if remaining := maxSourcePreview - len(preview); keep > remaining {
					keep = remaining
				}
				preview = append(preview, buffer[:keep]...)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			http.Error(w, "asset source read failed: "+readErr.Error(), http.StatusInternalServerError)
			return
		}
	}
	contentHash := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	writeJSON(w, http.StatusOK, assetSourceResponse{AssetID: asset.ID, SourcePath: *asset.SourcePath, Content: string(preview), ContentHash: contentHash, Truncated: total > int64(maxSourcePreview)})
}

type dispositionRequest struct {
	Action          string               `json:"action"`
	StateInstanceID int64                `json:"state_instance_id"`
	Confirmed       bool                 `json:"confirmed"`
	Reason          string               `json:"reason"`
	Rollback        vital.RollbackRecord `json:"rollback"`
}

func (s *Server) handleCreateDisposition(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	if err := s.requireAsset(r.Context(), assetID); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var request dispositionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid disposition request: "+err.Error(), http.StatusBadRequest)
		return
	}
	item, err := vital.NewDispositionStore(s.db, s.vital).Apply(r.Context(), vital.DispositionRequest{
		AssetID: assetID, Action: vital.Action(request.Action), StateInstanceID: request.StateInstanceID,
		Confirmed: request.Confirmed, Reason: request.Reason, Rollback: request.Rollback,
	})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "stale") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) handleRestoreAsset(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Confirmed bool `json:"confirmed"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid restore request: "+err.Error(), http.StatusBadRequest)
		return
	}
	assetID := r.PathValue("id")
	if err := s.requireAsset(r.Context(), assetID); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := vital.NewDispositionStore(s.db, s.vital).Restore(r.Context(), assetID, request.Confirmed); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"asset_id": assetID, "restored": true})
}

func (s *Server) listAssets(ctx context.Context) ([]assetListItem, error) {
	items, err := assets.New(s.db).List(ctx)
	if err != nil {
		return nil, err
	}
	environmentMarkers, err := s.environmentChangeMarkers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]assetListItem, 0, len(items))
	for _, asset := range items {
		state, status, err := s.currentState(ctx, asset.ID)
		if err != nil {
			return nil, err
		}
		facts, err := s.assetFactsWithMarkers(ctx, asset.ID, environmentMarkers)
		if err != nil {
			return nil, err
		}
		out = append(out, assetListItem{ID: asset.ID, Kind: asset.Kind, Name: asset.Name, Scope: asset.Scope, SourcePath: asset.SourcePath, FirstSeenAt: asset.FirstSeenAt, LastSeenAt: asset.LastSeenAt, ArchivedAt: asset.ArchivedAt, StateStatus: status, CurrentState: state, Facts: facts})
	}
	return out, nil
}

// listWallAssets is the wall-specific full projection. The wall needs facts
// and sparklines for every asset, but it does not need the full state evidence
// payload that is drilled into on the detail page. Environment anchors are
// session-level facts and are deliberately sampled here because the same
// anchors are rendered on every asset row; the detail endpoint remains
// lossless.
func (s *Server) listWallAssets(ctx context.Context) ([]assetListItem, error) {
	items, err := assets.New(s.db).List(ctx)
	if err != nil {
		return nil, err
	}
	environmentMarkers, err := s.environmentChangeMarkers(ctx)
	if err != nil {
		return nil, err
	}
	environmentMarkers = compactEnvironmentMarkers(environmentMarkers, 16)
	out := make([]assetListItem, 0, len(items))
	for _, asset := range items {
		state, status, err := s.currentState(ctx, asset.ID)
		if err != nil {
			return nil, err
		}
		facts, err := s.assetFactsWithMarkers(ctx, asset.ID, environmentMarkers)
		if err != nil {
			return nil, err
		}
		if state != nil {
			stateCopy := *state
			stateCopy.Evidence = nil
			stateCopy.Baseline = nil
			state = &stateCopy
		}
		out = append(out, assetListItem{ID: asset.ID, Kind: asset.Kind, Name: asset.Name, Scope: asset.Scope, SourcePath: asset.SourcePath, FirstSeenAt: asset.FirstSeenAt, LastSeenAt: asset.LastSeenAt, ArchivedAt: asset.ArchivedAt, StateStatus: status, CurrentState: state, Facts: facts})
	}
	return out, nil
}

func (s *Server) listAssetSummaries(ctx context.Context) ([]assetSummaryItem, error) {
	items, err := assets.New(s.db).List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]assetSummaryItem, 0, len(items))
	for _, asset := range items {
		state, status, err := s.currentState(ctx, asset.ID)
		if err != nil {
			return nil, err
		}
		var summaryState *assetSummaryState
		if state != nil {
			summaryState = &assetSummaryState{State: state.State, BrokenOverlay: state.BrokenOverlay}
		}
		out = append(out, assetSummaryItem{ID: asset.ID, Kind: asset.Kind, Name: asset.Name, Scope: asset.Scope, SourcePath: asset.SourcePath, FirstSeenAt: asset.FirstSeenAt, LastSeenAt: asset.LastSeenAt, ArchivedAt: asset.ArchivedAt, StateStatus: status, CurrentState: summaryState})
	}
	return out, nil
}

func (s *Server) assetFacts(ctx context.Context, assetID string) (assetFacts, error) {
	return s.assetFactsWithMarkers(ctx, assetID, nil)
}

func (s *Server) assetFactsWithMarkers(ctx context.Context, assetID string, environmentMarkers []changeMarker) (assetFacts, error) {
	facts := assetFacts{ObservationLevels: make([]string, 0), Sparkline: make([]sparkPoint, 0), ChangeMarkers: make([]changeMarker, 0)}
	var sourcePath sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT source_path FROM assets WHERE id = ?`, assetID).Scan(&sourcePath); err != nil {
		return assetFacts{}, err
	}
	if sourcePath.Valid && strings.TrimSpace(sourcePath.String) != "" {
		if info, err := os.Stat(sourcePath.String); err == nil && !info.IsDir() {
			size := info.Size()
			facts.SourceBytes = &size
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_versions WHERE asset_id = ?`, assetID).Scan(&facts.VersionCount); err != nil {
		return assetFacts{}, err
	}
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT p.session_id), COUNT(*), MAX(p.occurred_at)
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
		WHERE av.asset_id = ?`, assetID).Scan(&facts.SessionCount, &facts.ParticipationCount, &last); err != nil {
		return assetFacts{}, err
	}
	if last.Valid && last.String != "" {
		parsed, err := time.Parse(time.RFC3339Nano, last.String)
		if err != nil {
			return assetFacts{}, err
		}
		facts.LastParticipationAt = &parsed
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM opportunities WHERE asset_id = ?`, assetID).Scan(&facts.OpportunityCount); err != nil {
		return assetFacts{}, err
	}
	comparisonItems, err := s.assetComparisonItems(ctx, assetID)
	if err != nil {
		return assetFacts{}, err
	}
	applyAssetComparison(comparisonItems, &facts)
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT p.observation_level
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
		WHERE av.asset_id = ? ORDER BY p.observation_level`, assetID)
	if err != nil {
		return assetFacts{}, err
	}
	for rows.Next() {
		var level string
		if err := rows.Scan(&level); err != nil {
			rows.Close()
			return assetFacts{}, err
		}
		facts.ObservationLevels = append(facts.ObservationLevels, level)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return assetFacts{}, err
	}
	if err := rows.Close(); err != nil {
		return assetFacts{}, err
	}
	rateObservations := make([]rateObservation, 0, len(comparisonItems))
	for _, item := range comparisonItems {
		rateObservations = append(rateObservations, rateObservation{At: item.At, Participated: item.Participated})
	}
	facts.Sparkline = makeParticipationRateSparkline(rateObservations)
	versionRows, err := s.db.QueryContext(ctx, `SELECT observed_at FROM asset_versions WHERE asset_id = ? ORDER BY observed_at, id`, assetID)
	if err != nil {
		return assetFacts{}, err
	}
	for versionRows.Next() {
		var observed string
		if err := versionRows.Scan(&observed); err != nil {
			versionRows.Close()
			return assetFacts{}, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			versionRows.Close()
			return assetFacts{}, err
		}
		facts.ChangeMarkers = append(facts.ChangeMarkers, changeMarker{At: parsed, Kind: "asset"})
	}
	if err := versionRows.Err(); err != nil {
		versionRows.Close()
		return assetFacts{}, err
	}
	if err := versionRows.Close(); err != nil {
		return assetFacts{}, err
	}
	// Environment anchors are session-level facts, not asset facts. They are
	// intentionally shown on every asset sparkline so the time alignment is
	// visible without assigning the environment change to a specific asset.
	if environmentMarkers == nil {
		environmentMarkers, err = s.environmentChangeMarkers(ctx)
		if err != nil {
			return assetFacts{}, err
		}
	}
	facts.ChangeMarkers = append(facts.ChangeMarkers, environmentMarkers...)
	return facts, nil
}

func (s *Server) environmentChangeMarkers(ctx context.Context) ([]changeMarker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT occurred_at FROM events WHERE event_type = 'environment_changed' AND occurred_at IS NOT NULL AND occurred_at <> '' ORDER BY occurred_at, id`)
	if err != nil {
		return nil, err
	}
	markers := make([]changeMarker, 0)
	for rows.Next() {
		var occurred string
		if err := rows.Scan(&occurred); err != nil {
			rows.Close()
			return nil, err
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			rows.Close()
			return nil, err
		}
		markers = append(markers, changeMarker{At: parsed, Kind: "environment"})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return markers, nil
}

// compactEnvironmentMarkers keeps the temporal span of a high-volume shared
// marker series while bounding the payload and DOM work performed by every
// wall row. The first and last observations are always retained, and the
// remaining observations are selected deterministically across the interval.
func compactEnvironmentMarkers(markers []changeMarker, max int) []changeMarker {
	if max <= 0 || len(markers) == 0 {
		return nil
	}
	if len(markers) <= max {
		return append([]changeMarker(nil), markers...)
	}
	if max == 1 {
		return []changeMarker{markers[len(markers)-1]}
	}
	out := make([]changeMarker, 0, max)
	last := len(markers) - 1
	denominator := max - 1
	for index := 0; index < max; index++ {
		markerIndex := (index*last + denominator/2) / denominator
		out = append(out, markers[markerIndex])
	}
	return out
}

type assetComparisonOpportunity struct {
	ShapeClass   string
	At           time.Time
	Source       string
	Participated bool
}

// assetComparison provides the compact wall metric without collapsing
// unknown task shapes into zero. It uses the latest recorded shape class for
// the asset, then compares its older known opportunities with the latest
// configured window.
func (s *Server) assetComparison(ctx context.Context, assetID string, facts *assetFacts) error {
	items, err := s.assetComparisonItems(ctx, assetID)
	if err != nil {
		return err
	}
	applyAssetComparison(items, facts)
	return nil
}

func (s *Server) assetComparisonItems(ctx context.Context, assetID string) ([]assetComparisonOpportunity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.shape_class, o.detected_at, s.source,
		       CASE WHEN EXISTS (
				SELECT 1 FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
				WHERE p.session_id = o.session_id AND av.asset_id = o.asset_id
		       ) THEN 1 ELSE 0 END
		FROM opportunities o JOIN sessions s ON s.id = o.session_id
		WHERE o.asset_id = ? ORDER BY o.detected_at, o.id`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]assetComparisonOpportunity, 0)
	for rows.Next() {
		var item assetComparisonOpportunity
		var detected string
		var participated int
		if err := rows.Scan(&item.ShapeClass, &detected, &item.Source, &participated); err != nil {
			return nil, err
		}
		item.At, err = time.Parse(time.RFC3339Nano, detected)
		if err != nil {
			return nil, err
		}
		item.Participated = participated != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > 0 {
		shape := items[len(items)-1].ShapeClass
		filtered := items[:0]
		for _, item := range items {
			if item.ShapeClass == shape {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	return items, nil
}

func applyAssetComparison(items []assetComparisonOpportunity, facts *assetFacts) {
	facts.BaselineNumerator = nil
	facts.BaselineDenominator = nil
	facts.CurrentNumerator = nil
	facts.CurrentDenominator = nil
	if len(items) == 0 {
		return
	}
	currentStart := len(items) - vital.DefaultConfig().SilentConsecutiveOpportunities
	if currentStart < 0 {
		currentStart = 0
	}
	setWindow := func(window []assetComparisonOpportunity) (*int, *int) {
		denominator, numerator := 0, 0
		for _, item := range window {
			if item.Source != "claude_code" && item.Source != "codex" {
				continue
			}
			denominator++
			if item.Participated {
				numerator++
			}
		}
		if denominator == 0 {
			return nil, nil
		}
		return intPointer(numerator), intPointer(denominator)
	}
	facts.BaselineNumerator, facts.BaselineDenominator = setWindow(items[:currentStart])
	facts.CurrentNumerator, facts.CurrentDenominator = setWindow(items[currentStart:])
}

type funnelOpportunityFact struct {
	SessionID  string
	Source     string
	ShapeClass string
	At         time.Time
}

type funnelParticipationFact struct {
	SessionID string
	Signal    string
	Level     string
}

// assetFunnel builds a factual current-vs-baseline view from the same
// opportunity boundary used by the VSM: the latest configured number of
// opportunities are current, and older opportunities are the comparison
// window. A missing numerator remains null when the source did not record that
// participation form; it is never silently rendered as zero.
func (s *Server) assetFunnel(ctx context.Context, assetID string) (funnelResponse, error) {
	opportunityRows, err := s.db.QueryContext(ctx, `
		SELECT o.session_id, s.source, o.shape_class, o.detected_at
		FROM opportunities o JOIN sessions s ON s.id = o.session_id
		WHERE o.asset_id = ? ORDER BY o.detected_at, o.id`, assetID)
	if err != nil {
		return funnelResponse{}, err
	}
	opportunities := make([]funnelOpportunityFact, 0)
	for opportunityRows.Next() {
		var item funnelOpportunityFact
		var detected string
		if err := opportunityRows.Scan(&item.SessionID, &item.Source, &item.ShapeClass, &detected); err != nil {
			opportunityRows.Close()
			return funnelResponse{}, err
		}
		item.At, err = time.Parse(time.RFC3339Nano, detected)
		if err != nil {
			opportunityRows.Close()
			return funnelResponse{}, err
		}
		opportunities = append(opportunities, item)
	}
	if err := opportunityRows.Err(); err != nil {
		opportunityRows.Close()
		return funnelResponse{}, err
	}
	if err := opportunityRows.Close(); err != nil {
		return funnelResponse{}, err
	}
	if len(opportunities) > 0 {
		latestShape := opportunities[len(opportunities)-1].ShapeClass
		filtered := opportunities[:0]
		for _, opportunity := range opportunities {
			if opportunity.ShapeClass == latestShape {
				filtered = append(filtered, opportunity)
			}
		}
		opportunities = filtered
	}

	participationRows, err := s.db.QueryContext(ctx, `
		SELECT p.session_id, p.participation_signal, p.observation_level
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
		WHERE av.asset_id = ? ORDER BY p.occurred_at, p.id`, assetID)
	if err != nil {
		return funnelResponse{}, err
	}
	participations := make([]funnelParticipationFact, 0)
	for participationRows.Next() {
		var item funnelParticipationFact
		if err := participationRows.Scan(&item.SessionID, &item.Signal, &item.Level); err != nil {
			participationRows.Close()
			return funnelResponse{}, err
		}
		participations = append(participations, item)
	}
	if err := participationRows.Err(); err != nil {
		participationRows.Close()
		return funnelResponse{}, err
	}
	if err := participationRows.Close(); err != nil {
		return funnelResponse{}, err
	}

	currentStart := len(opportunities) - vital.DefaultConfig().SilentConsecutiveOpportunities
	if currentStart < 0 {
		currentStart = 0
	}
	current := buildFunnelWindow("当前窗口", opportunities[currentStart:], participations)
	var baseline *funnelWindowResponse
	if currentStart > 0 {
		value := buildFunnelWindow("已记录基线", opportunities[:currentStart], participations)
		baseline = &value
	}
	return funnelResponse{
		Current:  current,
		Baseline: baseline,
		Note:     "当前窗口取最近 8 个相关任务；更早的相关任务才会进入基线。没有相关任务记录时不计算参与率。",
	}, nil
}

func buildFunnelWindow(name string, opportunities []funnelOpportunityFact, participations []funnelParticipationFact) funnelWindowResponse {
	knownSessions := make(map[string]struct{})
	windowSessions := make(map[string]struct{})
	for _, opportunity := range opportunities {
		windowSessions[opportunity.SessionID] = struct{}{}
		if opportunity.Source == "claude_code" || opportunity.Source == "codex" {
			knownSessions[opportunity.SessionID] = struct{}{}
		}
	}
	if len(opportunities) == 0 {
		// With no task-shape denominator, participation rows are still shown as
		// observations, but the denominator stays unknown.
		for _, participation := range participations {
			windowSessions[participation.SessionID] = struct{}{}
		}
	}
	var denominator *int
	if len(knownSessions) > 0 {
		value := len(knownSessions)
		denominator = &value
	}
	steps := make([]funnelStepResponse, 0, 4)
	for _, signal := range []string{"offered", "loaded", "invoked", "observed-use", "followed"} {
		sessions := make(map[string]struct{})
		levels := make(map[string]struct{})
		for _, participation := range participations {
			if _, ok := windowSessions[participation.SessionID]; !ok || participation.Signal != signal {
				continue
			}
			sessions[participation.SessionID] = struct{}{}
			if participation.Level != "" {
				levels[participation.Level] = struct{}{}
			}
		}
		var numerator *int
		if len(sessions) > 0 {
			value := len(sessions)
			numerator = &value
		}
		observedLevels := make([]string, 0, len(levels))
		for level := range levels {
			observedLevels = append(observedLevels, level)
		}
		sort.Strings(observedLevels)
		steps = append(steps, funnelStepResponse{Signal: signal, Numerator: numerator, Denominator: denominator, ObservationLevels: observedLevels})
	}
	basis := "没有相关任务记录，无法建立任务分母。"
	if len(opportunities) > 0 {
		basis = "基于 " + strconv.Itoa(len(opportunities)) + " 个已记录相关任务；只对来源可提供参与判定的会话计算分母。"
	}
	return funnelWindowResponse{Name: name, Basis: basis, OpportunityCount: denominator, Steps: steps}
}

type rateObservation struct {
	At           time.Time
	Participated bool
}

func makeParticipationRateSparkline(observations []rateObservation) []sparkPoint {
	if len(observations) == 0 {
		return []sparkPoint{}
	}
	ordered := append([]rateObservation(nil), observations...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
	if len(ordered) == 1 || ordered[0].At.Equal(ordered[len(ordered)-1].At) {
		return []sparkPoint{{At: ordered[0].At, Value: rateValue(ordered)}}
	}
	const buckets = 12
	start, end := ordered[0].At, ordered[len(ordered)-1].At
	span := end.Sub(start)
	numerators := make([]int, buckets)
	denominators := make([]int, buckets)
	for _, observation := range ordered {
		bucket := int(float64(observation.At.Sub(start)) / float64(span) * float64(buckets-1))
		if bucket < 0 {
			bucket = 0
		}
		if bucket >= buckets {
			bucket = buckets - 1
		}
		denominators[bucket]++
		if observation.Participated {
			numerators[bucket]++
		}
	}
	out := make([]sparkPoint, buckets)
	for i := range denominators {
		var value *int
		if denominators[i] > 0 {
			rounded := (numerators[i]*100 + denominators[i]/2) / denominators[i]
			value = &rounded
		}
		out[i] = sparkPoint{At: start.Add(time.Duration(float64(span) * float64(i) / float64(buckets-1))), Value: value}
	}
	return out
}

func rateValue(observations []rateObservation) *int {
	if len(observations) == 0 {
		return nil
	}
	numerator := 0
	for _, observation := range observations {
		if observation.Participated {
			numerator++
		}
	}
	rounded := (numerator*100 + len(observations)/2) / len(observations)
	return &rounded
}

func (s *Server) requireAsset(ctx context.Context, assetID string) error {
	_, err := assets.New(s.db).Get(ctx, assetID)
	return err
}

func (s *Server) dispositions(ctx context.Context, assetID string, limit int) ([]vital.Disposition, error) {
	return vital.NewDispositionStore(s.db, s.vital).List(ctx, assetID, limit)
}

func (s *Server) referenceChecks(ctx context.Context, assetID string, limit int) ([]referenceCheckResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rc.id, rc.asset_version_id, rc.checked_at, rc.overall_status, rc.checker_version
		FROM reference_checks rc JOIN asset_versions av ON av.id = rc.asset_version_id
		WHERE av.asset_id = ? ORDER BY rc.checked_at DESC, rc.id DESC LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]referenceCheckResponse, 0)
	checkIDs := make([]int64, 0)
	for rows.Next() {
		var item referenceCheckResponse
		var checkedAt string
		if err := rows.Scan(&item.ID, &item.AssetVersionID, &checkedAt, &item.OverallStatus, &item.CheckerVersion); err != nil {
			rows.Close()
			return nil, err
		}
		item.CheckedAt, err = time.Parse(time.RFC3339Nano, checkedAt)
		if err != nil {
			rows.Close()
			return nil, err
		}
		item.Items = make([]referenceItemResponse, 0)
		out = append(out, item)
		checkIDs = append(checkIDs, item.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i, checkID := range checkIDs {
		itemRows, err := s.db.QueryContext(ctx, `SELECT id, ref_kind, ref_value, "exists", COALESCE(detail, '') FROM reference_check_items WHERE check_id = ? ORDER BY id`, checkID)
		if err != nil {
			return nil, err
		}
		for itemRows.Next() {
			var item referenceItemResponse
			var exists sql.NullInt64
			if err := itemRows.Scan(&item.ID, &item.Kind, &item.Value, &exists, &item.Detail); err != nil {
				itemRows.Close()
				return nil, err
			}
			if exists.Valid {
				value := exists.Int64 != 0
				item.Exists = &value
			}
			out[i].Items = append(out[i].Items, item)
		}
		if err := itemRows.Err(); err != nil {
			itemRows.Close()
			return nil, err
		}
		if err := itemRows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Server) currentState(ctx context.Context, assetID string) (*stateResponse, string, error) {
	current, err := s.vital.Current(ctx, assetID)
	if err != nil {
		return nil, "", err
	}
	if current == nil {
		return nil, "not_evaluated", nil
	}
	evidence := json.RawMessage(current.EvidenceJSON)
	if len(evidence) == 0 {
		evidence = json.RawMessage(`{}`)
	}
	baseline := json.RawMessage(current.BaselineJSON)
	if len(baseline) == 0 {
		baseline = json.RawMessage(`null`)
	}
	return &stateResponse{InstanceID: current.InstanceID, State: current.State, BrokenOverlay: current.BrokenOverlay, Evidence: evidence, Baseline: baseline, DetectorVersion: current.DetectorVersion, SchemaVersion: current.SchemaVersion, ThresholdVersion: current.ThresholdVersion, StartedAt: current.StartedAt}, "evaluated", nil
}

func versionResponses(versions []assets.AssetVersion) []versionResponse {
	out := make([]versionResponse, 0, len(versions))
	for _, version := range versions {
		out = append(out, versionResponse{ID: version.ID, AssetID: version.AssetID, Version: version.Version, ContentHash: version.ContentHash, ContentRef: version.ContentRef, ObservationLevel: string(version.ObservationLevel), ObservedAt: version.ObservedAt})
	}
	return out
}

func (s *Server) assetOpportunities(ctx context.Context, assetID string, limit int) ([]opportunityResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.id, o.session_id, o.shape_class, o.shape_rule_version, o.asset_id, o.detector_version, o.detected_at,
		       CASE WHEN EXISTS (
				SELECT 1 FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
				WHERE p.session_id = o.session_id AND av.asset_id = o.asset_id
		       ) THEN 1 ELSE 0 END,
		       CASE WHEN EXISTS (
				SELECT 1 FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
				WHERE p.session_id = o.session_id AND av.asset_id = o.asset_id
		       ) OR s.source IN ('claude_code', 'codex') THEN 1 ELSE NULL END
		FROM opportunities o JOIN sessions s ON s.id = o.session_id
		WHERE o.asset_id = ? ORDER BY o.detected_at, o.id LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]opportunityResponse, 0)
	for rows.Next() {
		var item opportunityResponse
		var detected string
		var participated, known sql.NullInt64
		if err := rows.Scan(&item.ID, &item.SessionID, &item.ShapeClass, &item.ShapeRuleVersion, &item.AssetID, &item.DetectorVersion, &detected, &participated, &known); err != nil {
			return nil, err
		}
		item.DetectedAt, err = time.Parse(time.RFC3339Nano, detected)
		if err != nil {
			return nil, err
		}
		if known.Valid {
			knownValue := known.Int64 != 0
			item.ParticipationKnown = &knownValue
			participatedValue := participated.Valid && participated.Int64 != 0
			item.Participated = &participatedValue
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Server) assetParticipations(ctx context.Context, assetID string, limit int) ([]participationResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.asset_version_id, p.session_id, p.opportunity_id, p.participation_signal,
		       p.observation_level, p.occurred_at, COALESCE(p.locator_json, '')
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
		WHERE av.asset_id = ? ORDER BY p.occurred_at, p.id LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]participationResponse, 0)
	for rows.Next() {
		var item participationResponse
		var opportunity sql.NullInt64
		var occurred, locator sql.NullString
		if err := rows.Scan(&item.ID, &item.AssetVersionID, &item.SessionID, &opportunity, &item.Signal, &item.ObservationLevel, &occurred, &locator); err != nil {
			return nil, err
		}
		if opportunity.Valid {
			item.OpportunityID = &opportunity.Int64
		}
		if occurred.Valid && occurred.String != "" {
			parsed, err := time.Parse(time.RFC3339Nano, occurred.String)
			if err != nil {
				return nil, err
			}
			item.OccurredAt = &parsed
		}
		if !locator.Valid || locator.String == "" {
			item.Locator = json.RawMessage(`null`)
		} else {
			item.Locator = json.RawMessage(locator.String)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// assetRelatedSessions returns every session that created an opportunity for
// the asset, including sessions where participation was not recorded. An
// opportunity is the related-task denominator; filtering this projection to
// participations would make valid non-participating task records disappear.
func (s *Server) assetRelatedSessions(ctx context.Context, assetID string, limit int) ([]sessionResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.source, s.source_session_id, s.title, s.task_text, s.started_at, s.ended_at, s.harness_version, s.model, s.cwd,
		       (SELECT COUNT(*) FROM events e WHERE e.session_id = s.id),
		       (SELECT COUNT(*) FROM events e WHERE e.session_id = s.id AND e.event_type IN ('transcript_message', 'transcript_tool_call', 'transcript_tool_result')),
		       (SELECT COUNT(DISTINCT e.asset_id) FROM events e WHERE e.session_id = s.id AND e.asset_id IS NOT NULL)
		FROM sessions s
		WHERE EXISTS (
			SELECT 1 FROM opportunities o
			WHERE o.session_id = s.id AND o.asset_id = ?
		) OR EXISTS (
			SELECT 1
			FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
			WHERE p.session_id = s.id AND av.asset_id = ?
		)
		ORDER BY COALESCE(s.started_at, '') DESC, s.id DESC LIMIT ?`, assetID, assetID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]sessionResponse, 0)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	return out, rows.Close()
}

type sessionResponse struct {
	ID              string     `json:"id"`
	Source          string     `json:"source"`
	SourceSessionID string     `json:"source_session_id"`
	Title           *string    `json:"title,omitempty"`
	TaskText        *string    `json:"task_text,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	HarnessVersion  *string    `json:"harness_version,omitempty"`
	Model           *string    `json:"model,omitempty"`
	CWD             *string    `json:"cwd,omitempty"`
	EventCount      int        `json:"event_count"`
	TranscriptCount int        `json:"transcript_count"`
	AssetCount      int        `json:"asset_count"`
}

type sessionSummaryResponse struct {
	ID              string     `json:"id"`
	Source          string     `json:"source"`
	SourceSessionID string     `json:"source_session_id"`
	Title           *string    `json:"title,omitempty"`
	TaskText        *string    `json:"task_text,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
}

type frictionRecordResponse struct {
	ID               int64           `json:"id"`
	SourceEventID    string          `json:"source_event_id"`
	FrictionKind     string          `json:"friction_kind"`
	EventType        string          `json:"event_type"`
	ObservationLevel string          `json:"observation_level"`
	IsError          *bool           `json:"is_error,omitempty"`
	ExitCode         *int            `json:"exit_code,omitempty"`
	Payload          json.RawMessage `json:"payload"`
	Locator          json.RawMessage `json:"locator"`
	OccurredAt       string          `json:"occurred_at,omitempty"`
}

type frictionResponse struct {
	Count            int                      `json:"count"`
	Records          []frictionRecordResponse `json:"records"`
	Complete         bool                     `json:"complete"`
	RecordsTruncated bool                     `json:"records_truncated"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, 200)
	if r.URL.Query().Get("summary") == "1" {
		rows, err := s.db.QueryContext(r.Context(), `
			SELECT id, source, source_session_id, title, task_text, started_at
			FROM sessions ORDER BY COALESCE(started_at, ''), id LIMIT ?`, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := make([]sessionSummaryResponse, 0)
		for rows.Next() {
			var item sessionSummaryResponse
			var title, taskText, started sql.NullString
			if err := rows.Scan(&item.ID, &item.Source, &item.SourceSessionID, &title, &taskText, &started); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if title.Valid {
				item.Title = &title.String
			}
			if taskText.Valid {
				item.TaskText = &taskText.String
			}
			if started.Valid && started.String != "" {
				parsed, err := time.Parse(time.RFC3339Nano, started.String)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				item.StartedAt = &parsed
			}
			out = append(out, item)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, source, source_session_id, title, task_text, started_at, ended_at, harness_version, model, cwd,
		       (SELECT COUNT(*) FROM events e WHERE e.session_id = sessions.id),
		       (SELECT COUNT(*) FROM events e WHERE e.session_id = sessions.id AND e.event_type IN ('transcript_message', 'transcript_tool_call', 'transcript_tool_result')),
		       (SELECT COUNT(DISTINCT e.asset_id) FROM events e WHERE e.session_id = sessions.id AND e.asset_id IS NOT NULL)
		FROM sessions ORDER BY COALESCE(started_at, ''), id LIMIT ?`, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := make([]sessionResponse, 0)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, *item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	row := s.db.QueryRowContext(r.Context(), `
		SELECT id, source, source_session_id, title, task_text, started_at, ended_at, harness_version, model, cwd,
		       (SELECT COUNT(*) FROM events e WHERE e.session_id = sessions.id),
		       (SELECT COUNT(*) FROM events e WHERE e.session_id = sessions.id AND e.event_type IN ('transcript_message', 'transcript_tool_call', 'transcript_tool_result')),
		       (SELECT COUNT(DISTINCT e.asset_id) FROM events e WHERE e.session_id = sessions.id AND e.asset_id IS NOT NULL)
		FROM sessions WHERE id = ?`, r.PathValue("id"))
	item, err := scanSession(row)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	paged := r.URL.Query().Get("events") == "page"
	eventOffset := queryOffset(r)
	eventLimit := queryLimit(r, 1000)
	eventQuery := `SELECT id, session_id, event_type, asset_id, asset_version_id, COALESCE(source_event_id, ''), participation_signal, observation_level, COALESCE(payload_json, ''), COALESCE(locator_json, ''), COALESCE(occurred_at, ''), COALESCE(adapter_version, '') FROM events WHERE session_id = ? ORDER BY occurred_at, id`
	args := []any{item.ID}
	if paged {
		eventQuery += ` LIMIT ? OFFSET ?`
		args = append(args, eventLimit, eventOffset)
	}
	rows, err := s.db.QueryContext(r.Context(), eventQuery, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	events := make([]map[string]any, 0, eventLimit)
	for rows.Next() {
		var id int64
		var sessionID, eventType, sourceID, observation, payload, locator, occurred, adapterVersion string
		var assetID, signal sql.NullString
		var versionID sql.NullInt64
		if err := rows.Scan(&id, &sessionID, &eventType, &assetID, &versionID, &sourceID, &signal, &observation, &payload, &locator, &occurred, &adapterVersion); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		events = append(events, eventResponseFromFields(id, sessionID, eventType, assetID, versionID, sourceID, signal, observation, payload, locator, occurred, adapterVersion, paged))
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	friction, err := s.sessionFriction(r.Context(), item.ID, 500)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	response := map[string]any{"session": item, "events": events, "friction": friction}
	if paged {
		response["event_offset"] = eventOffset
		response["event_limit"] = eventLimit
		response["event_total"] = item.EventCount
		response["events_has_more"] = eventOffset+len(events) < item.EventCount
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) sessionFriction(ctx context.Context, sessionID string, limit int) (*frictionResponse, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM friction_records WHERE session_id = ?) +
			(SELECT COUNT(*) FROM events WHERE session_id = ? AND event_type = 'asset_violation')`, sessionID, sessionID).Scan(&count); err != nil {
		return nil, fmt.Errorf("api: count session friction: %w", err)
	}
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_event_id, friction_kind, event_type, observation_level,
		       is_error, exit_code, payload_json, locator_json, occurred_at
		FROM (
			SELECT id, source_event_id, friction_kind, event_type, observation_level,
			       is_error, exit_code, payload_json, locator_json, occurred_at
			FROM friction_records WHERE session_id = ?
			UNION ALL
			SELECT id, COALESCE(source_event_id, ''), 'asset_violation', event_type, observation_level,
			       NULL, NULL, COALESCE(payload_json, ''), COALESCE(locator_json, ''), COALESCE(occurred_at, '')
			FROM events WHERE session_id = ? AND event_type = 'asset_violation'
		)
		ORDER BY occurred_at IS NULL, occurred_at, id
		LIMIT ?`, sessionID, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("api: query session friction: %w", err)
	}
	defer rows.Close()
	records := make([]frictionRecordResponse, 0, limit)
	for rows.Next() {
		var record frictionRecordResponse
		var observation, payload, locator, occurred sql.NullString
		var isError sql.NullInt64
		var exitCode sql.NullInt64
		if err := rows.Scan(&record.ID, &record.SourceEventID, &record.FrictionKind, &record.EventType, &observation, &isError, &exitCode, &payload, &locator, &occurred); err != nil {
			return nil, fmt.Errorf("api: scan session friction: %w", err)
		}
		record.ObservationLevel = observation.String
		if isError.Valid {
			value := isError.Int64 != 0
			record.IsError = &value
		}
		if exitCode.Valid {
			value := int(exitCode.Int64)
			record.ExitCode = &value
		}
		record.Payload = rawJSONOrNull(payload.String)
		record.Locator = rawJSONOrNull(locator.String)
		if occurred.Valid {
			record.OccurredAt = occurred.String
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api: iterate session friction: %w", err)
	}
	return &frictionResponse{Count: count, Records: records, Complete: true, RecordsTruncated: len(records) < count}, nil
}

func rawJSONOrNull(value string) json.RawMessage {
	if strings.TrimSpace(value) == "" {
		return json.RawMessage(`null`)
	}
	return json.RawMessage(value)
}

func (s *Server) handleSessionEvent(w http.ResponseWriter, r *http.Request) {
	eventID, err := strconv.ParseInt(r.PathValue("event_id"), 10, 64)
	if err != nil || eventID <= 0 {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}
	row := s.db.QueryRowContext(r.Context(), `
		SELECT id, session_id, event_type, asset_id, asset_version_id, COALESCE(source_event_id, ''), participation_signal,
		       observation_level, COALESCE(payload_json, ''), COALESCE(locator_json, ''), COALESCE(occurred_at, ''), COALESCE(adapter_version, '')
		FROM events WHERE session_id = ? AND id = ?`, r.PathValue("id"), eventID)
	var id int64
	var sessionID, eventType, sourceID, observation, payload, locator, occurred, adapterVersion string
	var assetID, signal sql.NullString
	var versionID sql.NullInt64
	if err := row.Scan(&id, &sessionID, &eventType, &assetID, &versionID, &sourceID, &signal, &observation, &payload, &locator, &occurred, &adapterVersion); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": eventResponseFromFields(id, sessionID, eventType, assetID, versionID, sourceID, signal, observation, payload, locator, occurred, adapterVersion, false)})
}

func eventResponseFromFields(id int64, sessionID, eventType string, assetID sql.NullString, versionID sql.NullInt64, sourceID string, signal sql.NullString, observation, payload, locator, occurred, adapterVersion string, compact bool) map[string]any {
	payloadJSON := json.RawMessage(payload)
	locatorJSON := json.RawMessage(locator)
	payloadTruncated := false
	locatorTruncated := false
	if compact {
		payloadJSON, payloadTruncated = compactEventJSON(payload)
		locatorJSON, locatorTruncated = compactEventJSON(locator)
	}
	event := map[string]any{"id": id, "session_id": sessionID, "event_type": eventType, "observation_level": observation, "payload": payloadJSON, "locator": locatorJSON}
	if payloadTruncated {
		event["payload_truncated"] = true
	}
	if locatorTruncated {
		event["locator_truncated"] = true
	}
	if assetID.Valid {
		event["asset_id"] = assetID.String
	}
	if versionID.Valid {
		event["asset_version_id"] = versionID.Int64
	}
	if signal.Valid {
		event["participation_signal"] = signal.String
	}
	if occurred != "" {
		event["occurred_at"] = occurred
	}
	if adapterVersion != "" {
		event["adapter_version"] = adapterVersion
	}
	if sourceID != "" {
		event["source_event_id"] = sourceID
	}
	return event
}

func compactEventJSON(raw string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return json.RawMessage(`null`), false
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return json.RawMessage(strconv.Quote(compactEventString(trimmed))), len(trimmed) > 512
	}
	compact, changed := compactEventValue(value)
	encoded, err := json.Marshal(compact)
	if err != nil {
		return json.RawMessage(`null`), true
	}
	return json.RawMessage(encoded), changed
}

func compactEventValue(value any) (any, bool) {
	switch item := value.(type) {
	case string:
		if len([]rune(item)) <= 512 {
			return item, false
		}
		return compactEventString(item), true
	case []any:
		changed := false
		for index := range item {
			compact, itemChanged := compactEventValue(item[index])
			item[index] = compact
			changed = changed || itemChanged
		}
		return item, changed
	case map[string]any:
		changed := false
		for key, nested := range item {
			compact, itemChanged := compactEventValue(nested)
			item[key] = compact
			changed = changed || itemChanged
		}
		return item, changed
	default:
		return value, false
	}
}

func compactEventString(value string) string {
	runes := []rune(value)
	if len(runes) <= 512 {
		return value
	}
	return string(runes[:512]) + "… [payload truncated; select the event to load the full local record]"
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, 500)
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT 'state_transition', asset_id, occurred_at, evidence_json, COALESCE(alignment_json, ''), COALESCE(to_state, '')
		FROM state_transitions
		UNION ALL
		SELECT 'environment_changed', COALESCE(asset_id, ''), occurred_at, payload_json, COALESCE(locator_json, ''), ''
		FROM events WHERE event_type = 'environment_changed'
		UNION ALL
		SELECT 'asset_version', asset_id, observed_at, content_hash, COALESCE(content_ref, ''), ''
		FROM asset_versions
		ORDER BY 3, 2 LIMIT ?`, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var kind, assetID, occurred, evidence, alignment, state string
		if err := rows.Scan(&kind, &assetID, &occurred, &evidence, &alignment, &state); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		item := map[string]any{"kind": kind, "asset_id": assetID, "occurred_at": occurred, "evidence": evidence, "alignment": alignment}
		if state != "" {
			item["state"] = state
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := rows.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	clusters, err := s.timelineClusters(r.Context())
	if err != nil {
		http.Error(w, "timeline cluster query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"timeline": items, "clusters": clusters})
}

type notificationResponse struct {
	ID              int64           `json:"id"`
	AssetID         string          `json:"asset_id"`
	AssetName       string          `json:"asset_name"`
	StateInstanceID int64           `json:"state_instance_id"`
	State           vital.State     `json:"state"`
	Kind            string          `json:"kind"`
	Severity        string          `json:"severity"`
	Title           string          `json:"title"`
	Summary         string          `json:"summary"`
	Rule            string          `json:"rule"`
	Evidence        json.RawMessage `json:"evidence"`
	OccurredAt      time.Time       `json:"occurred_at"`
	SessionID       string          `json:"session_id,omitempty"`
	Locator         json.RawMessage `json:"locator,omitempty"`
}

type rawTransitionNotification struct {
	Transition vital.Transition
	AssetName  string
}

// handleNotifications is a read projection over state transitions. There is
// no second mutable notification store: this keeps the rule "state migration
// is the only notification source" and makes replay deterministic. An ignore
// disposition suppresses only the matching state instance.
func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	ignored, err := s.ignoredNotificationInstances(r.Context())
	if err != nil {
		http.Error(w, "notification suppression query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT t.id, t.asset_id, a.name, t.from_state, t.to_state, t.broken_overlay,
		       t.occurred_at, t.evidence_json, COALESCE(t.alignment_json, ''),
		       t.detector_version, t.schema_version, t.threshold_version
		FROM state_transitions t JOIN assets a ON a.id = t.asset_id
		ORDER BY t.occurred_at DESC, t.id DESC LIMIT ?`, queryLimit(r, 1000))
	if err != nil {
		http.Error(w, "notification query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	transitions := make([]rawTransitionNotification, 0)
	for rows.Next() {
		var (
			transition              vital.Transition
			assetName               string
			to, occurred, alignment string
			overlay                 int
			nullFrom                sql.NullString
		)
		if err := rows.Scan(&transition.ID, &transition.AssetID, &assetName, &nullFrom, &to, &overlay, &occurred, &transition.EvidenceJSON, &alignment, &transition.DetectorVersion, &transition.SchemaVersion, &transition.ThresholdVersion); err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if nullFrom.Valid {
			value := vital.State(nullFrom.String)
			transition.FromState = &value
		}
		transition.ToState = vital.State(to)
		transition.BrokenOverlay = overlay != 0
		transition.AlignmentJSON = alignment
		transition.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		transitions = append(transitions, rawTransitionNotification{Transition: transition, AssetName: assetName})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := rows.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	items := make([]notificationResponse, 0)
	for _, raw := range transitions {
		if _, ok := ignored[notificationInstanceKey(raw.Transition.AssetID, raw.Transition.ID)]; ok {
			continue
		}
		item, ok := s.notificationFromTransition(r.Context(), raw)
		if !ok {
			continue
		}
		items = append(items, item)
		if len(items) >= queryLimit(r, 100) {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": items})
}

func (s *Server) ignoredNotificationInstances(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT asset_id, state_instance_id FROM dispositions WHERE action = 'ignore'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ignored := make(map[string]struct{})
	for rows.Next() {
		var assetID string
		var stateID int64
		if err := rows.Scan(&assetID, &stateID); err != nil {
			return nil, err
		}
		ignored[notificationInstanceKey(assetID, stateID)] = struct{}{}
	}
	return ignored, rows.Err()
}

func notificationInstanceKey(assetID string, stateID int64) string {
	return assetID + "\x00" + strconv.FormatInt(stateID, 10)
}

func (s *Server) notificationFromTransition(ctx context.Context, raw rawTransitionNotification) (notificationResponse, bool) {
	transition := raw.Transition
	var envelope struct {
		Reason       string `json:"reason"`
		Rule         string `json:"rule"`
		Resurrection bool   `json:"resurrection"`
	}
	var evidence map[string]json.RawMessage
	if err := json.Unmarshal([]byte(transition.EvidenceJSON), &evidence); err == nil {
		if value, ok := evidence["decision"]; ok {
			_ = json.Unmarshal(value, &envelope)
		}
	}

	kind, severity, title := "", "", ""
	switch {
	case transition.ToState == vital.StateHealthy && envelope.Resurrection:
		kind, severity, title = "verification_passed", "positive", "修改后验证通过"
	case transition.FromState != nil && *transition.FromState == vital.StateAwaitingResurrection && transition.ToState == vital.StateSilent:
		kind, severity, title = "verification_failed", "attention", "修改后验证未通过"
	case transition.BrokenOverlay || transition.ToState == vital.StateBroken:
		kind, severity, title = "broken", "attention", "引用检查发现失效"
	case transition.ToState == vital.StateSilent:
		kind, severity, title = "silent", "attention", "资产进入沉默"
	case transition.ToState == vital.StateBypassed:
		kind, severity, title = "bypassed", "attention", "记录到资产被绕行"
	default:
		return notificationResponse{}, false
	}
	if envelope.Reason == "" {
		envelope.Reason = notificationFallbackSummary(transition, kind)
	}
	if envelope.Rule == "" {
		envelope.Rule = notificationFallbackRule(transition, kind)
	}
	item := notificationResponse{
		ID: transition.ID, AssetID: transition.AssetID, AssetName: raw.AssetName,
		StateInstanceID: transition.ID, State: transition.ToState, Kind: kind,
		Severity: severity, Title: title, Summary: envelope.Reason, Rule: envelope.Rule,
		Evidence: json.RawMessage(transition.EvidenceJSON), OccurredAt: transition.OccurredAt,
	}
	if len(item.Evidence) == 0 {
		item.Evidence = json.RawMessage(`{}`)
	}
	item.SessionID, item.Locator = s.notificationSession(ctx, transition.AssetID, transition.OccurredAt)
	return item, true
}

func (s *Server) notificationSession(ctx context.Context, assetID string, at time.Time) (string, json.RawMessage) {
	var sessionID, locator string
	err := s.db.QueryRowContext(ctx, `
		SELECT p.session_id, COALESCE(p.locator_json, '')
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id
		WHERE av.asset_id = ? AND p.occurred_at IS NOT NULL AND p.occurred_at <> ''
		  AND julianday(p.occurred_at) >= julianday(?)
		ORDER BY p.occurred_at, p.id LIMIT 1`, assetID, formatTime(at)).Scan(&sessionID, &locator)
	if err != nil || sessionID == "" {
		return "", nil
	}
	if locator == "" {
		return sessionID, nil
	}
	return sessionID, json.RawMessage(locator)
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func intPointer(value int) *int { return &value }

func notificationFallbackSummary(transition vital.Transition, kind string) string {
	switch kind {
	case "verification_passed":
		return "修改后首次记录到符合条件的参与。"
	case "verification_failed":
		return "修改后验证窗口内没有记录到符合条件的参与。"
	case "broken":
		return "引用检查记录到缺失项；具体分子、分母和条目见证据。"
	case "silent":
		return "最近相关任务的参与记录触发沉默判定。"
	case "bypassed":
		return "同一会话中记录到资产调用与明确绕行。"
	default:
		return "状态迁移已记录。"
	}
}

func notificationFallbackRule(transition vital.Transition, kind string) string {
	switch kind {
	case "verification_passed":
		return "修改后首次记录到参与且没有明确违背记录。"
	case "verification_failed":
		return "修改后连续 8 个可判定机会没有符合条件的参与。"
	case "broken":
		return "已检查引用中至少有 1 个明确缺失。"
	case "silent":
		return "历史参与至少达到 5 个相关任务且参与率至少 30%，最近 8 个相关任务记录到 0 次参与。"
	case "bypassed":
		return "同一会话中同时记录资产调用和明确绕行。"
	default:
		return "状态迁移规则已记录。"
	}
}

type timelineClusterResponse struct {
	At         time.Time `json:"at"`
	WindowDays int       `json:"window_days"`
	AssetIDs   []string  `json:"asset_ids"`
	AssetNames []string  `json:"asset_names"`
	Summary    string    `json:"summary"`
}

type timelineTransitionFact struct {
	AssetID string
	State   vital.State
	At      time.Time
}

// timelineClusters reports only temporal alignment. It deliberately avoids a
// causal label: an environment anchor and multiple later/nearby state changes
// are facts that can be inspected together, not an explanation of why they
// happened.
func (s *Server) timelineClusters(ctx context.Context) ([]timelineClusterResponse, error) {
	type anchor struct {
		At    time.Time
		Label string
	}
	anchorRows, err := s.db.QueryContext(ctx, `
		SELECT occurred_at, COALESCE(payload_json, '')
		FROM events WHERE event_type = 'environment_changed'
		  AND occurred_at IS NOT NULL AND occurred_at <> ''
		ORDER BY occurred_at, id`)
	if err != nil {
		return nil, err
	}
	anchors := make([]anchor, 0)
	for anchorRows.Next() {
		var occurred, payload string
		if err := anchorRows.Scan(&occurred, &payload); err != nil {
			anchorRows.Close()
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			anchorRows.Close()
			return nil, err
		}
		label := "环境变化"
		var fields struct {
			Field string `json:"field"`
			From  string `json:"from"`
			To    string `json:"to"`
		}
		if json.Unmarshal([]byte(payload), &fields) == nil && fields.Field != "" {
			label = fields.Field + " 发生变化"
			if fields.From != "" && fields.To != "" {
				label += "（" + fields.From + " → " + fields.To + "）"
			}
		}
		anchors = append(anchors, anchor{At: at, Label: label})
	}
	if err := anchorRows.Err(); err != nil {
		anchorRows.Close()
		return nil, err
	}
	if err := anchorRows.Close(); err != nil {
		return nil, err
	}
	if len(anchors) == 0 {
		return []timelineClusterResponse{}, nil
	}

	transitionRows, err := s.db.QueryContext(ctx, `
		SELECT asset_id, to_state, broken_overlay, occurred_at
		FROM state_transitions
		WHERE to_state IN ('silent', 'broken', 'bypassed') OR broken_overlay = 1
		ORDER BY occurred_at, id`)
	if err != nil {
		return nil, err
	}
	transitions := make([]timelineTransitionFact, 0)
	for transitionRows.Next() {
		var item timelineTransitionFact
		var state, occurred string
		var overlay int
		if err := transitionRows.Scan(&item.AssetID, &state, &overlay, &occurred); err != nil {
			transitionRows.Close()
			return nil, err
		}
		item.State = vital.State(state)
		item.At, err = time.Parse(time.RFC3339Nano, occurred)
		if err != nil {
			transitionRows.Close()
			return nil, err
		}
		if item.State == vital.StateBroken || item.State == vital.StateSilent || item.State == vital.StateBypassed || overlay != 0 {
			transitions = append(transitions, item)
		}
	}
	if err := transitionRows.Err(); err != nil {
		transitionRows.Close()
		return nil, err
	}
	if err := transitionRows.Close(); err != nil {
		return nil, err
	}
	if len(transitions) == 0 {
		return []timelineClusterResponse{}, nil
	}

	names := make(map[string]string)
	nameRows, err := s.db.QueryContext(ctx, `SELECT id, name FROM assets`)
	if err != nil {
		return nil, err
	}
	for nameRows.Next() {
		var id, name string
		if err := nameRows.Scan(&id, &name); err != nil {
			nameRows.Close()
			return nil, err
		}
		names[id] = name
	}
	if err := nameRows.Err(); err != nil {
		nameRows.Close()
		return nil, err
	}
	if err := nameRows.Close(); err != nil {
		return nil, err
	}

	const window = 5 * 24 * time.Hour
	clusters := make([]timelineClusterResponse, 0)
	seen := make(map[string]struct{})
	for _, anchor := range anchors {
		ids := make(map[string]struct{})
		for _, transition := range transitions {
			if absDuration(transition.At.Sub(anchor.At)) <= window {
				ids[transition.AssetID] = struct{}{}
			}
		}
		if len(ids) < 2 {
			continue
		}
		assetIDs := make([]string, 0, len(ids))
		for id := range ids {
			assetIDs = append(assetIDs, id)
		}
		sort.Strings(assetIDs)
		key := anchor.At.Format(time.RFC3339Nano) + "\x00" + strings.Join(assetIDs, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		assetNames := make([]string, 0, len(assetIDs))
		for _, id := range assetIDs {
			assetNames = append(assetNames, names[id])
		}
		clusters = append(clusters, timelineClusterResponse{
			At: anchor.At, WindowDays: 5, AssetIDs: assetIDs, AssetNames: assetNames,
			Summary: strconv.Itoa(len(assetIDs)) + " 个资产的需要注意状态迁移与“" + anchor.Label + "”相差不超过 5 天；仅表示时间对齐。",
		})
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].At.Before(clusters[j].At) })
	return clusters, nil
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

type statsResponse struct {
	AssetCount         int            `json:"asset_count"`
	VersionCount       int            `json:"version_count"`
	SessionCount       int            `json:"session_count"`
	EventCount         int            `json:"event_count"`
	OpportunityCount   int            `json:"opportunity_count"`
	ParticipationCount int            `json:"participation_count"`
	StateCounts        map[string]int `json:"state_counts"`
	SourceCounts       map[string]int `json:"source_counts"`
	ObservationLevels  map[string]int `json:"observation_levels"`
	ActivityByDay      map[string]int `json:"activity_by_day"`
	LastEventAt        *time.Time     `json:"last_event_at,omitempty"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := statsResponse{StateCounts: make(map[string]int), SourceCounts: make(map[string]int), ObservationLevels: make(map[string]int), ActivityByDay: make(map[string]int)}
	queries := []struct {
		target *int
		query  string
	}{
		{&stats.AssetCount, `SELECT COUNT(*) FROM assets`},
		{&stats.VersionCount, `SELECT COUNT(*) FROM asset_versions`},
		{&stats.SessionCount, `SELECT COUNT(*) FROM sessions`},
		{&stats.EventCount, `SELECT COUNT(*) FROM events`},
		{&stats.OpportunityCount, `SELECT COUNT(*) FROM opportunities`},
		{&stats.ParticipationCount, `SELECT COUNT(*) FROM participations`},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(r.Context(), item.query).Scan(item.target); err != nil {
			http.Error(w, "stats query failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT state, COUNT(*) FROM vital_states WHERE ended_at IS NULL GROUP BY state ORDER BY state`)
	if err != nil {
		http.Error(w, "state stats query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stats.StateCounts[state] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := rows.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err = s.db.QueryContext(r.Context(), `SELECT source, COUNT(*) FROM sessions GROUP BY source ORDER BY source`)
	if err != nil {
		http.Error(w, "source stats query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var source string
		var count int
		if err := rows.Scan(&source, &count); err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stats.SourceCounts[source] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := rows.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err = s.db.QueryContext(r.Context(), `SELECT observation_level, COUNT(*) FROM participations GROUP BY observation_level ORDER BY observation_level`)
	if err != nil {
		http.Error(w, "observation stats query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var level string
		var count int
		if err := rows.Scan(&level, &count); err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stats.ObservationLevels[level] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := rows.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err = s.db.QueryContext(r.Context(), `
		SELECT substr(occurred_at, 1, 10), COUNT(*)
		FROM events
		WHERE occurred_at IS NOT NULL AND occurred_at <> ''
		GROUP BY substr(occurred_at, 1, 10)
		ORDER BY substr(occurred_at, 1, 10)`)
	if err != nil {
		http.Error(w, "daily activity stats query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			rows.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if day != "" {
			stats.ActivityByDay[day] = count
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := rows.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var last sql.NullString
	if err := s.db.QueryRowContext(r.Context(), `SELECT MAX(occurred_at) FROM events WHERE occurred_at IS NOT NULL AND occurred_at <> ''`).Scan(&last); err != nil {
		http.Error(w, "last event query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if last.Valid && last.String != "" {
		parsed, err := time.Parse(time.RFC3339Nano, last.String)
		if err != nil {
			http.Error(w, "last event timestamp is invalid: "+err.Error(), http.StatusInternalServerError)
			return
		}
		stats.LastEventAt = &parsed
	}
	writeJSON(w, http.StatusOK, stats)
}

type cleanupCandidate struct {
	Asset         assetListItem        `json:"asset"`
	Reason        string               `json:"reason"`
	Rollback      vital.RollbackRecord `json:"rollback"`
	StateInstance int64                `json:"state_instance_id"`
}

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	items, err := s.listAssets(r.Context())
	if err != nil {
		http.Error(w, "cleanup query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]cleanupCandidate, 0)
	for _, item := range items {
		if item.CurrentState == nil || item.CurrentState.State != vital.StateDormant {
			continue
		}
		rollback := vital.RollbackRecord{Reversible: item.SourcePath != nil && strings.TrimSpace(*item.SourcePath) != "", Strategy: "保留源文件；仅撤销逻辑归档标记"}
		if item.SourcePath != nil {
			rollback.SourcePath = *item.SourcePath
		}
		out = append(out, cleanupCandidate{Asset: item, Reason: "资产已达到休眠判定，当前参与次数不超过阈值", Rollback: rollback, StateInstance: item.CurrentState.InstanceID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": out})
}

func (s *Server) handleBatchCleanup(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AssetIDs  []string `json:"asset_ids"`
		Confirmed bool     `json:"confirmed"`
		Reason    string   `json:"reason"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid cleanup request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !request.Confirmed {
		http.Error(w, "batch cleanup requires explicit confirmation", http.StatusBadRequest)
		return
	}
	if len(request.AssetIDs) == 0 {
		http.Error(w, "asset_ids is required", http.StatusBadRequest)
		return
	}
	type pending struct {
		assetID string
		stateID int64
		path    string
	}
	pendingItems := make([]pending, 0, len(request.AssetIDs))
	seen := make(map[string]struct{}, len(request.AssetIDs))
	for _, assetID := range request.AssetIDs {
		if _, ok := seen[assetID]; ok {
			continue
		}
		seen[assetID] = struct{}{}
		asset, err := assets.New(s.db).Get(r.Context(), assetID)
		if err != nil {
			if err == sql.ErrNoRows {
				http.Error(w, "asset not found: "+assetID, http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		current, err := s.vital.Current(r.Context(), assetID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if current == nil || current.State != vital.StateDormant {
			http.Error(w, "only dormant assets can enter batch cleanup: "+assetID, http.StatusConflict)
			return
		}
		if asset.SourcePath == nil || strings.TrimSpace(*asset.SourcePath) == "" {
			http.Error(w, "cleanup requires a recorded source path: "+assetID, http.StatusBadRequest)
			return
		}
		pendingItems = append(pendingItems, pending{assetID: assetID, stateID: current.InstanceID, path: *asset.SourcePath})
	}
	created := make([]vital.Disposition, 0, len(pendingItems))
	store := vital.NewDispositionStore(s.db, s.vital)
	for _, item := range pendingItems {
		disposition, err := store.Apply(r.Context(), vital.DispositionRequest{AssetID: item.assetID, Action: vital.ActionPrune, StateInstanceID: item.stateID, Confirmed: true, Reason: request.Reason, Rollback: vital.RollbackRecord{SourcePath: item.path, Strategy: "保留源文件；仅撤销逻辑归档标记", Reversible: true}})
		if err != nil {
			http.Error(w, "cleanup failed for "+item.assetID+": "+err.Error(), http.StatusConflict)
			return
		}
		created = append(created, *disposition)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"archived": created, "source_files_changed": false})
}

func (s *Server) handleIngestStatus(w http.ResponseWriter, r *http.Request) {
	var assetsCount, sessionsCount, eventsCount int
	if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM assets`).Scan(&assetsCount); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM sessions`).Scan(&sessionsCount); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM events`).Scan(&eventsCount); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "source": "daemon_owned_sqlite", "assets": assetsCount, "sessions": sessionsCount, "events": eventsCount, "message": "counts reflect persisted local facts; no external upload is performed"})
}

func scanSession(row interface{ Scan(...any) error }) (*sessionResponse, error) {
	var item sessionResponse
	var title, taskText, started, ended, harness, model, cwd sql.NullString
	if err := row.Scan(&item.ID, &item.Source, &item.SourceSessionID, &title, &taskText, &started, &ended, &harness, &model, &cwd, &item.EventCount, &item.TranscriptCount, &item.AssetCount); err != nil {
		return nil, err
	}
	if title.Valid {
		item.Title = &title.String
	}
	if taskText.Valid {
		item.TaskText = &taskText.String
	}
	if started.Valid {
		value, err := time.Parse(time.RFC3339Nano, started.String)
		if err != nil {
			return nil, err
		}
		item.StartedAt = &value
	}
	if ended.Valid {
		value, err := time.Parse(time.RFC3339Nano, ended.String)
		if err != nil {
			return nil, err
		}
		item.EndedAt = &value
	}
	if harness.Valid {
		item.HarnessVersion = &harness.String
	}
	if model.Valid {
		item.Model = &model.String
	}
	if cwd.Valid {
		item.CWD = &cwd.String
	}
	return &item, nil
}

func queryLimit(r *http.Request, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > 5000 {
		return 5000
	}
	return value
}

func queryOffset(r *http.Request) int {
	value, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || value < 0 {
		return 0
	}
	if value > 1000000000 {
		return 1000000000
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
