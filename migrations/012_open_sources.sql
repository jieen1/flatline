-- Open the session source enumeration.
--
-- sessions.source was pinned by CHECK (source IN ('claude_code','codex')).
-- Adding a harness must not need a schema change, so the CHECK is removed and
-- the allowed set moves to the Go adapter registry (adapters.Source.Valid).
--
-- Why this is not the 003 _vN rebuild. Every child of sessions declares
-- ON DELETE CASCADE, storage.Open holds foreign_keys(1), and SQLite's
-- DROP TABLE runs an implicit DELETE FROM that fires those cascades. The
-- documented rebuild starts with PRAGMA foreign_keys=OFF, which is a no-op
-- inside a transaction, and every migration here runs inside one. A rebuild
-- was tried and measured: it emptied events, session_stats, session_tags and
-- session_annotations. Removing a CHECK does not change the on-disk row
-- format, so the schema text is edited in place instead and not one row moves.
-- RESET rather than OFF because a plain OFF leaves this connection enforcing
-- the CHECK it already parsed.
--
-- This form is also column-agnostic: a later migration that adds a sessions
-- column needs no change here, where a _vN rebuild would silently drop it.
--
-- Rollback note: reversing this means putting the CHECK back, and the CHECK
-- cannot be satisfied while rows from other sources exist. Deleting them
-- cascades away all of their events. Rollback therefore discards the new
-- sources' data; it is not a lossless step.

PRAGMA writable_schema = ON;

UPDATE sqlite_master
SET sql = replace(sql, ' CHECK (source IN (''claude_code'', ''codex''))', '')
WHERE type = 'table' AND name = 'sessions';

PRAGMA writable_schema = RESET;

-- Fail loudly if the replace did not match: a silently unchanged CHECK would
-- look like an applied migration and then reject every new source at ingest.
CREATE TABLE migration_012_guard (ok INTEGER NOT NULL CHECK (ok = 1));
INSERT INTO migration_012_guard (ok)
SELECT CASE WHEN instr(sql, 'CHECK (source IN') = 0 THEN 1 ELSE 0 END
FROM sqlite_master WHERE type = 'table' AND name = 'sessions';
DROP TABLE migration_012_guard;
