-- Usage signal (ADR-9, roadmap P1 #4): per entry and day, how often it was shown in search results,
-- delivered in a kn_context pack and opened in full. No principal and no query text by design.
CREATE TABLE entry_usage (
    entry_id  uuid NOT NULL REFERENCES entries(id),
    day       date NOT NULL,
    shown     integer NOT NULL DEFAULT 0 CHECK (shown >= 0),
    delivered integer NOT NULL DEFAULT 0 CHECK (delivered >= 0),
    opened    integer NOT NULL DEFAULT 0 CHECK (opened >= 0),
    PRIMARY KEY (entry_id, day)
);

CREATE INDEX entry_usage_day_idx ON entry_usage (day);

-- Upserts need UPDATE; no DELETE, like the audit log.
GRANT SELECT, INSERT, UPDATE ON entry_usage TO kn_app;
