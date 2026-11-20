-- Freshness (ADR-12, roadmap P2 #6): a kind may carry a re-check period. An active entry is due for review at
-- its explicit review_after, else at its last change (an approval in the review queue also counts) plus the
-- period. Computed on read, so no existing entry is rewritten.
ALTER TABLE entry_kinds ADD COLUMN review_days integer CHECK (review_days > 0);

UPDATE entry_kinds SET review_days = CASE kind
    WHEN 'fact'    THEN 180
    WHEN 'snippet' THEN 180
    WHEN 'lesson'  THEN 365
    WHEN 'rule'    THEN 365
END;
