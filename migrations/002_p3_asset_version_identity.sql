-- 002_p3_asset_version_identity.sql
-- P3 asset snapshot idempotency guard (ADR-10).
--
-- A content hash identifies one immutable asset version for an asset. The
-- registry already treats repeated observations as idempotent; this unique
-- index makes that invariant hold under concurrent replays as well.
--
-- Rollback note: DROP INDEX idx_asset_versions_asset_hash;

CREATE UNIQUE INDEX IF NOT EXISTS idx_asset_versions_asset_hash
    ON asset_versions (asset_id, content_hash);
