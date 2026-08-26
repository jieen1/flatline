-- Friction categories and tool identity.
--
-- friction_records gains a closed set of rule-produced categories, the
-- one-line rule that matched, the resolved human-readable tool name, and the
-- classifier version that produced them. SQLite cannot alter a CHECK
-- constraint, so the table is rebuilt (the _v3 pattern of migration 003) and
-- every existing row is preserved. Category columns stay NULL for carried-over
-- rows: NULL means "not classified yet", never "no category". The daemon
-- reclassifies every row whose classifier_version differs from the current one.
--
-- Rollback note: export friction_records before reverting. Recreate the
-- 005 table shape and copy back the columns it knows; the category columns are
-- derived and can be rebuilt from native history. The source history and the
-- canonical events table are never edited by this migration.

CREATE TABLE friction_records_v2 (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    source_event_id    TEXT    NOT NULL,
    friction_kind      TEXT    NOT NULL CHECK (friction_kind IN ('tool_error', 'user_interrupt')),
    event_type         TEXT    NOT NULL CHECK (event_type IN ('transcript_tool_result', 'transcript_message')),
    observation_level  TEXT    NOT NULL CHECK (observation_level IN (
        'invoked', 'observed-use', 'loaded', 'offered', 'inferred', 'unknown'
    )),
    tool_name          TEXT,
    category           TEXT,
    category_rule      TEXT,
    classifier_version TEXT,
    is_error           INTEGER CHECK (is_error IS NULL OR is_error IN (0, 1)),
    exit_code          INTEGER,
    payload_json       TEXT    NOT NULL,
    locator_json       TEXT    NOT NULL,
    occurred_at        TEXT,
    created_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (session_id, source_event_id, friction_kind)
);

INSERT INTO friction_records_v2
    (id, session_id, source_event_id, friction_kind, event_type, observation_level,
     is_error, exit_code, payload_json, locator_json, occurred_at, created_at)
SELECT id, session_id, source_event_id, friction_kind, event_type, observation_level,
       is_error, exit_code, payload_json, locator_json, occurred_at, created_at
FROM friction_records;

DROP TABLE friction_records;
ALTER TABLE friction_records_v2 RENAME TO friction_records;

CREATE INDEX idx_friction_records_session_time ON friction_records (session_id, occurred_at, id);
CREATE INDEX idx_friction_records_occurred ON friction_records (occurred_at);
CREATE INDEX idx_friction_records_category ON friction_records (category);
CREATE INDEX idx_friction_records_tool ON friction_records (tool_name);
CREATE INDEX idx_friction_records_session_kind ON friction_records (session_id, friction_kind);
