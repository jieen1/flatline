package eventstore

import (
	"context"
	"database/sql"
	"fmt"
)

// A stored event names the session it belongs to and carries a source event id
// derived from that session plus its position in the source text. When a
// reader learns that a transcript is its own thread rather than part of
// another one — a Claude Code subagent writes the parent's sessionId into
// every record, but the file is the thread — those two columns are what was
// wrong about the stored row.
//
// Relocate corrects exactly those two columns. The row keeps its id, its
// payload, its locator, its timestamps and its ingestion time; nothing about
// what was observed changes, only which session it is filed under. That is why
// this is not a rewrite of history: it is the same record, filed correctly.
// Re-ingesting the transcript afterwards then matches the moved rows by their
// new ids and inserts nothing twice.

// RelocateReport counts what one correction moved.
type RelocateReport struct {
	Events   int
	Friction int
}

// RelocateEvents moves the events of one transcript from the session they were
// filed under to the session that produced them. newIDs maps the source
// position each event records in its locator to the source event id the same
// event now has. An event whose position is not in the map is left alone: it
// came from a different transcript of the same session.
func (s *Store) RelocateEvents(ctx context.Context, from, to string, newIDs map[string]string) (RelocateReport, error) {
	var report RelocateReport
	if from == "" || to == "" || from == to || len(newIDs) == 0 {
		return report, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(source_event_id, ''), COALESCE(json_extract(locator_json, '$.raw_ref'), '')
		FROM events WHERE session_id = ?`, from)
	if err != nil {
		return report, fmt.Errorf("eventstore: read events to relocate %s: %w", from, err)
	}
	type move struct {
		id    int64
		oldID string
		newID string
	}
	var moves []move
	for rows.Next() {
		var id int64
		var sourceEventID, rawRef string
		if err := rows.Scan(&id, &sourceEventID, &rawRef); err != nil {
			rows.Close()
			return report, fmt.Errorf("eventstore: scan event to relocate: %w", err)
		}
		if newID, ok := newIDs[rawRef]; ok && rawRef != "" {
			moves = append(moves, move{id: id, oldID: sourceEventID, newID: newID})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, fmt.Errorf("eventstore: iterate events to relocate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return report, fmt.Errorf("eventstore: close events to relocate: %w", err)
	}
	if len(moves) == 0 {
		return report, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("eventstore: begin relocation: %w", err)
	}
	rollback := func(err error) (RelocateReport, error) {
		_ = tx.Rollback()
		return RelocateReport{}, err
	}
	for _, item := range moves {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM friction_records WHERE session_id = ? AND source_event_id = ?`, from, item.oldID)
		if err != nil {
			return rollback(fmt.Errorf("eventstore: clear relocated friction %s: %w", item.oldID, err))
		}
		if removed, err := result.RowsAffected(); err == nil {
			report.Friction += int(removed)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE events SET session_id = ?, source_event_id = ? WHERE id = ?`,
			to, item.newID, item.id); err != nil {
			return rollback(fmt.Errorf("eventstore: relocate event %d: %w", item.id, err))
		}
		report.Events++
	}
	// The pair, command, file and tool projections are keyed on event ids that
	// have just changed session. They are fully recomputable, so both sessions
	// are cleared here and projected again by the caller.
	for _, table := range []string{"event_pairs", "session_commands", "session_files", "tool_call_stats"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE session_id IN (?, ?)`, from, to); err != nil {
			return rollback(fmt.Errorf("eventstore: clear %s for relocation: %w", table, err))
		}
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("eventstore: commit relocation: %w", err)
	}
	return report, nil
}

// MisfiledTranscript is one transcript whose events are filed under a session
// that is not the one the current reader says produced it.
type MisfiledTranscript struct {
	Path      string
	SessionID string
	Source    string
}

// ReattachNativeFile records that a transcript now belongs to another session.
func (s *Store) ReattachNativeFile(ctx context.Context, path, sessionID string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE native_files SET session_id = ? WHERE path = ?`, sessionID, path); err != nil {
		return fmt.Errorf("eventstore: reattach native file %s: %w", path, err)
	}
	return nil
}

// SubagentsMissingIdentity lists the Claude Code subagent sessions whose role
// and nickname are not filled in yet, with the parent that launched them.
func (s *Store) SubagentsMissingIdentity(ctx context.Context) ([]MisfiledTranscript, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.source_session_id, COALESCE(s.parent_session_id, '')
		FROM sessions s
		WHERE s.source = 'claude_code' AND s.thread_kind = 'subagent'
		  AND s.agent_role IS NULL AND s.parent_session_id IS NOT NULL
		ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("eventstore: list subagents without identity: %w", err)
	}
	defer rows.Close()
	out := make([]MisfiledTranscript, 0)
	for rows.Next() {
		var item MisfiledTranscript
		if err := rows.Scan(&item.SessionID, &item.Path, &item.Source); err != nil {
			return nil, fmt.Errorf("eventstore: scan subagent without identity: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// LaunchInput returns the tool input of the parent's Agent call that launched
// this agent. Claude Code writes the agent id into that call's own result, so
// the launch and the thread are linked by the harness itself, not by a guess.
func (s *Store) LaunchInput(ctx context.Context, parentSessionID, agentID string) (string, bool, error) {
	var input sql.NullString
	err := s.db.QueryRowContext(ctx, `
		WITH launch AS (
			SELECT json_extract(payload_json, '$.tool_use_id') AS call_id
			FROM events
			WHERE session_id = ?1 AND event_type = 'transcript_tool_result'
			  AND payload_json LIKE '%agentId: ' || ?2 || '%'
			LIMIT 1
		)
		SELECT json_extract(e.payload_json, '$.tool_input')
		FROM events e JOIN launch ON json_extract(e.payload_json, '$.tool_use_id') = launch.call_id
		WHERE e.session_id = ?1 AND e.event_type = 'transcript_tool_call'
		LIMIT 1`, parentSessionID, agentID).Scan(&input)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("eventstore: read launch input %s: %w", agentID, err)
	}
	return input.String, input.Valid && input.String != "", nil
}

// SetSubagentIdentity records what the parent's launch call said this agent
// was for. Both fields stay NULL when the call did not name them.
func (s *Store) SetSubagentIdentity(ctx context.Context, sessionID, role, nickname string) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET agent_role = COALESCE(agent_role, ?), agent_nickname = COALESCE(agent_nickname, ?)
		WHERE id = ?`, nullableString(role), nullableString(nickname), sessionID); err != nil {
		return fmt.Errorf("eventstore: set subagent identity %s: %w", sessionID, err)
	}
	return s.upsertSessionSearch(ctx, sessionID)
}
