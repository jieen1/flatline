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
	"log"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"flatline/migrations"
)

// SchemaVersion is the highest schema migration version the daemon expects.
const SchemaVersion = 22

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
	// WAL lets readers proceed while the single ingest writer holds a write
	// transaction. Writers still serialize on the database lock and wait out
	// busy_timeout, so a small pool bounds contention without starving reads.
	sqlDB.SetMaxOpenConns(4)

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

// noTransactionMarker opts one migration out of the surrounding transaction.
//
// Rebuilding a table is the case that needs it. SQLite cannot turn foreign
// keys off inside a transaction, and this daemon runs with them on, so a
// `CREATE new / INSERT SELECT / DROP old / RENAME` inside a transaction
// cascades the drop into every child table. A migration that declares itself
// with this marker on its first line runs outside the transaction with foreign
// keys off, and the runner then checks that nothing was left dangling before
// recording it. That is the supported way to rebuild a table; writable_schema
// is not.
const noTransactionMarker = "-- flatline:no-transaction"

// tolerateExistingMarker declares a migration whose additions may already be
// present.
//
// A migration is append-only once any database has recorded it: an edited file
// is never re-run, so a column added to it later exists only in the databases
// created afterwards. Splitting the late additions into a new file is the fix,
// and that new file has to be a no-op on the databases that already got them.
// Such a migration runs statement by statement and skips only the one error
// that means "already there".
const tolerateExistingMarker = "-- flatline:tolerate-existing"

// alreadyPresent is the SQLite error for adding a column a table already has.
const alreadyPresent = "duplicate column name"

// applyOne runs a single migration and records it in schema_migrations only
// after the DDL succeeds.
func (db *DB) applyOne(ctx context.Context, m migrations.Migration) error {
	if strings.HasPrefix(strings.TrimSpace(m.SQL), noTransactionMarker) {
		return db.applyOutsideTransaction(ctx, m)
	}
	if strings.HasPrefix(strings.TrimSpace(m.SQL), tolerateExistingMarker) {
		return db.applyTolerantly(ctx, m)
	}
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

// applyTolerantly runs each statement of a migration on its own and skips the
// ones that fail only because what they add is already there.
func (db *DB) applyTolerantly(ctx context.Context, m migrations.Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// Comments are stripped before the split: a `--` line can hold a semicolon
	// of its own, and that would cut a statement in half.
	for _, statement := range strings.Split(stripSQLComments(m.SQL), ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), alreadyPresent) {
				continue
			}
			tx.Rollback()
			return fmt.Errorf("exec sql: %w", err)
		}
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

// stripSQLComments removes whole `--` lines. It is used only on the migrations
// that declare themselves statement-splittable, and none of those holds a
// string literal.
func stripSQLComments(statement string) string {
	var b strings.Builder
	for _, line := range strings.Split(statement, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// applyOutsideTransaction runs a table-rebuilding migration with foreign keys
// off and refuses to record it if the rebuild left a dangling reference.
func (db *DB) applyOutsideTransaction(ctx context.Context, m migrations.Migration) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	restore := func() {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
			log.Printf("storage: re-enable foreign keys after migration %03d: %v", m.Version, err)
		}
	}
	defer restore()
	if _, err := conn.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("exec sql: %w", err)
	}
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	dangling := rows.Next()
	rows.Close()
	if dangling {
		return fmt.Errorf("migration %03d left a dangling foreign key; the database is unchanged on disk only if the migration itself is transactional", m.Version)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.Version, m.Name); err != nil {
		return fmt.Errorf("record migration: %w", err)
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
