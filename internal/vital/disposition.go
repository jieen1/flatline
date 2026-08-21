package vital

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"flatline/internal/storage"
)

// Action is a user-confirmed response to a state instance. None of these
// actions edits an asset source file; archive and prune are logical archive
// operations with a required rollback record.
type Action string

const (
	ActionModify  Action = "modify"
	ActionPrune   Action = "prune"
	ActionArchive Action = "archive"
	ActionIgnore  Action = "ignore"
)

func (a Action) Valid() bool {
	switch a {
	case ActionModify, ActionPrune, ActionArchive, ActionIgnore:
		return true
	default:
		return false
	}
}

// RollbackRecord explains how a logical cleanup can be reversed. It is data
// for a future/manual recovery action, not permission to touch the source.
type RollbackRecord struct {
	SourcePath string `json:"source_path"`
	Strategy   string `json:"strategy"`
	Reversible bool   `json:"reversible"`
}

type DispositionRequest struct {
	AssetID         string
	Action          Action
	StateInstanceID int64
	Confirmed       bool
	Reason          string
	Rollback        RollbackRecord
	CreatedAt       time.Time
}

type Disposition struct {
	ID              int64           `json:"id"`
	AssetID         string          `json:"asset_id"`
	StateInstanceID int64           `json:"state_instance_id"`
	Action          Action          `json:"action"`
	Reason          string          `json:"reason,omitempty"`
	Rollback        *RollbackRecord `json:"rollback,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

// DispositionStore persists explicit user decisions and delegates all state
// changes to the VSM repository. It deliberately has no filesystem mutation
// capability.
type DispositionStore struct {
	db     *storage.DB
	states *Repository
}

func NewDispositionStore(db *storage.DB, states *Repository) *DispositionStore {
	return &DispositionStore{db: db, states: states}
}

func (s *DispositionStore) Apply(ctx context.Context, request DispositionRequest) (*Disposition, error) {
	if s == nil || s.db == nil || s.states == nil {
		return nil, fmt.Errorf("vital: disposition store is required")
	}
	if strings.TrimSpace(request.AssetID) == "" {
		return nil, fmt.Errorf("vital: disposition asset id is required")
	}
	if !request.Action.Valid() {
		return nil, fmt.Errorf("vital: invalid disposition action %q", request.Action)
	}
	if !request.Confirmed {
		return nil, fmt.Errorf("vital: disposition requires explicit confirmation")
	}
	if request.StateInstanceID <= 0 {
		return nil, fmt.Errorf("vital: state instance id is required")
	}
	if request.Action == ActionPrune || request.Action == ActionArchive {
		if !request.Rollback.Reversible || strings.TrimSpace(request.Rollback.SourcePath) == "" || strings.TrimSpace(request.Rollback.Strategy) == "" {
			return nil, fmt.Errorf("vital: %s requires a reversible rollback record", request.Action)
		}
	}

	current, err := s.states.Current(ctx, request.AssetID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("vital: asset %s has no current state", request.AssetID)
	}
	if current.InstanceID != request.StateInstanceID {
		return nil, fmt.Errorf("vital: disposition state instance %d is stale; current instance is %d", request.StateInstanceID, current.InstanceID)
	}

	createdAt := request.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if createdAt.Location() != time.UTC {
		return nil, fmt.Errorf("vital: disposition created_at must be UTC")
	}

	// State changes are intentionally performed before the disposition row is
	// recorded so the row's foreign key always points to an existing instance.
	// The operation is still source-safe: a failure can leave only derived
	// state to be replayed, never a partially edited asset file.
	switch request.Action {
	case ActionModify:
		if _, err := s.states.Apply(ctx, Assessment{AssetID: request.AssetID, At: createdAt, RequestResurrection: true}); err != nil {
			return nil, fmt.Errorf("vital: apply modify disposition: %w", err)
		}
	case ActionPrune, ActionArchive:
		if _, err := s.states.Apply(ctx, Assessment{AssetID: request.AssetID, At: createdAt, Archived: true}); err != nil {
			return nil, fmt.Errorf("vital: archive state: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE assets SET archived_at = ? WHERE id = ?`, formatTime(createdAt), request.AssetID); err != nil {
			return nil, fmt.Errorf("vital: mark asset archived: %w", err)
		}
	case ActionIgnore:
		// Ignore is scoped to the current state instance and does not alter
		// the derived state or source asset.
	}

	rollbackJSON := any(nil)
	var rollback *RollbackRecord
	if request.Rollback != (RollbackRecord{}) {
		encoded, err := json.Marshal(request.Rollback)
		if err != nil {
			return nil, fmt.Errorf("vital: marshal rollback: %w", err)
		}
		rollbackJSON = string(encoded)
		copy := request.Rollback
		rollback = &copy
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO dispositions (asset_id, state_instance_id, action, reason, rollback_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, request.AssetID, request.StateInstanceID, string(request.Action), nullableText(request.Reason), rollbackJSON, formatTime(createdAt))
	if err != nil {
		return nil, fmt.Errorf("vital: record disposition: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("vital: disposition id: %w", err)
	}
	return &Disposition{ID: id, AssetID: request.AssetID, StateInstanceID: request.StateInstanceID, Action: request.Action, Reason: request.Reason, Rollback: rollback, CreatedAt: createdAt}, nil
}

// Restore removes the logical archive marker only after explicit confirmation
// and re-enters the VSM through dormant. It never recreates or edits source
// bytes.
func (s *DispositionStore) Restore(ctx context.Context, assetID string, confirmed bool) error {
	if s == nil || s.db == nil || s.states == nil {
		return fmt.Errorf("vital: disposition store is required")
	}
	if strings.TrimSpace(assetID) == "" {
		return fmt.Errorf("vital: restore asset id is required")
	}
	if !confirmed {
		return fmt.Errorf("vital: restore requires explicit confirmation")
	}
	current, err := s.states.Current(ctx, assetID)
	if err != nil {
		return err
	}
	if current == nil || current.State != StateArchived {
		return fmt.Errorf("vital: asset %s is not archived", assetID)
	}
	at := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE assets SET archived_at = NULL WHERE id = ?`, assetID); err != nil {
		return fmt.Errorf("vital: restore archive marker: %w", err)
	}
	if _, err := s.states.Apply(ctx, Assessment{AssetID: assetID, At: at, Restore: true}); err != nil {
		return fmt.Errorf("vital: restore state: %w", err)
	}
	return nil
}

// List returns user dispositions in creation order. Rollback JSON is decoded
// back into a typed record so clients can display the recovery path without
// interpreting database internals.
func (s *DispositionStore) List(ctx context.Context, assetID string, limit int) ([]Disposition, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("vital: disposition store is required")
	}
	if strings.TrimSpace(assetID) == "" {
		return nil, fmt.Errorf("vital: disposition asset id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, asset_id, state_instance_id, action, COALESCE(reason, ''), rollback_json, created_at
		FROM dispositions WHERE asset_id = ? ORDER BY created_at, id LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, fmt.Errorf("vital: query dispositions: %w", err)
	}
	defer rows.Close()
	out := make([]Disposition, 0)
	for rows.Next() {
		var item Disposition
		var action, createdAt string
		var reason, rollbackValue sql.NullString
		if err := rows.Scan(&item.ID, &item.AssetID, &item.StateInstanceID, &action, &reason, &rollbackValue, &createdAt); err != nil {
			return nil, fmt.Errorf("vital: scan disposition: %w", err)
		}
		item.Action = Action(action)
		if reason.Valid {
			item.Reason = reason.String
		}
		if rollbackValue.Valid && rollbackValue.String != "" {
			var rollback RollbackRecord
			if err := json.Unmarshal([]byte(rollbackValue.String), &rollback); err != nil {
				return nil, fmt.Errorf("vital: decode disposition rollback: %w", err)
			}
			item.Rollback = &rollback
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("vital: parse disposition created_at: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vital: iterate dispositions: %w", err)
	}
	return out, nil
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
