-- Session measurement, versioned re-reading, and expected nonzero exits.
--
-- Three additions, all daemon-owned derived projections (ADR-10). Nothing here
-- holds a fact the local transcripts do not already record, and no source file
-- is read or written by this DDL.
--
-- 1. session_usage / session_model_usage are what a session cost and changed:
--    tokens, turns, edited lines, touched files, active time. Every measured
--    column is nullable and stays NULL when the source did not record it —
--    usage_source says which record it came from, and 'unrecorded' with NULL
--    columns is the honest answer, never zero (AGENTS.md §2.4).
--    Measurement happens per transcript file, and one Claude Code session can
--    be spread over a main transcript plus one file per subagent, so
--    native_file_usage holds what each file recorded and the two session
--    tables are the roll-up over the files of that session. Writing the
--    session row straight from one file would let the last subagent read
--    overwrite the whole session's tokens.
--
-- 2. native_files.parser_version stamps the parser that last read a file. When
--    the daemon's ParserVersion changes, files carrying an older stamp are read
--    once more and replayed through the normal ingest path: events are inserted
--    idempotently, so a newer parser can only add records an older one missed
--    (a Codex turn_aborted, a token_count) and never rewrites a stored event.
--    It supersedes pairing_version from migration 010, which is left in place
--    so a rollback to that daemon still finds its own stamp.
--
-- 3. session_commands.expected_exit and session_stats.expected_exit_count keep
--    a documented nonzero exit code out of the failure counts. `rg` exiting 1
--    means "nothing matched"; counting it as a failed command is wrong data.
--    The row is still stored — it happened — but it is marked so every failure
--    rate can leave it out and still say how many it left out.
--
-- Rollback note: drop session_usage and session_model_usage and clear
-- native_files.parser_version; both tables and both counters are recomputed in
-- full from the event store and the local transcripts. No user record is
-- involved.

ALTER TABLE native_files ADD COLUMN parser_version TEXT;

-- What one transcript file recorded. by_model_json is the per-file model split
-- the roll-up sums; it is a derived intermediate, not a fact store.
CREATE TABLE IF NOT EXISTS native_file_usage (
    path                TEXT PRIMARY KEY,
    session_id          TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    input_tokens        INTEGER,
    cached_input_tokens INTEGER,
    cache_write_tokens  INTEGER,
    output_tokens       INTEGER,
    reasoning_tokens    INTEGER,
    total_tokens        INTEGER,
    assistant_turns     INTEGER,
    user_turns          INTEGER,
    lines_added         INTEGER,
    lines_removed       INTEGER,
    files_changed       INTEGER,
    active_ms           INTEGER,
    context_window      INTEGER,
    usage_source        TEXT NOT NULL,
    by_model_json       TEXT,
    parser_version      TEXT NOT NULL,
    computed_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_native_file_usage_session ON native_file_usage (session_id);

CREATE TABLE IF NOT EXISTS session_usage (
    session_id          TEXT PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    input_tokens        INTEGER,
    cached_input_tokens INTEGER,
    cache_write_tokens  INTEGER,
    output_tokens       INTEGER,
    reasoning_tokens    INTEGER,
    total_tokens        INTEGER,
    assistant_turns     INTEGER,
    user_turns          INTEGER,
    lines_added         INTEGER,
    lines_removed       INTEGER,
    files_changed       INTEGER,
    -- active_ms sums the gaps between consecutive recorded events, counting a
    -- gap only while it stays under the idle bound. Wall-clock duration is
    -- already on session_stats; this is the part of it that has events in it.
    active_ms           INTEGER,
    context_window      INTEGER,
    -- Which record the token columns were read out of, or 'unrecorded'.
    usage_source        TEXT NOT NULL,
    parser_version      TEXT NOT NULL,
    computed_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_session_usage_total ON session_usage (total_tokens);
CREATE INDEX IF NOT EXISTS idx_session_usage_active ON session_usage (active_ms);
CREATE INDEX IF NOT EXISTS idx_session_usage_lines ON session_usage (lines_added, lines_removed);

CREATE TABLE IF NOT EXISTS session_model_usage (
    session_id    TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    model         TEXT    NOT NULL,
    turns         INTEGER NOT NULL,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    total_tokens  INTEGER,
    PRIMARY KEY (session_id, model)
);
CREATE INDEX IF NOT EXISTS idx_session_model_usage_model ON session_model_usage (model);

ALTER TABLE session_commands ADD COLUMN expected_exit INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_stats ADD COLUMN expected_exit_count INTEGER NOT NULL DEFAULT 0;
