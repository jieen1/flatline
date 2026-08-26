-- A session's span only ever grows.
--
-- sessions.started_at / ended_at were written with COALESCE, so the first
-- import of a transcript that was still being written froze the end of the
-- session at that moment. Every measurement taken from a later read of the same
-- file kept growing past it: one local session reported 50 minutes of active
-- time inside a 3m40s session, and 15 sessions had active_ms > duration_ms.
--
-- The upsert now keeps the earliest start and the latest end. This repairs the
-- rows written under the old rule, from a fact the event store already holds:
-- session_stats.last_event_at is the last record stored for the session, which
-- is the same reading ended_at is taken from. No source file is opened.
--
-- duration_ms is a function of the two bounds, so it is recomputed for exactly
-- the rows that moved.
--
-- Rollback note: both columns are read from the source text on every ingest and
-- both are derived facts. There is nothing to restore.

UPDATE sessions
SET ended_at = (SELECT st.last_event_at FROM session_stats st WHERE st.session_id = sessions.id)
WHERE EXISTS (
    SELECT 1 FROM session_stats st
    WHERE st.session_id = sessions.id
      AND st.last_event_at IS NOT NULL AND st.last_event_at <> ''
      AND (sessions.ended_at IS NULL
           OR julianday(st.last_event_at) > julianday(sessions.ended_at)));

UPDATE sessions
SET started_at = (SELECT st.first_event_at FROM session_stats st WHERE st.session_id = sessions.id)
WHERE EXISTS (
    SELECT 1 FROM session_stats st
    WHERE st.session_id = sessions.id
      AND st.first_event_at IS NOT NULL AND st.first_event_at <> ''
      AND (sessions.started_at IS NULL
           OR julianday(st.first_event_at) < julianday(sessions.started_at)));

UPDATE session_stats
SET duration_ms = (
    SELECT CASE WHEN s.started_at IS NULL OR s.ended_at IS NULL THEN NULL
                ELSE CAST(ROUND((julianday(s.ended_at) - julianday(s.started_at)) * 86400000.0) AS INTEGER) END
    FROM sessions s WHERE s.id = session_stats.session_id);
