-- Signature watches: the user's statement "I wrote a rule for this friction
-- signature" plus the window the verification runs in. This is the write path
-- of the improvement loop (ADR-21) and the first user-intent table beside the
-- append-only fact layer: rows are created only through the explicit-confirm
-- endpoint and never deleted physically — a cancelled watch keeps its row with
-- status='cancelled' so the history of what was tried stays auditable.
--
-- Status is re-evaluated at read time from the fact layer; the stored columns
-- (baseline_*) only freeze what the counts were when the watch started, so the
-- before/after comparison cannot drift.
--
-- Rollback note: additive and self-contained. Dropping the table loses only
-- the watches themselves; no fact-layer table references it.

CREATE TABLE signature_watches (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    signature              TEXT NOT NULL,
    created_at             TEXT NOT NULL,
    window_days            INTEGER NOT NULL DEFAULT 14,
    baseline_count         INTEGER NOT NULL DEFAULT 0,
    baseline_session_count INTEGER NOT NULL DEFAULT 0,
    project_keys_json      TEXT NOT NULL DEFAULT '[]',
    status                 TEXT NOT NULL DEFAULT 'watching'
        CHECK (status IN ('watching', 'verified', 'no_change', 'unobservable', 'cancelled')),
    note                   TEXT,
    last_evaluated_at      TEXT,
    resolved_at            TEXT
);
CREATE INDEX idx_signature_watches_signature ON signature_watches (signature, status);
