-- Tool call/result pairing and the tool usage projection.
--
-- ADR-10 / ADR-17: everything here is a daemon-owned derived projection of the
-- append-only event store plus one read-only re-read of the native transcript.
-- No canonical event is rewritten and no source file is written.
--
-- event_pairs answers "which tool call produced this tool result" for the
-- histories whose recorded ids do not line up: an older parser wrote Codex's
-- response-item id into turn_id, so a function_call_output (call_… / fco_… /
-- ctco_…) carries a different id from its function_call (fc_… / ctc_…). Events
-- are append-only, so the link is recorded beside them instead.
--
-- pair_source names how the link was established, so a reader can tell an
-- id match from a re-read of the source file:
--   'id'      the two events already carry the same tool_use_id / call_id.
--   'reparse' the two ids differ; the pair was read out of the native
--             transcript again (read-only) and mapped back through the
--             locator raw_ref both events already carry.
--
-- Rollback note: drop event_pairs and tool_call_stats and clear
-- native_files.pairing_version. Both tables are fully recomputable.

CREATE TABLE IF NOT EXISTS event_pairs (
    session_id      TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    result_event_id INTEGER NOT NULL,
    call_event_id   INTEGER NOT NULL,
    tool_name       TEXT,
    pair_source     TEXT    NOT NULL CHECK (pair_source IN ('id', 'reparse')),
    PRIMARY KEY (session_id, result_event_id)
);
CREATE INDEX IF NOT EXISTS idx_event_pairs_call ON event_pairs (session_id, call_event_id);

-- Which pairing pass has already re-read this transcript. NULL means never;
-- a value different from the daemon's PairingVersion means a newer pass has
-- to read it once more.
ALTER TABLE native_files ADD COLUMN pairing_version TEXT;

-- Per-session tool usage, so /tools does not scan every tool_call payload.
-- known_outcomes is the denominator failures is counted out of: a call whose
-- result the source never recorded is neither a success nor a failure.
CREATE TABLE IF NOT EXISTS tool_call_stats (
    session_id     TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    tool_name      TEXT    NOT NULL,
    harness        TEXT    NOT NULL,
    calls          INTEGER NOT NULL,
    known_outcomes INTEGER NOT NULL,
    failures       INTEGER NOT NULL,
    PRIMARY KEY (session_id, tool_name)
);
CREATE INDEX IF NOT EXISTS idx_tool_call_stats_tool ON tool_call_stats (tool_name);

-- The other half of /tools is the program list, which stays on
-- session_commands (§10.2). Grouping it needs the program, the session and the
-- outcome; with all four in one index the aggregate never touches the table.
CREATE INDEX IF NOT EXISTS idx_session_commands_program_outcome
    ON session_commands (program, session_id, exit_code, is_error);
