-- Structured session logs and handoff (ADR-11, roadmap P1 #5). New optional attributes; old logs stay valid.
-- status in_progress / blocked marks unfinished work that kn_context offers to the next agent of the project
-- until a later session log lists it in "continues".
UPDATE entry_kinds SET attributes_schema = '{"type": "object", "additionalProperties": false,
  "properties": {
    "agent":          {"type": "string"},
    "outcomes":       {"type": "array", "items": {"type": "string"}},
    "files_touched":  {"type": "array", "items": {"type": "string"}},
    "goal":           {"type": "string", "maxLength": 500},
    "status":         {"enum": ["done", "in_progress", "blocked"]},
    "done":           {"type": "array", "items": {"type": "string"}, "maxItems": 50},
    "commits":        {"type": "array", "items": {"type": "string"}, "maxItems": 50},
    "open_questions": {"type": "array", "items": {"type": "string"}, "maxItems": 30},
    "next_steps":     {"type": "array", "items": {"type": "string"}, "maxItems": 30},
    "continues":      {"type": "array", "items": {"type": "string", "format": "uuid"}, "maxItems": 20}}}'
WHERE kind = 'session_log';

-- Open handoffs are looked up per project among session logs.
CREATE INDEX entries_handoff_idx ON entries (project_id, created_at DESC)
    WHERE kind = 'session_log' AND status = 'active';
