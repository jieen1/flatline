-- The English rendering of the one-sentence rule that put a friction record in
-- its category.
--
-- The rule sentence is not a label the page can look up: it names the literal
-- that matched, or the exit code that was recorded, so it is composed when the
-- record is classified and stored beside the category. Up to now it was stored
-- in one language only, and the English pages showed the Chinese sentence
-- verbatim. This column holds the same sentence, from the same match, written
-- in English; category_rule is unchanged.
--
-- It is a derived column (ADR-10): friction.ClassifierVersion moves to
-- friction/5 with it, and every row carrying an older version is reclassified
-- from its own stored payload on the next start. No source file is re-read and
-- no event is touched.
--
-- Rollback note: derived and additive. Dropping the column loses nothing that
-- a reclassify pass cannot write again; SQLite keeps the column otherwise.

ALTER TABLE friction_records ADD COLUMN category_rule_en TEXT;
