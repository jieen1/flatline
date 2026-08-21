package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// requiredTables are the v0.4 object-model tables that must exist after the
// initial migration (system design §7.2). decision_tasks and the
// improvement_cycles family must NOT exist.
var requiredTables = []string{
	"schema_migrations",
	"assets",
	"asset_versions",
	"sessions",
	"events",
	"effective_bundles",
	"opportunities",
	"participations",
	"vital_states",
	"state_transitions",
	"dispositions",
	"reference_checks",
	"reference_check_items",
}

var forbiddenTables = []string{
	"decision_tasks",
	"improvement_cycles",
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func tableNames(t *testing.T, db *DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	return out
}

func TestOpenAppliesSchema(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	names := tableNames(t, db)
	for _, table := range requiredTables {
		if !names[table] {
			t.Errorf("table %q not found after migration", table)
		}
	}
	for _, table := range forbiddenTables {
		if names[table] {
			t.Errorf("forbidden table %q exists (must not be in MVP schema)", table)
		}
	}

	v, err := db.SchemaVersionOf(ctx)
	if err != nil {
		t.Fatalf("SchemaVersionOf: %v", err)
	}
	if v != SchemaVersion {
		t.Errorf("SchemaVersionOf = %d, want %d", v, SchemaVersion)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Re-running the runner on an already-migrated DB must be a no-op.
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("third Migrate: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != SchemaVersion {
		t.Errorf("schema_migrations rows = %d, want %d (idempotent)", count, SchemaVersion)
	}
}

func TestAssetVersionContentHashIsUniquePerAsset(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, id := range []string{"skill:test:a", "skill:test:b"} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO assets (id, kind, name, first_seen_at)
			VALUES (?, 'skill', ?, '2026-01-01T00:00:00Z')`, id, id); err != nil {
			t.Fatalf("insert asset %s: %v", id, err)
		}
	}
	insert := `
		INSERT INTO asset_versions (asset_id, version, content_hash, observation_level, observed_at)
		VALUES (?, 1, 'sha256:same', 'unknown', '2026-01-01T00:00:00Z')`
	if _, err := db.ExecContext(ctx, insert, "skill:test:a"); err != nil {
		t.Fatalf("first version insert: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert, "skill:test:b"); err != nil {
		t.Fatalf("same content on another asset: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert, "skill:test:a"); err == nil {
		t.Fatal("same asset and content hash: expected uniqueness error")
	}
}

func TestReopenExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")
	db1, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db1.Close()

	db2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()

	if err := db2.Ping(context.Background()); err != nil {
		t.Fatalf("Ping after reopen: %v", err)
	}
}

func TestObservationLevelCheck(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Seed minimal rows to exercise the events.observation_level CHECK.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO assets (id, kind, name, first_seen_at)
		VALUES ('skill:test:demo', 'skill', 'demo', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (id, source, source_session_id)
		VALUES ('claude_code:s1', 'claude_code', 's1')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// A canonical level must be accepted.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO events (session_id, event_type, observation_level, locator_json)
		VALUES ('claude_code:s1', 'asset_invoked', 'invoked', '{"line":1}')`); err != nil {
		t.Fatalf("insert event with canonical level: %v", err)
	}

	// A non-canonical level must be rejected by the CHECK constraint.
	_, err := db.ExecContext(ctx, `
		INSERT INTO events (session_id, event_type, observation_level, locator_json)
		VALUES ('claude_code:s1', 'asset_invoked', 'exact', '{"line":2}')`)
	if err == nil {
		t.Error("insert event with non-canonical observation_level: expected CHECK violation, got nil")
	}
}

func TestParticipationSignalCheck(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO assets (id, kind, name, first_seen_at)
		VALUES ('skill:test:demo', 'skill', 'demo', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO asset_versions (asset_id, version, content_hash, observation_level, observed_at)
		VALUES ('skill:test:demo', 1, 'sha256:abc', 'invoked', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert asset_version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (id, source, source_session_id)
		VALUES ('claude_code:s1', 'claude_code', 's1')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// `followed` is a valid participation signal (orthogonal to observation level).
	if _, err := db.ExecContext(ctx, `
		INSERT INTO participations (asset_version_id, session_id, participation_signal, observation_level)
		VALUES (1, 'claude_code:s1', 'followed', 'invoked')`); err != nil {
		t.Fatalf("insert participation with followed: %v", err)
	}

	// `followed` is NOT a valid observation level.
	_, err := db.ExecContext(ctx, `
		INSERT INTO participations (asset_version_id, session_id, participation_signal, observation_level)
		VALUES (1, 'claude_code:s1', 'invoked', 'followed')`)
	if err == nil {
		t.Error("insert participation with observation_level='followed': expected CHECK violation, got nil")
	}
}

func TestEventSourceIDIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (id, source, source_session_id)
		VALUES ('claude_code:s1', 'claude_code', 's1')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	insert := `
		INSERT INTO events (session_id, source_event_id, event_type, observation_level, locator_json)
		VALUES ('claude_code:s1', 'evt-1', 'session_started', 'inferred', '{}')`
	if _, err := db.ExecContext(ctx, insert); err != nil {
		t.Fatalf("first event insert: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert); err == nil {
		t.Error("duplicate source_event_id: expected uniqueness error, got nil")
	}
}

func TestVitalStateHasOneOpenInstancePerAsset(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO assets (id, kind, name, first_seen_at)
		VALUES ('skill:test:demo', 'skill', 'demo', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	insert := `
		INSERT INTO vital_states
		(asset_id, state, evidence_json, detector_version, schema_version, threshold_version, started_at)
		VALUES ('skill:test:demo', 'healthy', '{}', 'detector-1', 'schema-1', 'threshold-1', '2026-01-01T00:00:00Z')`
	if _, err := db.ExecContext(ctx, insert); err != nil {
		t.Fatalf("first state insert: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert); err == nil {
		t.Error("second open state: expected uniqueness error, got nil")
	}
}

func TestDispositionStateInstanceMustMatchAsset(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, id := range []string{"skill:test:a", "skill:test:b"} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO assets (id, kind, name, first_seen_at)
			VALUES (?, 'skill', ?, '2026-01-01T00:00:00Z')`, id, id); err != nil {
			t.Fatalf("insert asset %s: %v", id, err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO state_transitions
		(asset_id, to_state, occurred_at, evidence_json, detector_version, schema_version, threshold_version)
		VALUES ('skill:test:a', 'silent', '2026-01-02T00:00:00Z', '{}', 'detector-1', 'schema-1', 'threshold-1')`); err != nil {
		t.Fatalf("insert transition: %v", err)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO dispositions (asset_id, state_instance_id, action)
		VALUES ('skill:test:b', 1, 'ignore')`)
	if err == nil {
		t.Error("cross-asset disposition: expected foreign-key error, got nil")
	}
}
