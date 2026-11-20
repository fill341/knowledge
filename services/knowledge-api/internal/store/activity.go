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

// ── Sessions ────────────────────────────────────────────────────────────────

type SessionRow struct {
	domain.Session
	SpaceID     uuid.UUID
	ProjectID   *uuid.UUID
	PrincipalID uuid.UUID
}

func (q *Queries) InsertSession(ctx context.Context, principalID, spaceID uuid.UUID, projectID *uuid.UUID, agent string, clientInfo json.RawMessage) (uuid.UUID, error) {
	id := uuid.Must(uuid.NewV7())
	if len(clientInfo) == 0 {
		clientInfo = json.RawMessage(`{}`)
	}
	_, err := q.db.Exec(ctx, `INSERT INTO agent_sessions (id, principal_id, space_id, project_id, agent, client_info)
		VALUES ($1, $2, $3, $4, $5, $6)`, id, principalID, spaceID, projectID, agent, clientInfo)
	return id, err
}

func (q *Queries) GetSession(ctx context.Context, id uuid.UUID) (SessionRow, error) {
	var r SessionRow
	var projectSlug *string
	err := q.db.QueryRow(ctx, `SELECT a.id, s.slug, p.slug, a.agent, a.client_info, a.started_at, a.ended_at,
			a.space_id, a.project_id, a.principal_id
		FROM agent_sessions a JOIN spaces s ON s.id = a.space_id LEFT JOIN projects p ON p.id = a.project_id
		WHERE a.id = $1`, id).
		Scan(&r.ID, &r.Space, &projectSlug, &r.Agent, &r.ClientInfo, &r.StartedAt, &r.EndedAt,
			&r.SpaceID, &r.ProjectID, &r.PrincipalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if projectSlug != nil {
		ref := r.Space + "/" + *projectSlug
		r.Project = &ref
	}
	return r, err
}

func (q *Queries) EndSession(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, `UPDATE agent_sessions SET ended_at = coalesce(ended_at, now()) WHERE id = $1`, id)
	return err
}

// ── Reviews ─────────────────────────────────────────────────────────────────

func (q *Queries) InsertReview(ctx context.Context, entryID uuid.UUID, version int, reviewer uuid.UUID, action string, comment *string) error {
	_, err := q.db.Exec(ctx, `INSERT INTO reviews (id, entry_id, entry_version, reviewer_id, action, comment)
		VALUES ($1, $2, $3, $4, $5, $6)`, uuid.Must(uuid.NewV7()), entryID, version, reviewer, action, comment)
	return err
}

// DueExpr is the date an entry is due for re-check (ADR-12): its explicit review_after, else its last change plus
// the kind's review_days; NULL when neither applies. DueCond selects active entries past that date.
const (
	DueExpr = `coalesce(e.review_after, e.updated_at::date + (SELECT k.review_days FROM entry_kinds k WHERE k.kind = e.kind))`
	DueCond = `e.status = 'active' AND ` + DueExpr + ` <= current_date`
)

// ReviewDue returns the re-check date of an entry, nil when it has none.
func (q *Queries) ReviewDue(ctx context.Context, id uuid.UUID) (*time.Time, error) {
	var d *time.Time
	err := q.db.QueryRow(ctx, `SELECT `+DueExpr+` FROM entries e WHERE e.id = $1`, id).Scan(&d)
	return d, err
}

// ReviewQueue lists proposed entries and active entries due for re-check.
func (q *Queries) ReviewQueue(ctx context.Context, spaces []string, limit int) ([]EntryRow, error) {
	var b builder
	b.and("s.slug = ANY(%s::text[])", nonNil(spaces))
	b.conds = append(b.conds, "(e.status = 'proposed' OR ("+DueCond+"))")
	rows, err := q.db.Query(ctx, `SELECT `+entryCols+entryFrom+b.where()+
		` ORDER BY (e.status = 'proposed') DESC, e.created_at LIMIT `+itoa(clampLimit(limit)), b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (EntryRow, error) { return scanEntryInto(r) })
}

// ── Audit ───────────────────────────────────────────────────────────────────

type AuditEvent struct {
	PrincipalID *uuid.UUID
	TokenID     *uuid.UUID
	SpaceID     *uuid.UUID
	Action      string
	TargetType  string
	TargetID    *uuid.UUID
	Details     map[string]any
}

func (q *Queries) Audit(ctx context.Context, e AuditEvent) error {
	details, err := json.Marshal(e.Details)
	if err != nil {
		return err
	}
	if e.Details == nil {
		details = []byte(`{}`)
	}
	var targetType *string
	if e.TargetType != "" {
		targetType = &e.TargetType
	}
	_, err = q.db.Exec(ctx, `INSERT INTO audit_log (principal_id, token_id, space_id, action, target_type, target_id, details)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.PrincipalID, e.TokenID, e.SpaceID, e.Action, targetType, e.TargetID, details)
	return err
}

type AuditRecord struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	Principal  *string         `json:"principal"`
	Space      *string         `json:"space"`
	Action     string          `json:"action"`
	TargetType *string         `json:"target_type"`
	TargetID   *uuid.UUID      `json:"target_id"`
	Details    json.RawMessage `json:"details"`
}

type AuditFilter struct {
	Principal string
	Action    string
	TargetID  *uuid.UUID
	Since     *time.Time
	BeforeID  int64
	Limit     int
}

func (q *Queries) ListAudit(ctx context.Context, f AuditFilter) ([]AuditRecord, error) {
	var b builder
	if f.Principal != "" {
		b.and("p.name = %s", f.Principal)
	}
	if f.Action != "" {
		b.and("a.action = %s", f.Action)
	}
	if f.TargetID != nil {
		b.and("a.target_id = %s", *f.TargetID)
	}
	if f.Since != nil {
		b.and("a.at >= %s", *f.Since)
	}
	if f.BeforeID > 0 {
		b.and("a.id < %s", f.BeforeID)
	}
	rows, err := q.db.Query(ctx, `SELECT a.id, a.at, p.name, s.slug, a.action, a.target_type, a.target_id, a.details
		FROM audit_log a LEFT JOIN principals p ON p.id = a.principal_id LEFT JOIN spaces s ON s.id = a.space_id`+
		b.where()+` ORDER BY a.id DESC LIMIT `+itoa(clampLimit(f.Limit)), b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[AuditRecord])
}

// ── Idempotency ─────────────────────────────────────────────────────────────

type IdempotentResponse struct {
	RequestHash []byte
	StatusCode  int
	Body        []byte
}

func (q *Queries) GetIdempotent(ctx context.Context, principalID uuid.UUID, key string) (IdempotentResponse, error) {
	var r IdempotentResponse
	err := q.db.QueryRow(ctx, `SELECT request_hash, status_code, response FROM idempotency_keys
		WHERE principal_id = $1 AND key = $2 AND created_at > now() - interval '24 hours'`, principalID, key).
		Scan(&r.RequestHash, &r.StatusCode, &r.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// PutIdempotent stores a response. kn_app has no UPDATE on this table (ON CONFLICT DO UPDATE
// would need it), so an expired key is deleted first and a concurrent duplicate is ignored.
func (q *Queries) PutIdempotent(ctx context.Context, principalID uuid.UUID, key string, r IdempotentResponse) error {
	if _, err := q.db.Exec(ctx, `DELETE FROM idempotency_keys WHERE principal_id = $1 AND key = $2
		AND created_at <= now() - interval '24 hours'`, principalID, key); err != nil {
		return err
	}
	_, err := q.db.Exec(ctx, `INSERT INTO idempotency_keys (principal_id, key, request_hash, status_code, response)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (principal_id, key) DO NOTHING`,
		principalID, key, r.RequestHash, r.StatusCode, r.Body)
	return err
}

func (q *Queries) PurgeIdempotent(ctx context.Context) (int64, error) {
	tag, err := q.db.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < now() - interval '24 hours'`)
	return tag.RowsAffected(), err
}

// ── Stats ───────────────────────────────────────────────────────────────────

type StatRow struct {
	Space  string
	Kind   string
	Status string
	Count  int
}

// EntryStats counts visible entries by space, kind and status.
func (q *Queries) EntryStats(ctx context.Context, v Visibility) ([]StatRow, error) {
	var b builder
	b.visible(v)
	rows, err := q.db.Query(ctx, `SELECT s.slug, e.kind, e.status, count(*)::int
		FROM entries e JOIN spaces s ON s.id = e.space_id`+b.where()+` GROUP BY 1, 2, 3`, b.args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[StatRow])
}

func (q *Queries) ReviewQueueCount(ctx context.Context, spaces []string) (int, error) {
	var n int
	err := q.db.QueryRow(ctx, `SELECT count(*)::int FROM entries e JOIN spaces s ON s.id = e.space_id
		WHERE s.slug = ANY($1::text[])
		  AND (e.status = 'proposed' OR (`+DueCond+`))`,
		nonNil(spaces)).Scan(&n)
	return n, err
}
