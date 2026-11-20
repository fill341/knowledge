-- Content duplicates (roadmap P0 #2): a hash of the body with whitespace runs collapsed and trimmed,
-- so the same text with different formatting or under another title is recognised. Bodies shorter
-- than 200 characters get NULL: short texts coincide legitimately and are not compared.
-- Collapse first, then trim: btrim alone strips only spaces, not newlines.
CREATE FUNCTION kn_body_hash(body text) RETURNS bytea
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN CASE WHEN char_length(btrim(regexp_replace(body, '\s+', ' ', 'g'))) >= 200
                THEN decode(md5(btrim(regexp_replace(body, '\s+', ' ', 'g'))), 'hex') END;

ALTER TABLE entries ADD COLUMN body_hash bytea GENERATED ALWAYS AS (kn_body_hash(body)) STORED;

-- Not unique: duplicates are reported (write warnings, health report), never blocked by the schema.
CREATE INDEX entries_body_hash_idx ON entries (space_id, body_hash)
    WHERE body_hash IS NOT NULL AND status NOT IN ('archived', 'superseded');
