-- Session-first management layer.
--
-- Four daemon-owned tables plus two search indexes. Everything here is either
-- a persisted read cache of local observation (native_files), a recomputable
-- projection of the append-only event store (session_stats, sessions_fts,
-- events_fts), or a local-only user record (session_tags kind='user',
-- session_annotations). No source file is read or written by this DDL.
--
-- Rollback note: export session_annotations and the kind='user' rows of
-- session_tags first; they are the only rows here that cannot be recomputed
-- from the canonical fact layer. Then drop in any order:
--   events_fts, sessions_fts, session_annotations, session_tags,
--   session_stats, native_files, and the indexes added below.

-- Native transcript fingerprints, so a daemon restart re-reads only files
-- whose size or mtime changed. session_id is nullable: a file can be seen
-- before it parses into a session.
CREATE TABLE IF NOT EXISTS native_files (
    path          TEXT PRIMARY KEY,
    size          INTEGER NOT NULL,
    mtime_ns      INTEGER NOT NULL,
    session_id    TEXT,
    last_read_at  TEXT NOT NULL
);

-- Session-level projection. Fully recomputable from events / friction_records
-- (ADR-10); duration_ms is NULL whenever either bound is not recorded.
CREATE TABLE IF NOT EXISTS session_stats (
    session_id         TEXT PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    event_count        INTEGER NOT NULL,
    transcript_count   INTEGER NOT NULL,
    message_count      INTEGER NOT NULL,
    user_message_count INTEGER NOT NULL,
    tool_call_count    INTEGER NOT NULL,
    tool_result_count  INTEGER NOT NULL,
    friction_count     INTEGER NOT NULL,
    tool_error_count   INTEGER NOT NULL,
    nonzero_exit_count INTEGER NOT NULL,
    asset_count        INTEGER NOT NULL,
    first_event_at     TEXT,
    last_event_at      TEXT,
    duration_ms        INTEGER,
    computed_at        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS session_tags (
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    tag        TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('task', 'workspace', 'user')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (session_id, tag, kind)
);
CREATE INDEX IF NOT EXISTS idx_session_tags_tag ON session_tags (tag, kind);

CREATE TABLE IF NOT EXISTS session_annotations (
    session_id TEXT PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    pinned     INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
    note       TEXT,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_type_session ON events (event_type, session_id);
CREATE INDEX IF NOT EXISTS idx_events_occurred ON events (occurred_at);
-- The daily activity aggregates group on the date prefix; a plain occurred_at
-- index cannot serve that expression.
CREATE INDEX IF NOT EXISTS idx_events_day ON events (substr(occurred_at, 1, 10));
CREATE INDEX IF NOT EXISTS idx_sessions_started ON sessions (started_at);
CREATE INDEX IF NOT EXISTS idx_sessions_cwd ON sessions (cwd);
CREATE INDEX IF NOT EXISTS idx_sessions_source_started ON sessions (source, started_at);
CREATE INDEX IF NOT EXISTS idx_friction_records_occurred ON friction_records (occurred_at);

-- Trigram so a Chinese or English substring matches without a word segmenter.
CREATE VIRTUAL TABLE sessions_fts USING fts5(
    session_id UNINDEXED, title, task_text, cwd, model, source_session_id,
    tokenize = 'trigram'
);

-- Contentless: rowid is events.id, so a hit points straight back at the
-- canonical event. Events are append-only, so no delete path is needed.
CREATE VIRTUAL TABLE events_fts USING fts5(
    text, content = '', tokenize = 'trigram'
);

INSERT INTO sessions_fts (session_id, title, task_text, cwd, model, source_session_id)
SELECT id, COALESCE(title, ''), COALESCE(task_text, ''), COALESCE(cwd, ''),
       COALESCE(model, ''), source_session_id
FROM sessions;

INSERT INTO events_fts (rowid, text)
SELECT id, json_extract(payload_json, '$.text')
FROM events
WHERE event_type = 'transcript_message'
  AND json_extract(payload_json, '$.text') IS NOT NULL;

INSERT INTO session_stats (
    session_id, event_count, transcript_count, message_count, user_message_count,
    tool_call_count, tool_result_count, friction_count, tool_error_count,
    nonzero_exit_count, asset_count, first_event_at, last_event_at, duration_ms, computed_at)
SELECT s.id,
       COALESCE(e.event_count, 0),
       COALESCE(e.transcript_count, 0),
       COALESCE(e.message_count, 0),
       COALESCE(e.user_message_count, 0),
       COALESCE(e.tool_call_count, 0),
       COALESCE(e.tool_result_count, 0),
       COALESCE(f.friction_count, 0) + COALESCE(e.violation_count, 0),
       COALESCE(f.tool_error_count, 0),
       COALESCE(f.nonzero_exit_count, 0),
       COALESCE(e.asset_count, 0),
       e.first_event_at,
       e.last_event_at,
       CASE WHEN s.started_at IS NULL OR s.ended_at IS NULL THEN NULL
            ELSE CAST(ROUND((julianday(s.ended_at) - julianday(s.started_at)) * 86400000.0) AS INTEGER) END,
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM sessions s
LEFT JOIN (
    SELECT session_id,
           COUNT(*) AS event_count,
           SUM(CASE WHEN event_type IN ('transcript_message', 'transcript_tool_call', 'transcript_tool_result') THEN 1 ELSE 0 END) AS transcript_count,
           SUM(CASE WHEN event_type = 'transcript_message' THEN 1 ELSE 0 END) AS message_count,
           SUM(CASE WHEN event_type = 'transcript_message' AND json_extract(payload_json, '$.role') = 'user' THEN 1 ELSE 0 END) AS user_message_count,
           SUM(CASE WHEN event_type = 'transcript_tool_call' THEN 1 ELSE 0 END) AS tool_call_count,
           SUM(CASE WHEN event_type = 'transcript_tool_result' THEN 1 ELSE 0 END) AS tool_result_count,
           SUM(CASE WHEN event_type = 'asset_violation' THEN 1 ELSE 0 END) AS violation_count,
           COUNT(DISTINCT asset_id) AS asset_count,
           MIN(NULLIF(occurred_at, '')) AS first_event_at,
           MAX(NULLIF(occurred_at, '')) AS last_event_at
    FROM events GROUP BY session_id
) e ON e.session_id = s.id
LEFT JOIN (
    SELECT session_id,
           COUNT(DISTINCT source_event_id) AS friction_count,
           SUM(CASE WHEN is_error = 1 THEN 1 ELSE 0 END) AS tool_error_count,
           SUM(CASE WHEN exit_code IS NOT NULL AND exit_code <> 0 THEN 1 ELSE 0 END) AS nonzero_exit_count
    FROM friction_records GROUP BY session_id
) f ON f.session_id = s.id;
