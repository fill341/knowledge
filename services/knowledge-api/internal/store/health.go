package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Health detectors. All are read-only and filtered by Visibility; pairs and groups never cross spaces,
// so legitimate copies between spaces (promote) are not reported as duplicates.

// liveCond excludes retired entries.
const liveCond = "%s.status NOT IN ('archived', 'superseded')"

// visibleAs restricts a spaces alias to the visible spaces.
func (b *builder) visibleAs(v Visibility, alias string) {
	if v.All {
		return
	}
	b.and(alias+".slug = ANY(%s::text[])", nonNil(v.Spaces))
}

func projectCond(b *builder, alias string, projectID *uuid.UUID) {
	if projectID != nil {
		b.and(alias+".project_id = %s", *projectID)
	}
}

type HealthItem struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Kind      string    `json:"kind"`
	Space     string    `json:"space"`
	Project   *string   `json:"project"`
	Status    string    `json:"status"`
	Links     int       `json:"links"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

const healthItemCols = `e.id, e.title, e.kind, s.slug AS space,
	CASE WHEN p.slug IS NULL THEN NULL ELSE s.slug || '/' || p.slug END AS project, e.status,
	(SELECT count(*)::int FROM entry_links l WHERE l.from_id = e.id OR l.to_id = e.id) AS links,
	e.created_at, cb.name AS created_by`

const healthItemFrom = ` FROM entries e JOIN spaces s ON s.id = e.space_id LEFT JOIN projects p ON p.id = e.project_id
	JOIN principals cb ON cb.id = e.created_by`

func scanHealthItem(row pgx.Row, extra ...any) (HealthItem, error) {
	var h HealthItem
	err := row.Scan(append([]any{&h.ID, &h.Title, &h.Kind, &h.Space, &h.Project, &h.Status, &h.Links, &h.CreatedAt, &h.CreatedBy}, extra...)...)
	return h, err
}

// LiveCount counts live entries in scope, optionally only of some kinds.
func (q *Queries) LiveCount(ctx context.Context, v Visibility, projectID *uuid.UUID, kinds []string) (int, error) {
	var b builder
	b.visible(v)
	projectCond(&b, "e", projectID)
	b.conds = append(b.conds, fmt.Sprintf(liveCond, "e"))
	if len(kinds) > 0 {
		b.and("e.kind = ANY(%s::text[])", kinds)
	}
	var n int
	err := q.db.QueryRow(ctx, `SELECT count(*)::int FROM entries e JOIN spaces s ON s.id = e.space_id`+b.where(), b.args...).Scan(&n)
	return n, err
}

// DuplicateContent groups live entries of one space with the same normalized body (body_hash, migration 0004;
// bodies under 200 characters are not compared). Within a group the best keeper — most links, then oldest — comes first.
func (q *Queries) DuplicateContent(ctx context.Context, v Visibility, projectID *uuid.UUID) ([][]HealthItem, error) {
	var b builder
	b.visible(v)
	projectCond(&b, "e", projectID)
	b.conds = append(b.conds, fmt.Sprintf(liveCond, "e"), "e.body_hash IS NOT NULL")
	sql := `WITH live AS (
			SELECT ` + healthItemCols + `, e.space_id, e.body_hash AS h` + healthItemFrom + b.where() + `
		), grp AS (SELECT space_id, h FROM live GROUP BY 1, 2 HAVING count(*) > 1)
		SELECT l.id, l.title, l.kind, l.space, l.project, l.status, l.links, l.created_at, l.created_by,
			l.space_id::text || ':' || encode(l.h, 'hex')
		FROM live l JOIN grp USING (space_id, h)
		ORDER BY l.space_id, l.h, l.links DESC, l.created_at, l.id`
	rows, err := q.db.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups [][]HealthItem
	last := ""
	for rows.Next() {
		var key string
		h, err := scanHealthItem(rows, &key)
		if err != nil {
			return nil, err
		}
		if key != last {
			groups = append(groups, nil)
			last = key
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], h)
	}
	return groups, rows.Err()
}

type HealthPair struct {
	A          HealthItem `json:"a"`
	B          HealthItem `json:"b"`
	Similarity float64    `json:"similarity,omitempty"`
	Rel        string     `json:"rel,omitempty"`
}

func ref(space string, slug *string) *string {
	if slug == nil {
		return nil
	}
	r := space + "/" + *slug
	return &r
}

// NearDuplicateTitles finds live pairs of the same kind and space with very similar titles,
// different text and no link between them.
func (q *Queries) NearDuplicateTitles(ctx context.Context, v Visibility, projectID *uuid.UUID, minSim float64, limit int) ([]HealthPair, int, error) {
	var b builder
	b.visibleAs(v, "s")
	projectCond(&b, "a", projectID)
	b.conds = append(b.conds, fmt.Sprintf(liveCond, "a"), fmt.Sprintf(liveCond, "b"),
		"(a.body_hash IS NULL OR a.body_hash IS DISTINCT FROM b.body_hash)", // same text is duplicate_content's job
		`NOT EXISTS (SELECT 1 FROM entry_links l WHERE (l.from_id = a.id AND l.to_id = b.id) OR (l.from_id = b.id AND l.to_id = a.id))`)
	b.and("similarity(a.title, b.title) >= %s", minSim)
	sql := `SELECT a.id, a.title, a.kind, s.slug, pa.slug, a.status, a.created_at,
			b.id, b.title, b.kind, s.slug, pb.slug, b.status, b.created_at,
			similarity(a.title, b.title)::float8 AS sim, count(*) OVER ()::int
		FROM entries a JOIN entries b ON a.id < b.id AND a.space_id = b.space_id AND a.kind = b.kind AND a.title % b.title
		JOIN spaces s ON s.id = a.space_id
		LEFT JOIN projects pa ON pa.id = a.project_id LEFT JOIN projects pb ON pb.id = b.project_id` + b.where() +
		fmt.Sprintf(` ORDER BY sim DESC, a.id LIMIT %d`, limit)
	return q.collectPairs(ctx, sql, b.args, true)
}

// collectPairs scans rows of (a fields, b fields, similarity|rel, total).
func (q *Queries) collectPairs(ctx context.Context, sql string, args []any, withSim bool) ([]HealthPair, int, error) {
	rows, err := q.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []HealthPair{}
	total := 0
	for rows.Next() {
		var p HealthPair
		var pa, pb *string
		dest := []any{&p.A.ID, &p.A.Title, &p.A.Kind, &p.A.Space, &pa, &p.A.Status, &p.A.CreatedAt,
			&p.B.ID, &p.B.Title, &p.B.Kind, &p.B.Space, &pb, &p.B.Status, &p.B.CreatedAt}
		if withSim {
			dest = append(dest, &p.Similarity)
		} else {
			dest = append(dest, &p.Rel)
		}
		if err := rows.Scan(append(dest, &total)...); err != nil {
			return nil, 0, err
		}
		p.A.Project, p.B.Project = ref(p.A.Space, pa), ref(p.B.Space, pb)
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// linkPairs returns links (a → b) matching cond where both ends are visible.
func (q *Queries) linkPairs(ctx context.Context, v Visibility, projectID *uuid.UUID, cond string, limit int) ([]HealthPair, int, error) {
	var b builder
	b.visibleAs(v, "sa")
	b.visibleAs(v, "sb")
	projectCond(&b, "a", projectID)
	b.conds = append(b.conds, cond)
	sql := `SELECT a.id, a.title, a.kind, sa.slug, pa.slug, a.status, a.created_at,
			b.id, b.title, b.kind, sb.slug, pb.slug, b.status, b.created_at, l.rel, count(*) OVER ()::int
		FROM entry_links l
		JOIN entries a ON a.id = l.from_id JOIN spaces sa ON sa.id = a.space_id LEFT JOIN projects pa ON pa.id = a.project_id
		JOIN entries b ON b.id = l.to_id JOIN spaces sb ON sb.id = b.space_id LEFT JOIN projects pb ON pb.id = b.project_id` +
		b.where() + fmt.Sprintf(` ORDER BY l.created_at DESC LIMIT %d`, limit)
	return q.collectPairs(ctx, sql, b.args, false)
}

// Contradictions: "contradicts" links between two live entries.
func (q *Queries) Contradictions(ctx context.Context, v Visibility, projectID *uuid.UUID, limit int) ([]HealthPair, int, error) {
	return q.linkPairs(ctx, v, projectID,
		"l.rel = 'contradicts' AND "+fmt.Sprintf(liveCond, "a")+" AND "+fmt.Sprintf(liveCond, "b"), limit)
}

// DependsOnRetired: live entries that depend on or reference deprecated, superseded or archived knowledge.
func (q *Queries) DependsOnRetired(ctx context.Context, v Visibility, projectID *uuid.UUID, limit int) ([]HealthPair, int, error) {
	return q.linkPairs(ctx, v, projectID,
		"l.rel IN ('depends_on', 'references') AND "+fmt.Sprintf(liveCond, "a")+
			" AND b.status IN ('deprecated', 'superseded', 'archived')", limit)
}

// HealthItems lists entries in scope matching cond, with a total count. detail is an SQL expression explaining
// the problem for each row; its (localized) text pieces are passed as parameters.
func (q *Queries) HealthItems(ctx context.Context, v Visibility, projectID *uuid.UUID, cond, detail Expr, order string, limit int) ([]HealthItem, int, error) {
	var b builder
	b.visible(v)
	projectCond(&b, "e", projectID)
	b.and(cond.SQL, cond.Args...)
	detailSQL := b.expr(detail.SQL, detail.Args...)
	sql := `SELECT ` + healthItemCols + `, ` + detailSQL + `, count(*) OVER ()::int` + healthItemFrom + b.where() +
		fmt.Sprintf(` ORDER BY %s LIMIT %d`, order, limit)
	rows, err := q.db.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []HealthItem{}
	total := 0
	for rows.Next() {
		var detailText string
		h, err := scanHealthItem(rows, &detailText, &total)
		if err != nil {
			return nil, 0, err
		}
		h.Detail = detailText
		out = append(out, h)
	}
	return out, total, rows.Err()
}

// ReviewBacklog counts proposed entries and the age of the oldest one.
func (q *Queries) ReviewBacklog(ctx context.Context, spaces []string) (int, *time.Time, error) {
	var n int
	var oldest *time.Time
	err := q.db.QueryRow(ctx, `SELECT count(*)::int, min(e.created_at) FROM entries e JOIN spaces s ON s.id = e.space_id
		WHERE e.status = 'proposed' AND s.slug = ANY($1::text[])`, nonNil(spaces)).Scan(&n, &oldest)
	return n, oldest, err
}

type RetrievalStat struct {
	Principal string `json:"principal"`
	Action    string `json:"action"`
	Calls     int    `json:"calls"`
	Empty     int    `json:"empty"`
}

// RetrievalStats aggregates search/context calls and empty results from the audit log since a time.
// principalID limits the stats to one principal (non-admin callers see only their own).
func (q *Queries) RetrievalStats(ctx context.Context, since time.Time, principalID *uuid.UUID) ([]RetrievalStat, error) {
	rows, err := q.db.Query(ctx, `SELECT coalesce(p.name, '?'), a.action, count(*)::int,
			count(*) FILTER (WHERE (a.details->>'results')::int = 0)::int
		FROM audit_log a LEFT JOIN principals p ON p.id = a.principal_id
		WHERE a.action IN ('search', 'context') AND a.at >= $1 AND ($2::uuid IS NULL OR a.principal_id = $2)
		GROUP BY 1, 2 ORDER BY 3 DESC`, since, principalID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[RetrievalStat])
}

type OpenSession struct {
	ID        uuid.UUID `json:"id"`
	Principal string    `json:"principal"`
	Agent     string    `json:"agent"`
	Space     string    `json:"space"`
	Project   *string   `json:"project"`
	StartedAt time.Time `json:"started_at"`
	Entries   int       `json:"entries"`
}

// OpenSessions lists agent sessions that started before `before` and were never ended.
func (q *Queries) OpenSessions(ctx context.Context, v Visibility, projectID *uuid.UUID, before time.Time) ([]OpenSession, error) {
	var b builder
	b.visible(v)
	if projectID != nil {
		b.and("a.project_id = %s", *projectID)
	}
	b.and("a.started_at < %s", before)
	b.conds = append(b.conds, "a.ended_at IS NULL")
	rows, err := q.db.Query(ctx, `SELECT a.id, pr.name, a.agent, s.slug,
			CASE WHEN p.slug IS NULL THEN NULL ELSE s.slug || '/' || p.slug END, a.started_at,
			(SELECT count(*)::int FROM entries e WHERE e.session_id = a.id)
		FROM agent_sessions a JOIN spaces s ON s.id = a.space_id JOIN principals pr ON pr.id = a.principal_id
		LEFT JOIN projects p ON p.id = a.project_id`+b.where()+` ORDER BY a.started_at`, b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[OpenSession])
}

type RulesSize struct {
	Project string
	Chars   int
}

// RulesSizes estimates, per visible project, the characters of the always-included layer of its context:
// active rules and pinned entries of the project plus pinned space-wide entries of its space and shared.
func (q *Queries) RulesSizes(ctx context.Context, v Visibility, projectID *uuid.UUID) ([]RulesSize, error) {
	var b builder
	b.visible(v)
	if projectID != nil {
		b.and("p.id = %s", *projectID)
	}
	rows, err := q.db.Query(ctx, `SELECT s.slug || '/' || p.slug,
			(SELECT coalesce(sum(char_length(e.title) + char_length(coalesce(nullif(btrim(e.body), ''), e.summary, '')) + 60), 0)::int
			 FROM entries e WHERE e.status = 'active' AND (
			   (e.project_id = p.id AND (e.kind = 'rule' OR e.pinned)) OR
			   (e.project_id IS NULL AND e.pinned AND (e.space_id = p.space_id OR e.space_id = (SELECT id FROM spaces WHERE slug = 'shared')))))
		FROM projects p JOIN spaces s ON s.id = p.space_id`+b.where()+` ORDER BY 2 DESC`, b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[RulesSize])
}

// StaleHandoffs lists unfinished work (see OpenHandoffs) created before `before` that nobody picked up.
// startedPrefix is the localized detail prefix before the start date (e.g. "начата ").
func (q *Queries) StaleHandoffs(ctx context.Context, v Visibility, projectID *uuid.UUID, before time.Time, limit int, startedPrefix string) ([]HealthItem, int, error) {
	return q.HealthItems(ctx, v, projectID, X(openHandoffCond+" AND e.created_at < %s", before),
		X(`%s::text || to_char(e.created_at, 'YYYY-MM-DD') || coalesce(': ' || (e.attributes->>'goal'), '')`, startedPrefix),
		"e.created_at", limit)
}

type HealthSnapshot struct {
	Day         string                   `json:"day"`
	Score       int                      `json:"score"`
	Severity    string                   `json:"severity"`
	LiveEntries int                      `json:"live_entries"`
	Checks      map[string]SnapshotCheck `json:"checks"`
}

type SnapshotCheck struct {
	Severity string `json:"severity"`
	Count    int    `json:"count"`
}

// SaveHealthSnapshot stores today's snapshot unless one exists; reports whether it was stored.
func (q *Queries) SaveHealthSnapshot(ctx context.Context, s HealthSnapshot) (bool, error) {
	tag, err := q.db.Exec(ctx, `INSERT INTO health_snapshots (day, score, severity, live_entries, checks)
		VALUES (current_date, $1, $2, $3, $4) ON CONFLICT (day) DO NOTHING`, s.Score, s.Severity, s.LiveEntries, s.Checks)
	return tag.RowsAffected() > 0, err
}

// HealthSnapshots returns the snapshots of the last `days` days, oldest first.
func (q *Queries) HealthSnapshots(ctx context.Context, days int) ([]HealthSnapshot, error) {
	rows, err := q.db.Query(ctx, `SELECT to_char(day, 'YYYY-MM-DD'), score, severity, live_entries, checks
		FROM health_snapshots WHERE day > current_date - $1::int ORDER BY day`, days)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[HealthSnapshot])
}
