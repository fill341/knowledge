package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"kn.local/api/internal/domain"
	"kn.local/api/internal/quality"
	"kn.local/api/internal/store"
)

type Warning struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Items   []store.Similar `json:"items,omitempty"`
}

type EntryResult struct {
	Entry    domain.Entry `json:"entry"`
	Warnings []Warning    `json:"warnings"`
}

type CreateEntryInput struct {
	Space               string          `json:"space"`
	Project             string          `json:"project"`
	Kind                string          `json:"kind"`
	Title               string          `json:"title"`
	Summary             *string         `json:"summary"`
	Body                string          `json:"body"`
	Attributes          json.RawMessage `json:"attributes"`
	Tags                []string        `json:"tags"`
	Pinned              bool            `json:"pinned"`
	Confidence          *float32        `json:"confidence"`
	ReviewAfter         *string         `json:"review_after"`
	SessionID           *uuid.UUID      `json:"session_id"`
	SourceRef           *string         `json:"source_ref"`
	Draft               bool            `json:"draft"`
	AllowSecretPatterns []string        `json:"allow_secret_patterns"`
	// OnDuplicateContent: "warn" (default) saves and returns a duplicate_content warning when a live entry
	// of the space has the same text; "reject" refuses with 409 duplicate_content (for import scripts).
	OnDuplicateContent string `json:"on_duplicate_content"`

	// Set only by import: preserve identity and status.
	id          *uuid.UUID
	forceStatus *domain.Status
}

func (s *Service) CreateEntry(ctx context.Context, c Caller, in CreateEntryInput) (EntryResult, error) {
	if err := s.checkWriteRate(c); err != nil {
		return EntryResult{}, err
	}
	var res EntryResult
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		res, err = s.createEntry(ctx, q, c, in)
		return err
	})
	return res, err
}

// createEntry runs the full quality pipeline and inserts the entry. See docs/api.md §5.
func (s *Service) createEntry(ctx context.Context, q *store.Queries, c Caller, in CreateEntryInput) (EntryResult, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Summary = trimPtr(in.Summary)

	// 1. Scope: resolve space (from project ref when not given) and require write.
	var projectID *uuid.UUID
	var sp domain.Space
	var err error
	if in.Project != "" {
		var p domain.Project
		p, sp, err = resolveProject(ctx, q, c, in.Project, domain.LevelWrite)
		if err != nil {
			return EntryResult{}, err
		}
		if in.Space != "" && in.Space != p.Space {
			return EntryResult{}, domain.Validation("project %s does not belong to space %s", in.Project, in.Space)
		}
		projectID = &p.ID
	} else {
		sp, err = requireSpace(ctx, q, c, in.Space, domain.LevelWrite)
		if err != nil {
			return EntryResult{}, err
		}
	}

	// 2. Structure.
	kind, err := q.Kind(ctx, in.Kind)
	if errors.Is(err, store.ErrNotFound) {
		return EntryResult{}, domain.Validation("unknown kind %q", in.Kind)
	} else if err != nil {
		return EntryResult{}, err
	}
	tags, err := s.checkStructure(c, in.Title, in.Summary, in.Body, in.Tags, in.Pinned, in.Confidence, in.ReviewAfter)
	if err != nil {
		return EntryResult{}, err
	}

	// 3. Attributes schema.
	if err := s.schemas.Validate(kind.Kind, kind.AttributesSchema, in.Attributes); err != nil {
		return EntryResult{}, domain.Validation("%s", err)
	}

	// 4. Provenance.
	if err := checkProvenance(ctx, q, c, in.SessionID, in.SourceRef); err != nil {
		return EntryResult{}, err
	}

	// 5. Secrets.
	if err := checkSecrets(c, sp.Slug, in.Title, in.Summary, in.Body, in.Attributes, in.AllowSecretPatterns); err != nil {
		return EntryResult{}, err
	}

	// 8 (decided early: duplicates only matter among live entries). Status.
	status := domain.InitialStatus(in.Draft, c.actor(sp.Slug), domain.RequiresReview(kind, sp))
	if in.forceStatus != nil {
		status = *in.forceStatus
	}

	// 6. Exact duplicate.
	hash := quality.ContentHash(kind.Kind, in.Title, in.Body)
	if live(status) {
		if id, err := q.LiveByHash(ctx, sp.ID, hash); err == nil {
			return EntryResult{}, domain.Duplicate(id.String())
		} else if !errors.Is(err, store.ErrNotFound) {
			return EntryResult{}, err
		}
	}

	// 7. Similar entries → warning only.
	id := uuid.Must(uuid.NewV7())
	if in.id != nil {
		id = *in.id
	}
	warnings, err := similarWarnings(ctx, q, sp.ID, projectID, kind.Kind, in.Title, id)
	if err != nil {
		return EntryResult{}, err
	}

	// 7b. Same text under another title → warning, or refusal on request (roadmap P0 #2).
	if live(status) {
		dup, err := contentWarning(ctx, q, sp.ID, in.Body, id, in.OnDuplicateContent)
		if err != nil {
			return EntryResult{}, err
		}
		warnings = append(warnings, dup...)
	}

	// 9. Insert (history trigger writes version 1) + audit.
	w := store.EntryWrite{
		SpaceID: sp.ID, ProjectID: projectID, Kind: kind.Kind, Title: in.Title, Summary: in.Summary,
		Body: in.Body, Attributes: in.Attributes, Tags: tags, Status: status, Pinned: in.Pinned,
		Confidence: in.Confidence, ContentHash: hash, ReviewAfter: in.ReviewAfter, SessionID: in.SessionID,
		SourceRef: in.SourceRef, By: c.Principal.ID,
	}
	if err := q.InsertEntry(ctx, id, w); err != nil {
		return EntryResult{}, mapWriteError(ctx, q, err, sp.ID, hash)
	}
	if err := q.Audit(ctx, c.audit("entry.create", "entry", &sp.ID, &id,
		map[string]any{"kind": kind.Kind, "status": status})); err != nil {
		return EntryResult{}, err
	}
	row, err := q.GetEntry(ctx, c.visibility(), id, false)
	if err != nil {
		return EntryResult{}, err
	}
	// Proposals only — linking stays a decision of the agent or the person (roadmap P2 #7). Not on import.
	if in.id == nil {
		if warnings, err = s.suggestLinksWarning(ctx, q, c, row, warnings); err != nil {
			return EntryResult{}, err
		}
	}
	return EntryResult{Entry: row.Entry, Warnings: warnings}, nil
}

func (s *Service) checkStructure(c Caller, title string, summary *string, body string, tags []string, pinned bool, confidence *float32, reviewAfter *string) ([]string, error) {
	if err := quality.CheckLengths(title, summary, body); err != nil {
		return nil, domain.Validation("%s", err)
	}
	norm, err := quality.NormalizeTags(tags)
	if err != nil {
		return nil, domain.Validation("%s", err)
	}
	if confidence != nil && (*confidence < 0 || *confidence > 1) {
		return nil, domain.Validation("confidence must be between 0 and 1")
	}
	if reviewAfter != nil {
		if _, err := time.Parse(time.DateOnly, *reviewAfter); err != nil {
			return nil, domain.Validation("review_after must be YYYY-MM-DD")
		}
	}
	// Pinned entries go into every context of the project; only humans decide that.
	if pinned && c.Principal.IsAgent() {
		return nil, domain.Forbidden("agents cannot pin entries")
	}
	return norm, nil
}

func checkProvenance(ctx context.Context, q *store.Queries, c Caller, sessionID *uuid.UUID, sourceRef *string) error {
	if sessionID != nil {
		sess, err := q.GetSession(ctx, *sessionID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && sess.PrincipalID != c.Principal.ID) {
			return domain.Validation("session %s not found", *sessionID)
		} else if err != nil {
			return err
		}
	}
	if c.Principal.IsAgent() && sessionID == nil && (sourceRef == nil || strings.TrimSpace(*sourceRef) == "") {
		return domain.Unprocessable("provenance_required", "agents must provide session_id or source_ref")
	}
	return nil
}

func checkSecrets(c Caller, space, title string, summary *string, body string, attrs json.RawMessage, allow []string) error {
	if len(allow) > 0 && (c.Principal.IsAgent() || !c.Scope.Can(space, domain.LevelReview)) {
		return domain.Forbidden("allow_secret_patterns requires review permission and a human principal")
	}
	findings := quality.ScanSecrets(quality.SecretFields(title, summary, body, attrs), allow)
	if len(findings) > 0 {
		return domain.Unprocessable("secret_detected",
			"content looks like it contains a secret (%s in %s); remove it before saving", findings[0].Rule, findings[0].Field).
			With("findings", findings)
	}
	return nil
}

func contentWarning(ctx context.Context, q *store.Queries, spaceID uuid.UUID, body string, exclude uuid.UUID, mode string) ([]Warning, error) {
	if mode != "" && mode != "warn" && mode != "reject" {
		return nil, domain.Validation("on_duplicate_content must be warn or reject")
	}
	twins, err := q.ContentTwins(ctx, spaceID, body, exclude)
	if err != nil || len(twins) == 0 {
		return nil, err
	}
	if mode == "reject" {
		return nil, domain.DuplicateContent(twins[0].ID.String())
	}
	return []Warning{{
		Code:    "duplicate_content",
		Message: "entries with the same text already exist in this space — update one of them instead of keeping copies",
		Items:   twins,
	}}, nil
}

func similarWarnings(ctx context.Context, q *store.Queries, spaceID uuid.UUID, projectID *uuid.UUID, kind, title string, exclude uuid.UUID) ([]Warning, error) {
	sim, err := q.SimilarTitles(ctx, spaceID, projectID, kind, title, exclude)
	if err != nil || len(sim) == 0 {
		return []Warning{}, err
	}
	return []Warning{{
		Code:    "similar_entries",
		Message: "similar entries exist — consider updating one of them instead",
		Items:   sim,
	}}, nil
}

// mapWriteError turns constraint violations into domain errors.
func mapWriteError(ctx context.Context, q *store.Queries, err error, spaceID uuid.UUID, hash []byte) error {
	if store.IsUniqueViolation(err, "entries_dedup_idx") {
		if id, e := q.LiveByHash(ctx, spaceID, hash); e == nil {
			return domain.Duplicate(id.String())
		}
		return domain.Conflict("an identical entry already exists")
	}
	if store.IsUniqueViolation(err, "entries_pkey") {
		return domain.Conflict("entry id already exists")
	}
	if pg, ok := store.IsConstraintViolation(err); ok {
		return domain.Validation("constraint %s violated: %s", pg.ConstraintName, pg.Message)
	}
	return err
}

func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}

// ── Read ──────────────────────────────────────────────────────────────────────

type EntryDetail struct {
	domain.Entry
	Links struct {
		Outgoing []domain.Link `json:"outgoing"`
		Incoming []domain.Link `json:"incoming"`
	} `json:"links"`
	Usage *store.EntryUsage `json:"usage,omitempty"` // only on a full read (ReadEntry)
	// ReviewDue is when the entry is due for re-check (ADR-12): review_after or last change + the kind's period.
	ReviewDue *string `json:"review_due,omitempty"`
}

// usageWindow is the period of usage shown on an entry and used by the health report (days).
const usageWindow = 30

// ReadEntry is GetEntry for a full read by a client (kn_get, the entry page): it adds the usage of the last
// 30 days and counts the read as "opened" (ADR-9).
func (s *Service) ReadEntry(ctx context.Context, c Caller, id uuid.UUID) (EntryDetail, error) {
	d, err := s.GetEntry(ctx, c, id)
	if err != nil {
		return d, err
	}
	u, err := s.store.Q().UsageOf(ctx, id, usageWindow)
	if err != nil {
		return d, err
	}
	d.Usage = &u
	// Only live knowledge is re-checked; a date on a retired entry would only suggest pointless work.
	if d.Status == domain.StatusActive {
		due, err := s.store.Q().ReviewDue(ctx, id)
		if err != nil {
			return d, err
		}
		if due != nil {
			v := due.Format(time.DateOnly)
			d.ReviewDue = &v
		}
	}
	s.recordUsage(ctx, []uuid.UUID{id}, store.UsageOpened)
	return d, nil
}

func (s *Service) GetEntry(ctx context.Context, c Caller, id uuid.UUID) (EntryDetail, error) {
	q := s.store.Q()
	e, err := s.getEntry(ctx, q, c, id, false)
	if err != nil {
		return EntryDetail{}, err
	}
	var d EntryDetail
	d.Entry = e.Entry
	d.Links.Outgoing, d.Links.Incoming, err = q.Links(ctx, c.visibility(), id)
	if d.Links.Outgoing == nil {
		d.Links.Outgoing = []domain.Link{}
	}
	if d.Links.Incoming == nil {
		d.Links.Incoming = []domain.Link{}
	}
	return d, err
}

type ListEntriesInput struct {
	Space        string
	Project      string
	Kinds        []string
	Statuses     []string
	Tags         []string
	Pinned       *bool
	UpdatedSince *time.Time
	Limit        int
	Cursor       string
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (s *Service) ListEntries(ctx context.Context, c Caller, in ListEntriesInput) (Page[domain.Entry], error) {
	q := s.store.Q()
	f := store.EntryFilter{Kinds: in.Kinds, Statuses: in.Statuses, Tags: in.Tags, Pinned: in.Pinned,
		UpdatedSince: in.UpdatedSince, Limit: in.Limit, Cursor: in.Cursor}
	if in.Space != "" {
		if _, err := requireSpace(ctx, q, c, in.Space, domain.LevelRead); err != nil {
			return Page[domain.Entry]{}, err
		}
		f.Spaces = []string{in.Space}
	}
	if in.Project != "" {
		p, _, err := resolveProject(ctx, q, c, in.Project, domain.LevelRead)
		if err != nil {
			return Page[domain.Entry]{}, err
		}
		f.ProjectID = &p.ID
	}
	rows, next, err := q.ListEntries(ctx, c.visibility(), f)
	if err != nil {
		return Page[domain.Entry]{}, err
	}
	items := make([]domain.Entry, len(rows))
	for i, r := range rows {
		items[i] = r.Entry
	}
	return Page[domain.Entry]{Items: items, NextCursor: next}, nil
}

// ── Update ────────────────────────────────────────────────────────────────────

type PatchEntryInput struct {
	Project             *string         `json:"project"` // "" detaches from project
	Title               *string         `json:"title"`
	Summary             *string         `json:"summary"` // "" clears
	Body                *string         `json:"body"`
	Attributes          json.RawMessage `json:"attributes"`
	Tags                *[]string       `json:"tags"`
	Pinned              *bool           `json:"pinned"`
	Confidence          *float32        `json:"confidence"`
	ReviewAfter         *string         `json:"review_after"` // "" clears
	SourceRef           *string         `json:"source_ref"`
	ChangeNote          string          `json:"change_note"`
	AllowSecretPatterns []string        `json:"allow_secret_patterns"`
}

func (s *Service) PatchEntry(ctx context.Context, c Caller, id uuid.UUID, expectedVersion int, in PatchEntryInput) (EntryResult, error) {
	if err := s.checkWriteRate(c); err != nil {
		return EntryResult{}, err
	}
	var res EntryResult
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		cur, err := s.getEntry(ctx, q, c, id, true)
		if err != nil {
			return err
		}
		if !c.Scope.Can(cur.Space, domain.LevelWrite) {
			return domain.ForbiddenSpace(cur.Space)
		}
		if cur.Version != expectedVersion {
			return domain.VersionConflict(expectedVersion, cur.Version)
		}
		w := writeFromRow(cur)
		if in.Project != nil {
			if *in.Project == "" {
				w.ProjectID = nil
			} else {
				p, _, err := resolveProject(ctx, q, c, *in.Project, domain.LevelWrite)
				if err != nil {
					return err
				}
				if p.Space != cur.Space {
					return domain.Validation("cannot move an entry to another space; use promote")
				}
				w.ProjectID = &p.ID
			}
		}
		if in.Title != nil {
			w.Title = strings.TrimSpace(*in.Title)
		}
		if in.Summary != nil {
			w.Summary = trimPtr(in.Summary)
		}
		if in.Body != nil {
			w.Body = *in.Body
		}
		if in.Attributes != nil {
			w.Attributes = in.Attributes
		}
		if in.Tags != nil {
			w.Tags = *in.Tags
		}
		if in.Confidence != nil {
			w.Confidence = in.Confidence
		}
		if in.ReviewAfter != nil {
			w.ReviewAfter = trimPtr(in.ReviewAfter)
		}
		if in.SourceRef != nil {
			w.SourceRef = trimPtr(in.SourceRef)
		}
		pinChanged := in.Pinned != nil && *in.Pinned != cur.Pinned
		if in.Pinned != nil {
			w.Pinned = *in.Pinned
		}
		if pinChanged && c.Principal.IsAgent() {
			return domain.Forbidden("agents cannot pin or unpin entries")
		}
		var err2 error
		res, err2 = s.saveRevision(ctx, q, c, cur, w, in.ChangeNote, in.AllowSecretPatterns, "entry.update")
		return err2
	})
	return res, err
}

func writeFromRow(r store.EntryRow) store.EntryWrite {
	return store.EntryWrite{
		SpaceID: r.SpaceID, ProjectID: r.ProjectID, Kind: r.Kind, Title: r.Title, Summary: r.Summary,
		Body: r.Body, Attributes: r.Attributes, Tags: r.Tags, Status: r.Status, Pinned: r.Pinned,
		Confidence: r.Confidence, ContentHash: r.ContentHash, SupersededBy: r.SupersededBy,
		ReviewAfter: r.ReviewAfter, SessionID: r.SessionID, SourceRef: r.SourceRef,
	}
}

// saveRevision validates a modified entry (pipeline steps 2–7) and writes the next version.
func (s *Service) saveRevision(ctx context.Context, q *store.Queries, c Caller, cur store.EntryRow, w store.EntryWrite, note string, allow []string, action string) (EntryResult, error) {
	pinned := w.Pinned && c.Principal.IsAgent() && !cur.Pinned // agents may keep, not add, a pin
	tags, err := s.checkStructure(c, w.Title, w.Summary, w.Body, w.Tags, pinned, w.Confidence, w.ReviewAfter)
	if err != nil {
		return EntryResult{}, err
	}
	w.Tags = tags
	kind, err := q.Kind(ctx, w.Kind)
	if err != nil {
		return EntryResult{}, err
	}
	if err := s.schemas.Validate(kind.Kind, kind.AttributesSchema, w.Attributes); err != nil {
		return EntryResult{}, domain.Validation("%s", err)
	}
	if err := checkSecrets(c, cur.Space, w.Title, w.Summary, w.Body, w.Attributes, allow); err != nil {
		return EntryResult{}, err
	}
	w.ContentHash = quality.ContentHash(w.Kind, w.Title, w.Body)
	if live(w.Status) {
		if id, err := q.LiveByHash(ctx, w.SpaceID, w.ContentHash); err == nil && id != cur.ID {
			return EntryResult{}, domain.Duplicate(id.String())
		}
	}
	warnings, err := similarWarnings(ctx, q, w.SpaceID, w.ProjectID, w.Kind, w.Title, cur.ID)
	if err != nil {
		return EntryResult{}, err
	}
	if live(w.Status) && w.Body != cur.Body {
		dup, err := contentWarning(ctx, q, w.SpaceID, w.Body, cur.ID, "")
		if err != nil {
			return EntryResult{}, err
		}
		warnings = append(warnings, dup...)
	}
	w.By = c.Principal.ID
	if note != "" {
		if err := q.SetChangeNote(ctx, note); err != nil {
			return EntryResult{}, err
		}
	}
	if err := q.UpdateEntry(ctx, cur.ID, cur.Version, w); err != nil {
		return EntryResult{}, mapWriteError(ctx, q, err, w.SpaceID, w.ContentHash)
	}
	if err := q.Audit(ctx, c.audit(action, "entry", &cur.SpaceID, &cur.ID,
		map[string]any{"from_version": cur.Version, "status": w.Status})); err != nil {
		return EntryResult{}, err
	}
	row, err := q.GetEntry(ctx, c.visibility(), cur.ID, false)
	if err != nil {
		return EntryResult{}, err
	}
	return EntryResult{Entry: row.Entry, Warnings: warnings}, nil
}

func live(s domain.Status) bool { return s != domain.StatusArchived && s != domain.StatusSuperseded }

// ── Lifecycle ─────────────────────────────────────────────────────────────────

type TransitionInput struct {
	To           domain.Status `json:"to"`
	SupersededBy *uuid.UUID    `json:"superseded_by"`
	Note         string        `json:"note"`
}

// Transition changes status. expectedVersion is optional (0 = no check).
func (s *Service) Transition(ctx context.Context, c Caller, id uuid.UUID, expectedVersion int, in TransitionInput) (EntryResult, error) {
	if err := s.checkWriteRate(c); err != nil {
		return EntryResult{}, err
	}
	var res EntryResult
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		var err error
		res, err = s.transition(ctx, q, c, id, expectedVersion, in, "entry.transition")
		return err
	})
	return res, err
}

func (s *Service) transition(ctx context.Context, q *store.Queries, c Caller, id uuid.UUID, expectedVersion int, in TransitionInput, action string) (EntryResult, error) {
	if !in.To.Valid() {
		return EntryResult{}, domain.Validation("unknown status %q", in.To)
	}
	cur, err := s.getEntry(ctx, q, c, id, true)
	if err != nil {
		return EntryResult{}, err
	}
	if !c.Scope.Can(cur.Space, domain.LevelWrite) {
		return EntryResult{}, domain.ForbiddenSpace(cur.Space)
	}
	if expectedVersion > 0 && cur.Version != expectedVersion {
		return EntryResult{}, domain.VersionConflict(expectedVersion, cur.Version)
	}
	if err := domain.CheckTransition(cur.Status, in.To, c.actor(cur.Space)); err != nil {
		return EntryResult{}, err
	}
	w := writeFromRow(cur)
	w.Status = in.To
	w.SupersededBy = nil
	if in.To == domain.StatusSuperseded {
		if in.SupersededBy == nil {
			return EntryResult{}, domain.Validation("superseded_by is required when status is superseded")
		}
		if *in.SupersededBy == id {
			return EntryResult{}, domain.Validation("an entry cannot supersede itself")
		}
		if _, err := s.getEntry(ctx, q, c, *in.SupersededBy, false); err != nil {
			return EntryResult{}, err
		}
		w.SupersededBy = in.SupersededBy
		if err := q.InsertLink(ctx, *in.SupersededBy, id, "supersedes", c.Principal.ID); err != nil {
			return EntryResult{}, err
		}
	}
	if in.To == domain.StatusActive {
		w.ReviewAfter = clearPastReview(w.ReviewAfter)
	}
	return s.saveRevision(ctx, q, c, cur, w, in.Note, nil, action)
}

func clearPastReview(d *string) *string {
	if d == nil {
		return nil
	}
	if t, err := time.Parse(time.DateOnly, *d); err == nil && !t.After(time.Now()) {
		return nil
	}
	return d
}

type PromoteInput struct {
	ToSpace   string `json:"to_space"`
	ToProject string `json:"to_project"`
}

// Promote copies an entry into another space and links the copy with derived_from.
// It is the only way to move knowledge across spaces, so it is always visible in the audit log.
func (s *Service) Promote(ctx context.Context, c Caller, id uuid.UUID, in PromoteInput) (EntryResult, error) {
	if err := s.checkWriteRate(c); err != nil {
		return EntryResult{}, err
	}
	var res EntryResult
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		src, err := s.getEntry(ctx, q, c, id, false)
		if err != nil {
			return err
		}
		if !c.Scope.Can(src.Space, domain.LevelWrite) {
			return domain.ForbiddenSpace(src.Space)
		}
		target := in.ToSpace
		if in.ToProject != "" {
			if target, _, err = ParseProjectRef(in.ToProject); err != nil {
				return err
			}
		}
		if target == src.Space {
			return domain.Validation("entry is already in space %s", target)
		}
		ref := "kn:" + src.ID.String()
		res, err = s.createEntry(ctx, q, c, CreateEntryInput{
			Space: in.ToSpace, Project: in.ToProject, Kind: src.Kind, Title: src.Title, Summary: src.Summary,
			Body: src.Body, Attributes: src.Attributes, Tags: src.Tags, Confidence: src.Confidence,
			SourceRef: &ref,
		})
		if err != nil {
			return err
		}
		if err := q.InsertLink(ctx, res.Entry.ID, src.ID, "derived_from", c.Principal.ID); err != nil {
			return err
		}
		return q.Audit(ctx, c.audit("entry.promote", "entry", &src.SpaceID, &src.ID,
			map[string]any{"to_space": res.Entry.Space, "new_id": res.Entry.ID}))
	})
	return res, err
}

// ── History ───────────────────────────────────────────────────────────────────

func (s *Service) Versions(ctx context.Context, c Caller, id uuid.UUID) ([]domain.EntryVersion, error) {
	q := s.store.Q()
	if _, err := s.getEntry(ctx, q, c, id, false); err != nil {
		return nil, err
	}
	return q.Versions(ctx, id)
}

func (s *Service) Version(ctx context.Context, c Caller, id uuid.UUID, n int) (domain.EntryVersion, error) {
	q := s.store.Q()
	if _, err := s.getEntry(ctx, q, c, id, false); err != nil {
		return domain.EntryVersion{}, err
	}
	v, err := q.Version(ctx, id, n)
	if errors.Is(err, store.ErrNotFound) {
		return v, domain.NotFound("version %d of entry %s not found", n, id)
	}
	return v, err
}

// Revert writes a new version with the content of an older one. Status is not changed.
func (s *Service) Revert(ctx context.Context, c Caller, id uuid.UUID, expectedVersion, toVersion int) (EntryResult, error) {
	if err := s.checkWriteRate(c); err != nil {
		return EntryResult{}, err
	}
	var res EntryResult
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		cur, err := s.getEntry(ctx, q, c, id, true)
		if err != nil {
			return err
		}
		if !c.Scope.Can(cur.Space, domain.LevelWrite) {
			return domain.ForbiddenSpace(cur.Space)
		}
		if cur.Version != expectedVersion {
			return domain.VersionConflict(expectedVersion, cur.Version)
		}
		old, err := q.Version(ctx, id, toVersion)
		if errors.Is(err, store.ErrNotFound) {
			return domain.NotFound("version %d of entry %s not found", toVersion, id)
		} else if err != nil {
			return err
		}
		oldProject, err := q.VersionProjectID(ctx, id, toVersion)
		if err != nil {
			return err
		}
		w := writeFromRow(cur)
		w.ProjectID, w.Title, w.Summary, w.Body = oldProject, old.Title, old.Summary, old.Body
		w.Attributes, w.Tags, w.Pinned = old.Attributes, old.Tags, old.Pinned
		if w.Pinned != cur.Pinned && c.Principal.IsAgent() {
			w.Pinned = cur.Pinned
		}
		res, err = s.saveRevision(ctx, q, c, cur, w, "revert to version "+itoa(toVersion), nil, "entry.revert")
		return err
	})
	return res, err
}

// ── Links ─────────────────────────────────────────────────────────────────────

func (s *Service) AddLink(ctx context.Context, c Caller, from, to uuid.UUID, rel domain.LinkRel) error {
	if !domain.LinkRels[rel] {
		return domain.Validation("unknown link type %q", rel)
	}
	if from == to {
		return domain.Validation("an entry cannot link to itself")
	}
	return s.store.Tx(ctx, func(q *store.Queries) error {
		src, err := s.getEntry(ctx, q, c, from, false)
		if err != nil {
			return err
		}
		if !c.Scope.Can(src.Space, domain.LevelWrite) {
			return domain.ForbiddenSpace(src.Space)
		}
		if _, err := s.getEntry(ctx, q, c, to, false); err != nil {
			return err
		}
		if err := q.InsertLink(ctx, from, to, rel, c.Principal.ID); err != nil {
			return err
		}
		return q.Audit(ctx, c.audit("link.create", "entry", &src.SpaceID, &from, map[string]any{"to": to, "rel": rel}))
	})
}

func (s *Service) DeleteLink(ctx context.Context, c Caller, from, to uuid.UUID, rel domain.LinkRel) error {
	return s.store.Tx(ctx, func(q *store.Queries) error {
		src, err := s.getEntry(ctx, q, c, from, false)
		if err != nil {
			return err
		}
		if !c.Scope.Can(src.Space, domain.LevelWrite) {
			return domain.ForbiddenSpace(src.Space)
		}
		ok, err := q.DeleteLink(ctx, from, to, rel)
		if err != nil {
			return err
		}
		if !ok {
			return domain.NotFound("link not found")
		}
		return q.Audit(ctx, c.audit("link.delete", "entry", &src.SpaceID, &from, map[string]any{"to": to, "rel": rel}))
	})
}
