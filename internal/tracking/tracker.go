package tracking

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"flatline/internal/canonical"
	"flatline/internal/storage"
)

// TrackerVersion is the replay metadata carried by every derived row this
// package writes (ADR-10: the derived layer must be recomputable).
const TrackerVersion = "tracker/1"

// Tracker records opportunities and participations and answers explainable
// baseline queries. All writes are idempotent: re-recording the same input is
// a no-op, so the derived layer can be recomputed from canonical events +
// asset versions (ADR-10).
type Tracker struct{ db *storage.DB }

func New(db *storage.DB) *Tracker { return &Tracker{db: db} }

// Opportunity is the persisted denominator row used by detectors and API
// drill-downs. The tracker keeps the shape rule and detector versions visible
// so a replay can explain which classification produced the row.
type Opportunity struct {
	ID               int64
	SessionID        string
	ShapeClass       string
	ShapeRuleVersion string
	AssetID          string
	DetectorVersion  string
	DetectedAt       time.Time
}

// OpportunityFor returns the opportunity for a session/asset/shape tuple.
// Absence is sql.ErrNoRows and is not converted into a zero opportunity.
func (t *Tracker) OpportunityFor(ctx context.Context, sessionID, assetID, shapeClass string) (*Opportunity, error) {
	if sessionID == "" || assetID == "" || shapeClass == "" {
		return nil, fmt.Errorf("tracking: opportunity lookup requires session, asset, and shape class")
	}
	row := t.db.QueryRowContext(ctx, `
		SELECT id, session_id, shape_class, shape_rule_version, asset_id, detector_version, detected_at
		FROM opportunities WHERE session_id = ? AND asset_id = ? AND shape_class = ? AND superseded_at IS NULL`, sessionID, assetID, shapeClass)
	var opportunity Opportunity
	var detectedAt string
	if err := row.Scan(&opportunity.ID, &opportunity.SessionID, &opportunity.ShapeClass, &opportunity.ShapeRuleVersion, &opportunity.AssetID, &opportunity.DetectorVersion, &detectedAt); err != nil {
		return nil, err
	}
	var err error
	opportunity.DetectedAt, err = time.Parse(time.RFC3339Nano, detectedAt)
	if err != nil {
		return nil, fmt.Errorf("tracking: parse opportunity detected_at: %w", err)
	}
	return &opportunity, nil
}

// Opportunities returns denominator rows for an asset in [start, end),
// optionally restricted to one shape class.
func (t *Tracker) Opportunities(ctx context.Context, assetID, shapeClass string, start, end time.Time, limit int) ([]Opportunity, error) {
	if assetID == "" || !start.Before(end) {
		return nil, fmt.Errorf("tracking: opportunity query requires asset and valid window")
	}
	if limit <= 0 {
		limit = 1000
	}
	query := `
		SELECT id, session_id, shape_class, shape_rule_version, asset_id, detector_version, detected_at
		FROM opportunities
		WHERE superseded_at IS NULL AND asset_id = ?
		  AND julianday(detected_at) >= julianday(?) AND julianday(detected_at) < julianday(?)`
	args := []any{assetID, formatTime(start), formatTime(end)}
	if shapeClass != "" {
		query += " AND shape_class = ?"
		args = append(args, shapeClass)
	}
	query += " ORDER BY detected_at, id LIMIT ?"
	args = append(args, limit)
	rows, err := t.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("tracking: query opportunities: %w", err)
	}
	defer rows.Close()
	var out []Opportunity
	for rows.Next() {
		var opportunity Opportunity
		var detectedAt string
		if err := rows.Scan(&opportunity.ID, &opportunity.SessionID, &opportunity.ShapeClass, &opportunity.ShapeRuleVersion, &opportunity.AssetID, &opportunity.DetectorVersion, &detectedAt); err != nil {
			return nil, fmt.Errorf("tracking: scan opportunity: %w", err)
		}
		opportunity.DetectedAt, err = time.Parse(time.RFC3339Nano, detectedAt)
		if err != nil {
			return nil, fmt.Errorf("tracking: parse opportunity detected_at: %w", err)
		}
		out = append(out, opportunity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tracking: iterate opportunities: %w", err)
	}
	return out, nil
}

// SessionShape is the deterministic task-shape input for one session: the
// raw task tags (classified by rule shape/1) and the assets the session
// could have involved.
type SessionShape struct {
	SessionID  string
	Tags       []string
	AssetIDs   []string
	DetectedAt time.Time
}

// RecordSessionShape classifies the session's task shape and records one
// opportunity per (session, shape class, asset). It returns the shape class
// and the number of opportunities inserted (0 on a fully idempotent replay).
//
// A session with no classifiable tags is an error, not an empty class:
// absence of a shape is never recorded as a shape (缺失 ≠ 零).
func (t *Tracker) RecordSessionShape(ctx context.Context, shape SessionShape) (string, int, error) {
	if shape.SessionID == "" {
		return "", 0, fmt.Errorf("tracking: session id is required")
	}
	if len(shape.AssetIDs) == 0 {
		return "", 0, fmt.Errorf("tracking: at least one asset id is required")
	}
	if shape.DetectedAt.IsZero() {
		return "", 0, fmt.Errorf("tracking: detected_at is required")
	}
	if shape.DetectedAt.Location() != time.UTC {
		return "", 0, fmt.Errorf("tracking: detected_at must be UTC")
	}
	class, _, err := ClassifyShape(shape.Tags)
	if err != nil {
		return "", 0, err
	}
	if class == "" {
		return "", 0, fmt.Errorf("tracking: session %s has no task shape; opportunities require a shape class", shape.SessionID)
	}
	if err := t.sessionExists(ctx, shape.SessionID); err != nil {
		return "", 0, err
	}
	// Validate every asset before opening the transaction. The storage layer
	// serializes access on a single connection (MaxOpenConns(1)), so a query
	// issued while a transaction is open would deadlock.
	for _, assetID := range shape.AssetIDs {
		if assetID == "" {
			return "", 0, fmt.Errorf("tracking: empty asset id in session %s", shape.SessionID)
		}
		if err := t.assetExists(ctx, assetID); err != nil {
			return "", 0, err
		}
	}

	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, fmt.Errorf("tracking: begin opportunity transaction: %w", err)
	}
	rollback := func(err error) (string, int, error) {
		_ = tx.Rollback()
		return "", 0, err
	}
	inserted := 0
	for _, assetID := range shape.AssetIDs {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO opportunities (session_id, shape_class, shape_rule_version, asset_id, detector_version, detected_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (session_id, shape_class, asset_id)
			DO UPDATE SET superseded_at = NULL WHERE opportunities.superseded_at IS NOT NULL`,
			shape.SessionID, class, ShapeRuleVersion, assetID, TrackerVersion, formatTime(shape.DetectedAt))
		if err != nil {
			return rollback(fmt.Errorf("tracking: record opportunity %s/%s: %w", shape.SessionID, assetID, err))
		}
		n, err := result.RowsAffected()
		if err != nil {
			return rollback(fmt.Errorf("tracking: count opportunity %s/%s: %w", shape.SessionID, assetID, err))
		}
		inserted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("tracking: commit opportunities: %w", err)
	}
	return class, inserted, nil
}

// ParticipationInput is one participation record. Signal (what happened) and
// Level (how we know it) are orthogonal closed enums; both are validated and
// non-canonical values are rejected.
type ParticipationInput struct {
	AssetVersionID int64
	SessionID      string
	OpportunityID  *int64
	Signal         canonical.ParticipationSignal
	Level          canonical.ObservationLevel
	OccurredAt     *time.Time
	Locator        *canonical.Locator
}

// RecordParticipation records one participation. It returns true when a row
// was inserted and false when the (asset_version, session, signal) row
// already existed (idempotent replay).
func (t *Tracker) RecordParticipation(ctx context.Context, p ParticipationInput) (bool, error) {
	if p.SessionID == "" {
		return false, fmt.Errorf("tracking: participation session id is required")
	}
	if p.AssetVersionID <= 0 {
		return false, fmt.Errorf("tracking: participation requires a positive asset version id")
	}
	if !p.Signal.Valid() {
		return false, fmt.Errorf("tracking: invalid participation signal %q", p.Signal)
	}
	if !p.Level.Valid() {
		return false, fmt.Errorf("tracking: invalid observation level %q", p.Level)
	}
	if p.OpportunityID != nil && *p.OpportunityID <= 0 {
		return false, fmt.Errorf("tracking: opportunity id must be positive")
	}
	if p.OccurredAt != nil {
		if p.OccurredAt.IsZero() {
			return false, fmt.Errorf("tracking: occurred_at must not be zero")
		}
		if p.OccurredAt.Location() != time.UTC {
			return false, fmt.Errorf("tracking: occurred_at must be UTC")
		}
	}
	if p.Locator != nil && !p.Locator.Valid() {
		return false, fmt.Errorf("tracking: invalid locator")
	}
	if err := t.sessionExists(ctx, p.SessionID); err != nil {
		return false, err
	}
	if err := t.assetVersionExists(ctx, p.AssetVersionID); err != nil {
		return false, err
	}
	if p.OpportunityID != nil {
		if err := t.opportunityMatchesVersion(ctx, *p.OpportunityID, p.SessionID, p.AssetVersionID); err != nil {
			return false, err
		}
	}

	var locator any
	if p.Locator != nil {
		encoded, err := json.Marshal(p.Locator)
		if err != nil {
			return false, fmt.Errorf("tracking: marshal locator: %w", err)
		}
		locator = string(encoded)
	}
	var occurred any
	if p.OccurredAt != nil {
		occurred = formatTime(*p.OccurredAt)
	}
	result, err := t.db.ExecContext(ctx, `
		INSERT INTO participations (asset_version_id, session_id, opportunity_id, participation_signal, observation_level, occurred_at, locator_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (asset_version_id, session_id, participation_signal) DO NOTHING`,
		p.AssetVersionID, p.SessionID, p.OpportunityID, string(p.Signal), string(p.Level), occurred, locator)
	if err != nil {
		return false, fmt.Errorf("tracking: record participation: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("tracking: count participation: %w", err)
	}
	return n > 0, nil
}

// Baseline is the explainable rolling-window baseline for one asset and shape
// class (ADR-4): the participation rate and absolute counts in opportunity
// sessions, with the window and rule version that produced it.
//
// Rate is nil when the window contains no opportunities: "no baseline" is
// represented explicitly, never as 0 (缺失 ≠ 零).
type Baseline struct {
	AssetID               string
	ShapeClass            string
	ShapeRuleVersion      string
	TrackerVersion        string
	WindowStart           time.Time
	WindowEnd             time.Time
	OpportunitySessions   int
	ParticipatingSessions int
	Rate                  *float64
}

// Baseline computes the rolling-window baseline for (asset, shape class) over
// the half-open window [start, end). The denominator is the number of
// distinct sessions with an opportunity for the asset in that shape class
// inside the window; the numerator is the number of those sessions with at
// least one participation of any version of the asset.
func (t *Tracker) Baseline(ctx context.Context, assetID, shapeClass string, start, end time.Time) (*Baseline, error) {
	if assetID == "" || shapeClass == "" {
		return nil, fmt.Errorf("tracking: baseline requires asset id and shape class")
	}
	if !start.Before(end) {
		return nil, fmt.Errorf("tracking: baseline window start must be before end")
	}
	if err := t.assetExists(ctx, assetID); err != nil {
		return nil, err
	}
	var denominator, numerator int
	err := t.db.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT o.session_id),
			COUNT(DISTINCT CASE WHEN p.session_id IS NOT NULL THEN o.session_id END)
		FROM opportunities o
		LEFT JOIN participations p
			ON p.session_id = o.session_id
			AND p.superseded_at IS NULL
			AND p.asset_version_id IN (SELECT id FROM asset_versions WHERE asset_id = o.asset_id)
		WHERE o.superseded_at IS NULL AND o.asset_id = ?
		  AND o.shape_class = ?
		  AND julianday(o.detected_at) >= julianday(?)
		  AND julianday(o.detected_at) < julianday(?)`,
		assetID, shapeClass, formatTime(start), formatTime(end)).Scan(&denominator, &numerator)
	if err != nil {
		return nil, fmt.Errorf("tracking: baseline query: %w", err)
	}
	baseline := &Baseline{
		AssetID:               assetID,
		ShapeClass:            shapeClass,
		ShapeRuleVersion:      ShapeRuleVersion,
		TrackerVersion:        TrackerVersion,
		WindowStart:           start,
		WindowEnd:             end,
		OpportunitySessions:   denominator,
		ParticipatingSessions: numerator,
	}
	if denominator > 0 {
		rate := float64(numerator) / float64(denominator)
		baseline.Rate = &rate
	}
	return baseline, nil
}

// CountOpportunities is the explainable denominator: distinct sessions with
// an opportunity for (asset, shape class) in the half-open window [start, end).
func (t *Tracker) CountOpportunities(ctx context.Context, assetID, shapeClass string, start, end time.Time) (int, error) {
	if assetID == "" || shapeClass == "" {
		return 0, fmt.Errorf("tracking: count requires asset id and shape class")
	}
	if !start.Before(end) {
		return 0, fmt.Errorf("tracking: window start must be before end")
	}
	var n int
	err := t.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT session_id)
		FROM opportunities
		WHERE superseded_at IS NULL AND asset_id = ? AND shape_class = ?
		  AND julianday(detected_at) >= julianday(?)
		  AND julianday(detected_at) < julianday(?)`,
		assetID, shapeClass, formatTime(start), formatTime(end)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("tracking: count opportunities: %w", err)
	}
	return n, nil
}

// CountParticipatingSessions is the explainable numerator: distinct sessions
// in the window that have an opportunity for (asset, shape class) and at
// least one participation of any version of the asset.
func (t *Tracker) CountParticipatingSessions(ctx context.Context, assetID, shapeClass string, start, end time.Time) (int, error) {
	if assetID == "" || shapeClass == "" {
		return 0, fmt.Errorf("tracking: count requires asset id and shape class")
	}
	if !start.Before(end) {
		return 0, fmt.Errorf("tracking: window start must be before end")
	}
	var n int
	err := t.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT o.session_id)
		FROM opportunities o
		JOIN participations p
			ON p.session_id = o.session_id
			AND p.superseded_at IS NULL
			AND p.asset_version_id IN (SELECT id FROM asset_versions WHERE asset_id = o.asset_id)
		WHERE o.superseded_at IS NULL AND o.asset_id = ? AND o.shape_class = ?
		  AND julianday(o.detected_at) >= julianday(?)
		  AND julianday(o.detected_at) < julianday(?)`,
		assetID, shapeClass, formatTime(start), formatTime(end)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("tracking: count participating sessions: %w", err)
	}
	return n, nil
}

func (t *Tracker) sessionExists(ctx context.Context, sessionID string) error {
	var found int
	if err := t.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`, sessionID).Scan(&found); err != nil {
		return fmt.Errorf("tracking: check session %s: %w", sessionID, err)
	}
	if found == 0 {
		return fmt.Errorf("tracking: session %s does not exist", sessionID)
	}
	return nil
}

func (t *Tracker) assetExists(ctx context.Context, assetID string) error {
	var found int
	if err := t.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE id = ?`, assetID).Scan(&found); err != nil {
		return fmt.Errorf("tracking: check asset %s: %w", assetID, err)
	}
	if found == 0 {
		return fmt.Errorf("tracking: asset %s does not exist", assetID)
	}
	return nil
}

func (t *Tracker) assetVersionExists(ctx context.Context, versionID int64) error {
	var found int
	if err := t.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_versions WHERE id = ?`, versionID).Scan(&found); err != nil {
		return fmt.Errorf("tracking: check asset version %d: %w", versionID, err)
	}
	if found == 0 {
		return fmt.Errorf("tracking: asset version %d does not exist", versionID)
	}
	return nil
}

func (t *Tracker) opportunityMatchesVersion(ctx context.Context, opportunityID int64, sessionID string, versionID int64) error {
	var opportunityAsset, versionAsset string
	err := t.db.QueryRowContext(ctx, `
		SELECT o.asset_id, av.asset_id
		FROM opportunities o
		JOIN asset_versions av ON av.id = ?
		WHERE o.id = ? AND o.session_id = ?`, versionID, opportunityID, sessionID).
		Scan(&opportunityAsset, &versionAsset)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("tracking: opportunity %d does not belong to session %s", opportunityID, sessionID)
		}
		return fmt.Errorf("tracking: check opportunity %d: %w", opportunityID, err)
	}
	if opportunityAsset != versionAsset {
		return fmt.Errorf("tracking: opportunity %d is for asset %s, version %d is for asset %s", opportunityID, opportunityAsset, versionID, versionAsset)
	}
	return nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
