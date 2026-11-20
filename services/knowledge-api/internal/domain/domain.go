// Package domain holds the core types and rules: entry statuses, allowed transitions,
// and who may perform them. It has no I/O.
package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type PrincipalKind string

const (
	PrincipalUser   PrincipalKind = "user"
	PrincipalAgent  PrincipalKind = "agent"
	PrincipalSystem PrincipalKind = "system"
)

type Principal struct {
	ID          uuid.UUID     `json:"id"`
	Kind        PrincipalKind `json:"kind"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	CreatedAt   time.Time     `json:"created_at"`
	DisabledAt  *time.Time    `json:"disabled_at,omitempty"`
}

func (p Principal) IsAgent() bool { return p.Kind == PrincipalAgent }

type Space struct {
	ID       uuid.UUID     `json:"id"`
	Slug     string        `json:"slug"`
	Name     string        `json:"name"`
	Settings SpaceSettings `json:"settings"`
}

type SpaceSettings struct {
	// ReviewKinds overrides entry_kinds.agent_requires_review for this space when non-nil.
	ReviewKinds []string `json:"review_kinds,omitempty"`
	// AgentAlwaysReview sends every agent-written entry in this space to review.
	AgentAlwaysReview bool `json:"agent_always_review,omitempty"`
}

type Project struct {
	ID          uuid.UUID       `json:"id"`
	Space       string          `json:"space"`
	Slug        string          `json:"slug"`
	Ref         string          `json:"ref"` // "<space>/<slug>"
	Name        string          `json:"name"`
	Description string          `json:"description"`
	RepoURLs    []string        `json:"repo_urls"`
	Metadata    json.RawMessage `json:"metadata"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	ArchivedAt  *time.Time      `json:"archived_at,omitempty"`
}

type Kind struct {
	Kind                string          `json:"kind"`
	Description         string          `json:"description"`
	AttributesSchema    json.RawMessage `json:"attributes_schema"`
	AgentRequiresReview bool            `json:"agent_requires_review"`
	// ReviewDays: active entries of this kind are due for re-check that many days after their last change (ADR-12).
	ReviewDays *int `json:"review_days"`
}

type Status string

const (
	StatusDraft      Status = "draft"
	StatusProposed   Status = "proposed"
	StatusActive     Status = "active"
	StatusDeprecated Status = "deprecated"
	StatusSuperseded Status = "superseded"
	StatusArchived   Status = "archived"
)

func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusProposed, StatusActive, StatusDeprecated, StatusSuperseded, StatusArchived:
		return true
	}
	return false
}

// DefaultSearchStatuses are visible in search unless the caller filters explicitly.
var DefaultSearchStatuses = []Status{StatusActive, StatusProposed, StatusDeprecated}

type Entry struct {
	ID           uuid.UUID       `json:"id"`
	Space        string          `json:"space"`
	Project      *string         `json:"project"` // "<space>/<slug>"
	Kind         string          `json:"kind"`
	Title        string          `json:"title"`
	Summary      *string         `json:"summary"`
	Body         string          `json:"body"`
	Attributes   json.RawMessage `json:"attributes"`
	Tags         []string        `json:"tags"`
	Status       Status          `json:"status"`
	Pinned       bool            `json:"pinned"`
	Confidence   *float32        `json:"confidence"`
	Version      int             `json:"version"`
	SupersededBy *uuid.UUID      `json:"superseded_by"`
	ReviewAfter  *string         `json:"review_after"` // YYYY-MM-DD
	SessionID    *uuid.UUID      `json:"session_id"`
	SourceRef    *string         `json:"source_ref"`
	CreatedBy    string          `json:"created_by"` // principal name
	UpdatedBy    string          `json:"updated_by"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type EntryVersion struct {
	Version    int             `json:"version"`
	Project    *string         `json:"project"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	Summary    *string         `json:"summary"`
	Body       string          `json:"body"`
	Attributes json.RawMessage `json:"attributes"`
	Tags       []string        `json:"tags"`
	Status     Status          `json:"status"`
	Pinned     bool            `json:"pinned"`
	ChangedBy  string          `json:"changed_by"`
	ChangeNote *string         `json:"change_note"`
	CreatedAt  time.Time       `json:"created_at"`
}

type LinkRel string

var LinkRels = map[LinkRel]bool{
	"relates_to": true, "depends_on": true, "supersedes": true,
	"derived_from": true, "contradicts": true, "references": true,
}

type Link struct {
	FromID    uuid.UUID `json:"from_id"`
	ToID      uuid.UUID `json:"to_id"`
	Rel       LinkRel   `json:"rel"`
	Title     string    `json:"title"` // title of the entry on the other end
	CreatedAt time.Time `json:"created_at"`
}

type Session struct {
	ID         uuid.UUID       `json:"id"`
	Space      string          `json:"space"`
	Project    *string         `json:"project"`
	Agent      string          `json:"agent"`
	ClientInfo json.RawMessage `json:"client_info"`
	StartedAt  time.Time       `json:"started_at"`
	EndedAt    *time.Time      `json:"ended_at"`
}
