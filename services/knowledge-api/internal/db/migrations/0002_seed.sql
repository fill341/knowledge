-- Default spaces, the human principal and the initial entry kinds.

INSERT INTO spaces (id, slug, name, settings) VALUES
    (gen_random_uuid(), 'personal', 'Personal', '{}'),
    (gen_random_uuid(), 'work',     'Work',     '{}'),
    -- shared is visible from both profiles: anything an agent writes there goes through review
    (gen_random_uuid(), 'shared',   'Shared',   '{"agent_always_review": true}');

INSERT INTO principals (id, kind, name, description) VALUES
    (gen_random_uuid(), 'user', 'me', 'Owner of the knowledge base');

INSERT INTO entry_kinds (kind, description, agent_requires_review, attributes_schema) VALUES
('note', 'Free-form note', false,
 '{"type": "object"}'),

('fact', 'Verifiable fact about a system or domain', true,
 '{"type": "object", "additionalProperties": false,
   "properties": {
     "verified_at": {"type": "string", "format": "date"},
     "source":      {"type": "string"}}}'),

('decision', 'Architectural or technical decision (ADR)', true,
 '{"type": "object", "additionalProperties": false,
   "required": ["context", "decision"],
   "properties": {
     "context":      {"type": "string", "minLength": 1},
     "decision":     {"type": "string", "minLength": 1},
     "alternatives": {"type": "array", "items": {"type": "string"}},
     "consequences": {"type": "string"},
     "decided_at":   {"type": "string", "format": "date"}}}'),

('rule', 'Rule or convention agents must follow', true,
 '{"type": "object", "additionalProperties": false,
   "properties": {
     "applies_to": {"type": "array", "items": {"type": "string"}}}}'),

('lesson', 'Lesson learned: what went wrong and how to avoid it', false,
 '{"type": "object", "additionalProperties": false,
   "properties": {
     "symptom": {"type": "string"},
     "cause":   {"type": "string"},
     "fix":     {"type": "string"}}}'),

('document', 'Long document: spec, module description', false,
 '{"type": "object", "additionalProperties": false,
   "properties": {
     "format": {"enum": ["md"]}}}'),

('snippet', 'Code fragment or command', false,
 '{"type": "object", "additionalProperties": false,
   "properties": {
     "language": {"type": "string"},
     "command":  {"type": "string"}}}'),

('session_log', 'Summary of an agent session', false,
 '{"type": "object", "additionalProperties": false,
   "properties": {
     "agent":         {"type": "string"},
     "outcomes":      {"type": "array", "items": {"type": "string"}},
     "files_touched": {"type": "array", "items": {"type": "string"}}}}');
