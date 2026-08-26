-- flatline:tolerate-existing
-- Columns and one index added after 011 was already applied somewhere.
--
-- A migration is append-only once any database has recorded it: an edited file
-- is never re-run, so the column it gained exists only in the databases that
-- were created afterwards. These three additions are therefore split out here,
-- and this file declares itself tolerant of a column that is already present,
-- because the databases built from the intermediate 011 already have them.
--
-- 1. session_usage.cost / native_file_usage.cost: what the source itself says
--    a session cost, in its own currency unit. Only opencode records one; NULL
--    everywhere else means "this source does not record a cost", not zero.
--
-- 2. session_stats.projection_version: which projection pass last rebuilt this
--    session's commands, files, tool counts and event counts. projected_at
--    only says "it has been projected once"; the version is what makes a rule
--    change — a new tool-name match, a new failure rule, a new counting
--    rule — reach the sessions that were projected under the old rules.
--
-- 3. The program aggregate counts calls, known outcomes, failures and expected
--    exits per program in one pass; with every column it reads in one index it
--    never touches the table.
--
-- Rollback note: all three are derived or defaulted. Drop the index and leave
-- the columns; nothing reads them when the daemon is rolled back.

ALTER TABLE native_file_usage ADD COLUMN cost REAL;
ALTER TABLE session_usage ADD COLUMN cost REAL;
ALTER TABLE session_stats ADD COLUMN projection_version TEXT;

DROP INDEX IF EXISTS idx_session_commands_expected;
CREATE INDEX IF NOT EXISTS idx_session_commands_program_outcome_v2
    ON session_commands (program, session_id, exit_code, is_error, expected_exit);
