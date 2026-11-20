-- Initial schema. See docs/data-model.md.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS vector;   -- installed now, used in phase 3

-- ── Spaces and principals ──────────────────────────────────────────────────

CREATE TABLE spaces (
    id          uuid PRIMARY KEY,
    slug        text NOT NULL UNIQUE CHECK (slug ~ '^[a-z][a-z0-9-]{1,31}$'),
    name        text NOT NULL,
    settings    jsonb NOT NULL DEFAULT '{}',      -- {review_kinds: [...], agent_always_review: bool}
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE principals (
    id          uuid PRIMARY KEY,
    kind        text NOT NULL CHECK (kind IN ('user', 'agent', 'system')),
    name        text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9._@-]{0,63}$'),
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    disabled_at timestamptz
);

CREATE TABLE api_tokens (
    id           uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES principals(id),
    name         text NOT NULL,
    prefix       text NOT NULL UNIQUE,            -- public part of the token, safe to log
    secret_hash  bytea NOT NULL,                  -- sha256(secret); 256-bit secret, no need for bcrypt
    scopes       text[] NOT NULL CHECK (cardinality(scopes) > 0),
    expires_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz
);

-- ── Projects and kinds ─────────────────────────────────────────────────────

CREATE TABLE projects (
    id          uuid PRIMARY KEY,
    space_id    uuid NOT NULL REFERENCES spaces(id),
    slug        text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    repo_urls   text[] NOT NULL DEFAULT '{}',     -- normalized git remotes, for auto-detection
    metadata    jsonb NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz,
    UNIQUE (space_id, slug),
    UNIQUE (space_id, id)                         -- target of composite FKs
);
CREATE INDEX projects_repo_urls_idx ON projects USING gin (repo_urls);

CREATE TABLE entry_kinds (
    kind                  text PRIMARY KEY CHECK (kind ~ '^[a-z][a-z0-9_]{1,31}$'),
    description           text NOT NULL,
    attributes_schema     jsonb NOT NULL DEFAULT '{}',   -- JSON Schema for entries.attributes
    agent_requires_review boolean NOT NULL DEFAULT false
);

-- ── Agent sessions ─────────────────────────────────────────────────────────

CREATE TABLE agent_sessions (
    id           uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES principals(id),
    space_id     uuid NOT NULL REFERENCES spaces(id),
    project_id   uuid,
    agent        text NOT NULL,
    client_info  jsonb NOT NULL DEFAULT '{}',
    started_at   timestamptz NOT NULL DEFAULT now(),
    ended_at     timestamptz,
    FOREIGN KEY (space_id, project_id) REFERENCES projects (space_id, id)
);

-- ── Entries ────────────────────────────────────────────────────────────────

CREATE TABLE entries (
    id            uuid PRIMARY KEY,
    space_id      uuid NOT NULL REFERENCES spaces(id),
    project_id    uuid,
    kind          text NOT NULL REFERENCES entry_kinds(kind),
    title         text NOT NULL CHECK (char_length(title) BETWEEN 3 AND 300),
    summary       text CHECK (char_length(summary) <= 1000),
    body          text NOT NULL DEFAULT '' CHECK (char_length(body) <= 200000),
    attributes    jsonb NOT NULL DEFAULT '{}',
    tags          text[] NOT NULL DEFAULT '{}',
    status        text NOT NULL CHECK (status IN
                    ('draft', 'proposed', 'active', 'deprecated', 'superseded', 'archived')),
    pinned        boolean NOT NULL DEFAULT false,
    confidence    real CHECK (confidence BETWEEN 0 AND 1),
    version       integer NOT NULL DEFAULT 1 CHECK (version > 0),
    content_hash  bytea NOT NULL,
    superseded_by uuid REFERENCES entries(id),
    review_after  date,
    session_id    uuid REFERENCES agent_sessions(id),
    source_ref    text,
    created_by    uuid NOT NULL REFERENCES principals(id),
    updated_by    uuid NOT NULL REFERENCES principals(id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- 'russian' config: Cyrillic gets the Russian stemmer, ASCII words the English one.
    search_tsv    tsvector GENERATED ALWAYS AS (
                      setweight(to_tsvector('russian', title), 'A') ||
                      setweight(to_tsvector('russian', coalesce(summary, '')), 'B') ||
                      setweight(to_tsvector('russian', body), 'C')
                  ) STORED,
    FOREIGN KEY (space_id, project_id) REFERENCES projects (space_id, id),
    CHECK (status <> 'superseded' OR superseded_by IS NOT NULL)
);

CREATE INDEX entries_scope_idx   ON entries (space_id, project_id, status, updated_at DESC);
CREATE INDEX entries_kind_idx    ON entries (space_id, kind, status);
CREATE INDEX entries_pinned_idx  ON entries (space_id, project_id) WHERE pinned AND status = 'active';
CREATE INDEX entries_tsv_idx     ON entries USING gin (search_tsv);
CREATE INDEX entries_tags_idx    ON entries USING gin (tags);
CREATE INDEX entries_title_trgm  ON entries USING gin (title gin_trgm_ops);
CREATE INDEX entries_review_idx  ON entries (review_after) WHERE review_after IS NOT NULL;
CREATE UNIQUE INDEX entries_dedup_idx ON entries (space_id, content_hash)
    WHERE status NOT IN ('archived', 'superseded');

-- ── History ────────────────────────────────────────────────────────────────

CREATE TABLE entry_versions (
    entry_id    uuid NOT NULL REFERENCES entries(id),
    version     integer NOT NULL,
    project_id  uuid,
    kind        text NOT NULL,
    title       text NOT NULL,
    summary     text,
    body        text NOT NULL,
    attributes  jsonb NOT NULL,
    tags        text[] NOT NULL,
    status      text NOT NULL,
    pinned      boolean NOT NULL,
    changed_by  uuid NOT NULL REFERENCES principals(id),
    change_note text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entry_id, version)
);

CREATE FUNCTION entries_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'entries.version must be incremented by 1 (old %, new %)', OLD.version, NEW.version;
    END IF;
    INSERT INTO entry_versions (entry_id, version, project_id, kind, title, summary, body,
                                attributes, tags, status, pinned, changed_by, change_note)
    VALUES (NEW.id, NEW.version, NEW.project_id, NEW.kind, NEW.title, NEW.summary, NEW.body,
            NEW.attributes, NEW.tags, NEW.status, NEW.pinned, NEW.updated_by,
            nullif(current_setting('app.change_note', true), ''));
    RETURN NULL;
END $$;

CREATE TRIGGER entries_history AFTER INSERT OR UPDATE ON entries
    FOR EACH ROW EXECUTE FUNCTION entries_snapshot();

-- ── Links, reviews, audit ──────────────────────────────────────────────────

CREATE TABLE entry_links (
    from_id    uuid NOT NULL REFERENCES entries(id),
    to_id      uuid NOT NULL REFERENCES entries(id),
    rel        text NOT NULL CHECK (rel IN
                 ('relates_to', 'depends_on', 'supersedes', 'derived_from', 'contradicts', 'references')),
    created_by uuid NOT NULL REFERENCES principals(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (from_id, to_id, rel),
    CHECK (from_id <> to_id)
);
CREATE INDEX entry_links_to_idx ON entry_links (to_id);

CREATE TABLE reviews (
    id            uuid PRIMARY KEY,
    entry_id      uuid NOT NULL REFERENCES entries(id),
    entry_version integer NOT NULL,
    reviewer_id   uuid NOT NULL REFERENCES principals(id),
    action        text NOT NULL CHECK (action IN ('approve', 'reject', 'request_changes')),
    comment       text,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at           timestamptz NOT NULL DEFAULT now(),
    principal_id uuid REFERENCES principals(id),
    token_id     uuid REFERENCES api_tokens(id),
    space_id     uuid REFERENCES spaces(id),
    action       text NOT NULL,
    target_type  text,
    target_id    uuid,
    details      jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at_idx     ON audit_log (at DESC);
CREATE INDEX audit_log_target_idx ON audit_log (target_id);

CREATE TABLE idempotency_keys (
    principal_id uuid NOT NULL REFERENCES principals(id),
    key          text NOT NULL,
    request_hash bytea NOT NULL,
    status_code  integer NOT NULL,
    response     bytea NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (principal_id, key)
);

-- ── Runtime role privileges ────────────────────────────────────────────────
-- kn_owner owns the schema and runs migrations; kn_app is the service runtime.
-- DELETE on entries / entry_versions / audit_log is intentionally not granted.

GRANT SELECT, INSERT, UPDATE ON spaces, principals, api_tokens, projects,
                                entry_kinds, agent_sessions, entries TO kn_app;
GRANT SELECT, INSERT         ON entry_versions, reviews, audit_log TO kn_app;
GRANT SELECT, INSERT, DELETE ON entry_links, idempotency_keys TO kn_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO kn_app;
GRANT SELECT                 ON schema_migrations TO kn_app;   -- readiness probe
