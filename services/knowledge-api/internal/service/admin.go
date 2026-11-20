package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"kn.local/api/internal/auth"
	"kn.local/api/internal/domain"
	"kn.local/api/internal/quality"
	"kn.local/api/internal/store"
)

func requireAdmin(c Caller) error {
	if !c.Scope.Admin {
		return domain.Forbidden("admin permission required")
	}
	return nil
}

var principalNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._@-]{0,63}$`)

type CreatePrincipalInput struct {
	Kind        domain.PrincipalKind `json:"kind"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
}

func (s *Service) ListPrincipals(ctx context.Context, c Caller) ([]domain.Principal, error) {
	if err := requireAdmin(c); err != nil {
		return nil, err
	}
	return s.store.Q().ListPrincipals(ctx)
}

func (s *Service) CreatePrincipal(ctx context.Context, c Caller, in CreatePrincipalInput) (domain.Principal, error) {
	if err := requireAdmin(c); err != nil {
		return domain.Principal{}, err
	}
	var out domain.Principal
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		out, err = createPrincipal(ctx, q, in)
		if err != nil {
			return err
		}
		return q.Audit(ctx, c.audit("principal.create", "principal", nil, &out.ID, map[string]any{"name": out.Name}))
	})
	return out, err
}

func createPrincipal(ctx context.Context, q *store.Queries, in CreatePrincipalInput) (domain.Principal, error) {
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if !principalNameRe.MatchString(in.Name) {
		return domain.Principal{}, domain.Validation("principal name must match %s", principalNameRe)
	}
	switch in.Kind {
	case domain.PrincipalUser, domain.PrincipalAgent, domain.PrincipalSystem:
	case "":
		in.Kind = domain.PrincipalAgent
	default:
		return domain.Principal{}, domain.Validation("kind must be user, agent or system")
	}
	p, err := q.CreatePrincipal(ctx, in.Kind, in.Name, in.Description)
	if store.IsUniqueViolation(err, "") {
		return p, domain.Conflict("principal %q already exists", in.Name)
	}
	return p, err
}

type CreateTokenInput struct {
	Principal string   `json:"principal"`
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	// ExpiresInDays = 0 means no expiry.
	ExpiresInDays int `json:"expires_in_days"`
	// CreatePrincipal creates an agent principal with this name if missing.
	CreatePrincipal bool `json:"create_principal"`
}

type IssuedToken struct {
	store.TokenRecord
	// Token is shown exactly once.
	Token string `json:"token"`
}

func (s *Service) CreateToken(ctx context.Context, c Caller, in CreateTokenInput) (IssuedToken, error) {
	if err := requireAdmin(c); err != nil {
		return IssuedToken{}, err
	}
	var out IssuedToken
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		out, err = IssueToken(ctx, q, in)
		if err != nil {
			return err
		}
		return q.Audit(ctx, c.audit("token.create", "token", nil, &out.ID,
			map[string]any{"principal": out.Principal, "prefix": out.Prefix, "scopes": out.Scopes}))
	})
	return out, err
}

// IssueToken creates a token without an authenticated caller; it is also used by the
// `kn-api token create` bootstrap command to mint the first admin token.
func IssueToken(ctx context.Context, q *store.Queries, in CreateTokenInput) (IssuedToken, error) {
	scope, err := domain.ParseScopes(in.Scopes)
	if err != nil {
		return IssuedToken{}, domain.Validation("%s", err)
	}
	if len(in.Scopes) == 0 {
		return IssuedToken{}, domain.Validation("at least one scope is required")
	}
	for sp := range scope.Spaces {
		if _, err := q.SpaceBySlug(ctx, sp); errors.Is(err, store.ErrNotFound) {
			return IssuedToken{}, domain.Validation("unknown space %q in scopes", sp)
		} else if err != nil {
			return IssuedToken{}, err
		}
	}
	p, err := q.PrincipalByName(ctx, strings.ToLower(in.Principal))
	if errors.Is(err, store.ErrNotFound) && in.CreatePrincipal {
		p, err = createPrincipal(ctx, q, CreatePrincipalInput{Kind: domain.PrincipalAgent, Name: in.Principal})
	}
	if errors.Is(err, store.ErrNotFound) {
		return IssuedToken{}, domain.NotFound("principal %q not found", in.Principal)
	} else if err != nil {
		return IssuedToken{}, err
	}
	if p.IsAgent() && (scope.Admin || anyLevel(scope, domain.LevelReview)) {
		return IssuedToken{}, domain.Validation("agent principals cannot hold review or admin scopes")
	}
	tok, err := auth.NewToken()
	if err != nil {
		return IssuedToken{}, err
	}
	var expires *time.Time
	if in.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		expires = &t
	}
	name := in.Name
	if name == "" {
		name = p.Name
	}
	id, err := q.CreateToken(ctx, p.ID, name, tok.Prefix, tok.Hash(), scope.Strings(), expires)
	if err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{
		TokenRecord: store.TokenRecord{ID: id, Principal: p.Name, Name: name, Prefix: tok.Prefix,
			Scopes: scope.Strings(), ExpiresAt: expires, CreatedAt: time.Now()},
		Token: tok.String(),
	}, nil
}

func anyLevel(s domain.Scope, lvl domain.Level) bool {
	for _, l := range s.Spaces {
		if l >= lvl {
			return true
		}
	}
	return false
}

func (s *Service) ListTokens(ctx context.Context, c Caller) ([]store.TokenRecord, error) {
	if err := requireAdmin(c); err != nil {
		return nil, err
	}
	return s.store.Q().ListTokens(ctx)
}

func (s *Service) RevokeToken(ctx context.Context, c Caller, id uuid.UUID) (store.TokenRecord, error) {
	if err := requireAdmin(c); err != nil {
		return store.TokenRecord{}, err
	}
	var out store.TokenRecord
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		out, err = q.RevokeToken(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return domain.NotFound("token %s not found", id)
		} else if err != nil {
			return err
		}
		return q.Audit(ctx, c.audit("token.revoke", "token", nil, &id, map[string]any{"prefix": out.Prefix}))
	})
	return out, err
}

func (s *Service) Audit(ctx context.Context, c Caller, f store.AuditFilter) ([]store.AuditRecord, error) {
	if err := requireAdmin(c); err != nil {
		return nil, err
	}
	return s.store.Q().ListAudit(ctx, f)
}

func (s *Service) ListKinds(ctx context.Context) ([]domain.Kind, error) {
	return s.store.Q().ListKinds(ctx)
}

type PutKindInput struct {
	Description         string          `json:"description"`
	AttributesSchema    json.RawMessage `json:"attributes_schema"`
	AgentRequiresReview bool            `json:"agent_requires_review"`
	ReviewDays          *int            `json:"review_days"` // null: no automatic re-check
}

var kindRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

func (s *Service) PutKind(ctx context.Context, c Caller, kind string, in PutKindInput) (domain.Kind, error) {
	if err := requireAdmin(c); err != nil {
		return domain.Kind{}, err
	}
	if !kindRe.MatchString(kind) {
		return domain.Kind{}, domain.Validation("kind must match %s", kindRe)
	}
	if strings.TrimSpace(in.Description) == "" {
		return domain.Kind{}, domain.Validation("description is required")
	}
	if len(in.AttributesSchema) == 0 {
		in.AttributesSchema = json.RawMessage(`{"type": "object"}`)
	}
	if in.ReviewDays != nil && (*in.ReviewDays < 1 || *in.ReviewDays > 3650) {
		return domain.Kind{}, domain.Validation("review_days must be between 1 and 3650")
	}
	if err := quality.CompileCheck(in.AttributesSchema); err != nil {
		return domain.Kind{}, domain.Validation("attributes_schema is not a valid JSON Schema: %s", err)
	}
	var out domain.Kind
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		out, err = q.UpsertKind(ctx, domain.Kind{Kind: kind, Description: in.Description,
			AttributesSchema: in.AttributesSchema, AgentRequiresReview: in.AgentRequiresReview, ReviewDays: in.ReviewDays})
		if err != nil {
			return err
		}
		return q.Audit(ctx, c.audit("kind.put", "kind", nil, nil, map[string]any{"kind": kind}))
	})
	return out, err
}

// SetPrincipalDisabled blocks or unblocks every token of a principal at once.
func (s *Service) SetPrincipalDisabled(ctx context.Context, c Caller, name string, disabled bool) (domain.Principal, error) {
	if err := requireAdmin(c); err != nil {
		return domain.Principal{}, err
	}
	if disabled && name == c.Principal.Name {
		return domain.Principal{}, domain.Validation("you cannot disable yourself")
	}
	var out domain.Principal
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		out, err = q.SetPrincipalDisabled(ctx, name, disabled)
		if errors.Is(err, store.ErrNotFound) {
			return domain.NotFound("principal %q not found", name)
		} else if err != nil {
			return err
		}
		action := "principal.enable"
		if disabled {
			action = "principal.disable"
		}
		return q.Audit(ctx, c.audit(action, "principal", nil, &out.ID, map[string]any{"name": name}))
	})
	return out, err
}
