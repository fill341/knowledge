-- Daily health snapshots (ADR-13, roadmap P2 #8): the score and per-check counts of the whole base, for trends.
-- Aggregates only (no titles), but over all spaces — readable by global admins only (enforced in the service).
CREATE TABLE health_snapshots (
    day          date PRIMARY KEY,
    score        integer NOT NULL CHECK (score BETWEEN 0 AND 100),
    severity     text NOT NULL,
    live_entries integer NOT NULL,
    checks       jsonb NOT NULL,          -- {"<check id>": {"severity": "...", "count": N}}
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- The first snapshot of a day wins (INSERT ... ON CONFLICT DO NOTHING): no UPDATE, no DELETE.
GRANT SELECT, INSERT ON health_snapshots TO kn_app;
