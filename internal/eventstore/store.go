// Package eventstore owns the append-only canonical event write path.
package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

func (s *Store) IngestSession(ctx context.Context, source adapters.Source, meta adapters.SessionMeta) (string, error) {
	if !source.Valid() {
		return "", fmt.Errorf("eventstore: invalid source %q", source)
	}
	if meta.SourceSessionID == "" {
		return "", fmt.Errorf("eventstore: source session id is required")
	}
	id := string(source) + ":" + meta.SourceSessionID
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, source, source_session_id, started_at, ended_at, harness_version, model, cwd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, source_session_id) DO NOTHING`,
		id, source, meta.SourceSessionID,
		nullableTime(meta.StartedAt), nullableTime(meta.EndedAt),
		nullableString(meta.HarnessVersion), nullableString(meta.Model), nullableString(meta.CWD))
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
