package eventstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// PairingVersion changes whenever the pass that re-reads native transcripts to
// build event_pairs changes. A native file stamped with a different version is
// read once more; a file stamped with this one is never re-read.
const PairingVersion = "pairs/1"

// unrecordedToolName is the explicit key for a tool the source never named. It
// is a bucket, never a substitute for zero.
const unrecordedToolName = "__unrecorded__"

// linkID is the id a harness uses to tie a tool result back to its call.
// Codex writes the call id into turn_id when the record carries no id of its
// own, so turn_id is the last resort rather than an alternative name.
//
// The match is made in Go, inside the pass that already decodes every tool
// payload for the command and file projections. Doing the same match in SQL
// would mean parsing the whole payload column a second time — hundreds of
// megabytes of recorded tool output — for the sake of three small fields.
func linkID(payload map[string]any) string {
	for _, key := range []string{"tool_use_id", "call_id", "turn_id"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// pairRow is one id-matched link about to be written.
type pairRow struct {
	resultEventID int64
	callEventID   int64
	toolName      string
}

// writeSessionIDPairs replaces the id-matched pairs of one session. Pairs
// recovered by re-reading the source transcript are left alone: they were read
// from the source itself, which is the stronger record, and INSERT OR IGNORE
// is what keeps them.
func writeSessionIDPairs(ctx context.Context, tx *sql.Tx, sessionID string, pairs []pairRow) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM event_pairs WHERE session_id = ? AND pair_source = 'id'`, sessionID); err != nil {
		return fmt.Errorf("eventstore: clear id pairs %s: %w", sessionID, err)
	}
	for _, pair := range pairs {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO event_pairs (session_id, result_event_id, call_event_id, tool_name, pair_source)
			VALUES (?, ?, ?, ?, 'id')`,
			sessionID, pair.resultEventID, pair.callEventID, nullableString(pair.toolName)); err != nil {
			return fmt.Errorf("eventstore: insert id pair %s/%d: %w", sessionID, pair.resultEventID, err)
		}
	}
	return nil
}

// SessionHasPairs reports whether a session has been paired at all. It is what
// separates "this session records no tool result" from "this session has never
// been projected".
func (s *Store) SessionHasPairs(ctx context.Context, sessionID string) (bool, error) {
	var present int
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM event_pairs WHERE session_id = ?)`, sessionID).Scan(&present); err != nil {
		return false, fmt.Errorf("eventstore: check session pairs %s: %w", sessionID, err)
	}
	return present != 0, nil
}

// PairCandidate is one native transcript that still has to be re-read: the
// session holds a tool result whose call the recorded ids do not identify.
type PairCandidate struct {
	Path      string
	SessionID string
	Source    string
}

// SessionsMissingPairs lists the transcripts worth re-reading. A file already
// stamped with the current pairing version is skipped even if some of its
// results stay unpaired: it has had its one read.
func (s *Store) SessionsMissingPairs(ctx context.Context) ([]PairCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.path, n.session_id, s.source
		FROM native_files n
		JOIN sessions s ON s.id = n.session_id
		WHERE (n.pairing_version IS NULL OR n.pairing_version <> ?)
		  AND EXISTS (
		      SELECT 1 FROM events e
		      WHERE e.session_id = n.session_id AND e.event_type = 'transcript_tool_result'
		        AND NOT EXISTS (
		            SELECT 1 FROM event_pairs p
		            WHERE p.session_id = e.session_id AND p.result_event_id = e.id))
		ORDER BY n.path`, PairingVersion)
	if err != nil {
		return nil, fmt.Errorf("eventstore: list transcripts missing pairs: %w", err)
	}
	defer rows.Close()
	out := make([]PairCandidate, 0)
	for rows.Next() {
		var item PairCandidate
		if err := rows.Scan(&item.Path, &item.SessionID, &item.Source); err != nil {
			return nil, fmt.Errorf("eventstore: scan transcript missing pairs: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ToolStatsGap counts the sessions that recorded tool calls but hold no row in
// the tool usage projection. It is what tells a daemon whose previous run was
// interrupted mid-pass that the projection still has to be rebuilt.
func (s *Store) ToolStatsGap(ctx context.Context) (int, error) {
	var gap int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM session_stats st
		WHERE st.tool_call_count > 0
		  AND NOT EXISTS (SELECT 1 FROM tool_call_stats t WHERE t.session_id = st.session_id)`).Scan(&gap); err != nil {
		return 0, fmt.Errorf("eventstore: count tool projection gap: %w", err)
	}
	return gap, nil
}

// ToolPairRef is one call/result link read straight out of a native transcript.
// The refs are the source positions both events already carry in their
// locator, which is how the link reaches the append-only events without
// rewriting any of them.
type ToolPairRef struct {
	ResultRef string
	CallRef   string
	ToolName  string
}

// RecordReparsePairs writes the links read out of one transcript. A ref that
// matches no stored event is dropped: the parser that produced the event and
// the one that produced the ref agreed on the source position, and anything
// else is a guess.
func (s *Store) RecordReparsePairs(ctx context.Context, sessionID string, pairs []ToolPairRef) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("eventstore: session id is required")
	}
	if len(pairs) == 0 {
		return 0, nil
	}
	callByRef, resultByRef, err := s.eventRefs(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("eventstore: begin pair write: %w", err)
	}
	written := 0
	for _, pair := range pairs {
		resultID, ok := resultByRef[pair.ResultRef]
		if !ok {
			continue
		}
		callID, ok := callByRef[pair.CallRef]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO event_pairs (session_id, result_event_id, call_event_id, tool_name, pair_source)
			VALUES (?, ?, ?, ?, 'reparse')
			ON CONFLICT (session_id, result_event_id) DO UPDATE SET
				call_event_id = excluded.call_event_id,
				tool_name     = excluded.tool_name,
				pair_source   = excluded.pair_source`,
			sessionID, resultID, callID, nullableString(pair.ToolName)); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("eventstore: record pair %s/%s: %w", sessionID, pair.ResultRef, err)
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("eventstore: commit pair write: %w", err)
	}
	return written, nil
}

// eventRefs indexes one session's tool events by the source position recorded
// in their locator, with the ":tool_call" / ":tool_result" suffix the adapter
// appends removed.
func (s *Store) eventRefs(ctx context.Context, sessionID string) (map[string]int64, map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_type, json_extract(locator_json, '$.raw_ref')
		FROM events
		WHERE session_id = ? AND event_type IN ('transcript_tool_call', 'transcript_tool_result')`, sessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("eventstore: read event refs %s: %w", sessionID, err)
	}
	defer rows.Close()
	calls := make(map[string]int64)
	results := make(map[string]int64)
	for rows.Next() {
		var id int64
		var eventType string
		var rawRef sql.NullString
		if err := rows.Scan(&id, &eventType, &rawRef); err != nil {
			return nil, nil, fmt.Errorf("eventstore: scan event ref: %w", err)
		}
		if !rawRef.Valid || rawRef.String == "" {
			continue
		}
		if eventType == "transcript_tool_call" {
			calls[strings.TrimSuffix(rawRef.String, ":tool_call")] = id
			continue
		}
		results[strings.TrimSuffix(rawRef.String, ":tool_result")] = id
	}
	return calls, results, rows.Err()
}

// StampPairingVersion records that this transcript has been re-read, so the
// next daemon start does not read it again.
func (s *Store) StampPairingVersion(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE native_files SET pairing_version = ? WHERE path = ?`, PairingVersion, path); err != nil {
		return fmt.Errorf("eventstore: stamp pairing version %s: %w", path, err)
	}
	return nil
}

// reparsedPairs are the links a re-read of the source transcript recovered,
// by result event id. They are the ones the projection cannot derive again, so
// they are the only rows it loads back.
func (s *Store) reparsedPairs(ctx context.Context, sessionID string) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT result_event_id, call_event_id FROM event_pairs
		WHERE session_id = ? AND pair_source = 'reparse'`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("eventstore: read reparsed pairs %s: %w", sessionID, err)
	}
	defer rows.Close()
	out := make(map[int64]int64)
	for rows.Next() {
		var resultID, callID int64
		if err := rows.Scan(&resultID, &callID); err != nil {
			return nil, fmt.Errorf("eventstore: scan reparsed pair: %w", err)
		}
		out[resultID] = callID
	}
	return out, rows.Err()
}
