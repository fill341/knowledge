-- Index string values of attributes too (decision context, lesson symptom/fix, ...).
-- A generated column cannot be altered in place, so it is recreated (and its index with it).
ALTER TABLE entries DROP COLUMN search_tsv;

ALTER TABLE entries ADD COLUMN search_tsv tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('russian', title), 'A') ||
    setweight(to_tsvector('russian', coalesce(summary, '')), 'B') ||
    setweight(to_tsvector('russian', body), 'C') ||
    setweight(jsonb_to_tsvector('russian', attributes, '["string"]'), 'C')
) STORED;

CREATE INDEX entries_tsv_idx ON entries USING gin (search_tsv);
