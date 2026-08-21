package baseline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"flatline/internal/storage"
)

// Baseline is the explainable rolling-window baseline for one asset (ADR-4):
// the participation rate and absolute counts in opportunity sessions, with the
// window and rule versions that produced it. Every ratio is drillable to its
// numerator and denominator (AGENTS.md §2.3).
//
// Rate is nil when the window contains no opportunities: "no baseline" is
// represented explicitly, never as 0 (缺失 ≠ 零).
type Baseline struct {
	AssetID          string
	ShapeClass       string // "" means all shape classes
	BaselineVersion  string
	ShapeRuleVersion string
	WindowStart      time.Time
	WindowEnd        time.Time

	// Denominator: distinct sessions with an opportunity for the asset (in the
	// shape class) inside the window.
	OpportunitySessions   int
	OpportunitySessionIDs []string

	// Numerator: of those sessions, the ones with at least one participation of
	// any version of the asset.
	ParticipatingSessions   int
	ParticipatingSessionIDs []string

	// Rate = ParticipatingSessions / OpportunitySessions; nil when the
	// denominator is zero.
	Rate *float64
}

// Summary returns a one-line, non-causal explanation of the baseline (ADR-8):
// the numerator, denominator, and window, with no causal language.
func (b *Baseline) Summary() string {
	if b.OpportunitySessions == 0 {
		return fmt.Sprintf("%s: no opportunities in window %s .. %s (no baseline)",
			b.AssetID, formatTime(b.WindowStart), formatTime(b.WindowEnd))
	}
	rate := 0.0
	if b.Rate != nil {
		rate = *b.Rate
	}
	return fmt.Sprintf("%s: %d of %d opportunity sessions participated (%.0f%%) in window %s .. %s",
		b.AssetID, b.ParticipatingSessions, b.OpportunitySessions, rate*100,
		formatTime(b.WindowStart), formatTime(b.WindowEnd))
}

// Query computes explainable baselines from opportunities and participations.
type Query struct{ db *storage.DB }

// NewQuery builds a baseline Query over the given database.
func NewQuery(db *storage.DB) *Query { return &Query{db: db} }

// Compute returns the baseline for (asset, shape class) over the half-open
// window [start, end). shapeClass may be "" to aggregate across all shape
// classes for the asset. The denominator is the number of distinct sessions
// with an opportunity for the asset in the window; the numerator is the number
// of those sessions with at least one participation of any version of the
// asset.
func (q *Query) Compute(ctx context.Context, assetID, shapeClass string, start, end time.Time) (*Baseline, error) {
	if assetID == "" {
		return nil, fmt.Errorf("baseline: asset id is required")
	}
	if !start.Before(end) {
		return nil, fmt.Errorf("baseline: window start must be before end")
	}
	if err := q.assetExists(ctx, assetID); err != nil {
		return nil, err
	}

	where, args := windowPredicate(assetID, shapeClass, start, end)

	opportunityIDs, err := q.sessionIDs(ctx, `
		SELECT DISTINCT o.session_id
		FROM opportunities o
		WHERE `+where+`
		ORDER BY o.session_id`, args...)
	if err != nil {
		return nil, err
	}

	participatingIDs, err := q.sessionIDs(ctx, `
		SELECT DISTINCT o.session_id
		FROM opportunities o
		JOIN participations p
			ON p.session_id = o.session_id
			AND p.asset_version_id IN (SELECT id FROM asset_versions WHERE asset_id = o.asset_id)
		WHERE `+where+`
		ORDER BY o.session_id`, args...)
	if err != nil {
		return nil, err
	}

	shapeRuleVersion, err := q.shapeRuleVersion(ctx, where, args...)
	if err != nil {
		return nil, err
	}

	b := &Baseline{
		AssetID:                 assetID,
		ShapeClass:              shapeClass,
		BaselineVersion:         BaselineVersion,
		ShapeRuleVersion:        shapeRuleVersion,
		WindowStart:             start,
		WindowEnd:               end,
		OpportunitySessions:     len(opportunityIDs),
		OpportunitySessionIDs:   opportunityIDs,
		ParticipatingSessions:   len(participatingIDs),
		ParticipatingSessionIDs: participatingIDs,
	}
	if b.OpportunitySessions > 0 {
		rate := float64(b.ParticipatingSessions) / float64(b.OpportunitySessions)
		b.Rate = &rate
	}
	return b, nil
}

// windowPredicate builds the shared WHERE clause and arguments for the
// (asset, shape class, half-open window) filter. The shape class filter is
// omitted when shapeClass is empty (aggregate across all shape classes).
func windowPredicate(assetID, shapeClass string, start, end time.Time) (string, []any) {
	where := "o.asset_id = ?"
	args := []any{assetID}
	if shapeClass != "" {
		where += " AND o.shape_class = ?"
		args = append(args, shapeClass)
	}
	where += " AND julianday(o.detected_at) >= julianday(?)"
	args = append(args, formatTime(start))
	where += " AND julianday(o.detected_at) < julianday(?)"
	args = append(args, formatTime(end))
	return where, args
}

// sessionIDs runs a query that returns distinct session ids and collects them.
func (q *Query) sessionIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("baseline: baseline query: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("baseline: scan session id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("baseline: iterate session ids: %w", err)
	}
	return out, nil
}

// shapeRuleVersion returns the distinct shape rule version(s) used by the
// opportunities in the window, comma-joined. It is empty when the window has no
// opportunities. Carrying it makes the baseline replayable (ADR-10): a change
// to the shape rule is visible in the derived output.
func (q *Query) shapeRuleVersion(ctx context.Context, where string, args ...any) (string, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT DISTINCT o.shape_rule_version
		FROM opportunities o
		WHERE `+where+`
		ORDER BY o.shape_rule_version`, args...)
	if err != nil {
		return "", fmt.Errorf("baseline: query shape rule version: %w", err)
	}
	defer rows.Close()
	var versions []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return "", fmt.Errorf("baseline: scan shape rule version: %w", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("baseline: iterate shape rule versions: %w", err)
	}
	return strings.Join(versions, ","), nil
}

func (q *Query) assetExists(ctx context.Context, assetID string) error {
	var found int
	if err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE id = ?`, assetID).Scan(&found); err != nil {
		return fmt.Errorf("baseline: check asset %s: %w", assetID, err)
	}
	if found == 0 {
		return fmt.Errorf("baseline: asset %s does not exist", assetID)
	}
	return nil
}
