package api

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// The friction records read as evidence for an asset are the drill-down behind
// its participation: the row on the wall says the hook took part, and this says
// which recorded block that reading came from.

const maxAssetFrictionLinks = 200

// assetFrictionLinks lists the friction records linked to one asset, most
// recent first.
func (s *Server) assetFrictionLinks(ctx context.Context, assetID string, limit int) ([]assetFrictionLink, error) {
	out := make([]assetFrictionLink, 0)
	has, err := s.hasTable(ctx, "asset_friction_links")
	if err != nil || !has {
		return out, err
	}
	if limit <= 0 || limit > maxAssetFrictionLinks {
		limit = maxAssetFrictionLinks
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.friction_id, l.rule, COALESCE(fr.signature, ''), fr.session_id,
		       s.title, ev.id, COALESCE(fr.occurred_at, '')
		FROM asset_friction_links l
		JOIN friction_records fr ON fr.id = l.friction_id
		JOIN sessions s ON s.id = fr.session_id
		LEFT JOIN events ev ON ev.session_id = fr.session_id AND ev.source_event_id = fr.source_event_id
		WHERE l.asset_id = ?
		ORDER BY fr.occurred_at DESC, l.friction_id DESC
		LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, fmt.Errorf("api: asset friction links: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item assetFrictionLink
		var title sql.NullString
		var eventID sql.NullInt64
		var occurred string
		if err := rows.Scan(&item.FrictionID, &item.Rule, &item.Signature, &item.SessionID,
			&title, &eventID, &occurred); err != nil {
			return nil, fmt.Errorf("api: scan asset friction link: %w", err)
		}
		if title.Valid && title.String != "" {
			value := title.String
			item.SessionTitle = &value
		}
		if eventID.Valid {
			value := eventID.Int64
			item.EventID = &value
		}
		item.SampleLine = frictionSignatureLine(item.Signature)
		if occurred != "" {
			if at, err := time.Parse(time.RFC3339Nano, occurred); err == nil {
				utc := at.UTC()
				item.OccurredAt = &utc
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// assetFrictionLinkCounts is how many friction records each asset is linked to,
// read once for a whole list rather than once per row.
func (s *Server) assetFrictionLinkCounts(ctx context.Context) (map[string]int, error) {
	out := make(map[string]int)
	has, err := s.hasTable(ctx, "asset_friction_links")
	if err != nil || !has {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT asset_id, COUNT(*) FROM asset_friction_links GROUP BY asset_id`)
	if err != nil {
		return nil, fmt.Errorf("api: asset friction link counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var assetID string
		var count int
		if err := rows.Scan(&assetID, &count); err != nil {
			return nil, fmt.Errorf("api: scan asset friction link count: %w", err)
		}
		out[assetID] = count
	}
	return out, rows.Err()
}
