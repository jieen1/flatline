-- The friction record that names a hook, and the hook asset it names.
--
-- A hook block written into a transcript ("Command blocked by PreToolUse
-- hook: …") is the one place a hook leaves a mark in a session: the harness
-- only writes that line after asking the hook and getting an answer back. Up
-- to now that mark went nowhere — the hook assets on the wall had no recorded
-- participation of any kind, because nothing reads a hook the way a Read of a
-- SKILL.md reads a skill.
--
-- The rule is one sentence: a hook block recorded in a session means that hook
-- took part in that session. It applies only when the recorded message names
-- the hook — by the full path of its script, or by a file name that exactly
-- one registered hook is called. A message that names no hook (most of them do
-- not; they carry the hook's own text and nothing else) produces no row here.
-- Nothing is inferred from the wording.
--
-- This table is the drill-down: it says which friction record was read as
-- evidence for which hook asset, and under which rule. The participation the
-- link produces is written into participations with
-- participation_signal = 'observed-use' and observation_level = 'observed-use',
-- the same as any other observed use, so no page has to special-case it.
--
-- It is a derived projection (ADR-10): the rows for one session are rewritten
-- from that session's friction records every time it is re-read, and deleting
-- the whole table costs nothing but a re-read.
--
-- Rollback note: derived. Drop the table and the next parser pass rebuilds it.

CREATE TABLE IF NOT EXISTS asset_friction_links (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id    TEXT    NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    friction_id INTEGER NOT NULL REFERENCES friction_records (id) ON DELETE CASCADE,
    rule        TEXT    NOT NULL,
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (asset_id, friction_id)
);

CREATE INDEX IF NOT EXISTS idx_asset_friction_links_asset ON asset_friction_links (asset_id);
CREATE INDEX IF NOT EXISTS idx_asset_friction_links_friction ON asset_friction_links (friction_id);
