package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SearchParams struct {
	Query string
	// BoostProjectID ranks entries of this project higher (×1.5) without excluding others.
	BoostProjectID *uuid.UUID
	Filter         EntryFilter
	Offset         int
}

type SearchHit struct {
	EntryRow
	Score     float64
	Highlight string
}

// Ranking: ts_rank_cd × status weight × recency decay (half-life 180 days) × project boost.
const scoreExpr = `ts_rank_cd(e.search_tsv, q.query, 32)
	* CASE e.status WHEN 'active' THEN 1.0 WHEN 'proposed' THEN 0.7 WHEN 'deprecated' THEN 0.3 ELSE 0.2 END
	* power(0.5, extract(epoch FROM now() - e.updated_at) / (86400 * 180.0))
	* CASE WHEN %s::uuid IS NOT NULL AND e.project_id = %s::uuid THEN 1.5 ELSE 1.0 END`

const headlineOpts = `'StartSel=**, StopSel=**, MaxWords=35, MinWords=12, MaxFragments=2, FragmentDelimiter=" … "'`

// Search runs full-text search (Russian config: Cyrillic and English stemming).
func (q *Queries) Search(ctx context.Context, v Visibility, p SearchParams) ([]SearchHit, error) {
	var b builder
	qArg := b.arg(p.Query)
	projArg := b.arg(p.BoostProjectID)
	b.visible(v.Narrow(p.Filter.Spaces))
	applyFilter(&b, p.Filter)
	b.conds = append(b.conds, "e.search_tsv @@ q.query")
	limit := clampLimit(p.Filter.Limit)
	sql := `WITH q AS (SELECT websearch_to_tsquery('russian', ` + qArg + `) AS query)
		SELECT ` + entryCols + `,
		  (` + fmt.Sprintf(scoreExpr, projArg, projArg) + `)::float8 AS score,
		  ts_headline('russian', coalesce(e.summary, '') || ' ' || left(e.body, 20000), q.query, ` + headlineOpts + `)
		` + entryFrom + `, q` + b.where() +
		fmt.Sprintf(` ORDER BY score DESC, e.id LIMIT %d OFFSET %d`, limit, max(p.Offset, 0))
	return q.collectHits(ctx, sql, b.args)
}

// SearchAny is the fallback for natural-language queries, where requiring every word (Search) finds nothing:
// entries matching any query word, ranked by the share of query words they contain (squared, so covering
// more of the query beats repeating one word; a word only in the body counts half, so long documents that
// contain every word somewhere do not crowd out entries about the topic), then by the usual score. Queries with websearch operators
// (quotes, -word, or) are left alone: the caller asked for exact logic. Query lexemes are cast with ::tsquery,
// not to_tsquery, so they are not stemmed a second time.
func (q *Queries) SearchAny(ctx context.Context, v Visibility, p SearchParams) ([]SearchHit, error) {
	var b builder
	qArg := b.arg(p.Query)
	projArg := b.arg(p.BoostProjectID)
	b.visible(v.Narrow(p.Filter.Spaces))
	applyFilter(&b, p.Filter)
	b.conds = append(b.conds, "q.n > 1", "e.search_tsv @@ q.query")
	limit := clampLimit(p.Filter.Limit)
	// Operators are detected in the raw text: the parsed tsquery also has <-> for hyphenated words like «офлайн-режим».
	sql := `WITH q AS (
			SELECT array_to_string(lx, ' | ')::tsquery AS query, lx, cardinality(lx) AS n
			FROM (SELECT ARRAY(SELECT DISTINCT quote_literal(w) FROM unnest(tsvector_to_array(to_tsvector('russian', ` + qArg + `))) w) AS lx) l
			WHERE ` + qArg + ` !~* '"|(^|\s)-\S|\sor\s'
		)
		SELECT ` + entryCols + `,
		  (power((SELECT sum(CASE WHEN e.search_tsv @@ (w || ':AB')::tsquery THEN 1.0 ELSE 0.5 END)
		          FROM unnest(q.lx) w WHERE e.search_tsv @@ w::tsquery)::float8 / q.n, 2)
		   * ` + fmt.Sprintf(scoreExpr, projArg, projArg) + `)::float8 AS score,
		  ts_headline('russian', coalesce(e.summary, '') || ' ' || left(e.body, 20000), q.query, ` + headlineOpts + `)
		` + entryFrom + `, q` + b.where() +
		fmt.Sprintf(` ORDER BY score DESC, e.id LIMIT %d`, limit)
	return q.collectHits(ctx, sql, b.args)
}

// SearchTitles is the fuzzy fallback: trigram similarity and substring match on titles.
func (q *Queries) SearchTitles(ctx context.Context, v Visibility, p SearchParams) ([]SearchHit, error) {
	var b builder
	qArg := b.arg(p.Query)
	b.visible(v.Narrow(p.Filter.Spaces))
	applyFilter(&b, p.Filter)
	b.conds = append(b.conds, fmt.Sprintf("(e.title %% %s OR strpos(lower(e.title), lower(%s)) > 0)", qArg, qArg))
	limit := clampLimit(p.Filter.Limit)
	sql := `SELECT ` + entryCols + `, (similarity(e.title, ` + qArg + `) * 0.5)::float8 AS score,
		  coalesce(e.summary, left(e.body, 200))` + entryFrom + b.where() +
		fmt.Sprintf(` ORDER BY score DESC, e.id LIMIT %d`, limit)
	return q.collectHits(ctx, sql, b.args)
}

func (q *Queries) collectHits(ctx context.Context, sql string, args []any) ([]SearchHit, error) {
	rows, err := q.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SearchHit, error) {
		var h SearchHit
		row, err := scanEntryInto(r, &h.Score, &h.Highlight)
		h.EntryRow = row
		return h, err
	})
}

// ── Graph ───────────────────────────────────────────────────────────────────

type GraphNode struct {
	ID        uuid.UUID `json:"id"`
	Space     string    `json:"space"`
	Project   *string   `json:"project"` // "<space>/<slug>"
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Pinned    bool      `json:"pinned"`
	Tags      []string  `json:"tags"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"` // the evolution player shows the entry from this moment
	Mass      int       `json:"mass"`       // volume of knowledge: characters of title, summary and body
	SourceRef *string   `json:"-"`          // used for implicit links only
	ReviewDue bool      `json:"review_due"` // active and past its re-check date (ADR-12)
	Used      int       `json:"used"`       // delivered or opened in the last GraphUsageDays days (ADR-9)
}

type GraphLink struct {
	From      uuid.UUID `json:"from"`
	To        uuid.UUID `json:"to"`
	Rel       string    `json:"rel"`
	CreatedAt time.Time `json:"created_at"`
}

// ActivityHour counts development events of a project in one hour: entry revisions (each create or
// edit) and agent sessions started in the project. The map turns recent activity into a halo around the star
// and replays it in the evolution player.
type ActivityHour struct {
	Project string    `json:"project"` // "<space>/<slug>"
	Hour    time.Time `json:"hour"`
	Events  int       `json:"events"`
}

// GraphActivity returns hourly activity of visible projects over the whole history.
func (q *Queries) GraphActivity(ctx context.Context, v Visibility) ([]ActivityHour, error) {
	var b builder
	b.visible(v)
	rows, err := q.db.Query(ctx, `SELECT s.slug || '/' || p.slug, date_trunc('hour', ev.at), count(*)::int
		FROM (SELECT ver.project_id, ver.created_at AS at FROM entry_versions ver WHERE ver.project_id IS NOT NULL
			UNION ALL
			SELECT a.project_id, a.started_at FROM agent_sessions a WHERE a.project_id IS NOT NULL) ev
		JOIN projects p ON p.id = ev.project_id JOIN spaces s ON s.id = p.space_id`+b.where()+`
		GROUP BY 1, 2 ORDER BY 2`, b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ActivityHour])
}

// GraphUsageDays is the window of the usage signal on the knowledge map.
const GraphUsageDays = 90

// GraphNodes returns lightweight entries for visualization, newest first.
func (q *Queries) GraphNodes(ctx context.Context, v Visibility, statuses []string, limit int) ([]GraphNode, error) {
	var b builder
	b.visible(v)
	if len(statuses) > 0 {
		b.and("e.status = ANY(%s::text[])", statuses)
	}
	rows, err := q.db.Query(ctx, `SELECT e.id, s.slug, CASE WHEN p.slug IS NULL THEN NULL ELSE s.slug || '/' || p.slug END,
			e.kind, e.title, e.status, e.pinned, e.tags, e.updated_at, e.created_at,
			char_length(e.title) + coalesce(char_length(e.summary), 0) + char_length(e.body), e.source_ref,
			coalesce(`+DueCond+`, false),
			coalesce((SELECT sum(u.delivered + u.opened) FROM entry_usage u
				WHERE u.entry_id = e.id AND u.day > current_date - `+fmt.Sprint(GraphUsageDays)+`), 0)::int
		FROM entries e JOIN spaces s ON s.id = e.space_id LEFT JOIN projects p ON p.id = e.project_id`+
		b.where()+fmt.Sprintf(` ORDER BY e.updated_at DESC LIMIT %d`, limit), b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[GraphNode])
}

type GraphTerm struct {
	ID     uuid.UUID
	Lexeme string
	Weight float64
}

// GraphTerms returns the most characteristic lexemes of each entry (TF-IDF over search_tsv within the given
// entries, title ×4 and summary ×2), at most perEntry per entry. Lexemes found in one entry only are skipped:
// they cannot connect anything. Caller passes visible ids only.
func (q *Queries) GraphTerms(ctx context.Context, ids []uuid.UUID, perEntry int) ([]GraphTerm, error) {
	rows, err := q.db.Query(ctx, `WITH lex AS (
			SELECT e.id, t.lexeme, cardinality(t.positions) *
				CASE WHEN 'A' = ANY(t.weights) THEN 4 WHEN 'B' = ANY(t.weights) THEN 2 ELSE 1 END AS tf
			FROM entries e, unnest(e.search_tsv) t WHERE e.id = ANY($1::uuid[])),
		df AS (SELECT lexeme, count(*) AS df FROM lex GROUP BY lexeme HAVING count(*) > 1),
		n AS (SELECT count(DISTINCT id)::float AS n FROM lex),
		scored AS (SELECT l.id, l.lexeme, (1 + ln(l.tf)) * ln(n.n / df.df) AS w FROM lex l JOIN df USING (lexeme), n),
		ranked AS (SELECT *, row_number() OVER (PARTITION BY id ORDER BY w DESC, lexeme) AS rk FROM scored)
		SELECT id, lexeme, w FROM ranked WHERE rk <= $2 AND w > 0`, ids, perEntry)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[GraphTerm])
}

// GraphLinks returns links whose both ends are visible.
func (q *Queries) GraphLinks(ctx context.Context, v Visibility) ([]GraphLink, error) {
	sql := `SELECT l.from_id, l.to_id, l.rel, l.created_at FROM entry_links l
		JOIN entries a ON a.id = l.from_id JOIN spaces sa ON sa.id = a.space_id
		JOIN entries b ON b.id = l.to_id JOIN spaces sb ON sb.id = b.space_id`
	var args []any
	if !v.All {
		sql += ` WHERE sa.slug = ANY($1::text[]) AND sb.slug = ANY($1::text[])`
		args = append(args, nonNil(v.Spaces))
	}
	rows, err := q.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[GraphLink])
}

// EntriesByRef resolves an eval expectation to visible live entries: field is "id", "source_ref" or "title"
// (title compared case-insensitively). Invisible entries are indistinguishable from missing ones.
func (q *Queries) EntriesByRef(ctx context.Context, v Visibility, field, value string) ([]uuid.UUID, error) {
	var b builder
	b.visible(v)
	b.conds = append(b.conds, "e.status NOT IN ('archived', 'superseded')")
	switch field {
	case "id":
		id, err := uuid.Parse(value)
		if err != nil {
			return nil, nil
		}
		b.and("e.id = %s", id)
	case "source_ref":
		b.and("e.source_ref = %s", value)
	case "title":
		b.and("lower(e.title) = lower(%s)", value)
	default:
		return nil, fmt.Errorf("unknown ref field %q", field)
	}
	rows, err := q.db.Query(ctx, `SELECT e.id FROM entries e JOIN spaces s ON s.id = e.space_id`+b.where()+` LIMIT 20`, b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}
