package runtime

import (
	"context"
	"fmt"
	"log"
	"time"

	"flatline/internal/assets"
	"flatline/internal/vital"
)

// FullEvaluationInterval bounds how long a state may go without a full sweep.
// Most detectors only react to new facts, but dormancy is a function of
// elapsed time alone: an asset that nothing touches would never be re-examined
// under a purely fact-driven trigger.
const FullEvaluationInterval = time.Hour

// evaluationMarks are the highest row ids seen by the previous evaluation.
// Anything above them is a fact this process has not judged yet.
type evaluationMarks struct {
	Event         int64
	AssetVersion  int64
	ReferenceRun  int64
	Opportunity   int64
	Participation int64
}

// EvaluationReport explains what one evaluation pass actually did, so the
// daemon log can say how many assets were judged and how many were left alone.
type EvaluationReport struct {
	Full      bool
	Evaluated int
	Skipped   int
	Reason    string
}

// EvaluateIncremental judges only the assets whose inputs changed since the
// last pass. With no new facts and no full sweep due, it judges nothing: no
// recorded fact changed, so no recorded state can have changed either.
func (a *App) EvaluateIncremental(ctx context.Context, asOf time.Time) (EvaluationReport, []vital.Decision, error) {
	if a == nil || a.db == nil || a.registry == nil {
		return EvaluationReport{}, nil, fmt.Errorf("runtime: app is not fully wired")
	}
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	if asOf.Location() != time.UTC {
		return EvaluationReport{}, nil, fmt.Errorf("runtime: as_of must be UTC")
	}

	a.evalMu.Lock()
	defer a.evalMu.Unlock()

	marks, err := a.currentMarks(ctx)
	if err != nil {
		return EvaluationReport{}, nil, err
	}
	assetsList, err := a.registry.List(ctx)
	if err != nil {
		return EvaluationReport{}, nil, err
	}

	if a.lastFullAt.IsZero() || asOf.Sub(a.lastFullAt) >= FullEvaluationInterval {
		reason := "periodic full sweep"
		if a.lastFullAt.IsZero() {
			reason = "first pass in this process"
		}
		decisions, err := a.evaluate(ctx, assetsList, asOf)
		if err != nil {
			return EvaluationReport{}, nil, err
		}
		a.evalMarks, a.lastFullAt = marks, asOf
		return EvaluationReport{Full: true, Evaluated: len(decisions), Reason: reason}, decisions, nil
	}

	changed, err := a.changedAssetIDs(ctx, a.evalMarks, marks)
	if err != nil {
		return EvaluationReport{}, nil, err
	}
	if len(changed) == 0 {
		a.evalMarks = marks
		return EvaluationReport{Skipped: len(assetsList), Reason: "no new events, versions, opportunities, participations or reference checks"}, nil, nil
	}
	subset := make([]assets.Asset, 0, len(changed))
	for _, asset := range assetsList {
		if _, ok := changed[asset.ID]; ok {
			subset = append(subset, asset)
		}
	}
	decisions, err := a.evaluate(ctx, subset, asOf)
	if err != nil {
		return EvaluationReport{}, nil, err
	}
	a.evalMarks = marks
	return EvaluationReport{Evaluated: len(decisions), Skipped: len(assetsList) - len(decisions), Reason: "changed inputs only"}, decisions, nil
}

func (a *App) currentMarks(ctx context.Context) (evaluationMarks, error) {
	var marks evaluationMarks
	err := a.db.QueryRowContext(ctx, `
		SELECT (SELECT COALESCE(MAX(id), 0) FROM events),
		       (SELECT COALESCE(MAX(id), 0) FROM asset_versions),
		       (SELECT COALESCE(MAX(id), 0) FROM reference_checks),
		       (SELECT COALESCE(MAX(id), 0) FROM opportunities),
		       (SELECT COALESCE(MAX(id), 0) FROM participations)`).
		Scan(&marks.Event, &marks.AssetVersion, &marks.ReferenceRun, &marks.Opportunity, &marks.Participation)
	if err != nil {
		return evaluationMarks{}, fmt.Errorf("runtime: read evaluation marks: %w", err)
	}
	return marks, nil
}

func (a *App) changedAssetIDs(ctx context.Context, from, to evaluationMarks) (map[string]struct{}, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT DISTINCT asset_id FROM events WHERE id > ? AND id <= ? AND asset_id IS NOT NULL
		UNION SELECT asset_id FROM asset_versions WHERE id > ? AND id <= ?
		UNION SELECT av.asset_id FROM reference_checks rc JOIN asset_versions av ON av.id = rc.asset_version_id WHERE rc.id > ? AND rc.id <= ?
		UNION SELECT asset_id FROM opportunities WHERE id > ? AND id <= ?
		UNION SELECT av.asset_id FROM participations p JOIN asset_versions av ON av.id = p.asset_version_id WHERE p.id > ? AND p.id <= ?`,
		from.Event, to.Event, from.AssetVersion, to.AssetVersion, from.ReferenceRun, to.ReferenceRun,
		from.Opportunity, to.Opportunity, from.Participation, to.Participation)
	if err != nil {
		return nil, fmt.Errorf("runtime: query changed assets: %w", err)
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("runtime: scan changed asset: %w", err)
		}
		if id != "" {
			out[id] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runtime: iterate changed assets: %w", err)
	}
	return out, nil
}

// RecomputeMissingSessionStats fills projection rows for sessions that have
// none, so a database migrated before the projection existed is complete after
// the first startup.
func (a *App) RecomputeMissingSessionStats(ctx context.Context) (int, error) {
	if a == nil || a.events == nil {
		return 0, fmt.Errorf("runtime: event store is not wired")
	}
	filled, err := a.events.RecomputeMissingSessionStats(ctx)
	if err != nil {
		return 0, err
	}
	// The same startup pass covers the thread facts and the command/file
	// projections: both were added after these sessions were ingested, and an
	// unchanged transcript is never replayed.
	threads, err := a.BackfillSessionHierarchy(ctx)
	if err != nil {
		return filled, err
	}
	if threads > 0 {
		log.Printf("session thread facts backfilled sessions=%d", threads)
	}
	projected, err := a.RecomputeMissingProjections(ctx)
	if err != nil {
		return filled, err
	}
	if projected > 0 {
		log.Printf("session command/file projections backfilled sessions=%d", projected)
	}
	return filled, nil
}
