// Package eventstore owns the append-only canonical event write path.
package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
	"flatline/internal/storage"
)

// Store exposes inserts and read queries only. There are intentionally no
// update or delete methods for canonical events.
type Store struct{ db *storage.DB }

func New(db *storage.DB) *Store { return &Store{db: db} }

const FrictionKindToolError = "tool_error"

// FrictionRecord is a daemon-owned projection of an explicit tool failure.
// It is intentionally separate from canonical.Event: events are append-only,
// while this projection can be populated when a newer parser replays an old
// source file without rewriting the original event row.
type FrictionRecord struct {
	ID               int64
	SessionID        string
	SourceEventID    string
	FrictionKind     string
	EventType        string
	ObservationLevel canonical.ObservationLevel
	IsError          *bool
	ExitCode         *int
	Payload          map[string]any
	Locator          canonical.Locator
	OccurredAt       *time.Time
}

func (s *Store) IngestSession(ctx context.Context, source adapters.Source, meta adapters.SessionMeta) (string, error) {
	if !source.Valid() {
		return "", fmt.Errorf("eventstore: invalid source %q", source)
	}
	if meta.SourceSessionID == "" {
		return "", fmt.Errorf("eventstore: source session id is required")
	}
	id := string(source) + ":" + meta.SourceSessionID
	projectKey, worktree := ProjectKeyOf(meta.CWD)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, source, source_session_id, started_at, ended_at, harness_version, model, cwd, title, task_text,
			parent_session_id, thread_kind, agent_role, agent_nickname, originator, project_key, worktree)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, source_session_id) DO UPDATE SET
			project_key       = COALESCE(excluded.project_key, sessions.project_key),
			worktree          = COALESCE(excluded.worktree, sessions.worktree),
			-- The span of a session only ever grows: a transcript that was read
			-- while it was still being written has more records in it the next
			-- time it is read. Keeping the first reading froze ended_at at the
			-- moment of the first import while every measurement derived from a
			-- later read kept growing — one local session reported 50 minutes of
			-- active time inside a 3m40s session.
			started_at        = CASE
				WHEN excluded.started_at IS NULL THEN sessions.started_at
				WHEN sessions.started_at IS NULL THEN excluded.started_at
				WHEN julianday(excluded.started_at) < julianday(sessions.started_at) THEN excluded.started_at
				ELSE sessions.started_at END,
			ended_at          = CASE
				WHEN excluded.ended_at IS NULL THEN sessions.ended_at
				WHEN sessions.ended_at IS NULL THEN excluded.ended_at
				WHEN julianday(excluded.ended_at) > julianday(sessions.ended_at) THEN excluded.ended_at
				ELSE sessions.ended_at END,
			harness_version   = COALESCE(sessions.harness_version, excluded.harness_version),
			model             = COALESCE(sessions.model, excluded.model),
			cwd               = COALESCE(sessions.cwd, excluded.cwd),
			-- The title and the task excerpt are read from the source text, and
			-- a newer parser reading the same transcript is the better reading
			-- of it: a Claude Code subagent's name comes from the launch record
			-- beside the file, which an older parser did not open. An empty
			-- value never wipes a recorded one.
			title             = COALESCE(NULLIF(excluded.title, ''), sessions.title),
			task_text         = COALESCE(NULLIF(excluded.task_text, ''), sessions.task_text),
			parent_session_id = COALESCE(excluded.parent_session_id, sessions.parent_session_id),
			thread_kind       = COALESCE(excluded.thread_kind, sessions.thread_kind),
			agent_role        = COALESCE(excluded.agent_role, sessions.agent_role),
			agent_nickname    = COALESCE(excluded.agent_nickname, sessions.agent_nickname),
			originator        = COALESCE(excluded.originator, sessions.originator)`,
		id, source, meta.SourceSessionID,
		nullableTime(meta.StartedAt), nullableTime(meta.EndedAt),
		nullableString(meta.HarnessVersion), nullableString(meta.Model), nullableString(meta.CWD),
		nullableString(meta.Title), nullableString(meta.TaskText),
		nullableString(meta.ParentSessionID), nullableString(meta.ThreadKind),
		nullableString(meta.AgentRole), nullableString(meta.AgentNickname), nullableString(meta.Originator),
		nullableString(projectKey), nullableString(worktree))
	if err != nil {
		return "", fmt.Errorf("eventstore: ingest session %s: %w", id, err)
	}
	if err := s.upsertSessionSearch(ctx, id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) IngestEvents(ctx context.Context, sessionID string, events []canonical.Event) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("eventstore: session id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("eventstore: begin event transaction: %w", err)
	}
	rollback := func(err error) (int, error) {
		_ = tx.Rollback()
		return 0, err
	}
	inserted := 0
	for _, event := range events {
		if event.SessionID != sessionID {
			return rollback(fmt.Errorf("eventstore: event session %q does not match %q", event.SessionID, sessionID))
		}
		if err := event.Validate(); err != nil {
			return rollback(fmt.Errorf("eventstore: validate event %q: %w", event.SourceEventID, err))
		}
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return rollback(fmt.Errorf("eventstore: marshal payload %q: %w", event.SourceEventID, err))
		}
		locator, err := json.Marshal(event.Locator)
		if err != nil {
			return rollback(fmt.Errorf("eventstore: marshal locator %q: %w", event.SourceEventID, err))
		}
		var signal any
		if event.ParticipationSignal != nil {
			signal = string(*event.ParticipationSignal)
		}
		var assetVersion any
		if event.AssetVersionID != nil {
			assetVersion = *event.AssetVersionID
		}
		var occurred any
		if event.OccurredAt != nil {
			occurred = formatTime(*event.OccurredAt)
		}
		var eventID int64
		err = tx.QueryRowContext(ctx, `
			INSERT INTO events
			(session_id, event_type, asset_id, asset_version_id, source_event_id,
			 participation_signal, observation_level, payload_json, locator_json,
			 occurred_at, adapter_version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING
			RETURNING id`,
			sessionID, event.EventType, nullableString(event.AssetID), assetVersion,
			event.SourceEventID, signal, string(event.ObservationLevel), string(payload),
			string(locator), occurred, nullableString(event.AdapterVersion)).Scan(&eventID)
		if err == sql.ErrNoRows {
			// The row already exists. A parser-versioned re-read (the reparse
			// pass) may carry a refreshed payload — per-message usage, a wider
			// text bound — so the derived columns are updated in place. The
			// row's identity, its id and everything that references it stay;
			// this is a re-derivation under a new parser version, not a
			// rewrite of history, and it is not counted as an insert.
			if _, err := tx.ExecContext(ctx, `
				UPDATE events SET payload_json = ?, occurred_at = ?, adapter_version = ?
				WHERE session_id = ? AND source_event_id = ?`,
				string(payload), occurred, nullableString(event.AdapterVersion),
				sessionID, event.SourceEventID); err != nil {
				return rollback(fmt.Errorf("eventstore: refresh event %q: %w", event.SourceEventID, err))
			}
			if text := transcriptText(event); text != "" {
				if err := tx.QueryRowContext(ctx, `
					SELECT id FROM events WHERE session_id = ? AND source_event_id = ?`,
					sessionID, event.SourceEventID).Scan(&eventID); err != nil {
					return rollback(fmt.Errorf("eventstore: reload event %q: %w", event.SourceEventID, err))
				}
				if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO events_fts (rowid, text) VALUES (?, ?)`, eventID, text); err != nil {
					return rollback(fmt.Errorf("eventstore: index event text %q: %w", event.SourceEventID, err))
				}
			}
			continue
		}
		if err != nil {
			return rollback(fmt.Errorf("eventstore: insert event %q: %w", event.SourceEventID, err))
		}
		inserted++
		if text := transcriptText(event); text != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO events_fts (rowid, text) VALUES (?, ?)`, eventID, text); err != nil {
				return rollback(fmt.Errorf("eventstore: index event text %q: %w", event.SourceEventID, err))
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("eventstore: commit events: %w", err)
	}
	return inserted, nil
}

// FrictionRecordsForSession returns the bounded explicit-failure projection.
func (s *Store) FrictionRecordsForSession(ctx context.Context, sessionID string, limit int) ([]FrictionRecord, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("eventstore: session id is required")
	}
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, source_event_id, friction_kind, event_type,
		       observation_level, is_error, exit_code, payload_json, locator_json, occurred_at
		FROM friction_records
		WHERE session_id = ?
		ORDER BY occurred_at IS NULL, occurred_at, id
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("eventstore: friction records for session: %w", err)
	}
	defer rows.Close()
	out := make([]FrictionRecord, 0)
	for rows.Next() {
		var record FrictionRecord
		var observation, payload, locator, occurred sql.NullString
		var isError sql.NullInt64
		var exitCode sql.NullInt64
		if err := rows.Scan(&record.ID, &record.SessionID, &record.SourceEventID, &record.FrictionKind, &record.EventType, &observation, &isError, &exitCode, &payload, &locator, &occurred); err != nil {
			return nil, fmt.Errorf("eventstore: scan friction record: %w", err)
		}
		record.ObservationLevel = canonical.ObservationLevel(observation.String)
		if isError.Valid {
			value := isError.Int64 != 0
			record.IsError = &value
		}
		if exitCode.Valid {
			value := int(exitCode.Int64)
			record.ExitCode = &value
		}
		if payload.Valid && payload.String != "" {
			if err := json.Unmarshal([]byte(payload.String), &record.Payload); err != nil {
				return nil, fmt.Errorf("eventstore: decode friction payload: %w", err)
			}
		}
		if locator.Valid && locator.String != "" {
			if err := json.Unmarshal([]byte(locator.String), &record.Locator); err != nil {
				return nil, fmt.Errorf("eventstore: decode friction locator: %w", err)
			}
		}
		if occurred.Valid && occurred.String != "" {
			value, err := time.Parse(time.RFC3339Nano, occurred.String)
			if err != nil {
				return nil, fmt.Errorf("eventstore: decode friction occurred_at: %w", err)
			}
			record.OccurredAt = &value
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventstore: iterate friction records: %w", err)
	}
	return out, nil
}

// transcriptText returns the body text that belongs in the session-body search
// index. Only recorded transcript messages are indexed; tool payloads are not.
func transcriptText(event canonical.Event) string {
	if event.EventType != canonical.EventTypeTranscriptMessage {
		return ""
	}
	text, _ := event.Payload["text"].(string)
	return strings.TrimSpace(text)
}

func (s *Store) EventByLocator(ctx context.Context, locator canonical.Locator) (*canonical.Event, error) {
	if !locator.Valid() {
		return nil, fmt.Errorf("eventstore: invalid locator")
	}
	encoded, err := json.Marshal(locator)
	if err != nil {
		return nil, fmt.Errorf("eventstore: marshal query locator: %w", err)
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, event_type, asset_id, asset_version_id, source_event_id,
		       participation_signal, observation_level, payload_json, locator_json,
		       occurred_at, adapter_version
		FROM events WHERE locator_json = ? ORDER BY id LIMIT 1`, string(encoded))
	event, err := scanEvent(row)
	if err != nil {
		return nil, fmt.Errorf("eventstore: event by locator: %w", err)
	}
	return event, nil
}

func (s *Store) EventsForSession(ctx context.Context, sessionID string) ([]canonical.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, event_type, asset_id, asset_version_id, source_event_id,
		       participation_signal, observation_level, payload_json, locator_json,
		       occurred_at, adapter_version
		FROM events
		WHERE session_id = ?
		ORDER BY occurred_at IS NULL, occurred_at, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("eventstore: events for session: %w", err)
	}
	defer rows.Close()
	var out []canonical.Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("eventstore: scan event: %w", err)
		}
		out = append(out, *event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventstore: iterate events: %w", err)
	}
	return out, nil
}

type scanner interface{ Scan(...any) error }

func scanEvent(row scanner) (*canonical.Event, error) {
	var (
		id                                                                         int64
		event                                                                      canonical.Event
		assetID, sourceEventID, signal, payload, locator, occurred, adapterVersion sql.NullString
		assetVersionID                                                             sql.NullInt64
	)
	if err := row.Scan(&id, &event.SessionID, &event.EventType, &assetID, &assetVersionID, &sourceEventID,
		&signal, &event.ObservationLevel, &payload, &locator, &occurred, &adapterVersion); err != nil {
		return nil, err
	}
	_ = id
	if assetID.Valid {
		event.AssetID = assetID.String
	}
	if assetVersionID.Valid {
		event.AssetVersionID = &assetVersionID.Int64
	}
	if sourceEventID.Valid {
		event.SourceEventID = sourceEventID.String
	}
	if signal.Valid {
		value := canonical.ParticipationSignal(signal.String)
		event.ParticipationSignal = &value
	}
	if payload.Valid && payload.String != "" {
		if err := json.Unmarshal([]byte(payload.String), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode payload: %w", err)
		}
	}
	if locator.Valid {
		if err := json.Unmarshal([]byte(locator.String), &event.Locator); err != nil {
			return nil, fmt.Errorf("decode locator: %w", err)
		}
	}
	if occurred.Valid && occurred.String != "" {
		value, err := time.Parse(time.RFC3339Nano, occurred.String)
		if err != nil {
			return nil, fmt.Errorf("decode occurred_at: %w", err)
		}
		event.OccurredAt = &value
	}
	if adapterVersion.Valid {
		event.AdapterVersion = adapterVersion.String
	}
	return &event, nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

// The data version is the counter every cacheable API response is keyed on. It
// lives in meta rather than in memory because a restarted daemon that began
// again at 1 told a browser holding version 1 from the previous process that
// its copy was current — the overview then showed 903 sessions while the
// sidebar showed 1164.
const dataVersionKey = "data_version"

// LoadDataVersion is the counter the last process left behind, or 0 for a
// database that has never published one.
func (s *Store) LoadDataVersion(ctx context.Context) (int64, error) {
	var value sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, dataVersionKey).Scan(&value)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("eventstore: read data version: %w", err)
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value.String), 10, 64)
	if err != nil {
		return 0, nil
	}
	return parsed, nil
}

// SaveDataVersion persists the counter before it is published, so no two
// processes can ever hand out the same version for different data.
func (s *Store) SaveDataVersion(ctx context.Context, version int64) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO meta (key, value, updated_at) VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		dataVersionKey, strconv.FormatInt(version, 10)); err != nil {
		return fmt.Errorf("eventstore: write data version: %w", err)
	}
	return nil
}
