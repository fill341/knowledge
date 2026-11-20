package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"kn.local/api/internal/domain"
)

const entryCols = `e.id, s.slug, p.slug, e.kind, e.title, e.summary, e.body, e.attributes, e.tags, e.status,
	e.pinned, e.confidence, e.version, e.superseded_by, to_char(e.review_after, 'YYYY-MM-DD'),
	e.session_id, e.source_ref, cb.name, ub.name, e.created_at, e.updated_at,
	e.space_id, e.project_id, e.content_hash`

const entryFrom = ` FROM entries e
	JOIN spaces s ON s.id = e.space_id
	LEFT JOIN projects p ON p.id = e.project_id
	JOIN principals cb ON cb.id = e.created_by
	JOIN principals ub ON ub.id = e.updated_by`

// EntryRow is an entry plus the internal identifiers the service layer needs.
type EntryRow struct {
	domain.Entry
	SpaceID     uuid.UUID
	ProjectID   *uuid.UUID
	ContentHash []byte
}

func scanEntryInto(row pgx.Row, extra ...any) (EntryRow, error) {
	var e EntryRow
	var projectSlug *string
	dest := []any{&e.ID, &e.Space, &projectSlug, &e.Kind, &e.Title, &e.Summary, &e.Body, &e.Attributes, &e.Tags,
		&e.Status, &e.Pinned, &e.Confidence, &e.Version, &e.SupersededBy, &e.ReviewAfter,
		&e.SessionID, &e.SourceRef, &e.CreatedBy, &e.UpdatedBy, &e.CreatedAt, &e.UpdatedAt,
		&e.SpaceID, &e.ProjectID, &e.ContentHash}
	err := row.Scan(append(dest, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	if projectSlug != nil {
		ref := e.Space + "/" + *projectSlug
		e.Project = &ref
	}
	return e, err
}

// GetEntry returns an entry visible to v. forUpdate locks the row for the transaction.
func (q *Queries) GetEntry(ctx context.Context, v Visibility, id uuid.UUID, forUpdate bool) (EntryRow, error) {
	var b builder
	b.visible(v)
	b.and("e.id = %s", id)
	sql := `SELECT ` + entryCols + entryFrom + b.where()
	if forUpdate {
		sql += ` FOR UPDATE OF e`
	}
	return scanEntryInto(q.db.QueryRow(ctx, sql, b.args...))
}

type EntryFilter struct {
	Spaces       []string
	ProjectID    *uuid.UUID
	NoProject    bool // only entries without a project
	Kinds        []string
	Statuses     []string
	Tags         []string // all must match
	Pinned       *bool
	UpdatedSince *time.Time
	ExcludeIDs   []uuid.UUID
	Limit        int
	Cursor       string
}

// ListEntries pages by (updated_at DESC, id DESC).
func (q *Queries) ListEntries(ctx context.Context, v Visibility, f EntryFilter) ([]EntryRow, string, error) {
	var b builder
	b.visible(v.Narrow(f.Spaces))
	applyFilter(&b, f)
	if f.Cursor != "" {
		ts, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		b.and("(e.updated_at, e.id) < (%s, %s)", ts, id)
	}
	limit := clampLimit(f.Limit)
	sql := `SELECT ` + entryCols + entryFrom + b.where() +
		fmt.Sprintf(` ORDER BY e.updated_at DESC, e.id DESC LIMIT %d`, limit+1)
	rows, err := q.db.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, "", err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (EntryRow, error) { return scanEntryInto(r) })
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(last.UpdatedAt, last.ID)
	}
	return out, next, nil
}

func applyFilter(b *builder, f EntryFilter) {
	if f.ProjectID != nil {
		b.and("e.project_id = %s", *f.ProjectID)
	}
	if f.NoProject {
		b.and("e.project_id IS NULL")
	}
	if len(f.Kinds) > 0 {
		b.and("e.kind = ANY(%s::text[])", f.Kinds)
	}
	if len(f.Statuses) > 0 {
		b.and("e.status = ANY(%s::text[])", f.Statuses)
	}
	if len(f.Tags) > 0 {
		b.and("e.tags @> %s::text[]", f.Tags)
	}
	if f.Pinned != nil {
		b.and("e.pinned = %s", *f.Pinned)
	}
	if f.UpdatedSince != nil {
		b.and("e.updated_at >= %s", *f.UpdatedSince)
	}
	if len(f.ExcludeIDs) > 0 {
		b.and("NOT (e.id = ANY(%s::uuid[]))", f.ExcludeIDs)
	}
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > 200:
		return 200
	}
	return n
}

func encodeCursor(ts time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ts.Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c string) (time.Time, uuid.UUID, error) {
	bad := domain.BadRequest("invalid cursor")
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, bad
	}
	tsStr, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, bad
	}
	ts, err1 := time.Parse(time.RFC3339Nano, tsStr)
	id, err2 := uuid.Parse(idStr)
	if err1 != nil || err2 != nil {
		return time.Time{}, uuid.Nil, bad
	}
	return ts, id, nil
}

// EntryWrite holds all mutable columns of an entry.
type EntryWrite struct {
	SpaceID      uuid.UUID
	ProjectID    *uuid.UUID
	Kind         string
	Title        string
	Summary      *string
	Body         string
	Attributes   json.RawMessage
	Tags         []string
	Status       domain.Status
	Pinned       bool
	Confidence   *float32
	ContentHash  []byte
	SupersededBy *uuid.UUID
	ReviewAfter  *string
	SessionID    *uuid.UUID
	SourceRef    *string
	By           uuid.UUID
}

func (w EntryWrite) attrs() json.RawMessage {
	if len(w.Attributes) == 0 {
		return json.RawMessage(`{}`)
	}
	return w.Attributes
}

// InsertEntry creates an entry with a given ID (generated by the caller, or preserved on import).
func (q *Queries) InsertEntry(ctx context.Context, id uuid.UUID, w EntryWrite) error {
	_, err := q.db.Exec(ctx, `INSERT INTO entries (id, space_id, project_id, kind, title, summary, body, attributes,
		tags, status, pinned, confidence, content_hash, superseded_by, review_after, session_id, source_ref,
		created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::date, $16, $17, $18, $18)`,
		id, w.SpaceID, w.ProjectID, w.Kind, w.Title, w.Summary, w.Body, w.attrs(), nonNil(w.Tags), w.Status,
		w.Pinned, w.Confidence, w.ContentHash, w.SupersededBy, w.ReviewAfter, w.SessionID, w.SourceRef, w.By)
	return err
}

// UpdateEntry rewrites mutable columns and bumps the version; the trigger records history.
// The caller must hold the row lock and have checked expectedVersion.
func (q *Queries) UpdateEntry(ctx context.Context, id uuid.UUID, expectedVersion int, w EntryWrite) error {
	tag, err := q.db.Exec(ctx, `UPDATE entries SET project_id = $3, title = $4, summary = $5, body = $6,
		attributes = $7, tags = $8, status = $9, pinned = $10, confidence = $11, content_hash = $12,
		superseded_by = $13, review_after = $14::date, source_ref = $15, updated_by = $16,
		version = version + 1, updated_at = now()
		WHERE id = $1 AND version = $2`,
		id, expectedVersion, w.ProjectID, w.Title, w.Summary, w.Body, w.attrs(), nonNil(w.Tags), w.Status,
		w.Pinned, w.Confidence, w.ContentHash, w.SupersededBy, w.ReviewAfter, w.SourceRef, w.By)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// LiveByHash finds a non-archived, non-superseded entry with the same content hash in a space.
func (q *Queries) LiveByHash(ctx context.Context, spaceID uuid.UUID, hash []byte) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, `SELECT id FROM entries WHERE space_id = $1 AND content_hash = $2
		AND status NOT IN ('archived', 'superseded') LIMIT 1`, spaceID, hash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return id, err
}

type Similar struct {
	ID         uuid.UUID `json:"id"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Similarity float64   `json:"similarity"`
}

// SimilarTitles finds live entries of the same kind and project with a similar title.
func (q *Queries) SimilarTitles(ctx context.Context, spaceID uuid.UUID, projectID *uuid.UUID, kind, title string, exclude uuid.UUID) ([]Similar, error) {
	rows, err := q.db.Query(ctx, `SELECT id, title, status, similarity(title, $4)::float8 AS sim
		FROM entries
		WHERE space_id = $1 AND project_id IS NOT DISTINCT FROM $2 AND kind = $3 AND id <> $5
		  AND status NOT IN ('archived', 'superseded') AND similarity(title, $4) >= 0.6
		ORDER BY sim DESC LIMIT 5`, spaceID, projectID, kind, title, exclude)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Similar])
}

// ContentTwins finds live entries of a space whose body is the same text after whitespace
// normalization (kn_body_hash, migration 0004). Bodies under 200 characters never match.
func (q *Queries) ContentTwins(ctx context.Context, spaceID uuid.UUID, body string, exclude uuid.UUID) ([]Similar, error) {
	rows, err := q.db.Query(ctx, `SELECT id, title, status, 1::float8 FROM entries
		WHERE space_id = $1 AND body_hash = kn_body_hash($2) AND id <> $3
		  AND status NOT IN ('archived', 'superseded')
		ORDER BY created_at LIMIT 5`, spaceID, body, exclude)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Similar])
}

// ContentTwinsOf returns, for each given entry, the other live entries of its space with the same body hash.
func (q *Queries) ContentTwinsOf(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]Similar, error) {
	rows, err := q.db.Query(ctx, `SELECT e.id, t.id, t.title, t.status
		FROM entries e JOIN entries t ON t.space_id = e.space_id AND t.body_hash = e.body_hash AND t.id <> e.id
		WHERE e.id = ANY($1) AND e.body_hash IS NOT NULL AND t.status NOT IN ('archived', 'superseded')
		ORDER BY t.created_at`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID][]Similar{}
	for rows.Next() {
		var id uuid.UUID
		var t Similar
		if err := rows.Scan(&id, &t.ID, &t.Title, &t.Status); err != nil {
			return nil, err
		}
		t.Similarity = 1
		out[id] = append(out[id], t)
	}
	return out, rows.Err()
}

// ── Versions ────────────────────────────────────────────────────────────────

const versionSelect = `SELECT v.version, CASE WHEN p.slug IS NULL THEN NULL ELSE s.slug || '/' || p.slug END,
	v.kind, v.title, v.summary, v.body, v.attributes, v.tags, v.status, v.pinned, pr.name, v.change_note, v.created_at
	FROM entry_versions v
	JOIN entries e ON e.id = v.entry_id
	JOIN spaces s ON s.id = e.space_id
	LEFT JOIN projects p ON p.id = v.project_id
	JOIN principals pr ON pr.id = v.changed_by`

func scanVersion(row pgx.Row) (domain.EntryVersion, error) {
	var v domain.EntryVersion
	err := row.Scan(&v.Version, &v.Project, &v.Kind, &v.Title, &v.Summary, &v.Body, &v.Attributes, &v.Tags,
		&v.Status, &v.Pinned, &v.ChangedBy, &v.ChangeNote, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// Versions lists history of an entry. Visibility is checked by the caller via GetEntry.
func (q *Queries) Versions(ctx context.Context, entryID uuid.UUID) ([]domain.EntryVersion, error) {
	rows, err := q.db.Query(ctx, versionSelect+` WHERE v.entry_id = $1 ORDER BY v.version DESC`, entryID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.EntryVersion, error) { return scanVersion(r) })
}

func (q *Queries) Version(ctx context.Context, entryID uuid.UUID, version int) (domain.EntryVersion, error) {
	return scanVersion(q.db.QueryRow(ctx, versionSelect+` WHERE v.entry_id = $1 AND v.version = $2`, entryID, version))
}

// VersionProjectID returns the raw project_id stored in a version (needed for revert).
func (q *Queries) VersionProjectID(ctx context.Context, entryID uuid.UUID, version int) (*uuid.UUID, error) {
	var id *uuid.UUID
	err := q.db.QueryRow(ctx, `SELECT project_id FROM entry_versions WHERE entry_id = $1 AND version = $2`,
		entryID, version).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return id, err
}

// ── Links ───────────────────────────────────────────────────────────────────

func (q *Queries) InsertLink(ctx context.Context, from, to uuid.UUID, rel domain.LinkRel, by uuid.UUID) error {
	_, err := q.db.Exec(ctx, `INSERT INTO entry_links (from_id, to_id, rel, created_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, from, to, rel, by)
	return err
}

func (q *Queries) DeleteLink(ctx context.Context, from, to uuid.UUID, rel domain.LinkRel) (bool, error) {
	tag, err := q.db.Exec(ctx, `DELETE FROM entry_links WHERE from_id = $1 AND to_id = $2 AND rel = $3`, from, to, rel)
	return tag.RowsAffected() > 0, err
}

// Links returns outgoing and incoming links where the other end is visible.
func (q *Queries) Links(ctx context.Context, v Visibility, id uuid.UUID) (out, in []domain.Link, err error) {
	var b builder
	idArg := b.arg(id)
	b.visible(v)
	where := b.where() // visibility of the other end, may be empty
	scan := func(sql string) ([]domain.Link, error) {
		rows, err := q.db.Query(ctx, sql, b.args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Link, error) {
			var l domain.Link
			err := r.Scan(&l.FromID, &l.ToID, &l.Rel, &l.Title, &l.CreatedAt)
			return l, err
		})
	}
	out, err = scan(`SELECT l.from_id, l.to_id, l.rel, e.title, l.created_at FROM entry_links l
		JOIN entries e ON e.id = l.to_id AND l.from_id = ` + idArg + `
		JOIN spaces s ON s.id = e.space_id` + where + ` ORDER BY l.created_at`)
	if err != nil {
		return nil, nil, err
	}
	in, err = scan(`SELECT l.from_id, l.to_id, l.rel, e.title, l.created_at FROM entry_links l
		JOIN entries e ON e.id = l.from_id AND l.to_id = ` + idArg + `
		JOIN spaces s ON s.id = e.space_id` + where + ` ORDER BY l.created_at`)
	return out, in, err
}

// openHandoffCond selects unfinished session logs (status in_progress or blocked) that no later session log of
// the space lists in "continues" (ADR-11).
const openHandoffCond = `e.kind = 'session_log' AND e.status = 'active'
	AND e.attributes->>'status' IN ('in_progress', 'blocked')
	AND NOT EXISTS (SELECT 1 FROM entries c WHERE c.kind = 'session_log' AND c.space_id = e.space_id
		AND c.attributes->'continues' @> jsonb_build_array(e.id::text))`

// OpenHandoffs lists the visible unfinished work of a project created after `since`, newest first.
func (q *Queries) OpenHandoffs(ctx context.Context, v Visibility, projectID uuid.UUID, since time.Time, limit int) ([]EntryRow, error) {
	var b builder
	b.visible(v)
	b.and("e.project_id = %s", projectID)
	b.and("e.created_at >= %s", since)
	b.conds = append(b.conds, openHandoffCond)
	rows, err := q.db.Query(ctx, `SELECT `+entryCols+entryFrom+b.where()+
		` ORDER BY e.created_at DESC LIMIT `+itoa(clampLimit(limit)), b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (EntryRow, error) { return scanEntryInto(r) })
}

// Dependent is an entry that relies on another one, directly (depth 1) or through a chain of links.
type Dependent struct {
	HealthItem
	Depth int    `json:"depth"`
	Rel   string `json:"rel"`
	Via   string `json:"via"` // title of the entry it links to
}

// Dependents walks incoming depends_on / references / derived_from links up to maxDepth steps (impact analysis).
// Every step must be visible, so a hidden entry never leaks through a chain. Retired (archived, superseded) entries
// are neither listed nor walked through: reliance on retired knowledge is the health check depends_on_retired.
func (q *Queries) Dependents(ctx context.Context, v Visibility, id uuid.UUID, maxDepth int) ([]Dependent, error) {
	var b builder
	idArg := b.arg(id)
	depthArg := b.arg(maxDepth)
	b.visible(v)
	vis := "true"
	if len(b.conds) > 0 {
		vis = strings.Join(b.conds, " AND ")
	}
	sql := `WITH RECURSIVE dep(id, depth, rel, via) AS (
			SELECT l.from_id, 1, l.rel, l.to_id FROM entry_links l
			WHERE l.to_id = ` + idArg + ` AND l.rel IN ('depends_on', 'references', 'derived_from')
			UNION
			SELECT l.from_id, d.depth + 1, l.rel, l.to_id FROM entry_links l
			JOIN dep d ON l.to_id = d.id
			JOIN entries e ON e.id = d.id JOIN spaces s ON s.id = e.space_id
			WHERE d.depth < ` + depthArg + ` AND l.rel IN ('depends_on', 'references', 'derived_from') AND ` + vis + `
			  AND e.status NOT IN ('archived', 'superseded')
		)
		SELECT DISTINCT ON (e.id) ` + healthItemCols + `, d.depth, d.rel, coalesce(t.title, '')` + healthItemFrom + `
		JOIN dep d ON d.id = e.id LEFT JOIN entries t ON t.id = d.via
		WHERE e.id <> ` + idArg + ` AND e.status NOT IN ('archived', 'superseded') AND ` + vis + `
		ORDER BY e.id, d.depth`
	rows, err := q.db.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dependent
	for rows.Next() {
		var d Dependent
		if d.HealthItem, err = scanHealthItem(rows, &d.Depth, &d.Rel, &d.Via); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Depth < out[j].Depth || (out[i].Depth == out[j].Depth && out[i].Title < out[j].Title)
	})
	return out, rows.Err()
}
