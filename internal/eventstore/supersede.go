package eventstore

import (
	"context"
	"fmt"
	"time"

	"flatline/internal/canonical"
)

// Superseding asset evidence is what lets a parser rule get stricter without
// rewriting history.
//
// A canonical event is append-only: the row that says "this transcript loaded
// that asset" cannot be edited or deleted. But when the rule that produced it
// changes — a bare basename is no longer a reference — reading the same source
// text again produces a different, smaller set of those events, and the two
// readings cannot both stand. superseded_at records that the later reading of
// the same text no longer produces this row. The row itself, its payload, its
// locator and its timestamps are untouched, and clearing the column restores
// the earlier reading exactly.
//
// The derived rows follow the evidence: a participation or an opportunity is
// superseded only when every asset_invoked event for that session and that
// asset is superseded. One live event keeps them standing.

// SupersedeReport counts what one correction changed.
type SupersedeReport struct {
	Events         int
	Participations int
	Opportunities  int
	// Restored counts the events a re-read produced again after an earlier
	// pass had superseded them, which is what makes the channel reversible.
	Restored int
}

// SupersedeAssetEvidence reconciles one session's stored asset evidence with
// what the current parser produces for it. live is the set of source event ids
// the fresh parse of every transcript of this session emitted for
// asset_invoked; a stored event outside that set is superseded, and a stored
// event inside it that a previous pass had superseded is restored.
func (s *Store) SupersedeAssetEvidence(ctx context.Context, sessionID string, live map[string]struct{}) (SupersedeReport, error) {
	var report SupersedeReport
	if sessionID == "" {
		return report, fmt.Errorf("eventstore: session id is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(source_event_id, ''), superseded_at IS NOT NULL
		FROM events WHERE session_id = ? AND event_type = ?`,
		sessionID, canonical.EventTypeAssetInvoked)
	if err != nil {
		return report, fmt.Errorf("eventstore: read asset evidence %s: %w", sessionID, err)
	}
	var supersede, restore []int64
	for rows.Next() {
		var id int64
		var sourceEventID string
		var superseded bool
		if err := rows.Scan(&id, &sourceEventID, &superseded); err != nil {
			rows.Close()
			return report, fmt.Errorf("eventstore: scan asset evidence: %w", err)
		}
		_, produced := live[sourceEventID]
		switch {
		case !produced && !superseded:
			supersede = append(supersede, id)
		case produced && superseded:
			restore = append(restore, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, fmt.Errorf("eventstore: iterate asset evidence: %w", err)
	}
	if err := rows.Close(); err != nil {
		return report, fmt.Errorf("eventstore: close asset evidence: %w", err)
	}
	if len(supersede) == 0 && len(restore) == 0 {
		return report, nil
	}
	at := formatTime(time.Now().UTC())
	if err := s.markEvents(ctx, supersede, at); err != nil {
		return report, err
	}
	if err := s.markEvents(ctx, restore, nil); err != nil {
		return report, err
	}
	report.Events, report.Restored = len(supersede), len(restore)
	derived, err := s.followAssetEvidence(ctx, sessionID, at)
	if err != nil {
		return report, err
	}
	report.Participations, report.Opportunities = derived.Participations, derived.Opportunities
	return report, nil
}

func (s *Store) markEvents(ctx context.Context, ids []int64, at any) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE events SET superseded_at = ? WHERE id = ?`, at, id); err != nil {
			return fmt.Errorf("eventstore: mark event %d superseded: %w", id, err)
		}
	}
	return nil
}

// followAssetEvidence brings the derived rows in line with the events. An
// asset with no live asset_invoked event left in this session has its
// participations and opportunities superseded; an asset that has one again has
// them restored.
func (s *Store) followAssetEvidence(ctx context.Context, sessionID, at string) (SupersedeReport, error) {
	var report SupersedeReport
	rows, err := s.db.QueryContext(ctx, `
		SELECT asset_id,
		       SUM(CASE WHEN superseded_at IS NULL THEN 1 ELSE 0 END)
		FROM events
		WHERE session_id = ? AND event_type = ? AND asset_id IS NOT NULL
		GROUP BY asset_id`, sessionID, canonical.EventTypeAssetInvoked)
	if err != nil {
		return report, fmt.Errorf("eventstore: group asset evidence %s: %w", sessionID, err)
	}
	var dead, alive []any
	for rows.Next() {
		var assetID string
		var liveCount int
		if err := rows.Scan(&assetID, &liveCount); err != nil {
			rows.Close()
			return report, fmt.Errorf("eventstore: scan asset evidence group: %w", err)
		}
		if liveCount == 0 {
			dead = append(dead, assetID)
		} else {
			alive = append(alive, assetID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, fmt.Errorf("eventstore: iterate asset evidence groups: %w", err)
	}
	if err := rows.Close(); err != nil {
		return report, fmt.Errorf("eventstore: close asset evidence groups: %w", err)
	}
	participations, err := s.markDerived(ctx, sessionID, dead, alive, at,
		`participations`, `asset_version_id IN (SELECT id FROM asset_versions WHERE asset_id IN (%s))`)
	if err != nil {
		return report, err
	}
	opportunities, err := s.markDerived(ctx, sessionID, dead, alive, at,
		`opportunities`, `asset_id IN (%s)`)
	if err != nil {
		return report, err
	}
	report.Participations, report.Opportunities = participations, opportunities
	return report, nil
}

func (s *Store) markDerived(ctx context.Context, sessionID string, dead, alive []any, at, table, condition string) (int, error) {
	changed := 0
	for _, step := range []struct {
		assets []any
		value  any
		filter string
	}{
		{assets: dead, value: at, filter: "superseded_at IS NULL"},
		{assets: alive, value: nil, filter: "superseded_at IS NOT NULL"},
	} {
		if len(step.assets) == 0 {
			continue
		}
		args := append([]any{step.value, sessionID}, step.assets...)
		result, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET superseded_at = ?
			WHERE session_id = ? AND `+step.filter+` AND `+
			fmt.Sprintf(condition, placeholders(len(step.assets))), args...)
		if err != nil {
			return 0, fmt.Errorf("eventstore: supersede %s %s: %w", table, sessionID, err)
		}
		if affected, err := result.RowsAffected(); err == nil {
			changed += int(affected)
		}
	}
	return changed, nil
}

// OpportunityKey names one opportunity row inside a session: the shape class
// it was recorded under and the asset it is for.
type OpportunityKey struct {
	ShapeClass string
	AssetID    string
}

// SupersedeStaleOpportunities withdraws the opportunities of one session that
// the current rules no longer produce, and returns how many it withdrew.
//
// The supersede channel of §25.7 follows asset_invoked events, and an
// opportunity produced only by a path reference in the task text has no event
// behind it — so the strictened path rule withdrew the evidence and left those
// denominators standing. live is the complete set of (shape class, asset) the
// current parse produces for this session; anything else it holds is a row the
// rules no longer write. Restoring is not done here: recording a shape again
// clears superseded_at on the row it re-inserts.
func (s *Store) SupersedeStaleOpportunities(ctx context.Context, sessionID string, live []OpportunityKey) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("eventstore: session id is required")
	}
	produced := make(map[OpportunityKey]struct{}, len(live))
	for _, key := range live {
		produced[key] = struct{}{}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, shape_class, asset_id FROM opportunities
		WHERE session_id = ? AND superseded_at IS NULL`, sessionID)
	if err != nil {
		return 0, fmt.Errorf("eventstore: read opportunities %s: %w", sessionID, err)
	}
	var stale []int64
	for rows.Next() {
		var id int64
		var key OpportunityKey
		if err := rows.Scan(&id, &key.ShapeClass, &key.AssetID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("eventstore: scan opportunity: %w", err)
		}
		if _, ok := produced[key]; !ok {
			stale = append(stale, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("eventstore: iterate opportunities: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("eventstore: close opportunities: %w", err)
	}
	if len(stale) == 0 {
		return 0, nil
	}
	at := formatTime(time.Now().UTC())
	for _, id := range stale {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE opportunities SET superseded_at = ? WHERE id = ?`, at, id); err != nil {
			return 0, fmt.Errorf("eventstore: supersede opportunity %d: %w", id, err)
		}
	}
	return len(stale), nil
}

// TranscriptCountForSession is how many local transcript files this session
// was read out of. A Claude Code session is one main transcript plus one file
// per subagent, so "all of them" is not always one.
func (s *Store) TranscriptCountForSession(ctx context.Context, sessionID string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM native_files WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("eventstore: count transcripts %s: %w", sessionID, err)
	}
	return count, nil
}

// SupersededAssetEvidenceCount is how many asset_invoked events the current
// rules no longer produce. It is reported rather than hidden: the rows are
// still there, and the number says how much of the old reading was withdrawn.
func (s *Store) SupersededAssetEvidenceCount(ctx context.Context) (int, int, error) {
	var live, superseded int
	if err := s.db.QueryRowContext(ctx, `
		SELECT SUM(CASE WHEN superseded_at IS NULL THEN 1 ELSE 0 END),
		       SUM(CASE WHEN superseded_at IS NOT NULL THEN 1 ELSE 0 END)
		FROM events WHERE event_type = ?`, canonical.EventTypeAssetInvoked).
		Scan(&nullableCount{&live}, &nullableCount{&superseded}); err != nil {
		return 0, 0, fmt.Errorf("eventstore: count asset evidence: %w", err)
	}
	return live, superseded, nil
}

type nullableCount struct{ target *int }

func (n *nullableCount) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		*n.target = 0
	case int64:
		*n.target = int(typed)
	default:
		return fmt.Errorf("eventstore: unexpected count type %T", value)
	}
	return nil
}
