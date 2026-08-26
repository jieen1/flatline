-- flatline:no-transaction
-- One tool call can run more than one command.
--
-- Codex records an exec script as `const cmds = [[label, cmd, cwd], ...]`. The
-- projection only ever recorded the first tuple, because the table's unique key
-- was (session_id, event_id): a second command from the same call had nowhere
-- to go and was silently dropped. ordinal is that place — the position of the
-- command inside its own tool call — and the key becomes
-- (session_id, event_id, ordinal).
--
-- Why a rebuild and why outside the transaction: SQLite cannot alter a UNIQUE
-- constraint in place, and it cannot turn foreign keys off inside a
-- transaction. This daemon runs with them on, and every child of sessions
-- cascades on delete, so a DROP inside a transaction would take rows with it.
-- The runner therefore executes a migration whose first line is
-- `-- flatline:no-transaction` on its own connection with foreign keys off, and
-- records it only after PRAGMA foreign_key_check comes back empty (§20.9).
--
-- Existing rows keep their identity: every one of them was the first command of
-- its call, so they all carry ordinal 0 and the id they already had.
-- session_stats.command_count is a COUNT over this table and follows on the
-- next projection pass, which the bumped ProjectionVersion triggers.
--
-- Rollback note: the reverse rebuild drops ordinal, and with it every command
-- past the first of a call. Those rows are recomputable from the events, so the
-- loss is a projection, not a record.

CREATE TABLE session_commands_v2 (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id    TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    event_id      INTEGER NOT NULL,
    ordinal       INTEGER NOT NULL DEFAULT 0,
    tool_name     TEXT NOT NULL,
    program       TEXT,
    command       TEXT NOT NULL,
    exit_code     INTEGER,
    is_error      INTEGER,
    expected_exit INTEGER NOT NULL DEFAULT 0,
    occurred_at   TEXT,
    UNIQUE (session_id, event_id, ordinal)
);

INSERT INTO session_commands_v2
    (id, session_id, event_id, ordinal, tool_name, program, command, exit_code, is_error, expected_exit, occurred_at)
SELECT id, session_id, event_id, 0, tool_name, program, command, exit_code, is_error, expected_exit, occurred_at
FROM session_commands;

DROP TABLE session_commands;
ALTER TABLE session_commands_v2 RENAME TO session_commands;

CREATE INDEX idx_session_commands_program ON session_commands (program, occurred_at);
CREATE INDEX idx_session_commands_session ON session_commands (session_id, id);
CREATE INDEX idx_session_commands_program_outcome_v2
    ON session_commands (program, session_id, exit_code, is_error, expected_exit);
