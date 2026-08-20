// Package storage opens the SQLite database and applies schema migrations.
//
// The daemon is the single data owner (ADR-2). Migrations are applied in
// version order, each inside its own transaction, and are idempotent:
// re-running the runner on an already-migrated database is a no-op.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"flatline/migrations"
)

// SchemaVersion is the schema version written by the initial migration.
const SchemaVersion = 1

// DB wraps the open database handle.
type DB struct {
	*sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies
// all pending migrations. The parent directory is created if missing.
func Open(ctx context.Context, path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("storage: create db dir: %w", err)
		}
	}

	// WAL mode for a single-writer daemon; busy_timeout so a concurrent
	// reader (e.g. a CLI query) does not fail immediately.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	// modernc.org/sqlite is a single-connection driver; serialize access.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("storage: ping %s: %w", path, err)
	}

	db := &DB{DB: sqlDB}
	if err := db.Migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// Migrate applies all pending migrations in version order. Each migration
// runs in a single transaction; already-applied versions are skipped, so the
// runner is safe to invoke on every startup.
func (db *DB) Migrate(ctx context.Context) error {
	all, err := migrations.All()
	if err != nil {
		return err
	}

	// Ensure the bookkeeping table exists before checking applied versions.
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER PRIMARY KEY,
			name        TEXT    NOT NULL,
			applied_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		)`); err != nil {
		return fmt.Errorf("storage: create schema_migrations: %w", err)
	}

	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for _, m := range all {
		if applied[m.Version] {
			continue
		}
		if err := db.applyOne(ctx, m); err != nil {
			return fmt.Errorf("storage: apply migration %03d: %w", m.Version, err)
		}
	}
	return nil
}

func (db *DB) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("storage: read schema_migrations: %w", err)
	}
	defer rows.Close()

	out := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("storage: scan schema_migrations: %w", err)
		}
		out[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate schema_migrations: %w", err)
	}
	return out, nil
}

// applyOne runs a single migration inside a transaction and records it in
// schema_migrations only after the DDL succeeds.
func (db *DB) applyOne(ctx context.Context, m migrations.Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		tx.Rollback()
		return fmt.Errorf("exec sql: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`,
		m.Version, m.Name); err != nil {
		tx.Rollback()
		return fmt.Errorf("record migration: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Ping reports whether the database is reachable. It satisfies the
// api.HealthChecker interface.
func (db *DB) Ping(ctx context.Context) error {
	return db.PingContext(ctx)
}

// SchemaVersionOf returns the highest applied migration version, or 0 if the
// database has no migrations recorded.
func (db *DB) SchemaVersionOf(ctx context.Context) (int, error) {
	var v int
	err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("storage: schema version: %w", err)
	}
	return v, nil
}
