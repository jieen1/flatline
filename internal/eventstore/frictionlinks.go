package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"flatline/internal/canonical"
	"flatline/internal/friction"
)

// HookFrictionRule is the sentence the link is written under, stored on every
// row so the drill-down can state it without the reader looking anything up.
const HookFrictionRule = "hook 拦截记录出现在会话中 = 该 hook 参与了该会话"

// HookFrictionLink is one friction record read as evidence that one hook asset
// took part in the session.
type HookFrictionLink struct {
	AssetID    string
	FrictionID int64
	SessionID  string
	Rule       string
	OccurredAt *time.Time
	Locator    canonical.Locator
	// Reference is the text in the recorded message that named the hook. It is
	// what makes the match checkable by hand.
	Reference string
}

// hookAsset is a registered hook the links can point at.
type hookAsset struct {
	id   string
	name string
	path string
}

// LinkHookFriction rewrites one session's hook-friction links from its stored
// friction records and returns them.
//
// The rule is one sentence: a hook block recorded in a session means that hook
// took part in that session. It is applied only to records whose signature the
// hint dictionary reads as a user hook, and only when the recorded message
// names a hook the asset registry knows — by the full path of its file, or by
// a file name exactly one registered hook carries. A message that names no
// hook links to nothing; nothing is guessed.
func (s *Store) LinkHookFriction(ctx context.Context, sessionID string) ([]HookFrictionLink, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("eventstore: session id is required")
	}
	candidates, err := s.hookFrictionCandidates(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM asset_friction_links
		WHERE friction_id IN (SELECT id FROM friction_records WHERE session_id = ?)`, sessionID); err != nil {
		return nil, fmt.Errorf("eventstore: clear hook friction links %s: %w", sessionID, err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	byPath, byName, err := s.hookAssetIndex(ctx)
	if err != nil {
		return nil, err
	}
	links := make([]HookFrictionLink, 0, len(candidates))
	for _, candidate := range candidates {
		assetID, reference, matched := matchHookAsset(candidate.text, byPath, byName)
		if !matched {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO asset_friction_links (asset_id, friction_id, rule) VALUES (?, ?, ?)
			ON CONFLICT (asset_id, friction_id) DO NOTHING`,
			assetID, candidate.id, HookFrictionRule); err != nil {
			return nil, fmt.Errorf("eventstore: link hook friction %d: %w", candidate.id, err)
		}
		links = append(links, HookFrictionLink{AssetID: assetID, FrictionID: candidate.id,
			SessionID: sessionID, Rule: HookFrictionRule, OccurredAt: candidate.occurredAt,
			Locator: candidate.locator, Reference: reference})
	}
	return links, nil
}

// hookFrictionCandidate is one friction record whose mechanism is a user hook,
// with the recorded text the hook could be named in.
type hookFrictionCandidate struct {
	id         int64
	text       string
	occurredAt *time.Time
	locator    canonical.Locator
}

func (s *Store) hookFrictionCandidates(ctx context.Context, sessionID string) ([]hookFrictionCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(signature, ''), COALESCE(payload_json, ''), COALESCE(locator_json, ''), occurred_at
		FROM friction_records
		WHERE session_id = ? AND signature IS NOT NULL AND signature <> ''
		ORDER BY id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("eventstore: read hook friction %s: %w", sessionID, err)
	}
	defer rows.Close()
	out := make([]hookFrictionCandidate, 0)
	for rows.Next() {
		var id int64
		var signature, payload, locator string
		var occurred sql.NullString
		if err := rows.Scan(&id, &signature, &payload, &locator, &occurred); err != nil {
			return nil, fmt.Errorf("eventstore: scan hook friction: %w", err)
		}
		hint := friction.LookupHint(signature)
		if hint == nil || hint.Kind != friction.HintUserHook {
			continue
		}
		candidate := hookFrictionCandidate{id: id, text: hookFrictionText(payload)}
		if locator != "" {
			_ = json.Unmarshal([]byte(locator), &candidate.locator)
		}
		if occurred.Valid && occurred.String != "" {
			if at, err := time.Parse(time.RFC3339Nano, occurred.String); err == nil {
				utc := at.UTC()
				candidate.occurredAt = &utc
			}
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}

// hookFrictionText is the recorded text a hook could be named in: what the
// harness printed, plus what the call asked for.
func hookFrictionText(payload string) string {
	if payload == "" {
		return ""
	}
	var decoded map[string]any
	if json.Unmarshal([]byte(payload), &decoded) != nil {
		return payload
	}
	var builder strings.Builder
	for _, key := range []string{"tool_output", "text", "tool_input"} {
		if value, ok := decoded[key].(string); ok && value != "" {
			builder.WriteString(value)
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

// hookAssetIndex is the registered hooks keyed by full path and by name. A
// name shared by two hooks is left out of the name index: a mention that could
// mean either of them means neither.
func (s *Store) hookAssetIndex(ctx context.Context) (map[string]string, map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, COALESCE(source_path, '') FROM assets
		WHERE kind = 'hook' AND archived_at IS NULL`)
	if err != nil {
		return nil, nil, fmt.Errorf("eventstore: list hook assets: %w", err)
	}
	defer rows.Close()
	byPath := make(map[string]string)
	nameCounts := make(map[string]int)
	nameIDs := make(map[string]string)
	for rows.Next() {
		var item hookAsset
		if err := rows.Scan(&item.id, &item.name, &item.path); err != nil {
			return nil, nil, fmt.Errorf("eventstore: scan hook asset: %w", err)
		}
		if item.path != "" {
			byPath[filepath.Clean(item.path)] = item.id
		}
		name := strings.ToLower(hookAssetName(item))
		if name == "" {
			continue
		}
		nameCounts[name]++
		nameIDs[name] = item.id
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("eventstore: iterate hook assets: %w", err)
	}
	byName := make(map[string]string, len(nameIDs))
	for name, id := range nameIDs {
		if nameCounts[name] == 1 {
			byName[name] = id
		}
	}
	return byPath, byName, nil
}

// hookAssetName is the file name a hook is known by, taken from its source
// path when it has one and from the last segment of its registry name when it
// does not.
func hookAssetName(item hookAsset) string {
	if item.path != "" {
		return friction.HookName(item.path)
	}
	if cut := strings.LastIndex(item.name, ":"); cut >= 0 {
		return item.name[cut+1:]
	}
	return item.name
}

// matchHookAsset finds the one hook a recorded message names. A full path wins
// over a bare name, and a bare name counts only when it is unique.
func matchHookAsset(text string, byPath, byName map[string]string) (string, string, bool) {
	references := friction.HookReferences(text)
	for _, reference := range references {
		if id, ok := byPath[reference]; ok {
			return id, reference, true
		}
	}
	for _, reference := range references {
		if id, ok := byName[strings.ToLower(friction.HookName(reference))]; ok {
			return id, reference, true
		}
	}
	return "", "", false
}
