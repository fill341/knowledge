package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"kn.local/api/internal/domain"
)

const projectSelect = `SELECT p.id, s.slug, p.slug, p.name, p.description, p.repo_urls, p.metadata,
	p.created_at, p.updated_at, p.archived_at
	FROM projects p JOIN spaces s ON s.id = p.space_id`

func scanProject(row pgx.Row) (domain.Project, error) {
	var p domain.Project
	err := row.Scan(&p.ID, &p.Space, &p.Slug, &p.Name, &p.Description, &p.RepoURLs, &p.Metadata,
		&p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	p.Ref = p.Space + "/" + p.Slug
	return p, err
}

type ProjectFilter struct {
	Space           string
	RepoURL         string // normalized
	IncludeArchived bool
}

func (q *Queries) ListProjects(ctx context.Context, v Visibility, f ProjectFilter) ([]domain.Project, error) {
	var b builder
	b.visible(v)
	if f.Space != "" {
		b.and("s.slug = %s", f.Space)
	}
	if f.RepoURL != "" {
		b.and("%s = ANY(p.repo_urls)", f.RepoURL)
	}
	if !f.IncludeArchived {
		b.and("p.archived_at IS NULL")
	}
	rows, err := q.db.Query(ctx, projectSelect+b.where()+` ORDER BY s.slug, p.slug`, b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Project, error) { return scanProject(r) })
}

func (q *Queries) GetProject(ctx context.Context, v Visibility, space, slug string) (domain.Project, error) {
	var b builder
	b.visible(v)
	b.and("s.slug = %s", space)
	b.and("p.slug = %s", slug)
	return scanProject(q.db.QueryRow(ctx, projectSelect+b.where(), b.args...))
}

type ProjectInput struct {
	SpaceID     uuid.UUID
	Slug        string
	Name        string
	Description string
	RepoURLs    []string
	Metadata    json.RawMessage
}

func (q *Queries) CreateProject(ctx context.Context, in ProjectInput) (uuid.UUID, error) {
	id := uuid.Must(uuid.NewV7())
	if in.Metadata == nil {
		in.Metadata = json.RawMessage(`{}`)
	}
	_, err := q.db.Exec(ctx, `INSERT INTO projects (id, space_id, slug, name, description, repo_urls, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, in.SpaceID, in.Slug, in.Name, in.Description, nonNil(in.RepoURLs), in.Metadata)
	return id, err
}

type ProjectPatch struct {
	Name        *string
	Description *string
	RepoURLs    *[]string
	Metadata    json.RawMessage
	Archived    *bool
}

func (q *Queries) UpdateProject(ctx context.Context, id uuid.UUID, p ProjectPatch) error {
	var repo []string
	if p.RepoURLs != nil {
		repo = nonNil(*p.RepoURLs)
	}
	var meta []byte
	if p.Metadata != nil {
		meta = p.Metadata
	}
	_, err := q.db.Exec(ctx, `UPDATE projects SET
		name        = coalesce($2, name),
		description = coalesce($3, description),
		repo_urls   = coalesce($4, repo_urls),
		metadata    = coalesce($5, metadata),
		archived_at = CASE WHEN $6::boolean IS NULL THEN archived_at
		                   WHEN $6 THEN coalesce(archived_at, now()) ELSE NULL END,
		updated_at  = now()
		WHERE id = $1`, id, p.Name, p.Description, repo, meta, p.Archived)
	return err
}
