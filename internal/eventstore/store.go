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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, source, source_session_id, started_at, ended_at, harness_version, model, cwd, title, task_text)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, source_session_id) DO UPDATE SET
			started_at      = COALESCE(sessions.started_at, excluded.started_at),
			ended_at        = COALESCE(sessions.ended_at, excluded.ended_at),
			harness_version = COALESCE(sessions.harness_version, excluded.harness_version),
			model           = COALESCE(sessions.model, excluded.model),
			cwd             = COALESCE(sessions.cwd, excluded.cwd),
			title           = COALESCE(sessions.title, excluded.title),
			task_text       = COALESCE(sessions.task_text, excluded.task_text)`,
		id, source, meta.SourceSessionID,
		nullableTime(meta.StartedAt), nullableTime(meta.EndedAt),
		nullableString(meta.HarnessVersion), nullableString(meta.Model), nullableString(meta.CWD),
		nullableString(meta.Title), nullableString(meta.TaskText))
	if err != nil {
		return "", fmt.Errorf("eventstore: ingest session %s: %w", id, err)
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
		result, err := tx.ExecContext(ctx, `
			INSERT INTO events
			(session_id, event_type, asset_id, asset_version_id, source_event_id,
			 participation_signal, observation_level, payload_json, locator_json,
			 occurred_at, adapter_version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING`,
			sessionID, event.EventType, nullableString(event.AssetID), assetVersion,
			event.SourceEventID, signal, string(event.ObservationLevel), string(payload),
			string(locator), occurred, nullableString(event.AdapterVersion))
		if err != nil {
			return rollback(fmt.Errorf("eventstore: insert event %q: %w", event.SourceEventID, err))
		}
		n, err := result.RowsAffected()
		if err != nil {
			return rollback(fmt.Errorf("eventstore: rows affected %q: %w", event.SourceEventID, err))
		}
		inserted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("eventstore: commit events: %w", err)
	}
	return inserted, nil
}

// IngestFriction records only source-backed, explicit tool failures. Generic
// text containing words such as "error" is deliberately not classified.
// Repeated replays are idempotent by (session, source event, friction kind).
func (s *Store) IngestFriction(ctx context.Context, sessionID string, events []canonical.Event) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("eventstore: session id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("eventstore: begin friction transaction: %w", err)
	}
	rollback := func(err error) (int, error) {
		_ = tx.Rollback()
		return 0, err
	}
	inserted := 0
	for _, event := range events {
		if event.SessionID != sessionID {
			return rollback(fmt.Errorf("eventstore: friction event session %q does not match %q", event.SessionID, sessionID))
		}
		if event.EventType != canonical.EventTypeTranscriptResult {
			continue
		}
		isError, hasIsError := payloadBool(event.Payload, "is_error")
		exitCode, hasExitCode := payloadInt(event.Payload, "exit_code")
		if !(hasIsError && isError) && !(hasExitCode && exitCode != 0) {
			continue
		}
		payload, err := json.Marshal(boundedFrictionPayload(event.Payload))
		if err != nil {
			return rollback(fmt.Errorf("eventstore: marshal friction payload %q: %w", event.SourceEventID, err))
		}
		locator, err := json.Marshal(event.Locator)
		if err != nil {
			return rollback(fmt.Errorf("eventstore: marshal friction locator %q: %w", event.SourceEventID, err))
		}
		var recordedIsError any
		if hasIsError {
			recordedIsError = boolInt(isError)
		}
		var recordedExitCode any
		if hasExitCode {
			recordedExitCode = exitCode
		}
		var occurred any
		if event.OccurredAt != nil {
			occurred = formatTime(*event.OccurredAt)
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO friction_records
			(session_id, source_event_id, friction_kind, event_type, observation_level,
			 is_error, exit_code, payload_json, locator_json, occurred_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (session_id, source_event_id, friction_kind) DO NOTHING`,
			sessionID, event.SourceEventID, FrictionKindToolError, event.EventType,
			string(event.ObservationLevel), recordedIsError, recordedExitCode,
			string(payload), string(locator), occurred)
		if err != nil {
			return rollback(fmt.Errorf("eventstore: insert friction %q: %w", event.SourceEventID, err))
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return rollback(fmt.Errorf("eventstore: friction rows affected %q: %w", event.SourceEventID, err))
		}
		inserted += int(rows)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("eventstore: commit friction: %w", err)
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

func payloadBool(payload map[string]any, key string) (bool, bool) {
	value, ok := payload[key]
	if !ok {
		return false, false
	}
	switch value := value.(type) {
	case bool:
		return value, true
	case *bool:
		return value != nil && *value, value != nil
	default:
		return false, false
	}
}

func payloadInt(payload map[string]any, key string) (int, bool) {
	value, ok := payload[key]
	if !ok {
		return 0, false
	}
	switch value := value.(type) {
	case int:
		return value, true
	case int8:
		return int(value), true
	case int16:
		return int(value), true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case uint:
		return int(value), true
	case uint8:
		return int(value), true
	case uint16:
		return int(value), true
	case uint32:
		return int(value), true
	case uint64:
		if uint64(int(value)) != value {
			return 0, false
		}
		return int(value), true
	case float64:
		if value != float64(int(value)) {
			return 0, false
		}
		return int(value), true
	case json.Number:
		parsed, err := strconv.Atoi(string(value))
		return parsed, err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func boundedFrictionPayload(payload map[string]any) map[string]any {
	encoded, err := json.Marshal(payload)
	if err == nil && len(encoded) <= 16*1024 {
		return payload
	}
	out := make(map[string]any)
	for _, key := range []string{"message_id", "turn_id", "tool_name", "is_error", "exit_code", "truncated"} {
		if value, ok := payload[key]; ok {
			out[key] = value
		}
	}
	if value, ok := payload["tool_output"].(string); ok {
		if len([]rune(value)) > 8192 {
			value = string([]rune(value)[:8192]) + "…"
		}
		out["tool_output"] = value
	}
	out["friction_evidence_truncated"] = true
	return out
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
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
