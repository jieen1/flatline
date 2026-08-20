package eventstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"flatline/internal/adapters"
	"flatline/internal/canonical"
)

// DetectEnvironmentChanges compares a session with the immediately previous
// same-source session in deterministic started_at/id order. Each changed field
// becomes one inferred alignment anchor with a stable idempotency key.
func (s *Store) DetectEnvironmentChanges(ctx context.Context, sessionID string) (int, error) {
	var current struct {
		id, source, startedAt, harness, model string
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT id, source, COALESCE(started_at, ''), COALESCE(harness_version, ''), COALESCE(model, '')
		FROM sessions WHERE id = ?`, sessionID).Scan(
		&current.id, &current.source, &current.startedAt, &current.harness, &current.model); err != nil {
		return 0, fmt.Errorf("eventstore: load environment session: %w", err)
	}
	var previous struct {
		id, harness, model string
	}
	query := `
		SELECT id, COALESCE(harness_version, ''), COALESCE(model, '')
		FROM sessions
		WHERE source = ?
		  AND (COALESCE(started_at, '') < ? OR (COALESCE(started_at, '') = ? AND id < ?))
		ORDER BY COALESCE(started_at, '' ) DESC, id DESC
		LIMIT 1`
	row := s.db.QueryRowContext(ctx, query, current.source, current.startedAt, current.startedAt, current.id)
	if err := row.Scan(&previous.id, &previous.harness, &previous.model); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("eventstore: load previous environment session: %w", err)
	}
	currentTime := parseNullableTime(current.startedAt)
	var events []canonical.Event
	for _, change := range []struct{ field, from, to string }{
		{"harness_version", previous.harness, current.harness},
		{"model", previous.model, current.model},
	} {
		if change.from == "" || change.to == "" || change.from == change.to {
			continue
		}
		payload := map[string]any{
			"field":               change.field,
			"from":                change.from,
			"to":                  change.to,
			"previous_session_id": previous.id,
			"alignment_only":      true,
		}
		line := len(events)
		events = append(events, canonical.Event{
			SourceEventID:    "envchange:" + sessionID + ":" + change.field,
			SessionID:        sessionID,
			EventType:        canonical.EventTypeEnvironmentChanged,
			ObservationLevel: canonical.LevelInferred,
			Payload:          payload,
			Locator: canonical.Locator{
				Source: current.source, SessionID: sessionID,
				Line: &line, RawRef: "environment_changed:" + change.field,
			},
			OccurredAt:     currentTime,
			AdapterVersion: "eventstore/1",
		})
	}
	if len(events) == 0 {
		return 0, nil
	}
	return s.IngestEvents(ctx, sessionID, events)
}

// Legacy-compatible helper for callers that already carry version metadata.
// IngestSession persists the metadata; detection reads the canonical session row.
func (s *Store) DetectEnvironmentChangesWithVersion(ctx context.Context, sessionID string, _ adapters.VersionInfo) (int, error) {
	return s.DetectEnvironmentChanges(ctx, sessionID)
}

func parseNullableTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}
