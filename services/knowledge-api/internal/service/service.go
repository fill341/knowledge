// Package service implements the Knowledge API use cases on top of the store:
// authorization by space, the write-time quality pipeline, entry lifecycle, search and context.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"kn.local/api/internal/auth"
	"kn.local/api/internal/domain"
	"kn.local/api/internal/quality"
	"kn.local/api/internal/store"
)

type Service struct {
	store   *store.Store
	schemas *quality.Schemas
	limiter *limiter
	// WriteLimitPerMinute caps writes per token to stop a looping agent.
	WriteLimitPerMinute int
}

func New(st *store.Store) *Service {
	return &Service{
		store:               st,
		schemas:             quality.NewSchemas(),
		limiter:             &limiter{buckets: map[uuid.UUID]*bucket{}},
		WriteLimitPerMinute: 60,
	}
}

func (s *Service) Store() *store.Store { return s.store }

// Caller is the authenticated identity behind a request.
type Caller struct {
	Principal domain.Principal
	TokenID   uuid.UUID
	Scope     domain.Scope
}

func (c Caller) actor(space string) domain.Actor {
	return domain.Actor{Principal: c.Principal, Level: c.Scope.Level(space)}
}

func (c Caller) visibility() store.Visibility { return store.VisibilityOf(c.Scope) }

func (c Caller) audit(action, targetType string, spaceID, targetID *uuid.UUID, details map[string]any) store.AuditEvent {
	pid, tid := c.Principal.ID, c.TokenID
	ev := store.AuditEvent{PrincipalID: &pid, Action: action, TargetType: targetType, SpaceID: spaceID, TargetID: targetID, Details: details}
	if tid != uuid.Nil {
		ev.TokenID = &tid
	}
	return ev
}

// Authenticate verifies a raw bearer token.
func (s *Service) Authenticate(ctx context.Context, raw string) (Caller, error) {
	unauth := domain.Unauthenticated("invalid or expired token")
	tok, err := auth.Parse(raw)
	if err != nil {
		return Caller{}, unauth
	}
	q := s.store.Q()
	rec, err := q.TokenForAuth(ctx, tok.Prefix)
	if errors.Is(err, store.ErrNotFound) {
		return Caller{}, unauth
	}
	if err != nil {
		return Caller{}, err
	}
	now := time.Now()
	if !auth.Verify(tok.Secret, rec.SecretHash) || rec.RevokedAt != nil ||
		(rec.ExpiresAt != nil && now.After(*rec.ExpiresAt)) || rec.Principal.DisabledAt != nil {
		return Caller{}, unauth
	}
	scope, err := domain.ParseScopes(rec.Scopes)
	if err != nil {
		return Caller{}, fmt.Errorf("stored token %s has invalid scopes: %w", tok.Prefix, err)
	}
	if rec.LastUsedAt == nil || now.Sub(*rec.LastUsedAt) > time.Minute {
		_ = q.TouchToken(ctx, rec.TokenID)
	}
	return Caller{Principal: rec.Principal, TokenID: rec.TokenID, Scope: scope}, nil
}

// AuditAuthFailure records a failed authentication attempt (prefix only, never the secret).
func (s *Service) AuditAuthFailure(ctx context.Context, raw string) {
	details := map[string]any{}
	if tok, err := auth.Parse(raw); err == nil {
		details["prefix"] = tok.Prefix
	}
	_ = s.store.Q().Audit(ctx, store.AuditEvent{Action: "auth.failed", Details: details})
}

// ── Me and spaces ─────────────────────────────────────────────────────────────

type SpaceAccess struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Level string `json:"level"`
}

type Me struct {
	Principal domain.Principal `json:"principal"`
	Scopes    []string         `json:"scopes"`
	Spaces    []SpaceAccess    `json:"spaces"`
}

func (s *Service) Me(ctx context.Context, c Caller) (Me, error) {
	spaces, err := s.Spaces(ctx, c)
	if err != nil {
		return Me{}, err
	}
	return Me{Principal: c.Principal, Scopes: c.Scope.Strings(), Spaces: spaces}, nil
}

func (s *Service) Spaces(ctx context.Context, c Caller) ([]SpaceAccess, error) {
	spaces, err := s.store.Q().ListSpaces(ctx, c.visibility())
	if err != nil {
		return nil, err
	}
	out := make([]SpaceAccess, 0, len(spaces))
	for _, sp := range spaces {
		out = append(out, SpaceAccess{Slug: sp.Slug, Name: sp.Name, Level: c.Scope.Level(sp.Slug).String()})
	}
	return out, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// requireSpace loads a space and checks the caller's level in it.
// Unreadable spaces yield forbidden_space rather than not_found: the caller named it explicitly.
func requireSpace(ctx context.Context, q *store.Queries, c Caller, slug string, lvl domain.Level) (domain.Space, error) {
	if slug == "" {
		return domain.Space{}, domain.Validation("space is required")
	}
	if !c.Scope.Can(slug, lvl) {
		return domain.Space{}, domain.ForbiddenSpace(slug)
	}
	sp, err := q.SpaceBySlug(ctx, slug)
	if errors.Is(err, store.ErrNotFound) {
		return sp, domain.NotFound("space %q not found", slug)
	}
	return sp, err
}

// ParseProjectRef splits "<space>/<slug>".
func ParseProjectRef(ref string) (space, slug string, err error) {
	space, slug, ok := strings.Cut(ref, "/")
	if !ok || space == "" || slug == "" || strings.Contains(slug, "/") {
		return "", "", domain.Validation("project must be <space>/<slug>, got %q", ref)
	}
	return space, slug, nil
}

// resolveProject loads a project by ref, checking the caller's level in its space.
func resolveProject(ctx context.Context, q *store.Queries, c Caller, ref string, lvl domain.Level) (domain.Project, domain.Space, error) {
	spSlug, slug, err := ParseProjectRef(ref)
	if err != nil {
		return domain.Project{}, domain.Space{}, err
	}
	sp, err := requireSpace(ctx, q, c, spSlug, lvl)
	if err != nil {
		return domain.Project{}, sp, err
	}
	p, err := q.GetProject(ctx, c.visibility(), spSlug, slug)
	if errors.Is(err, store.ErrNotFound) {
		return p, sp, domain.NotFound("project %q not found", ref)
	}
	return p, sp, err
}

// notFoundEntry hides the existence of entries in spaces the caller cannot read.
func notFoundEntry(id uuid.UUID) error { return domain.NotFound("entry %s not found", id) }

func (s *Service) getEntry(ctx context.Context, q *store.Queries, c Caller, id uuid.UUID, forUpdate bool) (store.EntryRow, error) {
	e, err := q.GetEntry(ctx, c.visibility(), id, forUpdate)
	if errors.Is(err, store.ErrNotFound) {
		return e, notFoundEntry(id)
	}
	return e, err
}

// ── Rate limiting ─────────────────────────────────────────────────────────────

type limiter struct {
	mu      sync.Mutex
	buckets map[uuid.UUID]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// allow implements a token bucket refilled at perMinute/min with burst perMinute.
func (l *limiter) allow(key uuid.UUID, perMinute int) bool {
	if perMinute <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(perMinute), last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Minutes() * float64(perMinute)
	if b.tokens > float64(perMinute) {
		b.tokens = float64(perMinute)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (s *Service) checkWriteRate(c Caller) error {
	if !s.limiter.allow(c.TokenID, s.WriteLimitPerMinute) {
		return domain.RateLimited("write limit of %d per minute exceeded", s.WriteLimitPerMinute)
	}
	return nil
}
