-- Gives the demo base (scripts/demo-data.py) a believable history for the evolution player and activity halos:
-- entries, versions and links spread over ~4 months, agent sessions in bursts. Throwaway databases only.
SET session_replication_role = replica;  -- throwaway demo base: no version/audit triggers while rewriting time
-- profile: project → (start days ago, last activity days ago)
CREATE TEMP TABLE prof(slug text, d0 float, d1 float);
INSERT INTO prof VALUES ('knowledge', 30, 0.05), ('billing', 120, 0.3), ('mobile', 95, 6.5),
  ('infra', 110, 1.6), ('recsys', 70, 12), ('site', 60, 35);
-- entries: spread between project start and last activity, earlier entries denser
UPDATE entries e SET created_at = now() - make_interval(secs => 86400 * (pr.d1 + (pr.d0 - pr.d1) * power(random(), 0.8))),
  updated_at = now()
FROM projects p JOIN prof pr ON pr.slug = p.slug WHERE e.project_id = p.id;
UPDATE entries SET created_at = now() - interval '125 days' + random() * interval '20 days', updated_at = created_at WHERE project_id IS NULL;
UPDATE entries SET updated_at = created_at + random() * (now() - created_at) * 0.3;
-- each entry's first version at its birth; later versions in between
UPDATE entry_versions v SET created_at = e.created_at + (v.version - 1) * (e.updated_at - e.created_at) FROM entries e WHERE e.id = v.entry_id;
-- links appear when the younger end is written, a little later
UPDATE entry_links l SET created_at = greatest(a.created_at, b.created_at) + random() * interval '6 hours'
FROM entries a, entries b WHERE a.id = l.from_id AND b.id = l.to_id;
UPDATE entry_links SET created_at = least(created_at, now());
-- agent sessions: bursts of work, denser near the end of each project's active period
INSERT INTO agent_sessions (id, principal_id, space_id, project_id, agent, started_at, ended_at)
SELECT gen_random_uuid(), (SELECT id FROM principals WHERE name = 'me'), p.space_id, p.id,
  (ARRAY['claude-code','cursor','codex'])[1 + floor(random() * 3)::int], t, t + interval '40 minutes'
FROM projects p JOIN prof pr ON pr.slug = p.slug,
  LATERAL (SELECT now() - make_interval(secs => 86400 * (pr.d1 + (pr.d0 - pr.d1) * power(random(), 2.2))) AS t
           FROM generate_series(1, (pr.d0 * 1.5)::int)) g;
-- current sprint: extra sessions in the last days for the hottest projects
INSERT INTO agent_sessions (id, principal_id, space_id, project_id, agent, started_at, ended_at)
SELECT gen_random_uuid(), (SELECT id FROM principals WHERE name = 'me'), p.space_id, p.id, 'claude-code', t, t + interval '30 minutes'
FROM projects p, LATERAL (SELECT now() - random() * interval '30 hours' AS t FROM generate_series(1, CASE p.slug WHEN 'billing' THEN 30 WHEN 'knowledge' THEN 14 ELSE 0 END)) g;
SET session_replication_role = origin;
