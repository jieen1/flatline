-- A worktree is not a project.
--
-- Claude Code's EnterWorktree runs a session from
-- <repo>/.claude/worktrees/<name>, and the working directory is what the
-- project dimension was grouped by, so ten throwaway worktrees showed up as ten
-- projects beside the repository they belong to.
--
-- project_key is the grouping key: the recorded cwd, with a
-- /.claude/worktrees/<name> suffix folded back onto the repository that owns
-- it. worktree keeps the name that was folded away, so the session still says
-- where it ran and the project page can count how many of its sessions were
-- worktree sessions.
--
-- Only this one shape is folded. It is written by the harness and is therefore
-- recognisable with certainty. A directory that merely looks like a git
-- worktree by name (qwen-sm120-runtime-wt-deps, qsr-w-b1) is left exactly where
-- it is: guessing at a naming convention would silently merge two real
-- projects, and Flatline does not guess.
--
-- project_key is NULL only when the source recorded no working directory. The
-- API keeps reporting those under the explicit __unrecorded__ bucket.
--
-- Rollback note: both columns are derived from cwd, which is untouched. Drop
-- the index and leave the columns; nothing reads them when rolled back.

ALTER TABLE sessions ADD COLUMN project_key TEXT;
ALTER TABLE sessions ADD COLUMN worktree TEXT;

UPDATE sessions
SET worktree = NULLIF(
        CASE WHEN instr(substr(cwd, instr(cwd, '/.claude/worktrees/') + 19), '/') > 0
             THEN substr(substr(cwd, instr(cwd, '/.claude/worktrees/') + 19), 1,
                         instr(substr(cwd, instr(cwd, '/.claude/worktrees/') + 19), '/') - 1)
             ELSE substr(cwd, instr(cwd, '/.claude/worktrees/') + 19)
        END, ''),
    project_key = NULLIF(substr(cwd, 1, instr(cwd, '/.claude/worktrees/') - 1), '')
WHERE cwd IS NOT NULL AND instr(cwd, '/.claude/worktrees/') > 0;

UPDATE sessions SET project_key = NULLIF(TRIM(cwd), '') WHERE project_key IS NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_project_key ON sessions (project_key, started_at);
