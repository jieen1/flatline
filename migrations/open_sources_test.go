package migrations_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"flatline/migrations"
)

const openSourcesVersion = 12

// applyThrough opens a fresh database with foreign keys enforced exactly as
// storage.Open does, and applies migrations up to and including maxVersion.
// Each one runs in its own transaction, which is what the real runner does and
// is the constraint that rules out PRAGMA foreign_keys=OFF.
func applyThrough(t *testing.T, path string, maxVersion int) *sql.DB {
	return applyRange(t, path, 1, maxVersion)
}

func applyRange(t *testing.T, path string, minVersion, maxVersion int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	all, err := migrations.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, migration := range all {
		if migration.Version < minVersion || migration.Version > maxVersion {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin %s: %v", migration.Name, err)
		}
		if _, err := tx.Exec(migration.SQL); err != nil {
			tx.Rollback()
			t.Fatalf("apply %s: %v", migration.Name, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", migration.Name, err)
		}
	}
	return db
}

func seedLegacyRows(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO sessions (id, source, source_session_id, started_at, ended_at, harness_version, model, cwd,
			title, task_text, parent_session_id, thread_kind, agent_role, agent_nickname, originator)
		 VALUES ('claude_code:s1','claude_code','s1','2026-01-01T00:00:00Z','2026-01-01T01:00:00Z','1.2.3','opus','/home/dev',
			'Legacy title','legacy task','claude_code:p0','main','role','nick','claude_code')`,
		`INSERT INTO sessions (id, source, source_session_id) VALUES ('codex:s2','codex','s2')`,
		`INSERT INTO events (session_id, event_type, source_event_id, observation_level, payload_json, locator_json, occurred_at, adapter_version)
		 VALUES ('claude_code:s1','transcript_message','e1','unknown','{"text":"hello"}','{"source":"claude_code","session_id":"claude_code:s1","line":1}','2026-01-01T00:00:00Z','v1')`,
		`INSERT INTO session_stats (session_id, event_count, transcript_count, message_count, user_message_count,
			tool_call_count, tool_result_count, friction_count, tool_error_count, nonzero_exit_count, asset_count, computed_at)
		 VALUES ('claude_code:s1',1,1,1,1,0,0,0,0,0,0,'2026-01-01T00:00:00Z')`,
		`INSERT INTO session_tags (session_id, tag, kind) VALUES ('claude_code:s1','keep-me','user')`,
		`INSERT INTO session_annotations (session_id, pinned, note, updated_at) VALUES ('claude_code:s1',1,'my note','2026-01-01T00:00:00Z')`,
		`INSERT INTO sessions_fts (session_id, title, task_text, cwd, model, source_session_id)
		 VALUES ('claude_code:s1','Legacy title','legacy task','/home/dev','opus','s1')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
}

func tableCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"sessions", "events", "session_stats", "session_tags", "session_annotations", "sessions_fts"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = count
	}
	return out
}

// TestOpenSourcesPreservesExistingData is the guard on the one thing migration
// 012 must never do. Every child of sessions cascades on delete, so a rebuild
// that drops the table takes the whole event store with it.
func TestOpenSourcesPreservesExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.db")
	db := applyThrough(t, path, openSourcesVersion-1)
	seedLegacyRows(t, db)
	before := tableCounts(t, db)
	if before["events"] != 1 || before["session_tags"] != 1 || before["session_annotations"] != 1 {
		t.Fatalf("seed did not land: %v", before)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db = applyRange(t, path, openSourcesVersion, openSourcesVersion)
	defer db.Close()
	after := tableCounts(t, db)
	for table, count := range before {
		if after[table] != count {
			t.Fatalf("%s went from %d to %d rows across migration 012", table, count, after[table])
		}
	}

	var title, tag, note string
	if err := db.QueryRow(`SELECT title FROM sessions WHERE id = 'claude_code:s1'`).Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if err := db.QueryRow(`SELECT tag FROM session_tags WHERE session_id = 'claude_code:s1'`).Scan(&tag); err != nil {
		t.Fatalf("read tag: %v", err)
	}
	if err := db.QueryRow(`SELECT note FROM session_annotations WHERE session_id = 'claude_code:s1'`).Scan(&note); err != nil {
		t.Fatalf("read note: %v", err)
	}
	if title != "Legacy title" || tag != "keep-me" || note != "my note" {
		t.Fatalf("user-owned rows changed: %q %q %q", title, tag, note)
	}
}

func TestOpenSourcesAcceptsANewSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sources.db")
	db := applyThrough(t, path, openSourcesVersion)
	defer db.Close()
	for _, source := range []string{"opencode", "dsh", "hermes", "some_future_harness"} {
		if _, err := db.Exec(`INSERT INTO sessions (id, source, source_session_id) VALUES (?, ?, ?)`,
			source+":s1", source, "s1"); err != nil {
			t.Fatalf("insert %s session: %v", source, err)
		}
	}
}

// The CHECK is gone but nothing else about the table may be, so the constraints
// that keep the store consistent are re-checked explicitly.
func TestOpenSourcesKeepsConstraintsAndIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "constraints.db")
	db := applyThrough(t, path, openSourcesVersion)
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO sessions (id, source, source_session_id) VALUES ('opencode:s1','opencode','s1')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, source, source_session_id) VALUES ('opencode:dup','opencode','s1')`); err == nil {
		t.Fatal("UNIQUE (source, source_session_id) was lost")
	}
	if _, err := db.Exec(`INSERT INTO events (session_id, event_type, source_event_id, observation_level, payload_json, locator_json, adapter_version)
		VALUES ('opencode:missing','transcript_message','x','unknown','{}','{"source":"opencode","session_id":"x","line":1}','v1')`); err == nil {
		t.Fatal("the events foreign key was lost")
	}

	if _, err := db.Exec(`INSERT INTO events (session_id, event_type, source_event_id, observation_level, payload_json, locator_json, adapter_version)
		VALUES ('opencode:s1','transcript_message','x','unknown','{}','{"source":"opencode","session_id":"opencode:s1","line":1}','v1')`); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE id = 'opencode:s1'`); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	var orphans int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE session_id = 'opencode:s1'`).Scan(&orphans); err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("ON DELETE CASCADE was lost: %d orphan events", orphans)
	}

	wantIndexes := map[string]bool{
		"idx_sessions_title": false, "idx_sessions_started": false, "idx_sessions_cwd": false,
		"idx_sessions_source_started": false, "idx_sessions_parent": false, "idx_sessions_thread": false,
	}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='sessions'`)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		if _, ok := wantIndexes[name]; ok {
			wantIndexes[name] = true
		}
	}
	for name, found := range wantIndexes {
		if !found {
			t.Fatalf("index %s did not survive migration 012", name)
		}
	}

	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q", integrity)
	}
}

// The rebuild-free form of 012 is column-agnostic, so a later migration that
// adds a sessions column needs no change to it. This test states that as a
// contract: the column list must be identical either side of 012.
func TestOpenSourcesLeavesColumnsUntouched(t *testing.T) {
	base := filepath.Join(t.TempDir(), "before.db")
	beforeDB := applyThrough(t, base, openSourcesVersion-1)
	before := sessionColumns(t, beforeDB)
	beforeDB.Close()

	afterDB := applyRange(t, base, openSourcesVersion, openSourcesVersion)
	defer afterDB.Close()
	after := sessionColumns(t, afterDB)

	if len(before) != len(after) {
		t.Fatalf("sessions columns changed: %v -> %v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("sessions column %d changed: %q -> %q", i, before[i], after[i])
		}
	}
}

func sessionColumns(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info('sessions') ORDER BY cid`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		out = append(out, name)
	}
	return out
}
