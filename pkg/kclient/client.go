// Package kclient is the Go client for the Knowledge API (REST v1).
// It is shared by kn-mcp, the kn CLI and other tools; none of them talk to the database.
package kclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const DefaultURL = "http://127.0.0.1:8787"

type Client struct {
	BaseURL   string
	Token     string
	UserAgent string
	// Lang (en | ru), when set, is sent as Accept-Language: the API localizes report texts (health) by it.
	Lang string
	HTTP *http.Client
}

// EnvLang returns $KN_LANG when it is en or ru, else "ru" (the language agents and the CLI have always got).
func EnvLang() string {
	switch l := strings.ToLower(strings.TrimSpace(os.Getenv("KN_LANG"))); l {
	case "en", "ru":
		return l
	}
	return "ru"
}

func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Token:     token,
		UserAgent: "kclient",
		HTTP:      &http.Client{Timeout: 60 * time.Second},
	}
}

// Error is an RFC 9457 problem returned by the API.
type Error struct {
	Status     int    `json:"status"`
	Code       string `json:"code"`
	Detail     string `json:"detail"`
	ExistingID string `json:"existing_id,omitempty"`
	// CurrentVersion is set on version_conflict.
	CurrentVersion int `json:"current_version,omitempty"`
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%d %s", e.Status, e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

// AsError extracts an API error.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

type opts struct {
	headers map[string]string
	query   url.Values
	raw     io.Reader // raw request body instead of JSON
	rawType string
}

func (c *Client) request(ctx context.Context, method, path string, body any, o opts) (*http.Response, error) {
	var rd io.Reader
	ctype := ""
	switch {
	case o.raw != nil:
		rd, ctype = o.raw, o.rawType
	case body != nil:
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd, ctype = bytes.NewReader(b), "application/json"
	}
	u := c.BaseURL + path
	if len(o.query) > 0 {
		u += "?" + o.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if c.Lang != "" {
		req.Header.Set("Accept-Language", c.Lang)
	}
	for k, v := range o.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("knowledge API at %s is unreachable (is `make up` running?): %w", c.BaseURL, err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		apiErr := &Error{Status: resp.StatusCode}
		if json.Unmarshal(raw, apiErr) != nil || apiErr.Code == "" {
			apiErr.Code = http.StatusText(resp.StatusCode)
			apiErr.Detail = strings.TrimSpace(string(raw))
		}
		return nil, apiErr
	}
	return resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any, o opts) (http.Header, error) {
	resp, err := c.request(ctx, method, path, body, o)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.Header, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.Header, nil
}

type items[T any] struct {
	Items []T `json:"items"`
}

// ── Identity ──────────────────────────────────────────────────────────────────

func (c *Client) Me(ctx context.Context) (Me, error) {
	var out Me
	_, err := c.do(ctx, "GET", "/v1/me", nil, &out, opts{})
	return out, err
}

func (c *Client) Kinds(ctx context.Context) ([]Kind, error) {
	var out items[Kind]
	_, err := c.do(ctx, "GET", "/v1/kinds", nil, &out, opts{})
	return out.Items, err
}

// ── Projects ──────────────────────────────────────────────────────────────────

func (c *Client) ListProjects(ctx context.Context, space string) ([]Project, error) {
	q := url.Values{}
	if space != "" {
		q.Set("space", space)
	}
	var out items[Project]
	_, err := c.do(ctx, "GET", "/v1/projects", nil, &out, opts{query: q})
	return out.Items, err
}

func (c *Client) CreateProject(ctx context.Context, space, slug, name, description string, repoURLs []string) (Project, error) {
	var out Project
	_, err := c.do(ctx, "POST", "/v1/projects", map[string]any{
		"space": space, "slug": slug, "name": name, "description": description, "repo_urls": repoURLs,
	}, &out, opts{})
	return out, err
}

func (c *Client) ResolveProject(ctx context.Context, repoURL string) (Project, error) {
	var out Project
	_, err := c.do(ctx, "POST", "/v1/projects/resolve", map[string]string{"repo_url": repoURL}, &out, opts{})
	return out, err
}

// ResolveDir finds the project registered for the git repository containing dir, trying its ids best first
// (RepoIDs). ids is empty outside a git repository; err is the last lookup error when nothing matched.
func (c *Client) ResolveDir(ctx context.Context, dir string) (p Project, ids []string, err error) {
	ids = RepoIDs(dir)
	for _, id := range ids {
		if p, err = c.ResolveProject(ctx, id); err == nil {
			return p, ids, nil
		}
		if ae, ok := AsError(err); !ok || ae.Code != "not_found" {
			return Project{}, ids, err
		}
	}
	return Project{}, ids, err
}

// LinkRepo adds a repository id to a project's repo_urls, so the project is found from that repository.
func (c *Client) LinkRepo(ctx context.Context, p Project, id string) (Project, error) {
	urls := append(append([]string{}, p.RepoURLs...), id)
	var out Project
	_, err := c.do(ctx, "PATCH", "/v1/projects/"+url.PathEscape(p.Space)+"/"+url.PathEscape(p.Slug),
		map[string]any{"repo_urls": urls}, &out, opts{})
	return out, err
}

// ── Entries ───────────────────────────────────────────────────────────────────

type ListEntries struct {
	Space, Project, Cursor string
	Kinds, Statuses, Tags  []string
	Limit                  int
}

func (c *Client) ListEntries(ctx context.Context, in ListEntries) (Page[Entry], error) {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("space", in.Space)
	set("project", in.Project)
	set("cursor", in.Cursor)
	set("kind", strings.Join(in.Kinds, ","))
	set("status", strings.Join(in.Statuses, ","))
	set("tag", strings.Join(in.Tags, ","))
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	var out Page[Entry]
	_, err := c.do(ctx, "GET", "/v1/entries", nil, &out, opts{query: q})
	return out, err
}

func (c *Client) GetEntry(ctx context.Context, id string) (EntryDetail, error) {
	var out EntryDetail
	_, err := c.do(ctx, "GET", "/v1/entries/"+url.PathEscape(id), nil, &out, opts{})
	return out, err
}

// CreateEntry creates an entry. idempotencyKey (optional) makes retries safe.
func (c *Client) CreateEntry(ctx context.Context, in CreateEntry, idempotencyKey string) (EntryResult, error) {
	var out EntryResult
	o := opts{}
	if idempotencyKey != "" {
		o.headers = map[string]string{"Idempotency-Key": idempotencyKey}
	}
	_, err := c.do(ctx, "POST", "/v1/entries", in, &out, o)
	return out, err
}

func (c *Client) PatchEntry(ctx context.Context, id string, expectedVersion int, in PatchEntry) (EntryResult, error) {
	var out EntryResult
	_, err := c.do(ctx, "PATCH", "/v1/entries/"+url.PathEscape(id), in, &out,
		opts{headers: map[string]string{"If-Match": fmt.Sprintf(`"%d"`, expectedVersion)}})
	return out, err
}

func (c *Client) Transition(ctx context.Context, id, to, supersededBy, note string) (EntryResult, error) {
	body := map[string]any{"to": to}
	if supersededBy != "" {
		body["superseded_by"] = supersededBy
	}
	if note != "" {
		body["note"] = note
	}
	var out EntryResult
	_, err := c.do(ctx, "POST", "/v1/entries/"+url.PathEscape(id)+"/transition", body, &out, opts{})
	return out, err
}

func (c *Client) Promote(ctx context.Context, id, toSpace, toProject string) (EntryResult, error) {
	var out EntryResult
	_, err := c.do(ctx, "POST", "/v1/entries/"+url.PathEscape(id)+"/promote",
		map[string]string{"to_space": toSpace, "to_project": toProject}, &out, opts{})
	return out, err
}

func (c *Client) Versions(ctx context.Context, id string) ([]Version, error) {
	var out items[Version]
	_, err := c.do(ctx, "GET", "/v1/entries/"+url.PathEscape(id)+"/versions", nil, &out, opts{})
	return out.Items, err
}

func (c *Client) AddLink(ctx context.Context, from, to, rel string) error {
	_, err := c.do(ctx, "POST", "/v1/entries/"+url.PathEscape(from)+"/links",
		map[string]string{"to_id": to, "rel": rel}, nil, opts{})
	return err
}

// ── Search and context ────────────────────────────────────────────────────────

func (c *Client) Search(ctx context.Context, in SearchRequest) (Page[SearchItem], error) {
	var out Page[SearchItem]
	_, err := c.do(ctx, "POST", "/v1/search", in, &out, opts{})
	return out, err
}

func (c *Client) Context(ctx context.Context, in ContextRequest) (ContextResult, error) {
	var out ContextResult
	_, err := c.do(ctx, "POST", "/v1/context", in, &out, opts{})
	return out, err
}

// Impact lists entries that rely on id (depends_on / references / derived_from, up to three steps).
func (c *Client) Impact(ctx context.Context, id string) (Impact, error) {
	var out Impact
	_, err := c.do(ctx, "GET", "/v1/entries/"+url.PathEscape(id)+"/impact", nil, &out, opts{})
	return out, err
}

// LinkSuggestions proposes related structured entries of the same space to link id to.
func (c *Client) LinkSuggestions(ctx context.Context, id string) ([]Similar, error) {
	var out items[Similar]
	_, err := c.do(ctx, "GET", "/v1/entries/"+url.PathEscape(id)+"/link-suggestions", nil, &out, opts{})
	return out.Items, err
}

// HealthHistory returns daily health snapshots (global admin only).
func (c *Client) HealthHistory(ctx context.Context, days int) ([]HealthSnapshot, error) {
	q := url.Values{}
	if days > 0 {
		q.Set("days", strconv.Itoa(days))
	}
	var out items[HealthSnapshot]
	_, err := c.do(ctx, "GET", "/v1/health/history", nil, &out, opts{query: q})
	return out.Items, err
}

// ProjectSkill renders a project's approved rules and decisions; format is claude, cursor or agents.
func (c *Client) ProjectSkill(ctx context.Context, project, format string) (Skill, error) {
	space, slug, _ := strings.Cut(project, "/")
	var out Skill
	_, err := c.do(ctx, "GET", "/v1/projects/"+url.PathEscape(space)+"/"+url.PathEscape(slug)+"/skill", nil, &out,
		opts{query: url.Values{"format": {format}}})
	return out, err
}

// Health returns the knowledge health report (read-only); project and limit are optional.
func (c *Client) Health(ctx context.Context, project string, limit int) (HealthReport, error) {
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out HealthReport
	_, err := c.do(ctx, "GET", "/v1/health", nil, &out, opts{query: q})
	return out, err
}

// Eval scores search and context on a labelled query set (read-only, not recorded as searches).
func (c *Client) Eval(ctx context.Context, in EvalInput) (EvalReport, error) {
	var out EvalReport
	_, err := c.do(ctx, "POST", "/v1/eval", in, &out, opts{})
	return out, err
}

// ── Sessions and review ───────────────────────────────────────────────────────

func (c *Client) StartSession(ctx context.Context, in StartSession) (Session, error) {
	var out Session
	_, err := c.do(ctx, "POST", "/v1/sessions", in, &out, opts{})
	return out, err
}

func (c *Client) EndSession(ctx context.Context, id string, in EndSession) (EndSessionResult, error) {
	var out EndSessionResult
	_, err := c.do(ctx, "POST", "/v1/sessions/"+url.PathEscape(id)+"/end", in, &out, opts{})
	return out, err
}

func (c *Client) ReviewQueue(ctx context.Context, limit int) ([]ReviewItem, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out items[ReviewItem]
	_, err := c.do(ctx, "GET", "/v1/review-queue", nil, &out, opts{query: q})
	return out.Items, err
}

func (c *Client) Review(ctx context.Context, id, action, comment string) (EntryResult, error) {
	body := map[string]any{"action": action}
	if comment != "" {
		body["comment"] = comment
	}
	var out EntryResult
	_, err := c.do(ctx, "POST", "/v1/entries/"+url.PathEscape(id)+"/review", body, &out, opts{})
	return out, err
}

// ── Export / import ───────────────────────────────────────────────────────────

// Export streams an export (format "jsonl" or "markdown") into w.
func (c *Client) Export(ctx context.Context, w io.Writer, format string, spaces []string, history bool) error {
	q := url.Values{"format": {format}}
	for _, s := range spaces {
		q.Add("space", s)
	}
	if history {
		q.Set("include_history", "true")
	}
	resp, err := c.request(ctx, "GET", "/v1/export", nil, opts{query: q})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}

type ImportOptions struct {
	Mode   string // create_only (default) | upsert_by_id
	DryRun bool
	// SkipDuplicateContent skips new entries whose text equals a live entry of the space.
	SkipDuplicateContent bool
}

func (c *Client) Import(ctx context.Context, r io.Reader, o ImportOptions) (ImportResult, error) {
	q := url.Values{}
	if o.Mode != "" {
		q.Set("mode", o.Mode)
	}
	if o.DryRun {
		q.Set("dry_run", "true")
	}
	if o.SkipDuplicateContent {
		q.Set("skip_duplicate_content", "true")
	}
	var out ImportResult
	_, err := c.do(ctx, "POST", "/v1/import", nil, &out, opts{query: q, raw: r, rawType: "application/x-ndjson"})
	return out, err
}

// ── Admin ─────────────────────────────────────────────────────────────────────

func (c *Client) CreateToken(ctx context.Context, in CreateToken) (Token, error) {
	var out Token
	_, err := c.do(ctx, "POST", "/v1/admin/tokens", in, &out, opts{})
	return out, err
}

func (c *Client) ListTokens(ctx context.Context) ([]Token, error) {
	var out items[Token]
	_, err := c.do(ctx, "GET", "/v1/admin/tokens", nil, &out, opts{})
	return out.Items, err
}

func (c *Client) RevokeToken(ctx context.Context, id string) (Token, error) {
	var out Token
	_, err := c.do(ctx, "POST", "/v1/admin/tokens/"+url.PathEscape(id)+"/revoke", nil, &out, opts{})
	return out, err
}

// ── Credentials ───────────────────────────────────────────────────────────────

// KeychainService is the macOS Keychain service under which tokens are stored, one account per principal.
const KeychainService = "kn-token"

// ResolveToken returns $KN_TOKEN, or the token stored in the macOS Keychain for the account.
func ResolveToken(account string) (string, error) {
	if t := strings.TrimSpace(os.Getenv("KN_TOKEN")); t != "" {
		return t, nil
	}
	out, err := exec.Command("security", "find-generic-password", "-s", KeychainService, "-a", account, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("no token: set KN_TOKEN or store one in the Keychain "+
			"(security add-generic-password -s %s -a %s -w <token>)", KeychainService, account)
	}
	return strings.TrimSpace(string(out)), nil
}

// StoreToken saves a token in the macOS Keychain, replacing any existing one for the account.
func StoreToken(account, token string) error {
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", KeychainService, "-a", account, "-w", token)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
