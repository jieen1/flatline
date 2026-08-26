-- Session hierarchy and the command/file projections.
--
-- Two additions. First, sessions gain the thread facts the sources already
-- record: a Codex subagent thread names its parent thread, its role and its
-- nickname in session_meta, and every source names the program that started
-- it. thread_kind stays NULL when a session was ingested before this migration
-- and its transcript can no longer be re-read: NULL means "not recorded", not
-- "main".
--
-- Second, session_commands and session_files are daemon-owned projections
-- (ADR-10) of the bounded tool payloads already stored in events. They hold no
-- fact that is not already in the event store and can be dropped and rebuilt
-- from it at any time.
--
-- Rollback note: nothing here is a user record. Drop session_files,
-- session_commands and the indexes; the sessions and session_stats columns can
-- be left in place (they are nullable or defaulted) or the tables rebuilt
-- without them. No source file is read or written by this DDL.

ALTER TABLE sessions ADD COLUMN parent_session_id TEXT;
ALTER TABLE sessions ADD COLUMN thread_kind TEXT;
ALTER TABLE sessions ADD COLUMN agent_role TEXT;
ALTER TABLE sessions ADD COLUMN agent_nickname TEXT;
ALTER TABLE sessions ADD COLUMN originator TEXT;
CREATE INDEX idx_sessions_parent ON sessions (parent_session_id);
CREATE INDEX idx_sessions_thread ON sessions (thread_kind, started_at);

ALTER TABLE session_stats ADD COLUMN subagent_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_stats ADD COLUMN command_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_stats ADD COLUMN failed_command_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_stats ADD COLUMN file_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_stats ADD COLUMN is_empty INTEGER NOT NULL DEFAULT 0;
-- projected_at records that the command/file projection has run for this
-- session. It is what separates "projected and found nothing" from "never
-- projected", so a restart backfills only the sessions that still need it.
ALTER TABLE session_stats ADD COLUMN projected_at TEXT;

CREATE TABLE session_commands (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    event_id    INTEGER NOT NULL,
    tool_name   TEXT NOT NULL,
    program     TEXT,
    command     TEXT NOT NULL,
    exit_code   INTEGER,
    is_error    INTEGER,
    occurred_at TEXT,
    UNIQUE (session_id, event_id)
);
CREATE INDEX idx_session_commands_program ON session_commands (program, occurred_at);
CREATE INDEX idx_session_commands_session ON session_commands (session_id, id);

CREATE TABLE session_files (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    event_id    INTEGER NOT NULL,
    path        TEXT NOT NULL,
    action      TEXT NOT NULL CHECK (action IN ('read', 'edit', 'write', 'delete')),
    tool_name   TEXT NOT NULL,
    occurred_at TEXT,
    UNIQUE (session_id, event_id, path, action)
);
CREATE INDEX idx_session_files_path ON session_files (path, occurred_at);
CREATE INDEX idx_session_files_session ON session_files (session_id, id);

-- The session list hides empty sessions by default, so the flag is a filter
-- column on its own.
CREATE INDEX idx_session_stats_empty ON session_stats (is_empty);

-- is_empty is a function of counts this table already holds, so it can be
-- filled here instead of waiting for the first projection pass.
UPDATE session_stats
SET is_empty = CASE
    WHEN transcript_count = 0 OR (user_message_count = 0 AND tool_call_count = 0) THEN 1
    ELSE 0 END;
