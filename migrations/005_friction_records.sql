-- Native tool outcome projection.
--
-- This table is a daemon-owned, idempotent projection of explicit tool
-- failures. The canonical events table remains append-only; replaying a
-- source file can populate this projection even when the corresponding
-- canonical event was written by an older parser version.
--
-- Rollback note: export this projection before dropping it. It can be
-- rebuilt from native history, but the source history itself is never edited.

CREATE TABLE IF NOT EXISTS friction_records (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    source_event_id    TEXT    NOT NULL,
    friction_kind      TEXT    NOT NULL CHECK (friction_kind IN ('tool_error')),
    event_type         TEXT    NOT NULL CHECK (event_type = 'transcript_tool_result'),
    observation_level  TEXT    NOT NULL CHECK (observation_level IN (
        'invoked', 'observed-use', 'loaded', 'offered', 'inferred', 'unknown'
    )),
    is_error           INTEGER CHECK (is_error IS NULL OR is_error IN (0, 1)),
    exit_code          INTEGER,
    payload_json       TEXT    NOT NULL,
    locator_json       TEXT    NOT NULL,
    occurred_at        TEXT,
    created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, source_event_id, friction_kind)
);

CREATE INDEX IF NOT EXISTS idx_friction_records_session_time
    ON friction_records (session_id, occurred_at, id);
