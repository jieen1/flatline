-- Friction signature: the recurring-friction key.
--
-- signature is category || '|' || COALESCE(tool_name,'') || '|' || normalized_line,
-- where normalized_line is the recorded output line that carries the matched
-- literal, stripped of its "Exit code N" prefix, lowercased, with absolute
-- paths reduced to their last segment and digit runs collapsed to '#', bounded
-- to 120 characters. It is a derived projection of the bounded evidence that is
-- already stored in payload_json: it invents nothing and is recomputed in full
-- whenever the classifier version changes.
--
-- The column stays NULL for a row the classifier could not categorise. NULL
-- means "no signature was derived", never "no recurrence".
--
-- Rollback note: DROP INDEX idx_friction_records_signature, then rebuild the
-- table without the column (SQLite cannot drop a column in place before 3.35).
-- Nothing outside friction_records is touched; the canonical events table and
-- the source history are never edited.

ALTER TABLE friction_records ADD COLUMN signature TEXT;
CREATE INDEX idx_friction_records_signature ON friction_records (signature);
