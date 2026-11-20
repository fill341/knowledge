package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"kn.local/api/internal/contextpack"
	"kn.local/api/internal/domain"
	"kn.local/api/internal/store"
)

type SearchInput struct {
	Q           string   `json:"q"`
	Spaces      []string `json:"spaces"`
	Project     string   `json:"project"`      // boosts entries of this project
	ProjectOnly bool     `json:"project_only"` // restricts results to the project
	Kinds       []string `json:"kinds"`
	Status      []string `json:"status"`
	Tags        []string `json:"tags"`
	Mode        string   `json:"mode"`
	Limit       int      `json:"limit"`
	Cursor      string   `json:"cursor"`
}

type SearchItem struct {
	ID        uuid.UUID     `json:"id"`
	Space     string        `json:"space"`
	Project   *string       `json:"project"`
	Kind      string        `json:"kind"`
	Title     string        `json:"title"`
	Summary   *string       `json:"summary"`
	Status    domain.Status `json:"status"`
	Version   int           `json:"version"`
	Tags      []string      `json:"tags"`
	Score     float64       `json:"score"`
	Highlight string        `json:"highlight"`
	UpdatedAt time.Time     `json:"updated_at"`
}

func (s *Service) Search(ctx context.Context, c Caller, in SearchInput) (Page[SearchItem], error) {
	page, err := s.runSearch(ctx, c, in)
	if err != nil {
		return page, err
	}
	s.auditRead(ctx, c, "search", in.Q, len(page.Items), in.Spaces)
	ids := make([]uuid.UUID, len(page.Items))
	for i, it := range page.Items {
		ids[i] = it.ID
	}
	s.recordUsage(ctx, ids, store.UsageShown)
	return page, nil
}

// recordUsage bumps usage counters (ADR-9). Best-effort: a failed counter never fails the read.
func (s *Service) recordUsage(ctx context.Context, ids []uuid.UUID, f store.UsageField) {
	_ = s.store.Q().RecordUsage(ctx, ids, f)
}

// runSearch is Search without the audit record; eval reuses it so measurements do not count as usage.
func (s *Service) runSearch(ctx context.Context, c Caller, in SearchInput) (Page[SearchItem], error) {
	q := s.store.Q()
	in.Q = strings.TrimSpace(in.Q)
	if in.Q == "" {
		return Page[SearchItem]{}, domain.Validation("q is required")
	}
	if in.Mode != "" && in.Mode != "fts" {
		return Page[SearchItem]{}, domain.Unprocessable("unsupported_mode", "search mode %q is not available yet; use fts", in.Mode)
	}
	for _, sp := range in.Spaces {
		if !c.Scope.Can(sp, domain.LevelRead) {
			return Page[SearchItem]{}, domain.ForbiddenSpace(sp)
		}
	}
	offset := 0
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		if err == nil {
			offset, err = strconv.Atoi(string(raw))
		}
		if err != nil || offset < 0 {
			return Page[SearchItem]{}, domain.BadRequest("invalid cursor")
		}
	}
	statuses := in.Status
	if len(statuses) == 0 {
		for _, st := range domain.DefaultSearchStatuses {
			statuses = append(statuses, string(st))
		}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	p := store.SearchParams{Query: in.Q, Offset: offset, Filter: store.EntryFilter{
		Spaces: in.Spaces, Kinds: in.Kinds, Statuses: statuses, Tags: in.Tags, Limit: limit,
	}}
	if in.Project != "" {
		proj, _, err := resolveProject(ctx, q, c, in.Project, domain.LevelRead)
		if err != nil {
			return Page[SearchItem]{}, err
		}
		p.BoostProjectID = &proj.ID
		if in.ProjectOnly {
			p.Filter.ProjectID = &proj.ID
		}
	}
	hits, err := s.search(ctx, q, c, p)
	if err != nil {
		return Page[SearchItem]{}, err
	}
	items := make([]SearchItem, len(hits))
	for i, h := range hits {
		items[i] = SearchItem{ID: h.ID, Space: h.Space, Project: h.Project, Kind: h.Kind, Title: h.Title,
			Summary: h.Summary, Status: h.Status, Version: h.Version, Tags: h.Tags, Score: round(h.Score),
			Highlight: h.Highlight, UpdatedAt: h.UpdatedAt}
	}
	page := Page[SearchItem]{Items: items}
	if len(hits) == p.Filter.Limit {
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + len(hits))))
	}
	return page, nil
}

// search runs FTS and, on the first page, tops up with any-word FTS and fuzzy title matches when FTS finds little.
func (s *Service) search(ctx context.Context, q *store.Queries, c Caller, p store.SearchParams) ([]store.SearchHit, error) {
	hits, err := q.Search(ctx, c.visibility(), p)
	if err != nil {
		return nil, err
	}
	// Top-ups on the first page when every-word FTS finds little: a few fuzzy title matches (a close title is
	// a strong signal), then any-word FTS for the rest. Order measured with kn eval (eval/README.md).
	if p.Offset != 0 || len(hits) >= 3 {
		return hits, nil
	}
	for _, step := range []struct {
		run func(context.Context, store.Visibility, store.SearchParams) ([]store.SearchHit, error)
		max int
	}{{q.SearchTitles, 3}, {q.SearchAny, p.Filter.Limit}} {
		if len(hits) >= p.Filter.Limit {
			break
		}
		fp := p
		fp.Filter.Limit = min(step.max, p.Filter.Limit-len(hits))
		for _, h := range hits {
			fp.Filter.ExcludeIDs = append(fp.Filter.ExcludeIDs, h.ID)
		}
		more, err := step.run(ctx, c.visibility(), fp)
		if err != nil {
			return nil, err
		}
		hits = append(hits, more...)
	}
	return hits, nil
}

func round(f float64) float64 { return float64(int(f*10000+0.5)) / 10000 }

// auditRead logs searches without their text: a hash, result count and spaces.
// Zero-result searches feed the decision on when to add embeddings.
func (s *Service) auditRead(ctx context.Context, c Caller, action, query string, results int, spaces []string) {
	h := sha256.Sum256([]byte(strings.ToLower(query)))
	_ = s.store.Q().Audit(ctx, c.audit(action, "", nil, nil, map[string]any{
		"query_hash": hex.EncodeToString(h[:8]), "results": results, "spaces": spaces,
	}))
}

// ── Context ───────────────────────────────────────────────────────────────────

type ContextInput struct {
	Project   string   `json:"project"`
	RepoURL   string   `json:"repo_url"`
	Task      string   `json:"task"`
	Spaces    []string `json:"spaces"`
	MaxTokens int      `json:"max_tokens"`
	Sections  []string `json:"sections"`
}

type ContextProject struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

type ContextResult struct {
	Project *ContextProject `json:"project"`
	contextpack.Result
}

var allSections = []string{"rules", "handoff", "decisions", "lessons", "related"}

// Unfinished work of the project shown to the next agent (ADR-11): the latest few, not older than handoffWindow.
const (
	handoffWindow = 30 * 24 * time.Hour
	handoffMax    = 3
	handoffShare  = 0.15
)

func (s *Service) Context(ctx context.Context, c Caller, in ContextInput) (ContextResult, error) {
	res, err := s.buildContext(ctx, c, in)
	if err != nil {
		return res, err
	}
	var ids []uuid.UUID
	for _, sec := range res.Sections {
		for _, it := range sec.Items {
			if id, err := uuid.Parse(it.ID); err == nil {
				ids = append(ids, id)
			}
		}
	}
	s.auditRead(ctx, c, "context", in.Task, len(ids), in.Spaces)
	s.recordUsage(ctx, ids, store.UsageDelivered)
	return res, nil
}

// buildContext is Context without the audit record (see runSearch).
func (s *Service) buildContext(ctx context.Context, c Caller, in ContextInput) (ContextResult, error) {
	q := s.store.Q()
	if in.MaxTokens <= 0 {
		in.MaxTokens = DefaultContextTokens
	}
	if in.MaxTokens > 50000 {
		in.MaxTokens = 50000
	}
	for _, sp := range in.Spaces {
		if !c.Scope.Can(sp, domain.LevelRead) {
			return ContextResult{}, domain.ForbiddenSpace(sp)
		}
	}
	want := map[string]bool{}
	for _, n := range in.Sections {
		want[n] = true
	}
	if len(want) == 0 {
		for _, n := range allSections {
			want[n] = true
		}
	}

	var proj *domain.Project
	switch {
	case in.Project != "":
		p, _, err := resolveProject(ctx, q, c, in.Project, domain.LevelRead)
		if err != nil {
			return ContextResult{}, err
		}
		proj = &p
	case in.RepoURL != "":
		p, err := s.ResolveProject(ctx, c, in.RepoURL)
		var de *domain.Error
		if err == nil {
			proj = &p
		} else if !errors.As(err, &de) || de.Code != "not_found" {
			return ContextResult{}, err
		}
	}

	active := []string{string(domain.StatusActive)}
	stale, err := s.staleCheck(ctx, q)
	if err != nil {
		return ContextResult{}, err
	}
	seen := map[uuid.UUID]bool{}
	var sections []contextpack.Section
	collect := func(rows []store.EntryRow) []contextpack.Item {
		var items []contextpack.Item
		for _, r := range rows {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			it := toItem(r)
			it.Stale = stale(r)
			items = append(items, it)
		}
		return items
	}

	// 1. Rules: pinned or kind=rule, of the project and space-wide (no project).
	if want["rules"] {
		var items []contextpack.Item
		if proj != nil {
			rows, err := s.listAll(ctx, q, c, store.EntryFilter{ProjectID: &proj.ID, Statuses: active, Pinned: ptr(true), Limit: 50})
			if err != nil {
				return ContextResult{}, err
			}
			items = append(items, collect(rows)...)
			rows, err = s.listAll(ctx, q, c, store.EntryFilter{ProjectID: &proj.ID, Statuses: active, Kinds: []string{"rule"}, Limit: 50})
			if err != nil {
				return ContextResult{}, err
			}
			items = append(items, collect(rows)...)
		}
		spaces := in.Spaces
		if proj != nil && len(spaces) == 0 {
			// Space-wide rules come from the project's space and shared, not from every readable space.
			spaces = []string{proj.Space}
			if proj.Space != "shared" && c.Scope.Can("shared", domain.LevelRead) {
				spaces = append(spaces, "shared")
			}
		}
		rows, err := s.listAll(ctx, q, c, store.EntryFilter{Spaces: spaces, NoProject: true, Statuses: active, Pinned: ptr(true), Limit: 50})
		if err != nil {
			return ContextResult{}, err
		}
		items = append(items, collect(rows)...)
		// Rules go in full, but within 35% of the budget; past that compact, then the index (and the health
		// report warns that the project's rules no longer fit).
		sections = append(sections, contextpack.Section{Name: "rules", Title: "Правила", Full: true, MaxShare: rulesShare, Items: items})
	}

	// 2. Unfinished work left by earlier sessions of the project, so the next agent can pick it up.
	if want["handoff"] && proj != nil {
		rows, err := q.OpenHandoffs(ctx, c.visibility(), proj.ID, time.Now().Add(-handoffWindow), handoffMax)
		if err != nil {
			return ContextResult{}, err
		}
		if items := collect(rows); len(items) > 0 {
			sections = append(sections, contextpack.Section{Name: "handoff", Full: true, MaxShare: handoffShare, Items: items,
				Title: "Незавершённая работа (продолжаешь — укажи её id в continues при kn_log_session)"})
		}
	}

	// 3. Everything else. With a task: one pool ranked by relevance to the task, so a big project cannot crowd
	// out what the task needs. Without a task (session start): recent decisions, lessons and facts of the
	// project with caps, so one kind cannot take the whole budget. Whatever does not fit goes to the index.
	var rest []candSection
	if strings.TrimSpace(in.Task) != "" {
		rest, err = s.taskSection(ctx, q, c, in, proj, want, active)
	} else {
		rest, err = s.projectSections(ctx, q, c, proj, want, active)
	}
	if err != nil {
		return ContextResult{}, err
	}
	for _, sec := range rest {
		sec.Items = collect(sec.list)
		sections = append(sections, sec.Section)
	}

	header := "# Контекст из базы знаний\n"
	var cp *ContextProject
	if proj != nil {
		header = "# Контекст: " + proj.Name + " (`" + proj.Ref + "`)\n"
		if proj.Description != "" {
			header += "\n" + proj.Description + "\n"
		}
		cp = &ContextProject{Ref: proj.Ref, Name: proj.Name}
	}
	return ContextResult{Project: cp, Result: contextpack.Pack(header, sections, in.MaxTokens)}, nil
}

func (s *Service) listAll(ctx context.Context, q *store.Queries, c Caller, f store.EntryFilter) ([]store.EntryRow, error) {
	rows, _, err := q.ListEntries(ctx, c.visibility(), f)
	return rows, err
}

// DefaultContextTokens is the context budget when the client does not set one.
const DefaultContextTokens = 10000

// rulesShare caps the rules section (see buildContext).
const rulesShare = 0.35

// candSection is a context section before deduplication against the rules: rows in the order to pack.
type candSection struct {
	contextpack.Section
	list []store.EntryRow
}

// Kind prior in the task pool: structured knowledge before raw material.
var kindPrior = map[string]float64{
	"decision": 1.3, "lesson": 1.2, "fact": 1.15, "rule": 1.1, "snippet": 1.0, "note": 0.9, "document": 0.8, "session_log": 0.6,
}

// taskSection ranks candidates by reciprocal rank fusion of: search for the task within the project, search
// across the allowed spaces (project boosted), and — as a weak signal — the project's recent decisions and
// lessons; each multiplied by the kind prior. Sections asked for limit the kinds: decisions, lessons, related (the rest).
func (s *Service) taskSection(ctx context.Context, q *store.Queries, c Caller, in ContextInput, proj *domain.Project,
	want map[string]bool, active []string) ([]candSection, error) {
	score := map[uuid.UUID]float64{}
	rows := map[uuid.UUID]store.EntryRow{}
	add := func(list []store.EntryRow, weight float64) {
		for rank, r := range list {
			score[r.ID] += weight / float64(10+rank)
			rows[r.ID] = r
		}
	}
	hitsRows := func(hits []store.SearchHit) []store.EntryRow {
		out := make([]store.EntryRow, len(hits))
		for i, h := range hits {
			out[i] = h.EntryRow
		}
		return out
	}
	if proj != nil {
		hits, err := s.search(ctx, q, c, store.SearchParams{Query: in.Task,
			Filter: store.EntryFilter{ProjectID: &proj.ID, Statuses: active, Limit: 40}})
		if err != nil {
			return nil, err
		}
		add(hitsRows(hits), 1)
		for _, kind := range []string{"decision", "lesson"} {
			recent, err := s.listAll(ctx, q, c, store.EntryFilter{ProjectID: &proj.ID, Statuses: active, Kinds: []string{kind}, Limit: 20})
			if err != nil {
				return nil, err
			}
			add(recent, 0.3)
		}
	}
	p := store.SearchParams{Query: in.Task, Filter: store.EntryFilter{Spaces: in.Spaces, Statuses: active, Limit: 20}}
	if proj != nil {
		p.BoostProjectID = &proj.ID
	}
	hits, err := s.search(ctx, q, c, p)
	if err != nil {
		return nil, err
	}
	add(hitsRows(hits), 1)

	allowed := func(kind string) bool {
		switch kind {
		case "decision":
			return want["decisions"]
		case "lesson":
			return want["lessons"]
		default:
			return want["related"]
		}
	}
	var list []store.EntryRow
	for id, r := range rows {
		if allowed(r.Kind) {
			score[id] *= kindPrior[r.Kind] + 0.0001 // unknown kinds keep a tiny prior
			list = append(list, r)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if score[list[i].ID] != score[list[j].ID] {
			return score[list[i].ID] > score[list[j].ID]
		}
		return list[i].ID.String() < list[j].ID.String()
	})
	return []candSection{{Section: contextpack.Section{Name: "task", Title: "По задаче"}, list: list}}, nil
}

// projectSections is the context without a task: the project's recent decisions, lessons and facts, capped.
func (s *Service) projectSections(ctx context.Context, q *store.Queries, c Caller, proj *domain.Project,
	want map[string]bool, active []string) ([]candSection, error) {
	if proj == nil {
		return nil, nil
	}
	var out []candSection
	for _, k := range []struct {
		name, want, kind, title string
		share                   float64
	}{
		{"decisions", "decisions", "decision", "Решения", 0.30},
		{"lessons", "lessons", "lesson", "Уроки", 0.20},
		{"facts", "related", "fact", "Факты", 0},
	} {
		if !want[k.want] {
			continue
		}
		rows, err := s.listAll(ctx, q, c, store.EntryFilter{ProjectID: &proj.ID, Statuses: active, Kinds: []string{k.kind}, Limit: 30})
		if err != nil {
			return nil, err
		}
		out = append(out, candSection{Section: contextpack.Section{Name: k.name, Title: k.title, MaxShare: k.share}, list: rows})
	}
	return out, nil
}

// staleCheck reports whether an active entry is past its re-check date (same rule as store.DueExpr).
func (s *Service) staleCheck(ctx context.Context, q *store.Queries) (func(store.EntryRow) bool, error) {
	kinds, err := q.ListKinds(ctx)
	if err != nil {
		return nil, err
	}
	days := map[string]int{}
	for _, k := range kinds {
		if k.ReviewDays != nil {
			days[k.Kind] = *k.ReviewDays
		}
	}
	// UTC, like the database (TimeZone Etc/UTC), so context marks agree with the review queue and health report.
	today := time.Now().UTC().Format(time.DateOnly)
	return func(r store.EntryRow) bool {
		if r.Status != domain.StatusActive {
			return false
		}
		if r.ReviewAfter != nil {
			return *r.ReviewAfter <= today
		}
		d, ok := days[r.Kind]
		return ok && r.UpdatedAt.UTC().AddDate(0, 0, d).Format(time.DateOnly) <= today
	}, nil
}

func toItem(r store.EntryRow) contextpack.Item {
	it := contextpack.Item{ID: r.ID.String(), Kind: r.Kind, Title: r.Title, Body: r.Body, Status: string(r.Status)}
	if r.Summary != nil {
		it.Summary = *r.Summary
	}
	if r.Project != nil {
		it.Project = *r.Project
	}
	return it
}

func ptr[T any](v T) *T { return &v }

// ── Graph ─────────────────────────────────────────────────────────────────────

type Graph struct {
	Nodes     []store.GraphNode    `json:"nodes"`
	Links     []store.GraphLink    `json:"links"`
	Affinity  []Affinity           `json:"affinity"` // implicit links by content, see affinityLinks
	Projects  []domain.Project     `json:"projects"`
	Activity  []store.ActivityHour `json:"activity"` // hourly development events per project: activity halo and the player
	Truncated bool                 `json:"truncated"`
}

const graphLimit = 5000

// Graph returns the knowledge graph visible to the caller: entries, links between them and projects.
// Archived entries are excluded unless statuses are given explicitly.
func (s *Service) Graph(ctx context.Context, c Caller, statuses []string) (Graph, error) {
	q := s.store.Q()
	if len(statuses) == 0 {
		statuses = []string{"draft", "proposed", "active", "deprecated", "superseded"}
	}
	nodes, err := q.GraphNodes(ctx, c.visibility(), statuses, graphLimit+1)
	if err != nil {
		return Graph{}, err
	}
	g := Graph{Nodes: nodes}
	if len(nodes) > graphLimit {
		g.Nodes, g.Truncated = nodes[:graphLimit], true
	}
	present := make(map[uuid.UUID]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		present[n.ID] = true
	}
	links, err := q.GraphLinks(ctx, c.visibility())
	if err != nil {
		return Graph{}, err
	}
	g.Links = []store.GraphLink{}
	for _, l := range links {
		if present[l.From] && present[l.To] {
			g.Links = append(g.Links, l)
		}
	}
	ids := make([]uuid.UUID, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.Status != string(domain.StatusSuperseded) && n.Status != string(domain.StatusArchived) {
			ids = append(ids, n.ID)
		}
	}
	terms, err := q.GraphTerms(ctx, ids, affinityTerms)
	if err != nil {
		return Graph{}, err
	}
	g.Affinity = affinityLinks(g.Nodes, terms, g.Links)
	if g.Projects, err = q.ListProjects(ctx, c.visibility(), store.ProjectFilter{}); err != nil {
		return Graph{}, err
	}
	if g.Activity, err = q.GraphActivity(ctx, c.visibility()); err != nil {
		return Graph{}, err
	}
	if g.Nodes == nil {
		g.Nodes = []store.GraphNode{}
	}
	if g.Activity == nil {
		g.Activity = []store.ActivityHour{}
	}
	return g, nil
}
