-- P4 reference evidence must preserve an extracted-but-unchecked reference.
-- NULL means unknown; 0/1 remain explicit missing/present observations.
-- Rollback note: restore the previous table shape only after exporting rows
-- whose exists value is NULL; turning them into 0 would fabricate failure.

CREATE TABLE reference_check_items_v3 (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    check_id  INTEGER NOT NULL REFERENCES reference_checks (id) ON DELETE CASCADE,
    ref_kind  TEXT    NOT NULL CHECK (ref_kind IN ('command', 'path', 'tool')),
    ref_value TEXT    NOT NULL,
    "exists"  INTEGER CHECK ("exists" IS NULL OR "exists" IN (0, 1)),
    detail    TEXT
);

INSERT INTO reference_check_items_v3 (id, check_id, ref_kind, ref_value, "exists", detail)
SELECT id, check_id, ref_kind, ref_value, "exists", detail
FROM reference_check_items;

DROP TABLE reference_check_items;
ALTER TABLE reference_check_items_v3 RENAME TO reference_check_items;
CREATE INDEX idx_reference_check_items_check ON reference_check_items (check_id);
