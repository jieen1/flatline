-- Native session identity and transcript projection.
--
-- These columns are source-backed display metadata only. They never classify a
-- task or create an opportunity; missing values remain NULL. Transcript rows
-- continue to live in the append-only events table.
-- Rollback note: export values first, then DROP the two columns in a planned
-- SQLite rebuild. No source history or asset file is modified by this DDL.

ALTER TABLE sessions ADD COLUMN title TEXT;
ALTER TABLE sessions ADD COLUMN task_text TEXT;

CREATE INDEX IF NOT EXISTS idx_sessions_title ON sessions (title);
