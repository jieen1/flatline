package eventstore

import (
	"context"
	"fmt"
	"strings"

	"flatline/internal/canonical"
)

// claudeWorktreeMarker is the path segment Claude Code's own worktree command
// puts between a repository and the throwaway checkout it makes under it.
const claudeWorktreeMarker = "/.claude/worktrees/"

// ProjectKeyOf is the project a working directory belongs to, plus the name of
// the worktree it ran in when it ran in one.
//
// A session started from <repo>/.claude/worktrees/<name> is work on <repo>: the
// directory is a checkout the harness made for one task and threw away, and
// counting it as its own project put ten of them beside the repository they
// came from. That one shape is written by the harness itself, so folding it is
// a reading of the path, not a guess.
//
// Nothing else is folded. A directory that only looks like a worktree by name
// — qwen-sm120-runtime-wt-deps, qsr-w-b1 — is left alone: guessing at a naming
// convention would merge two real projects into one.
func ProjectKeyOf(cwd string) (string, string) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", ""
	}
	index := strings.Index(cwd, claudeWorktreeMarker)
	if index < 0 {
		return cwd, ""
	}
	root := cwd[:index]
	name := cwd[index+len(claudeWorktreeMarker):]
	if cut := strings.IndexByte(name, '/'); cut >= 0 {
		name = name[:cut]
	}
	if root == "" || name == "" {
		return cwd, ""
	}
	return root, name
}

// sessionStatsUpsert recomputes the session_stats projection. Parameter 1 is
// the session id to restrict to, or NULL to recompute every session.
// notInjected keeps the blocks a harness writes under the user role out of the
// message counts. The reader leaves them out of new transcripts; events are
// append-only, so the ones an older parser stored are excluded here instead.
var notInjected = canonical.NotInjectedSQL(`json_extract(payload_json, '$.text')`)

var sessionStatsUpsert = `
INSERT INTO session_stats (
	session_id, event_count, transcript_count, message_count, user_message_count,
	tool_call_count, tool_result_count, friction_count, tool_error_count,
	nonzero_exit_count, expected_exit_count, asset_count, first_event_at, last_event_at,
	duration_ms, computed_at)
SELECT s.id,
       COALESCE(e.event_count, 0),
       COALESCE(e.transcript_count, 0),
       COALESCE(e.message_count, 0),
       COALESCE(e.user_message_count, 0),
       COALESCE(e.tool_call_count, 0),
       COALESCE(e.tool_result_count, 0),
       COALESCE(f.friction_count, 0) + COALESCE(e.violation_count, 0),
       COALESCE(f.tool_error_count, 0),
       COALESCE(f.nonzero_exit_count, 0),
       COALESCE(f.expected_exit_count, 0),
       COALESCE(e.asset_count, 0),
       e.first_event_at,
       e.last_event_at,
       CASE WHEN s.started_at IS NULL OR s.ended_at IS NULL THEN NULL
            ELSE CAST(ROUND((julianday(s.ended_at) - julianday(s.started_at)) * 86400000.0) AS INTEGER) END,
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM sessions s
LEFT JOIN (
	SELECT session_id,
	       COUNT(*) AS event_count,
	       SUM(CASE WHEN event_type IN ('transcript_message', 'transcript_tool_call', 'transcript_tool_result') THEN 1 ELSE 0 END) AS transcript_count,
	       -- An aborted-turn record is a transcript record but not a message;
	       -- counting it would inflate what the UI calls "消息数".
	       SUM(CASE WHEN event_type = 'transcript_message'
	                 AND json_extract(payload_json, '$.abort_reason') IS NULL
	                 AND ` + notInjected + ` THEN 1 ELSE 0 END) AS message_count,
	       SUM(CASE WHEN event_type = 'transcript_message' AND json_extract(payload_json, '$.role') = 'user'
	                 AND ` + notInjected + ` THEN 1 ELSE 0 END) AS user_message_count,
	       SUM(CASE WHEN event_type = 'transcript_tool_call' THEN 1 ELSE 0 END) AS tool_call_count,
	       SUM(CASE WHEN event_type = 'transcript_tool_result' THEN 1 ELSE 0 END) AS tool_result_count,
	       SUM(CASE WHEN event_type = 'asset_violation' THEN 1 ELSE 0 END) AS violation_count,
	       COUNT(DISTINCT asset_id) AS asset_count,
	       MIN(NULLIF(occurred_at, '')) AS first_event_at,
	       MAX(NULLIF(occurred_at, '')) AS last_event_at
	FROM events WHERE ?1 IS NULL OR session_id = ?1 GROUP BY session_id
) e ON e.session_id = s.id
LEFT JOIN (
	-- A record the classifier reads as an expected nonzero exit (rg exiting 1
	-- means "nothing matched") is still stored, because it happened, but it is
	-- not friction and is counted on its own line instead.
	SELECT session_id,
	       COUNT(DISTINCT CASE WHEN COALESCE(category, '') <> 'expected_exit' THEN source_event_id END) AS friction_count,
	       SUM(CASE WHEN is_error = 1 AND COALESCE(category, '') <> 'expected_exit' THEN 1 ELSE 0 END) AS tool_error_count,
	       SUM(CASE WHEN exit_code IS NOT NULL AND exit_code <> 0
	                 AND COALESCE(category, '') <> 'expected_exit' THEN 1 ELSE 0 END) AS nonzero_exit_count,
	       COUNT(DISTINCT CASE WHEN category = 'expected_exit' THEN source_event_id END) AS expected_exit_count
	FROM friction_records WHERE ?1 IS NULL OR session_id = ?1 GROUP BY session_id
) f ON f.session_id = s.id
WHERE ?1 IS NULL OR s.id = ?1
ON CONFLICT (session_id) DO UPDATE SET
	event_count = excluded.event_count,
	transcript_count = excluded.transcript_count,
	message_count = excluded.message_count,
	user_message_count = excluded.user_message_count,
	tool_call_count = excluded.tool_call_count,
	tool_result_count = excluded.tool_result_count,
	friction_count = excluded.friction_count,
	tool_error_count = excluded.tool_error_count,
	nonzero_exit_count = excluded.nonzero_exit_count,
	expected_exit_count = excluded.expected_exit_count,
	asset_count = excluded.asset_count,
	first_event_at = excluded.first_event_at,
	last_event_at = excluded.last_event_at,
	duration_ms = excluded.duration_ms,
	computed_at = excluded.computed_at`

// RecomputeSessionStats refreshes the projection row for one session.
func (s *Store) RecomputeSessionStats(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("eventstore: session id is required")
	}
	if _, err := s.db.ExecContext(ctx, sessionStatsUpsert, sessionID); err != nil {
		return fmt.Errorf("eventstore: recompute session stats %s: %w", sessionID, err)
	}
	return nil
}

// RecomputeAllSessionStats rebuilds the whole projection and reports how many
// rows it wrote. It is the ADR-10 "recompute everything" entry point.
func (s *Store) RecomputeAllSessionStats(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx, sessionStatsUpsert, nil)
	if err != nil {
		return 0, fmt.Errorf("eventstore: recompute all session stats: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("eventstore: recompute all session stats rows: %w", err)
	}
	return int(rows), nil
}

// RecomputeMissingSessionStats fills in rows for sessions that have none, so a
// database migrated before the projection existed becomes complete on startup
// without paying for a full rebuild.
func (s *Store) RecomputeMissingSessionStats(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id FROM sessions s
		LEFT JOIN session_stats st ON st.session_id = s.id
		WHERE st.session_id IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("eventstore: list sessions without stats: %w", err)
	}
	var pending []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("eventstore: scan session without stats: %w", err)
		}
		pending = append(pending, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("eventstore: iterate sessions without stats: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("eventstore: close sessions without stats: %w", err)
	}
	for _, id := range pending {
		if err := s.RecomputeSessionStats(ctx, id); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}

// ReplaceRuleTags rewrites the daemon-derived tags for one session. User tags
// are never touched: they are the user's own record, not a derived value.
func (s *Store) ReplaceRuleTags(ctx context.Context, sessionID string, tags []string) error {
	if sessionID == "" {
		return fmt.Errorf("eventstore: session id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("eventstore: begin session tags: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_tags WHERE session_id = ? AND kind <> 'user'`, sessionID); err != nil {
		tx.Rollback()
		return fmt.Errorf("eventstore: clear rule tags %s: %w", sessionID, err)
	}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		kind := "task"
		if strings.HasPrefix(tag, "workspace-") {
			kind = "workspace"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_tags (session_id, tag, kind) VALUES (?, ?, ?)
			ON CONFLICT (session_id, tag, kind) DO NOTHING`, sessionID, tag, kind); err != nil {
			tx.Rollback()
			return fmt.Errorf("eventstore: insert rule tag %s/%s: %w", sessionID, tag, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("eventstore: commit session tags: %w", err)
	}
	return nil
}

func (s *Store) upsertSessionSearch(ctx context.Context, sessionID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions_fts WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("eventstore: clear session search row %s: %w", sessionID, err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions_fts (session_id, title, task_text, cwd, model, source_session_id)
		SELECT id, COALESCE(title, ''), COALESCE(task_text, ''), COALESCE(cwd, ''),
		       COALESCE(model, ''), source_session_id
		FROM sessions WHERE id = ?`, sessionID); err != nil {
		return fmt.Errorf("eventstore: write session search row %s: %w", sessionID, err)
	}
	return nil
}
