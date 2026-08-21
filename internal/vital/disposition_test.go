package vital

import (
	"context"
	"database/sql"
	"testing"
)

func TestDispositionRequiresConfirmationAndPreservesRollback(t *testing.T) {
	repository, db := testVitalRepository(t)
	ctx := context.Background()
	if _, err := repository.Apply(ctx, repositoryAssessment(1)); err != nil {
		t.Fatalf("initial state: %v", err)
	}
	store := NewDispositionStore(db, repository)
	current, err := repository.Current(ctx, "skill:project:fixture")
	if err != nil || current == nil || current.InstanceID <= 0 {
		t.Fatalf("current state = %+v, err=%v; want transition instance", current, err)
	}

	if _, err := store.Apply(ctx, DispositionRequest{AssetID: current.AssetID, Action: ActionIgnore, StateInstanceID: current.InstanceID}); err == nil {
		t.Fatal("unconfirmed ignore = nil, want confirmation error")
	}
	if _, err := store.Apply(ctx, DispositionRequest{AssetID: current.AssetID, Action: ActionIgnore, StateInstanceID: current.InstanceID, Confirmed: true, Reason: "synthetic fixture accepted"}); err != nil {
		t.Fatalf("confirmed ignore: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dispositions WHERE action = 'ignore'`).Scan(&count); err != nil {
		t.Fatalf("ignore count: %v", err)
	}
	if count != 1 {
		t.Fatalf("ignore count = %d, want 1", count)
	}

	if _, err := store.Apply(ctx, DispositionRequest{AssetID: current.AssetID, Action: ActionPrune, StateInstanceID: current.InstanceID, Confirmed: true}); err == nil {
		t.Fatal("prune without rollback = nil, want rollback error")
	}
	rollback := RollbackRecord{SourcePath: "/synthetic/fixture/SKILL.md", Strategy: "archive-only; restore archived_at to NULL", Reversible: true}
	if _, err := store.Apply(ctx, DispositionRequest{AssetID: current.AssetID, Action: ActionPrune, StateInstanceID: current.InstanceID, Confirmed: true, Reason: "synthetic cleanup", Rollback: rollback}); err != nil {
		t.Fatalf("confirmed prune: %v", err)
	}
	var archived sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT archived_at FROM assets WHERE id = ?`, current.AssetID).Scan(&archived); err != nil {
		t.Fatalf("archived_at: %v", err)
	}
	if !archived.Valid {
		t.Fatal("archived_at is NULL after confirmed logical prune")
	}
	archivedState, err := repository.Current(ctx, current.AssetID)
	if err != nil || archivedState == nil || archivedState.State != StateArchived {
		t.Fatalf("archived state = %+v, err=%v", archivedState, err)
	}

	if err := store.Restore(ctx, current.AssetID, false); err == nil {
		t.Fatal("unconfirmed restore = nil, want confirmation error")
	}
	if err := store.Restore(ctx, current.AssetID, true); err != nil {
		t.Fatalf("confirmed restore: %v", err)
	}
	restored, err := repository.Current(ctx, current.AssetID)
	if err != nil || restored == nil || restored.State != StateDormant {
		t.Fatalf("restored state = %+v, err=%v; want dormant re-entry", restored, err)
	}
}

func TestModifyDispositionEntersAwaitingResurrection(t *testing.T) {
	repository, db := testVitalRepository(t)
	ctx := context.Background()
	silent := repositoryAssessment(1)
	silent.Silent = silentVerdict(true)
	if _, err := repository.Apply(ctx, silent); err != nil {
		t.Fatalf("silent state: %v", err)
	}
	current, err := repository.Current(ctx, "skill:project:fixture")
	if err != nil || current == nil || current.State != StateSilent {
		t.Fatalf("current = %+v, err=%v", current, err)
	}
	store := NewDispositionStore(db, repository)
	if _, err := store.Apply(ctx, DispositionRequest{AssetID: current.AssetID, Action: ActionModify, StateInstanceID: current.InstanceID, Confirmed: true, Reason: "synthetic edit confirmation"}); err != nil {
		t.Fatalf("modify disposition: %v", err)
	}
	awaiting, err := repository.Current(ctx, current.AssetID)
	if err != nil || awaiting == nil || awaiting.State != StateAwaitingResurrection {
		t.Fatalf("awaiting state = %+v, err=%v", awaiting, err)
	}
}
