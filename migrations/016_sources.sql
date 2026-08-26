-- Configured source roots, and the source each session came from.
--
-- Until now the roots the daemon reads were flags and built-in probes, and a
-- session recorded only which harness wrote it. That is enough for one machine
-- and not enough for two: a directory rsynced from another machine holds
-- Codex sessions that are indistinguishable, in the database, from the local
-- ones.
--
-- sources is the registry of the roots the daemon reads. The daemon writes one
-- row per root it was given or probed on start; the user names them from the
-- data page (label, machine_label) and can turn one off. root is the absolute
-- path (or, for a SQLite-backed source, the database file), which is what makes
-- two roots of the same kind distinct rows.
--
-- The registry is read-only over the source itself: nothing here gives the
-- daemon a reason to write into a source directory, and no code path does.
--
-- sessions.source_id is nullable on purpose: a session ingested before this
-- migration, or one whose file is no longer under any configured root, has no
-- source row. NULL means "not recorded", not "local".
--
-- Rollback note: sources is configuration the user typed, so exporting it
-- before dropping is the only lossy part; sessions.source_id is derived from
-- the path each transcript was read from and is rebuilt on the next pass.

CREATE TABLE IF NOT EXISTS sources (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    kind          TEXT    NOT NULL,
    root          TEXT    NOT NULL,
    label         TEXT,
    machine_label TEXT,
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (kind, root)
);

ALTER TABLE sessions ADD COLUMN source_id INTEGER REFERENCES sources (id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_source_id ON sessions (source_id);
