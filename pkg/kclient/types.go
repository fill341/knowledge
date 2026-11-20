package kclient

import (
	"encoding/json"
	"time"
)

// Types mirror the REST v1 JSON contract (docs/api.md). Unknown fields are ignored on decode,
// so the client keeps working when the server adds fields.

type Principal struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type SpaceAccess struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Level string `json:"level"`
}

type Me struct {
	Principal Principal     `json:"principal"`
	Scopes    []string      `json:"scopes"`
	Spaces    []SpaceAccess `json:"spaces"`
}

type Kind struct {
	Kind                string          `json:"kind"`
	Description         string          `json:"description"`
	AttributesSchema    json.RawMessage `json:"attributes_schema"`
	AgentRequiresReview bool            `json:"agent_requires_review"`
	ReviewDays          *int            `json:"review_days"`
}

type Project struct {
	ID          string   `json:"id"`
	Space       string   `json:"space"`
	Slug        string   `json:"slug"`
	Ref         string   `json:"ref"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	RepoURLs    []string `json:"repo_urls"`
}

type Entry struct {
	ID           string          `json:"id"`
	Space        string          `json:"space"`
	Project      *string         `json:"project"`
	Kind         string          `json:"kind"`
	Title        string          `json:"title"`
	Summary      *string         `json:"summary"`
	Body         string          `json:"body"`
	Attributes   json.RawMessage `json:"attributes"`
	Tags         []string        `json:"tags"`
	Status       string          `json:"status"`
	Pinned       bool            `json:"pinned"`
	Confidence   *float32        `json:"confidence"`
	Version      int             `json:"version"`
	SupersededBy *string         `json:"superseded_by"`
	SourceRef    *string         `json:"source_ref"`
	CreatedBy    string          `json:"created_by"`
	UpdatedBy    string          `json:"updated_by"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type Link struct {
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
	Rel    string `json:"rel"`
	Title  string `json:"title"`
}

type EntryDetail struct {
	Entry
	Links struct {
		Outgoing []Link `json:"outgoing"`
		Incoming []Link `json:"incoming"`
	} `json:"links"`
	Usage     *Usage  `json:"usage"`      // last 30 days; set on GET /v1/entries/{id}
	ReviewDue *string `json:"review_due"` // re-check date: review_after or last change + the kind's period
}

// Dependent is an entry relying on another one (impact analysis), Depth steps away.
type Dependent struct {
	HealthItem
	Depth int    `json:"depth"`
	Rel   string `json:"rel"`
	Via   string `json:"via"`
}

type Impact struct {
	Count      int         `json:"count"`
	Direct     int         `json:"direct"`
	Dependents []Dependent `json:"dependents"`
}

type HealthSnapshot struct {
	Day         string `json:"day"`
	Score       int    `json:"score"`
	Severity    string `json:"severity"`
	LiveEntries int    `json:"live_entries"`
	Checks      map[string]struct {
		Severity string `json:"severity"`
		Count    int    `json:"count"`
	} `json:"checks"`
}

// Skill is a project's approved rules and decisions as an agent file (GET /v1/projects/{space}/{slug}/skill).
type Skill struct {
	Project string   `json:"project"`
	Format  string   `json:"format"`
	Path    string   `json:"path"`
	Content string   `json:"content"`
	Entries []string `json:"entries"`
	Indexed int      `json:"indexed"`
}

// Usage: how often an entry was shown in search, delivered in kn_context and opened in full.
type Usage struct {
	Days      int     `json:"days"`
	Shown     int     `json:"shown"`
	Delivered int     `json:"delivered"`
	Opened    int     `json:"opened"`
	LastDay   *string `json:"last_day"`
}

type Similar struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	Similarity float64 `json:"similarity"`
}

type Warning struct {
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Items   []Similar `json:"items"`
}

type EntryResult struct {
	Entry    Entry     `json:"entry"`
	Warnings []Warning `json:"warnings"`
}

type Version struct {
	Version    int       `json:"version"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	ChangedBy  string    `json:"changed_by"`
	ChangeNote *string   `json:"change_note"`
	CreatedAt  time.Time `json:"created_at"`
}

type CreateEntry struct {
	Space       string          `json:"space,omitempty"`
	Project     string          `json:"project,omitempty"`
	Kind        string          `json:"kind"`
	Title       string          `json:"title"`
	Summary     *string         `json:"summary,omitempty"`
	Body        string          `json:"body,omitempty"`
	Attributes  json.RawMessage `json:"attributes,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Pinned      bool            `json:"pinned,omitempty"`
	Confidence  *float32        `json:"confidence,omitempty"`
	ReviewAfter *string         `json:"review_after,omitempty"`
	SessionID   *string         `json:"session_id,omitempty"`
	SourceRef   *string         `json:"source_ref,omitempty"`
	Draft       bool            `json:"draft,omitempty"`
	// OnDuplicateContent: "" or "warn" saves with a duplicate_content warning; "reject" returns 409.
	OnDuplicateContent string `json:"on_duplicate_content,omitempty"`
}

// ReviewItem is a review queue entry; DuplicateOf lists live entries of its space with the same text.
type ReviewItem struct {
	Entry
	DuplicateOf []Similar `json:"duplicate_of"`
}

type PatchEntry struct {
	Project     *string         `json:"project,omitempty"`
	Title       *string         `json:"title,omitempty"`
	Summary     *string         `json:"summary,omitempty"`
	Body        *string         `json:"body,omitempty"`
	Attributes  json.RawMessage `json:"attributes,omitempty"`
	Tags        *[]string       `json:"tags,omitempty"`
	Pinned      *bool           `json:"pinned,omitempty"`
	Confidence  *float32        `json:"confidence,omitempty"`
	ReviewAfter *string         `json:"review_after,omitempty"`
	SourceRef   *string         `json:"source_ref,omitempty"`
	ChangeNote  string          `json:"change_note,omitempty"`
}

type SearchRequest struct {
	Q           string   `json:"q"`
	Spaces      []string `json:"spaces,omitempty"`
	Project     string   `json:"project,omitempty"`
	ProjectOnly bool     `json:"project_only,omitempty"`
	Kinds       []string `json:"kinds,omitempty"`
	Status      []string `json:"status,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	Cursor      string   `json:"cursor,omitempty"`
}

type SearchItem struct {
	ID        string    `json:"id"`
	Space     string    `json:"space"`
	Project   *string   `json:"project"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Summary   *string   `json:"summary"`
	Status    string    `json:"status"`
	Version   int       `json:"version"`
	Tags      []string  `json:"tags"`
	Score     float64   `json:"score"`
	Highlight string    `json:"highlight"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

type ContextRequest struct {
	Project   string   `json:"project,omitempty"`
	RepoURL   string   `json:"repo_url,omitempty"`
	Task      string   `json:"task,omitempty"`
	Spaces    []string `json:"spaces,omitempty"`
	MaxTokens int      `json:"max_tokens,omitempty"`
	Sections  []string `json:"sections,omitempty"`
}

type ContextItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type ContextResult struct {
	Project *struct {
		Ref  string `json:"ref"`
		Name string `json:"name"`
	} `json:"project"`
	Sections []struct {
		Name  string        `json:"name"`
		Items []ContextItem `json:"items"`
	} `json:"sections"`
	Markdown      string `json:"markdown"`
	TokenEstimate int    `json:"token_estimate"`
	Truncated     bool   `json:"truncated"`
	IndexedCount  int    `json:"indexed_count"` // did not fit: listed by id and title only (section "more")
	OmittedCount  int    `json:"omitted_count"` // did not fit and not listed
}

type StartSession struct {
	Agent      string          `json:"agent"`
	Space      string          `json:"space,omitempty"`
	Project    string          `json:"project,omitempty"`
	RepoURL    string          `json:"repo_url,omitempty"`
	ClientInfo json.RawMessage `json:"client_info,omitempty"`
}

type Session struct {
	ID      string  `json:"id"`
	Space   string  `json:"space"`
	Project *string `json:"project"`
	Agent   string  `json:"agent"`
}

type Outcome struct {
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	Summary    *string         `json:"summary,omitempty"`
	Body       string          `json:"body,omitempty"`
	Attributes json.RawMessage `json:"attributes,omitempty"`
	Tags       []string        `json:"tags,omitempty"`
}

type EndSession struct {
	Summary      string    `json:"summary"`
	Outcomes     []Outcome `json:"outcomes,omitempty"`
	FilesTouched []string  `json:"files_touched,omitempty"`

	// Structured log and handoff: status done | in_progress | blocked (default: in_progress with next steps).
	Goal          string   `json:"goal,omitempty"`
	Status        string   `json:"status,omitempty"`
	Done          []string `json:"done,omitempty"`
	Commits       []string `json:"commits,omitempty"`
	OpenQuestions []string `json:"open_questions,omitempty"`
	NextSteps     []string `json:"next_steps,omitempty"`
	Continues     []string `json:"continues,omitempty"`
}

type OutcomeResult struct {
	Title  string  `json:"title"`
	ID     *string `json:"id"`
	Status string  `json:"status"`
	Error  *Error  `json:"error"`
}

type EndSessionResult struct {
	Session    Session         `json:"session"`
	SessionLog *Entry          `json:"session_log"`
	Outcomes   []OutcomeResult `json:"outcomes"`
}

type CreateToken struct {
	Principal       string   `json:"principal"`
	Name            string   `json:"name,omitempty"`
	Scopes          []string `json:"scopes"`
	ExpiresInDays   int      `json:"expires_in_days,omitempty"`
	CreatePrincipal bool     `json:"create_principal,omitempty"`
}

type Token struct {
	ID         string     `json:"id"`
	Principal  string     `json:"principal"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	Token      string     `json:"token,omitempty"` // only on creation
}

type ImportResult struct {
	DryRun          bool `json:"dry_run"`
	ProjectsCreated int  `json:"projects_created"`
	EntriesCreated  int  `json:"entries_created"`
	EntriesUpdated  int  `json:"entries_updated"`
	EntriesSkipped  int  `json:"entries_skipped"`
	LinksCreated    int  `json:"links_created"`
	Duplicates      []struct {
		Line       int    `json:"line"`
		ID         string `json:"id"`
		Title      string `json:"title"`
		ExistingID string `json:"existing_id"`
		Skipped    bool   `json:"skipped"`
	} `json:"duplicates"`
	Errors []struct {
		Line  int    `json:"line"`
		Error string `json:"error"`
	} `json:"errors"`
}

type HealthItem struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Kind      string  `json:"kind"`
	Space     string  `json:"space"`
	Project   *string `json:"project"`
	Status    string  `json:"status"`
	Links     int     `json:"links"`
	CreatedBy string  `json:"created_by"`
	Detail    string  `json:"detail"`
}

type HealthPair struct {
	A          HealthItem `json:"a"`
	B          HealthItem `json:"b"`
	Similarity float64    `json:"similarity"`
	Rel        string     `json:"rel"`
}

type HealthCheck struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Severity   string         `json:"severity"`
	Count      int            `json:"count"`
	Message    string         `json:"message"`
	Suggestion string         `json:"suggestion"`
	Items      []HealthItem   `json:"items"`
	Pairs      []HealthPair   `json:"pairs"`
	Groups     [][]HealthItem `json:"groups"`
}

type HealthReport struct {
	Project     *string       `json:"project"`
	Spaces      []string      `json:"spaces"`
	Score       int           `json:"score"`
	Severity    string        `json:"severity"`
	LiveEntries int           `json:"live_entries"`
	Summary     string        `json:"summary"`
	Checks      []HealthCheck `json:"checks"`
}

// ── Retrieval eval (POST /v1/eval) ──────────────────────────────────────────

type EvalExpect struct {
	ID        string `json:"id,omitempty"`
	SourceRef string `json:"source_ref,omitempty"`
	Title     string `json:"title,omitempty"`
}

type EvalCase struct {
	ID      string       `json:"id"`
	Query   string       `json:"query"`
	Project string       `json:"project,omitempty"`
	Mode    string       `json:"mode,omitempty"` // search | context | both
	NoTask  bool         `json:"no_task,omitempty"`
	Expect  []EvalExpect `json:"expect"`
	Note    string       `json:"note,omitempty"`
}

type EvalInput struct {
	K         int        `json:"k,omitempty"`
	MaxTokens int        `json:"max_tokens,omitempty"`
	Cases     []EvalCase `json:"cases"`
}

type EvalSummary struct {
	Cases        int     `json:"cases"`
	LabelErrors  int     `json:"label_errors"`
	SearchCases  int     `json:"search_cases"`
	Recall5      float64 `json:"recall_at_5"`
	RecallK      float64 `json:"recall_at_k"`
	MRR          float64 `json:"mrr"`
	EmptyRate    float64 `json:"empty_rate"`
	ContextCases int     `json:"context_cases"`
	ContextHit   float64 `json:"context_hit"`
	ContextReach float64 `json:"context_reach"`
}

type EvalCaseResult struct {
	ID     string   `json:"id"`
	Query  string   `json:"query"`
	Errors []string `json:"errors"`
	Search *struct {
		Results   int     `json:"results"`
		FirstRank int     `json:"first_rank"`
		Ranks     []int   `json:"ranks"`
		RecallK   float64 `json:"recall_at_k"`
		Top       []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"top"`
	} `json:"search"`
	Context *struct {
		Found   int `json:"found"`
		Indexed int `json:"indexed"`
		Of      int `json:"of"`
		Tokens  int `json:"tokens"`
	} `json:"context"`
}

type EvalReport struct {
	K         int              `json:"k"`
	MaxTokens int              `json:"max_tokens"`
	Summary   EvalSummary      `json:"summary"`
	Cases     []EvalCaseResult `json:"cases"`
}
