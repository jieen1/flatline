-- Supersede channel for asset evidence.
--
-- The parser's path-reference rule changed: a bare basename is no longer
-- evidence that a transcript loaded an asset (one project writing
-- /tmp/x/taskboard/__init__.py was recorded as a load of another project's
-- .../hookify/hooks/__init__.py). Canonical events are append-only, so the
-- rows the old rule wrote cannot be deleted or edited — but they also cannot
-- be left standing beside the rows the new rule writes, or the same transcript
-- would carry two contradictory readings at once.
--
-- superseded_at is that third option: the row stays exactly as it was written,
-- and carries the time a later parse of the same source text failed to produce
-- it again. Nothing is deleted, no payload is rewritten, and the drill-down
-- still reaches the original source position.
--
-- The same column goes on the two derived tables the evidence produced.
-- participations and opportunities are inserted once and never removed, so a
-- superseded event would otherwise keep its participation alive. A
-- participation or opportunity is superseded only when every asset_invoked
-- event behind it, for that session and that asset, is superseded: one live
-- event is enough to keep the derived row standing.
--
-- Every participation, opportunity and asset statistic filters on
-- superseded_at IS NULL by default. A caller that wants the full history can
-- still read the rows; they were never removed.
--
-- Rollback note: clearing the column (UPDATE ... SET superseded_at = NULL)
-- restores the previous readings, which is why nothing is deleted here.

ALTER TABLE events ADD COLUMN superseded_at TEXT;
ALTER TABLE participations ADD COLUMN superseded_at TEXT;
ALTER TABLE opportunities ADD COLUMN superseded_at TEXT;

CREATE INDEX IF NOT EXISTS idx_events_asset_live
    ON events (asset_id, event_type, superseded_at);
