-- 001_initial.sql
-- Flatline v0.4 canonical schema (system design v0.4 §7, ADR-3).
--
-- Conventions:
--   * All timestamps are UTC ISO-8601 text (YYYY-MM-DDTHH:MM:SS.sssZ).
--   * `observation_level` is the closed canonical enum (design §3.1):
--       invoked / observed-use / loaded / offered / inferred / unknown
--     Adapters MUST NOT introduce new values; `unknown` is never treated as zero.
--   * `participation_signal` (what happened) is orthogonal to observation_level
--     (how we know it): offered / loaded / invoked / observed-use / followed.
--     `followed` is a participation form, never an observation level.
--   * `events` is append-only: the storage layer exposes no UPDATE/DELETE path.
--   * Derived rows (vital_states, state_transitions) carry replay metadata
--     (detector_version / schema_version / threshold_version) so the full state
--     history can be cheaply recomputed after threshold or detector upgrades
--     (ADR-10).
--   * `decision_tasks` and the `improvement_cycles` family are intentionally
--     absent (design §4.6 / Appendix B: not implemented in MVP).
--
-- Rollback note: this migration is additive (CREATE TABLE IF NOT EXISTS only).
-- To roll back, drop the tables in reverse dependency order:
--   reference_check_items, reference_checks, dispositions, state_transitions,
--   vital_states, participations, opportunities, effective_bundles, events,
--   asset_versions, sessions, assets, schema_migrations.
-- No data is mutated or deleted by this migration.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version     INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL,
    applied_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- ---------------------------------------------------------------------------
-- Fact layer (canonical, append-only where noted)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS assets (
    id            TEXT    PRIMARY KEY,              -- stable id, e.g. 'skill:user:sql-migrations'
    kind          TEXT    NOT NULL CHECK (kind IN ('skill', 'agents_md', 'rule', 'hook')),
    name          TEXT    NOT NULL,
    scope         TEXT    NOT NULL DEFAULT 'user' CHECK (scope IN ('user', 'project')),
    source_path   TEXT,
    description   TEXT,
    first_seen_at TEXT    NOT NULL,
    last_seen_at  TEXT,
    archived_at   TEXT,                              -- set when the user archives (stops monitoring)
    UNIQUE (kind, scope, name)
);

CREATE TABLE IF NOT EXISTS asset_versions (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id         TEXT    NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    version          INTEGER NOT NULL CHECK (version > 0),
    content_hash     TEXT    NOT NULL,
    content_ref      TEXT,                           -- locator to the stored snapshot content
    observation_level TEXT   NOT NULL CHECK (observation_level IN (
        'invoked', 'observed-use', 'loaded', 'offered', 'inferred', 'unknown'
    )),
    observed_at      TEXT    NOT NULL,
    UNIQUE (asset_id, version)
);
CREATE INDEX IF NOT EXISTS idx_asset_versions_asset ON asset_versions (asset_id, version);

CREATE TABLE IF NOT EXISTS sessions (
    id                TEXT    PRIMARY KEY,           -- source-qualified, e.g. 'claude_code:<uuid>'
    source            TEXT    NOT NULL CHECK (source IN ('claude_code', 'codex')),
    source_session_id TEXT    NOT NULL,
    started_at        TEXT,
    ended_at          TEXT,
    harness_version   TEXT,                          -- EnvironmentChanged anchor input
    model             TEXT,
    cwd               TEXT,
    UNIQUE (source, source_session_id)
);

-- Canonical Event Store: append-only, every row carries a locator so any
-- derived claim can be drilled down to the original source position.
CREATE TABLE IF NOT EXISTS events (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id          TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    event_type          TEXT    NOT NULL,
    asset_id            TEXT    REFERENCES assets (id) ON DELETE SET NULL,
    asset_version_id    INTEGER REFERENCES asset_versions (id) ON DELETE SET NULL,
    source_event_id     TEXT,                           -- adapter-provided idempotency key
    participation_signal TEXT   CHECK (participation_signal IS NULL OR participation_signal IN (
        'offered', 'loaded', 'invoked', 'observed-use', 'followed'
    )),
    observation_level   TEXT    NOT NULL CHECK (observation_level IN (
        'invoked', 'observed-use', 'loaded', 'offered', 'inferred', 'unknown'
    )),
    payload_json        TEXT,
    locator_json        TEXT    NOT NULL,            -- where in the source this event came from
    occurred_at         TEXT,
    ingested_at         TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    adapter_version     TEXT                             -- source adapter version (replay metadata)
);
CREATE INDEX IF NOT EXISTS idx_events_session ON events (session_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_events_asset ON events (asset_id, occurred_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_events_source_idempotency
    ON events (session_id, source_event_id)
    WHERE source_event_id IS NOT NULL;

-- Effective Bundle Resolver output: the asset version vector in force for a
-- session, so any session can answer "which version was effective then".
CREATE TABLE IF NOT EXISTS effective_bundles (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id       TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    bundle_json      TEXT    NOT NULL,               -- {asset_id: asset_version_id, ...}
    resolver_version TEXT    NOT NULL,
    resolved_at      TEXT    NOT NULL,
    UNIQUE (session_id)
);

-- ---------------------------------------------------------------------------
-- Derived layer (recomputable; carries replay metadata)
-- ---------------------------------------------------------------------------

-- Opportunity: (session, task-shape class, asset set). Silence is only judged
-- relative to opportunities — no opportunity is not failure.
CREATE TABLE IF NOT EXISTS opportunities (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    shape_class        TEXT    NOT NULL,
    shape_rule_version TEXT    NOT NULL,
    asset_id           TEXT    NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    detector_version   TEXT    NOT NULL,
    detected_at        TEXT    NOT NULL,
    UNIQUE (session_id, shape_class, asset_id)
);
CREATE INDEX IF NOT EXISTS idx_opportunities_asset ON opportunities (asset_id, detected_at);

-- Participation: (asset_version, session, participation form, observation level).
CREATE TABLE IF NOT EXISTS participations (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_version_id     INTEGER NOT NULL REFERENCES asset_versions (id) ON DELETE CASCADE,
    session_id           TEXT    NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    opportunity_id       INTEGER REFERENCES opportunities (id) ON DELETE SET NULL,
    participation_signal TEXT    NOT NULL CHECK (participation_signal IN (
        'offered', 'loaded', 'invoked', 'observed-use', 'followed'
    )),
    observation_level    TEXT    NOT NULL CHECK (observation_level IN (
        'invoked', 'observed-use', 'loaded', 'offered', 'inferred', 'unknown'
    )),
    occurred_at          TEXT,
    locator_json         TEXT,
    UNIQUE (asset_version_id, session_id, participation_signal)
);
CREATE INDEX IF NOT EXISTS idx_participations_session ON participations (session_id, occurred_at);

-- VitalState: one row per state instance. `broken` is an overlay dimension
-- (content validity is orthogonal to participation state; UI shows broken first),
-- so a row may carry broken_overlay = 1 on top of its primary state.
CREATE TABLE IF NOT EXISTS vital_states (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id         TEXT    NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    state            TEXT    NOT NULL CHECK (state IN (
        'healthy', 'degraded', 'silent', 'broken', 'bypassed', 'dormant',
        'no_opportunity', 'unobservable', 'awaiting_resurrection', 'archived'
    )),
    broken_overlay   INTEGER NOT NULL DEFAULT 0 CHECK (broken_overlay IN (0, 1)),
    evidence_json    TEXT    NOT NULL,               -- numerator/denominator/baseline/thresholds snapshot
    baseline_json    TEXT,
    detector_version TEXT    NOT NULL,               -- replay metadata (ADR-10)
    schema_version   TEXT    NOT NULL,
    threshold_version TEXT   NOT NULL,
    started_at       TEXT    NOT NULL,
    ended_at         TEXT,
    UNIQUE (asset_id, started_at)
);
CREATE INDEX IF NOT EXISTS idx_vital_states_asset ON vital_states (asset_id, started_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vital_states_one_open
    ON vital_states (asset_id)
    WHERE ended_at IS NULL;

-- StateTransition: one row per transition; its id doubles as the state-instance
-- id that dispositions are scoped to (design §4.3: an ignore applies to the
-- current state instance only; re-entering the same state later is a new
-- instance and alerts again).
CREATE TABLE IF NOT EXISTS state_transitions (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id         TEXT    NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    from_state       TEXT    CHECK (from_state IS NULL OR from_state IN (
        'healthy', 'degraded', 'silent', 'broken', 'bypassed', 'dormant',
        'no_opportunity', 'unobservable', 'awaiting_resurrection', 'archived'
    )),
    to_state         TEXT    NOT NULL CHECK (to_state IN (
        'healthy', 'degraded', 'silent', 'broken', 'bypassed', 'dormant',
        'no_opportunity', 'unobservable', 'awaiting_resurrection', 'archived'
    )),
    broken_overlay   INTEGER NOT NULL DEFAULT 0 CHECK (broken_overlay IN (0, 1)),
    occurred_at      TEXT    NOT NULL,
    evidence_json    TEXT    NOT NULL,               -- verdict basis, one-line explainable (ADR-8)
    alignment_json   TEXT,                           -- ±3d env/asset change alignment list (alignment, never causation)
    detector_version TEXT    NOT NULL,               -- replay metadata (ADR-10)
    schema_version   TEXT    NOT NULL,
    threshold_version TEXT   NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_state_transitions_asset ON state_transitions (asset_id, occurred_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_state_transitions_instance_asset
    ON state_transitions (id, asset_id);

-- Disposition: user's response to an alert (modify / prune / archive / ignore)
-- scoped to a state instance. Prune/archive must carry a rollback record.
CREATE TABLE IF NOT EXISTS dispositions (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id          TEXT    NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    state_instance_id INTEGER NOT NULL,
    action            TEXT    NOT NULL CHECK (action IN ('modify', 'prune', 'archive', 'ignore')),
    reason            TEXT,
    rollback_json     TEXT,                           -- required in practice for prune/archive
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (state_instance_id, asset_id) REFERENCES state_transitions (id, asset_id)
);
CREATE INDEX IF NOT EXISTS idx_dispositions_asset ON dispositions (asset_id, created_at);

-- ReferenceCheck: one reference-health run per asset version.
CREATE TABLE IF NOT EXISTS reference_checks (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_version_id INTEGER NOT NULL REFERENCES asset_versions (id) ON DELETE CASCADE,
    checked_at       TEXT    NOT NULL,
    overall_status   TEXT    NOT NULL CHECK (overall_status IN ('ok', 'failed', 'partial', 'unknown')),
    checker_version  TEXT    NOT NULL,
    UNIQUE (asset_version_id, checked_at)
);

-- ReferenceCheckItem: one extracted reference (command/path/tool) and its
-- existence result on this machine.
CREATE TABLE IF NOT EXISTS reference_check_items (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    check_id  INTEGER NOT NULL REFERENCES reference_checks (id) ON DELETE CASCADE,
    ref_kind  TEXT    NOT NULL CHECK (ref_kind IN ('command', 'path', 'tool')),
    ref_value TEXT    NOT NULL,
    "exists"  INTEGER NOT NULL CHECK ("exists" IN (0, 1)),
    detail    TEXT
);
CREATE INDEX IF NOT EXISTS idx_reference_check_items_check ON reference_check_items (check_id);