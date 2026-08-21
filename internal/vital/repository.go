package vital

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"flatline/internal/storage"
)

// Repository is the only persistence boundary for VSM output. It writes
// derived rows transactionally and never mutates canonical facts.
type Repository struct {
	db      *storage.DB
	machine *Machine
}

func NewRepository(db *storage.DB, machine *Machine) *Repository {
	return &Repository{db: db, machine: machine}
}

type CurrentState struct {
	ID               int64
	AssetID          string
	State            State
	BrokenOverlay    bool
	EvidenceJSON     string
	BaselineJSON     string
	DetectorVersion  string
	SchemaVersion    string
	ThresholdVersion string
	StartedAt        time.Time
	InstanceID       int64
}

type Transition struct {
	ID               int64     `json:"id"`
	AssetID          string    `json:"asset_id"`
	FromState        *State    `json:"from_state,omitempty"`
	ToState          State     `json:"to_state"`
	BrokenOverlay    bool      `json:"broken_overlay"`
	OccurredAt       time.Time `json:"occurred_at"`
	EvidenceJSON     string    `json:"evidence_json,omitempty"`
	AlignmentJSON    string    `json:"alignment_json,omitempty"`
	DetectorVersion  string    `json:"detector_version"`
	SchemaVersion    string    `json:"schema_version"`
	ThresholdVersion string    `json:"threshold_version"`
}

// Current loads the one open primary state for an asset. A missing row is
// represented as (nil, nil), allowing the machine to create the initial
// dormant instance without inventing a previous state.
func (r *Repository) Current(ctx context.Context, assetID string) (*CurrentState, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("vital: repository database is required")
	}
	if assetID == "" {
		return nil, fmt.Errorf("vital: asset id is required")
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, asset_id, state, broken_overlay, evidence_json, COALESCE(baseline_json, ''),
		       detector_version, schema_version, threshold_version, started_at,
		       COALESCE((SELECT id FROM state_transitions t WHERE t.asset_id = vital_states.asset_id
		                 AND t.occurred_at = vital_states.started_at ORDER BY t.id DESC LIMIT 1), 0)
		FROM vital_states
		WHERE asset_id = ? AND ended_at IS NULL`, assetID)
	state, err := scanCurrentState(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("vital: load current state for %s: %w", assetID, err)
	}
	return state, nil
}

// Apply evaluates and atomically persists one assessment. The database row is
// authoritative for the previous state; caller-supplied PreviousState is
// overwritten so a stale worker cannot fork state history.
func (r *Repository) Apply(ctx context.Context, assessment Assessment) (Decision, error) {
	if r == nil || r.db == nil || r.machine == nil {
		return Decision{}, fmt.Errorf("vital: repository and machine are required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, fmt.Errorf("vital: begin state transaction: %w", err)
	}
	fail := func(err error) (Decision, error) {
		_ = tx.Rollback()
		return Decision{}, err
	}

	current, err := scanCurrentState(tx.QueryRowContext(ctx, `
		SELECT id, asset_id, state, broken_overlay, evidence_json, COALESCE(baseline_json, ''),
		       detector_version, schema_version, threshold_version, started_at,
		       COALESCE((SELECT id FROM state_transitions t WHERE t.asset_id = vital_states.asset_id
		                 AND t.occurred_at = vital_states.started_at ORDER BY t.id DESC LIMIT 1), 0)
		FROM vital_states
		WHERE asset_id = ? AND ended_at IS NULL`, assessment.AssetID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fail(fmt.Errorf("vital: load current state: %w", err))
	}
	if errors.Is(err, sql.ErrNoRows) {
		current = nil
	}
	if current != nil {
		assessment.PreviousState = current.State
		assessment.PreviousBrokenOverlay = current.BrokenOverlay
	}
	decision, err := r.machine.Decide(assessment)
	if err != nil {
		return fail(err)
	}
	if current != nil && decision.Transition && decision.State != decision.From && !CanTransition(current.State, decision.State) {
		return fail(fmt.Errorf("vital: transition %s -> %s is not in the state map", current.State, decision.State))
	}
	// Persist the state-machine projection alongside detector evidence. The
	// detector records explain the numerator/denominator; this small decision
	// envelope preserves the human-readable rule and whether the transition is
	// a modification verification result, so API consumers do not have to
	// reconstruct the decision from implementation details.
	decision.Evidence["decision"] = map[string]any{
		"reason":       decision.Reason,
		"rule":         decision.Rule,
		"alert":        decision.Alert,
		"resurrection": decision.Resurrection,
	}

	evidenceJSON, err := json.Marshal(decision.Evidence)
	if err != nil {
		return fail(fmt.Errorf("vital: marshal evidence: %w", err))
	}
	baselineJSON, err := marshalEvidenceValue(decision.Evidence["silent"])
	if err != nil {
		return fail(fmt.Errorf("vital: marshal baseline evidence: %w", err))
	}
	alignmentJSON, err := json.Marshal(decision.Alignment)
	if err != nil {
		return fail(fmt.Errorf("vital: marshal alignment: %w", err))
	}
	if !decision.Transition {
		if current == nil {
			return fail(fmt.Errorf("vital: non-transition decision has no current state"))
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE vital_states
			SET broken_overlay = ?, evidence_json = ?, baseline_json = ?,
			    detector_version = ?, schema_version = ?, threshold_version = ?
			WHERE id = ? AND ended_at IS NULL`, boolInt(decision.BrokenOverlay), string(evidenceJSON), nullableJSON(baselineJSON), decision.DetectorVersion, decision.SchemaVersion, decision.ThresholdVersion, current.ID); err != nil {
			return fail(fmt.Errorf("vital: refresh current state: %w", err))
		}
		if err := tx.Commit(); err != nil {
			return Decision{}, fmt.Errorf("vital: commit current state: %w", err)
		}
		return decision, nil
	}

	if current != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE vital_states SET ended_at = ? WHERE id = ? AND ended_at IS NULL`, formatTime(assessment.At), current.ID); err != nil {
			return fail(fmt.Errorf("vital: close current state: %w", err))
		}
	}
	var from any
	if decision.From != "" {
		from = string(decision.From)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO state_transitions
		(asset_id, from_state, to_state, broken_overlay, occurred_at, evidence_json, alignment_json, detector_version, schema_version, threshold_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		assessment.AssetID, from, string(decision.State), boolInt(decision.BrokenOverlay), formatTime(assessment.At), string(evidenceJSON), string(alignmentJSON), decision.DetectorVersion, decision.SchemaVersion, decision.ThresholdVersion)
	if err != nil {
		return fail(fmt.Errorf("vital: record transition: %w", err))
	}
	if _, err := result.LastInsertId(); err != nil {
		return fail(fmt.Errorf("vital: transition id: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO vital_states
		(asset_id, state, broken_overlay, evidence_json, baseline_json, detector_version, schema_version, threshold_version, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		assessment.AssetID, string(decision.State), boolInt(decision.BrokenOverlay), string(evidenceJSON), nullableJSON(baselineJSON), decision.DetectorVersion, decision.SchemaVersion, decision.ThresholdVersion, formatTime(assessment.At)); err != nil {
		return fail(fmt.Errorf("vital: create state instance: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return Decision{}, fmt.Errorf("vital: commit transition: %w", err)
	}
	return decision, nil
}

// Transitions returns the persisted transition history in chronological
// order. It is intentionally read-only for API and replay consumers.
func (r *Repository) Transitions(ctx context.Context, assetID string, limit int) ([]Transition, error) {
	if assetID == "" {
		return nil, fmt.Errorf("vital: asset id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, asset_id, from_state, to_state, broken_overlay, occurred_at, evidence_json,
		       COALESCE(alignment_json, ''), detector_version, schema_version, threshold_version
		FROM state_transitions WHERE asset_id = ? ORDER BY occurred_at, id LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, fmt.Errorf("vital: query transitions: %w", err)
	}
	defer rows.Close()
	var out []Transition
	for rows.Next() {
		transition, err := scanTransition(rows)
		if err != nil {
			return nil, fmt.Errorf("vital: scan transition: %w", err)
		}
		out = append(out, *transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vital: iterate transitions: %w", err)
	}
	return out, nil
}

func scanCurrentState(row interface{ Scan(...any) error }) (*CurrentState, error) {
	var (
		state                        CurrentState
		stateText, baseline, started string
		overlay                      int
	)
	if err := row.Scan(&state.ID, &state.AssetID, &stateText, &overlay, &state.EvidenceJSON, &baseline, &state.DetectorVersion, &state.SchemaVersion, &state.ThresholdVersion, &started, &state.InstanceID); err != nil {
		return nil, err
	}
	state.State = State(stateText)
	state.BrokenOverlay = overlay != 0
	state.BaselineJSON = baseline
	var err error
	state.StartedAt, err = time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return nil, fmt.Errorf("parse state started_at: %w", err)
	}
	return &state, nil
}

func scanTransition(row interface{ Scan(...any) error }) (*Transition, error) {
	var (
		transition                    Transition
		from, to, occurred, alignment string
		overlay                       int
		nullFrom                      sql.NullString
	)
	if err := row.Scan(&transition.ID, &transition.AssetID, &nullFrom, &to, &overlay, &occurred, &transition.EvidenceJSON, &alignment, &transition.DetectorVersion, &transition.SchemaVersion, &transition.ThresholdVersion); err != nil {
		return nil, err
	}
	_ = from
	if nullFrom.Valid {
		value := State(nullFrom.String)
		transition.FromState = &value
	}
	transition.ToState = State(to)
	transition.BrokenOverlay = overlay != 0
	transition.AlignmentJSON = alignment
	var err error
	transition.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		return nil, fmt.Errorf("parse transition occurred_at: %w", err)
	}
	return &transition, nil
}

func marshalEvidenceValue(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func nullableJSON(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
