package api

import (
	"context"
	"testing"
	"time"

	"flatline/internal/eventstore"
)

func TestAssetFrictionLinksAppearOnTheAssetAndItsListRow(t *testing.T) {
	db := testAPIDB(t)
	ctx := context.Background()
	exec(t, db, `INSERT INTO assets (id, kind, scope, name, source_path, first_seen_at)
		VALUES ('hook:user:fixture', 'hook', 'user', 'fixture', '/synthetic/hooks/guard.sh', ?)`,
		time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano))
	var sessionID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM sessions LIMIT 1`).Scan(&sessionID); err != nil {
		t.Fatalf("session id: %v", err)
	}
	exec(t, db, `INSERT INTO friction_records
		(session_id, source_event_id, friction_kind, event_type, observation_level, tool_name, category, signature, payload_json, locator_json, occurred_at, created_at)
		VALUES (?, 'link-1', 'tool_error', 'transcript_tool_result', 'invoked', 'Bash', 'tool_error',
		        'tool_error|Bash|command blocked by pretooluse hook: guard', '{}', '{}', ?, ?)`,
		sessionID, periodStart.Format(time.RFC3339Nano), periodStart.Format(time.RFC3339Nano))
	exec(t, db, `INSERT INTO asset_friction_links (asset_id, friction_id, rule)
		SELECT 'hook:user:fixture', id, ? FROM friction_records WHERE source_event_id = 'link-1'`,
		eventstore.HookFrictionRule)

	handler := NewServerWithDB(db).Handler()
	var detail struct {
		Asset struct {
			FrictionLinkCount int                  `json:"friction_link_count"`
			FrictionLinks     *[]assetFrictionLink `json:"friction_links"`
		} `json:"asset"`
	}
	getJSON(t, handler, "/api/v1/assets/hook:user:fixture", &detail)
	if detail.Asset.FrictionLinks == nil {
		t.Fatal("asset detail omits friction_links: the detail endpoint always answers that question")
	}
	if detail.Asset.FrictionLinkCount != 1 || len(*detail.Asset.FrictionLinks) != 1 {
		t.Fatalf("asset detail friction links = %+v", detail.Asset)
	}
	link := (*detail.Asset.FrictionLinks)[0]
	if link.SessionID != sessionID || link.SampleLine == "" || link.Rule != eventstore.HookFrictionRule {
		t.Fatalf("friction link = %+v", link)
	}

	var list struct {
		Assets []struct {
			ID                string `json:"id"`
			FrictionLinkCount int    `json:"friction_link_count"`
		} `json:"assets"`
	}
	getJSON(t, handler, "/api/v1/assets", &list)
	found := false
	for _, item := range list.Assets {
		if item.ID != "hook:user:fixture" {
			continue
		}
		found = true
		if item.FrictionLinkCount != 1 {
			t.Fatalf("list friction_link_count = %d, want 1", item.FrictionLinkCount)
		}
	}
	if !found {
		t.Fatal("asset list does not contain the fixture hook")
	}
}
