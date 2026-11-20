package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"kn.local/api/internal/domain"
)

// ── Principals ──────────────────────────────────────────────────────────────

const principalCols = `id, kind, name, description, created_at, disabled_at`

func scanPrincipal(row pgx.Row) (domain.Principal, error) {
	var p domain.Principal
	err := row.Scan(&p.ID, &p.Kind, &p.Name, &p.Description, &p.CreatedAt, &p.DisabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (q *Queries) CreatePrincipal(ctx context.Context, kind domain.PrincipalKind, name, description string) (domain.Principal, error) {
	return scanPrincipal(q.db.QueryRow(ctx,
		`INSERT INTO principals (id, kind, name, description) VALUES ($1, $2, $3, $4) RETURNING `+principalCols,
		uuid.Must(uuid.NewV7()), kind, name, description))
}

func (q *Queries) PrincipalByName(ctx context.Context, name string) (domain.Principal, error) {
	return scanPrincipal(q.db.QueryRow(ctx, `SELECT `+principalCols+` FROM principals WHERE name = $1`, name))
}

func (q *Queries) ListPrincipals(ctx context.Context) ([]domain.Principal, error) {
	rows, err := q.db.Query(ctx, `SELECT `+principalCols+` FROM principals ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Principal, error) { return scanPrincipal(r) })
}

// ── Tokens ──────────────────────────────────────────────────────────────────

type TokenRecord struct {
	ID         uuid.UUID  `json:"id"`
	Principal  string     `json:"principal"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// AuthRecord is what the auth middleware needs to verify a token.
type AuthRecord struct {
	TokenID    uuid.UUID
	SecretHash []byte
	Scopes     []string
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	Principal  domain.Principal
}

func (q *Queries) TokenForAuth(ctx context.Context, prefix string) (AuthRecord, error) {
	var a AuthRecord
	p := &a.Principal
	err := q.db.QueryRow(ctx, `
		SELECT t.id, t.secret_hash, t.scopes, t.expires_at, t.revoked_at, t.last_used_at,
		       p.id, p.kind, p.name, p.description, p.created_at, p.disabled_at
		FROM api_tokens t JOIN principals p ON p.id = t.principal_id
		WHERE t.prefix = $1`, prefix).
		Scan(&a.TokenID, &a.SecretHash, &a.Scopes, &a.ExpiresAt, &a.RevokedAt, &a.LastUsedAt,
			&p.ID, &p.Kind, &p.Name, &p.Description, &p.CreatedAt, &p.DisabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// TouchToken records usage at most once a minute to avoid a write per request.
func (q *Queries) TouchToken(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, `UPDATE api_tokens SET last_used_at = now()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, id)
	return err
}

func (q *Queries) CreateToken(ctx context.Context, principalID uuid.UUID, name, prefix string, hash []byte, scopes []string, expiresAt *time.Time) (uuid.UUID, error) {
	id := uuid.Must(uuid.NewV7())
	_, err := q.db.Exec(ctx, `INSERT INTO api_tokens (id, principal_id, name, prefix, secret_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, principalID, name, prefix, hash, scopes, expiresAt)
	return id, err
}

const tokenCols = `t.id, p.name, t.name, t.prefix, t.scopes, t.expires_at, t.last_used_at, t.created_at, t.revoked_at`

func scanToken(row pgx.Row) (TokenRecord, error) {
	var t TokenRecord
	err := row.Scan(&t.ID, &t.Principal, &t.Name, &t.Prefix, &t.Scopes, &t.ExpiresAt, &t.LastUsedAt, &t.CreatedAt, &t.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (q *Queries) ListTokens(ctx context.Context) ([]TokenRecord, error) {
	rows, err := q.db.Query(ctx, `SELECT `+tokenCols+` FROM api_tokens t JOIN principals p ON p.id = t.principal_id
		ORDER BY t.created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TokenRecord, error) { return scanToken(r) })
}

func (q *Queries) RevokeToken(ctx context.Context, id uuid.UUID) (TokenRecord, error) {
	return scanToken(q.db.QueryRow(ctx, `
		WITH t AS (UPDATE api_tokens SET revoked_at = coalesce(revoked_at, now()) WHERE id = $1 RETURNING *)
		SELECT `+tokenCols+` FROM t JOIN principals p ON p.id = t.principal_id`, id))
}

// ── Spaces ──────────────────────────────────────────────────────────────────

func scanSpace(row pgx.Row) (domain.Space, error) {
	var s domain.Space
	var settings []byte
	err := row.Scan(&s.ID, &s.Slug, &s.Name, &settings)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(settings, &s.Settings)
	return s, err
}

func (q *Queries) ListSpaces(ctx context.Context, v Visibility) ([]domain.Space, error) {
	var b builder
	b.visible(v)
	rows, err := q.db.Query(ctx, `SELECT s.id, s.slug, s.name, s.settings FROM spaces s`+b.where()+` ORDER BY s.slug`, b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Space, error) { return scanSpace(r) })
}

func (q *Queries) SpaceBySlug(ctx context.Context, slug string) (domain.Space, error) {
	return scanSpace(q.db.QueryRow(ctx, `SELECT id, slug, name, settings FROM spaces WHERE slug = $1`, slug))
}

// ── Kinds ───────────────────────────────────────────────────────────────────

func scanKind(row pgx.Row) (domain.Kind, error) {
	var k domain.Kind
	err := row.Scan(&k.Kind, &k.Description, &k.AttributesSchema, &k.AgentRequiresReview, &k.ReviewDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

func (q *Queries) Kind(ctx context.Context, kind string) (domain.Kind, error) {
	return scanKind(q.db.QueryRow(ctx,
		`SELECT kind, description, attributes_schema, agent_requires_review, review_days FROM entry_kinds WHERE kind = $1`, kind))
}

func (q *Queries) ListKinds(ctx context.Context) ([]domain.Kind, error) {
	rows, err := q.db.Query(ctx, `SELECT kind, description, attributes_schema, agent_requires_review, review_days FROM entry_kinds ORDER BY kind`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Kind, error) { return scanKind(r) })
}

func (q *Queries) UpsertKind(ctx context.Context, k domain.Kind) (domain.Kind, error) {
	return scanKind(q.db.QueryRow(ctx, `
		INSERT INTO entry_kinds (kind, description, attributes_schema, agent_requires_review, review_days) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (kind) DO UPDATE SET description = EXCLUDED.description,
			attributes_schema = EXCLUDED.attributes_schema, agent_requires_review = EXCLUDED.agent_requires_review,
			review_days = EXCLUDED.review_days
		RETURNING kind, description, attributes_schema, agent_requires_review, review_days`,
		k.Kind, k.Description, k.AttributesSchema, k.AgentRequiresReview, k.ReviewDays))
}

func (q *Queries) SetPrincipalDisabled(ctx context.Context, name string, disabled bool) (domain.Principal, error) {
	return scanPrincipal(q.db.QueryRow(ctx, `UPDATE principals
		SET disabled_at = CASE WHEN $2 THEN coalesce(disabled_at, now()) ELSE NULL END
		WHERE name = $1 RETURNING `+principalCols, name, disabled))
}
