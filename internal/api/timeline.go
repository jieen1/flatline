package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"flatline/internal/vital"
)

// timelineUnion is the one ordered sequence the timeline page reads: state
// transitions, environment changes and asset versions, newest first. The row
// id is selected so the order is total — three tables mint ids independently,
// so the kind is the last tiebreak.
const timelineUnion = `
	SELECT 'state_transition' AS kind, id AS row_id, asset_id, occurred_at, evidence_json, COALESCE(alignment_json, ''), COALESCE(to_state, '')
	FROM state_transitions
	UNION ALL
	SELECT 'environment_changed', id, COALESCE(asset_id, ''), occurred_at, payload_json, COALESCE(locator_json, ''), ''
	FROM events WHERE event_type = 'environment_changed'
	UNION ALL
	SELECT 'asset_version', id, asset_id, observed_at, content_hash, COALESCE(content_ref, ''), ''
	FROM asset_versions`

// handleTimeline pages the sequence rather than returning a window of it. The
// page used to ask for a larger limit to see older entries, which re-fetched
// everything it already had; offset/limit with a stable order lets it ask only
// for what it does not have, and pagination.total says how much is left.
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	limit, offset := queryLimit(r, 1000), queryOffset(r)
	window, err := rangeWindow(r.URL.Query(), "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	where, args := timelineWhere(window)
	ctx := r.Context()
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+timelineUnion+`)`+where, args...).Scan(&total); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err := s.db.QueryContext(ctx, `SELECT * FROM (`+timelineUnion+`)`+where+`
		ORDER BY occurred_at DESC, row_id DESC, kind
		LIMIT ? OFFSET ?`, append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var kind, assetID, occurred, evidence, alignment, state string
		var rowID int64
		if err := rows.Scan(&kind, &rowID, &assetID, &occurred, &evidence, &alignment, &state); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		item := map[string]any{"kind": kind, "asset_id": assetID, "occurred_at": occurred, "evidence": evidence, "alignment": alignment}
		if state != "" {
			item["state"] = state
		}
		items = append(items, item)
	}
	if !closeRows(w, rows) {
		return
	}
	clusters, err := s.timelineClusters(ctx)
	if err != nil {
		http.Error(w, "timeline cluster query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"timeline": items, "clusters": clusters,
		"range": window,
		"pagination": map[string]any{
			"offset": offset, "limit": limit, "total": total,
			"has_more": offset+len(items) < total,
		},
		"data_version": s.dataVersion(),
	})
}

// timelineWhere bounds the union by when an entry was recorded. The clusters
// below it are not bounded: a cluster is an alignment between an anchor and
// the transitions around it, and cutting the window through one would report
// an alignment narrower than the one that was actually observed.
func timelineWhere(window overviewRange) (string, []any) {
	clause, args := "", make([]any, 0, 2)
	if window.From != nil {
		clause = andWhere(clause, "occurred_at >= ?")
		args = append(args, *window.From)
	}
	if window.To != nil {
		clause = andWhere(clause, "occurred_at <= ?")
		args = append(args, *window.To)
	}
	return clause, args
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
