package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"kn.local/api/internal/domain"
	"kn.local/api/internal/store"
)

// ── Sessions ──────────────────────────────────────────────────────────────────

type StartSessionInput struct {
	Agent      string          `json:"agent"`
	Space      string          `json:"space"`
	Project    string          `json:"project"`
	RepoURL    string          `json:"repo_url"`
	ClientInfo json.RawMessage `json:"client_info"`
}

func (s *Service) StartSession(ctx context.Context, c Caller, in StartSessionInput) (domain.Session, error) {
	in.Agent = strings.TrimSpace(in.Agent)
	if in.Agent == "" {
		return domain.Session{}, domain.Validation("agent is required")
	}
	var out domain.Session
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		var projectID *uuid.UUID
		space := in.Space
		var p *domain.Project
		switch {
		case in.Project != "":
			pr, _, err := resolveProject(ctx, q, c, in.Project, domain.LevelWrite)
			if err != nil {
				return err
			}
			p = &pr
		case in.RepoURL != "":
			pr, err := s.ResolveProject(ctx, c, in.RepoURL)
			if err == nil && c.Scope.Can(pr.Space, domain.LevelWrite) {
				p = &pr
			}
		}
		if p != nil {
			if space != "" && space != p.Space {
				return domain.Validation("project %s does not belong to space %s", p.Ref, space)
			}
			space, projectID = p.Space, &p.ID
		}
		sp, err := requireSpace(ctx, q, c, space, domain.LevelWrite)
		if err != nil {
			return err
		}
		id, err := q.InsertSession(ctx, c.Principal.ID, sp.ID, projectID, in.Agent, in.ClientInfo)
		if err != nil {
			return err
		}
		if err := q.Audit(ctx, c.audit("session.start", "session", &sp.ID, &id, map[string]any{"agent": in.Agent})); err != nil {
			return err
		}
		row, err := q.GetSession(ctx, id)
		out = row.Session
		return err
	})
	return out, err
}

type Outcome struct {
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	Summary    *string         `json:"summary"`
	Body       string          `json:"body"`
	Attributes json.RawMessage `json:"attributes"`
	Tags       []string        `json:"tags"`
	Confidence *float32        `json:"confidence"`
	SourceRef  *string         `json:"source_ref"`
}

type EndSessionInput struct {
	Summary      string    `json:"summary"`
	Outcomes     []Outcome `json:"outcomes"`
	FilesTouched []string  `json:"files_touched"`

	// Structured log and handoff (ADR-11). Status defaults to in_progress when next steps are given, else done.
	Goal          string   `json:"goal"`
	Status        string   `json:"status"` // done | in_progress | blocked
	Done          []string `json:"done"`
	Commits       []string `json:"commits"`
	OpenQuestions []string `json:"open_questions"`
	NextSteps     []string `json:"next_steps"`
	Continues     []string `json:"continues"` // ids of unfinished session logs this session picked up
}

type OutcomeResult struct {
	Title  string        `json:"title"`
	ID     *uuid.UUID    `json:"id,omitempty"`
	Status domain.Status `json:"status,omitempty"`
	Error  *domain.Error `json:"error,omitempty"`
}

type EndSessionResult struct {
	Session    domain.Session  `json:"session"`
	SessionLog *domain.Entry   `json:"session_log"`
	Outcomes   []OutcomeResult `json:"outcomes"`
}

// EndSession saves outcomes (each through the full quality pipeline, independently, so one bad
// outcome does not lose the others) and a session_log entry summarizing the session.
func (s *Service) EndSession(ctx context.Context, c Caller, id uuid.UUID, in EndSessionInput) (EndSessionResult, error) {
	q := s.store.Q()
	sess, err := q.GetSession(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sess.PrincipalID != c.Principal.ID) {
		return EndSessionResult{}, domain.NotFound("session %s not found", id)
	} else if err != nil {
		return EndSessionResult{}, err
	}
	if !c.Scope.Can(sess.Space, domain.LevelWrite) {
		return EndSessionResult{}, domain.ForbiddenSpace(sess.Space)
	}
	project := ""
	if sess.Project != nil {
		project = *sess.Project
	}
	// Validate the structured log before any outcome is written, so a bad field does not leave saved outcomes
	// behind an error (and duplicates on retry).
	status, err := s.handoffStatus(ctx, c, in)
	if err != nil {
		return EndSessionResult{}, err
	}
	nextSteps := clean(in.NextSteps)
	res := EndSessionResult{Outcomes: []OutcomeResult{}}
	var titles []string
	for _, o := range in.Outcomes {
		r, err := s.CreateEntry(ctx, c, CreateEntryInput{
			Space: sess.Space, Project: project, Kind: o.Kind, Title: o.Title, Summary: o.Summary, Body: o.Body,
			Attributes: o.Attributes, Tags: o.Tags, Confidence: o.Confidence, SessionID: &id, SourceRef: o.SourceRef,
		})
		or := OutcomeResult{Title: o.Title}
		if de, ok := domain.AsError(err); ok {
			or.Error = de
		} else if err != nil {
			return res, err
		} else {
			or.ID, or.Status = &r.Entry.ID, r.Entry.Status
			titles = append(titles, o.Title)
		}
		res.Outcomes = append(res.Outcomes, or)
	}

	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		summary = strings.TrimSpace(in.Goal)
	}
	if summary != "" {
		attrs, _ := json.Marshal(map[string]any{"agent": sess.Agent, "outcomes": nonNilStrings(titles), "files_touched": nonNilStrings(in.FilesTouched),
			"goal": strings.TrimSpace(in.Goal), "status": status, "done": clean(in.Done), "commits": clean(in.Commits),
			"open_questions": clean(in.OpenQuestions), "next_steps": nextSteps, "continues": clean(in.Continues)})
		var sum *string // the compact form of an unfinished log says what to do next
		if status != "done" && len(nextSteps) > 0 {
			// Bounded by the summary limit (quality.CheckLengths); the full list is in the body.
			next := truncateRunes("Не закончено: дальше — "+strings.Join(nextSteps, "; "), maxSummaryRunes)
			sum = &next
		}
		r, err := s.CreateEntry(ctx, c, CreateEntryInput{
			Space: sess.Space, Project: project, Kind: "session_log",
			Title: sessionTitle(sess.Agent, summary), Summary: sum, Body: sessionBody(summary, status, in), Attributes: attrs, SessionID: &id,
		})
		if de, ok := domain.AsError(err); ok {
			return res, de
		} else if err != nil {
			return res, err
		}
		res.SessionLog = &r.Entry
	}
	if err := q.EndSession(ctx, id); err != nil {
		return res, err
	}
	_ = q.Audit(ctx, c.audit("session.end", "session", &sess.SpaceID, &id, map[string]any{"outcomes": len(in.Outcomes)}))
	row, err := q.GetSession(ctx, id)
	res.Session = row.Session
	return res, err
}

// handoffStatus validates the structured fields and returns the status: given, or in_progress when next steps
// are listed, else done. Continued logs must be visible session logs.
func (s *Service) handoffStatus(ctx context.Context, c Caller, in EndSessionInput) (string, error) {
	if utf8.RuneCountInString(strings.TrimSpace(in.Goal)) > maxGoalRunes {
		return "", domain.Validation("goal must be at most %d characters", maxGoalRunes)
	}
	status := in.Status
	switch status {
	case "":
		status = "done"
		if len(clean(in.NextSteps)) > 0 {
			status = "in_progress"
		}
	case "done", "in_progress", "blocked":
	default:
		return "", domain.Validation("status must be done, in_progress or blocked")
	}
	for _, raw := range clean(in.Continues) {
		id, err := uuid.Parse(raw)
		if err != nil {
			return "", domain.Validation("continues: %q is not an entry id", raw)
		}
		e, err := s.getEntry(ctx, s.store.Q(), c, id, false)
		if de, ok := domain.AsError(err); (ok && de.Code == "not_found") || (err == nil && e.Kind != "session_log") {
			return "", domain.Validation("continues: %s is not a visible session log", raw)
		} else if err != nil {
			return "", err
		}
	}
	return status, nil
}

// Limits of the session_log summary (quality.CheckLengths) and of its goal attribute (migration 0006).
const (
	maxSummaryRunes = 1000
	maxGoalRunes    = 500
)

var statusTitle = map[string]string{"done": "завершено", "in_progress": "в работе — продолжить", "blocked": "заблокировано"}

// sessionBody renders the structured log as markdown, so it reads well in context and on the entry page.
func sessionBody(summary, status string, in EndSessionInput) string {
	var b strings.Builder
	b.WriteString(summary)
	b.WriteString("\n")
	if g := strings.TrimSpace(in.Goal); g != "" && g != summary {
		b.WriteString("\n**Цель:** " + g + "\n")
	}
	b.WriteString("\n**Статус:** " + statusTitle[status] + "\n")
	for _, sec := range []struct {
		title string
		items []string
	}{{"Сделано", in.Done}, {"Коммиты", in.Commits}, {"Открытые вопросы", in.OpenQuestions}, {"Следующие шаги", in.NextSteps}} {
		if items := clean(sec.items); len(items) > 0 {
			b.WriteString("\n## " + sec.title + "\n\n")
			for _, it := range items {
				b.WriteString("- " + it + "\n")
			}
		}
	}
	return b.String()
}

// clean trims items and drops empty ones; never nil (for JSON arrays).
func clean(items []string) []string {
	out := []string{}
	for _, it := range items {
		if t := strings.TrimSpace(it); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func sessionTitle(agent, summary string) string {
	first, _, _ := strings.Cut(summary, "\n")
	first = strings.TrimSpace(strings.TrimLeft(first, "#-* "))
	if utf8.RuneCountInString(first) > 120 {
		first = string([]rune(first)[:120]) + "…"
	}
	return "Сессия " + agent + ": " + first
}

// truncateRunes cuts s to at most n runes, marking a cut with "…".
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ── Review ────────────────────────────────────────────────────────────────────

func (s *Service) reviewSpaces(ctx context.Context, c Caller) ([]string, error) {
	if c.Principal.IsAgent() {
		return nil, nil
	}
	spaces, err := s.store.Q().ListSpaces(ctx, c.visibility())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, sp := range spaces {
		if c.Scope.Can(sp.Slug, domain.LevelReview) {
			out = append(out, sp.Slug)
		}
	}
	return out, nil
}

// ReviewItem is a queued entry plus live entries of its space with the same text, so the reviewer
// sees a copy before approving it.
type ReviewItem struct {
	domain.Entry
	DuplicateOf []store.Similar `json:"duplicate_of,omitempty"`
}

func (s *Service) ReviewQueue(ctx context.Context, c Caller, limit int) ([]ReviewItem, error) {
	spaces, err := s.reviewSpaces(ctx, c)
	if err != nil {
		return nil, err
	}
	if len(spaces) == 0 {
		return nil, domain.Forbidden("review permission required")
	}
	q := s.store.Q()
	rows, err := q.ReviewQueue(ctx, spaces, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	twins, err := q.ContentTwinsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ReviewItem, len(rows))
	for i, r := range rows {
		out[i] = ReviewItem{Entry: r.Entry, DuplicateOf: twins[r.ID]}
	}
	return out, nil
}

type ReviewInput struct {
	Action  string  `json:"action"` // approve | reject | request_changes
	Comment *string `json:"comment"`
}

// Review approves (→ active), rejects (→ archived) or sends back (→ draft) an entry.
// Approving an active entry that was due for re-review just clears review_after.
func (s *Service) Review(ctx context.Context, c Caller, id uuid.UUID, in ReviewInput) (EntryResult, error) {
	var res EntryResult
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		cur, err := s.getEntry(ctx, q, c, id, false)
		if err != nil {
			return err
		}
		if c.Principal.IsAgent() || !c.Scope.Can(cur.Space, domain.LevelReview) {
			return domain.Forbidden("review permission required")
		}
		note := "review: " + in.Action
		if in.Comment != nil && *in.Comment != "" {
			note += " — " + *in.Comment
		}
		switch {
		case in.Action == "approve" && cur.Status == domain.StatusActive:
			// Re-read under lock; assign to the outer err, so a failed save is not lost (it was shadowed before).
			locked, lerr := s.getEntry(ctx, q, c, id, true)
			if lerr != nil {
				return lerr
			}
			w := writeFromRow(locked)
			w.ReviewAfter = nil
			res, err = s.saveRevision(ctx, q, c, locked, w, note, nil, "entry.review")
		case in.Action == "approve":
			res, err = s.transition(ctx, q, c, id, 0, TransitionInput{To: domain.StatusActive, Note: note}, "entry.review")
		case in.Action == "reject":
			res, err = s.transition(ctx, q, c, id, 0, TransitionInput{To: domain.StatusArchived, Note: note}, "entry.review")
		case in.Action == "request_changes":
			res, err = s.transition(ctx, q, c, id, 0, TransitionInput{To: domain.StatusDraft, Note: note}, "entry.review")
		default:
			return domain.Validation("action must be approve, reject or request_changes")
		}
		if err != nil {
			return err
		}
		return q.InsertReview(ctx, id, res.Entry.Version, c.Principal.ID, in.Action, in.Comment)
	})
	return res, err
}

// ── Stats ─────────────────────────────────────────────────────────────────────

type Stats struct {
	Total       int                       `json:"total"`
	BySpace     map[string]map[string]int `json:"by_space"` // space → status → count
	ByKind      map[string]int            `json:"by_kind"`
	ReviewQueue int                       `json:"review_queue"` // 0 when the caller cannot review
	Projects    int                       `json:"projects"`
}

func (s *Service) Stats(ctx context.Context, c Caller) (Stats, error) {
	q := s.store.Q()
	rows, err := q.EntryStats(ctx, c.visibility())
	if err != nil {
		return Stats{}, err
	}
	st := Stats{BySpace: map[string]map[string]int{}, ByKind: map[string]int{}}
	for _, r := range rows {
		if st.BySpace[r.Space] == nil {
			st.BySpace[r.Space] = map[string]int{}
		}
		st.BySpace[r.Space][r.Status] += r.Count
		st.ByKind[r.Kind] += r.Count
		st.Total += r.Count
	}
	spaces, err := s.reviewSpaces(ctx, c)
	if err != nil {
		return Stats{}, err
	}
	if len(spaces) > 0 {
		if st.ReviewQueue, err = q.ReviewQueueCount(ctx, spaces); err != nil {
			return Stats{}, err
		}
	}
	ps, err := q.ListProjects(ctx, c.visibility(), store.ProjectFilter{})
	st.Projects = len(ps)
	return st, err
}
