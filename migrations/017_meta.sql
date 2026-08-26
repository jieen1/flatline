-- The data version has to survive a restart.
--
-- Every cacheable API response is keyed on a counter the daemon increments
-- when persisted data can have changed, and the browser keeps the response
-- under that key. The counter lived only in memory, so a restarted daemon
-- began again at 1 — and a browser holding version 1 from the previous process
-- was told its copy was current. The overview then showed 903 sessions while
-- the sidebar, fetched fresh, showed 1164: one page, two answers.
--
-- meta is the daemon's own small key/value record. data_version is read on
-- start and continues from where the last process left it, and every bump is
-- written back before it is published. The ETag additionally carries the
-- process boot time, so two processes can never mint the same tag even if the
-- counter were somehow reset.
--
-- Rollback note: meta holds no user record. Dropping it returns the counter to
-- its in-memory behaviour and, with it, the stale-cache problem above.

CREATE TABLE IF NOT EXISTS meta (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
