package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"flatline/internal/assets"
	"flatline/internal/vital"
)

type assetListResponse struct {
	Assets []assetListItem `json:"assets"`
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
	// FrictionLinkCount is how many friction records were read as evidence
	// that this asset took part in a session. FrictionLinks lists them and is
	// present only on the detail endpoint — absent means "this response does
	// not answer that", while an empty array means "none".
	FrictionLinkCount int                  `json:"friction_link_count"`
	FrictionLinks     *[]assetFrictionLink `json:"friction_links,omitempty"`
}

// assetFrictionLink is one friction record read as evidence for this asset,
// with enough of the record to show a row and drill into it.
type assetFrictionLink struct {
	FrictionID   int64      `json:"friction_id"`
	Signature    string     `json:"signature"`
	SampleLine   string     `json:"sample_line"`
	SessionID    string     `json:"session_id"`
	SessionTitle *string    `json:"session_title"`
	EventID      *int64     `json:"event_id"`
	OccurredAt   *time.Time `json:"occurred_at"`
	Rule         string     `json:"rule"`
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

// writeAssetLookupError answers a failed asset lookup: 404 when the asset is
// not on record, 500 with the caller's prefix otherwise.
func writeAssetLookupError(w http.ResponseWriter, err error, prefix string) {
	if err == sql.ErrNoRows {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	http.Error(w, prefix+err.Error(), http.StatusInternalServerError)
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
		writeAssetLookupError(w, err, "asset query failed: ")
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
	frictionLinks, err := s.assetFrictionLinks(r.Context(), id, queryLimit(r, maxAssetFrictionLinks))
	if err != nil {
		http.Error(w, "friction link query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, assetDetailResponse{
		Asset:       assetListItem{ID: asset.ID, Kind: asset.Kind, Name: asset.Name, Scope: asset.Scope, SourcePath: asset.SourcePath, FirstSeenAt: asset.FirstSeenAt, LastSeenAt: asset.LastSeenAt, ArchivedAt: asset.ArchivedAt, StateStatus: status, CurrentState: current, Facts: facts, FrictionLinkCount: len(frictionLinks), FrictionLinks: &frictionLinks},
		Description: asset.Description, Versions: versionResponses(versions), CurrentState: current, StateStatus: status,
		Funnel:      funnel,
		Transitions: transitions, Opportunities: opportunities, Participations: participations, RelatedSessions: relatedSessions, Dispositions: dispositions, ReferenceChecks: referenceChecks,
	})
}

func (s *Server) handleAssetTransitions(w http.ResponseWriter, r *http.Request) {
	if _, err := assets.New(s.db).Get(r.Context(), r.PathValue("id")); err != nil {
		writeAssetLookupError(w, err, "")
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
		writeAssetLookupError(w, err, "")
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
		writeAssetLookupError(w, err, "")
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
		writeAssetLookupError(w, err, "asset query failed: ")
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
		writeAssetLookupError(w, err, "")
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
		writeAssetLookupError(w, err, "")
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
	frictionLinks, err := s.assetFrictionLinkCounts(ctx)
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
		out = append(out, assetListItem{ID: asset.ID, Kind: asset.Kind, Name: asset.Name, Scope: asset.Scope, SourcePath: asset.SourcePath, FirstSeenAt: asset.FirstSeenAt, LastSeenAt: asset.LastSeenAt, ArchivedAt: asset.ArchivedAt, StateStatus: status, CurrentState: state, Facts: facts, FrictionLinkCount: frictionLinks[asset.ID]})
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
	frictionLinks, err := s.assetFrictionLinkCounts(ctx)
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
		if state != nil {
			stateCopy := *state
			stateCopy.Evidence = nil
			stateCopy.Baseline = nil
			state = &stateCopy
		}
		out = append(out, assetListItem{ID: asset.ID, Kind: asset.Kind, Name: asset.Name, Scope: asset.Scope, SourcePath: asset.SourcePath, FirstSeenAt: asset.FirstSeenAt, LastSeenAt: asset.LastSeenAt, ArchivedAt: asset.ArchivedAt, StateStatus: status, CurrentState: state, Facts: facts, FrictionLinkCount: frictionLinks[asset.ID]})
	}
	return out, nil
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
	if err := finishRows(rows); err != nil {
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
				SELECT 1 FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id AND p.superseded_at IS NULL
				WHERE p.session_id = o.session_id AND av.asset_id = o.asset_id
		       ) THEN 1 ELSE 0 END,
		       CASE WHEN EXISTS (
				SELECT 1 FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id AND p.superseded_at IS NULL
				WHERE p.session_id = o.session_id AND av.asset_id = o.asset_id
		       ) OR s.source IN ('claude_code', 'codex') THEN 1 ELSE NULL END
		FROM opportunities o JOIN sessions s ON s.id = o.session_id AND o.superseded_at IS NULL
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
		FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id AND p.superseded_at IS NULL
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
