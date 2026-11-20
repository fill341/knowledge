package service

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"kn.local/api/internal/domain"
	"kn.local/api/internal/export"
	"kn.local/api/internal/store"
)

// JSONL record types. Projects come first, then entries, then links, so a file can be
// imported in one pass.
type exportRecord struct {
	Type string `json:"type"` // project | entry | link
}

type ProjectRecord struct {
	Type        string          `json:"type"`
	Space       string          `json:"space"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	RepoURLs    []string        `json:"repo_urls"`
	Metadata    json.RawMessage `json:"metadata"`
}

type EntryRecord struct {
	Type string `json:"type"`
	domain.Entry
	Versions []domain.EntryVersion `json:"versions,omitempty"`
}

type LinkRecord struct {
	Type   string         `json:"type"`
	FromID uuid.UUID      `json:"from_id"`
	ToID   uuid.UUID      `json:"to_id"`
	Rel    domain.LinkRel `json:"rel"`
}

type ExportInput struct {
	Spaces         []string
	IncludeHistory bool
}

// exportSpaces resolves which spaces to export: requested ones (checked) or all readable.
func (s *Service) exportSpaces(ctx context.Context, c Caller, requested []string) ([]string, error) {
	for _, sp := range requested {
		if !c.Scope.Can(sp, domain.LevelRead) {
			return nil, domain.ForbiddenSpace(sp)
		}
	}
	if len(requested) > 0 {
		return requested, nil
	}
	spaces, err := s.store.Q().ListSpaces(ctx, c.visibility())
	if err != nil {
		return nil, err
	}
	out := make([]string, len(spaces))
	for i, sp := range spaces {
		out[i] = sp.Slug
	}
	return out, nil
}

// eachEntry pages through all entries (any status) of the given spaces.
func (s *Service) eachEntry(ctx context.Context, c Caller, spaces []string, fn func(store.EntryRow) error) error {
	q := s.store.Q()
	cursor := ""
	for {
		rows, next, err := q.ListEntries(ctx, c.visibility(), store.EntryFilter{Spaces: spaces, Limit: 200, Cursor: cursor})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := fn(r); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		cursor = next
	}
}

// ExportJSONL streams projects, entries and links as newline-delimited JSON.
func (s *Service) ExportJSONL(ctx context.Context, c Caller, in ExportInput, w io.Writer) error {
	spaces, err := s.exportSpaces(ctx, c, in.Spaces)
	if err != nil {
		return err
	}
	q := s.store.Q()
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)

	projects, err := q.ListProjects(ctx, store.Visibility{Spaces: spaces}, store.ProjectFilter{IncludeArchived: true})
	if err != nil {
		return err
	}
	for _, p := range projects {
		if err := enc.Encode(ProjectRecord{Type: "project", Space: p.Space, Slug: p.Slug, Name: p.Name,
			Description: p.Description, RepoURLs: p.RepoURLs, Metadata: p.Metadata}); err != nil {
			return err
		}
	}
	var links []LinkRecord
	vis := store.Visibility{Spaces: spaces}
	err = s.eachEntry(ctx, c, spaces, func(r store.EntryRow) error {
		rec := EntryRecord{Type: "entry", Entry: r.Entry}
		if in.IncludeHistory {
			if rec.Versions, err = q.Versions(ctx, r.ID); err != nil {
				return err
			}
		}
		out, _, err := q.Links(ctx, vis, r.ID)
		if err != nil {
			return err
		}
		for _, l := range out {
			links = append(links, LinkRecord{Type: "link", FromID: l.FromID, ToID: l.ToID, Rel: l.Rel})
		}
		return enc.Encode(rec)
	})
	if err != nil {
		return err
	}
	for _, l := range links {
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	s.auditRead(ctx, c, "export", "jsonl", 0, spaces)
	return bw.Flush()
}

// ExportMarkdown writes a tar.gz of Markdown files (Obsidian vault layout).
func (s *Service) ExportMarkdown(ctx context.Context, c Caller, in ExportInput, w io.Writer) error {
	spaces, err := s.exportSpaces(ctx, c, in.Spaces)
	if err != nil {
		return err
	}
	q := s.store.Q()
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	now := time.Now()
	add := func(name, content string) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), ModTime: now}); err != nil {
			return err
		}
		_, err := io.WriteString(tw, content)
		return err
	}
	if err := add("_README.md", export.VaultReadme); err != nil {
		return err
	}
	vis := store.Visibility{Spaces: spaces}
	err = s.eachEntry(ctx, c, spaces, func(r store.EntryRow) error {
		out, _, err := q.Links(ctx, vis, r.ID)
		if err != nil {
			return err
		}
		return add(export.Path(r.Entry), export.Markdown(r.Entry, out))
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	s.auditRead(ctx, c, "export", "markdown", 0, spaces)
	return gz.Close()
}

// ── Import ────────────────────────────────────────────────────────────────────

type ImportInput struct {
	Mode   string // create_only | upsert_by_id
	DryRun bool
	// SkipDuplicateContent skips new entries whose text equals a live entry of the space (another id).
	// Off by default so restoring an export reproduces it exactly; duplicates are reported either way.
	SkipDuplicateContent bool
}

// ImportDuplicate is an imported entry whose text equals a live entry already in the space.
type ImportDuplicate struct {
	Line       int       `json:"line"`
	ID         uuid.UUID `json:"id"`
	Title      string    `json:"title"`
	ExistingID uuid.UUID `json:"existing_id"`
	Skipped    bool      `json:"skipped"`
}

type ImportError struct {
	Line  int    `json:"line"`
	Error string `json:"error"`
}

type ImportResult struct {
	DryRun          bool              `json:"dry_run"`
	ProjectsCreated int               `json:"projects_created"`
	EntriesCreated  int               `json:"entries_created"`
	EntriesUpdated  int               `json:"entries_updated"`
	EntriesSkipped  int               `json:"entries_skipped"`
	LinksCreated    int               `json:"links_created"`
	Duplicates      []ImportDuplicate `json:"duplicates"`
	Errors          []ImportError     `json:"errors"`
}

var errDryRun = errors.New("dry run")

// Import restores a JSONL export. It is an admin operation: identities and statuses are
// preserved, every entry still passes validation and secret scanning. Each line is applied in
// its own savepoint, so bad lines are reported without aborting the rest.
func (s *Service) Import(ctx context.Context, c Caller, r io.Reader, in ImportInput) (ImportResult, error) {
	if err := requireAdmin(c); err != nil {
		return ImportResult{}, err
	}
	if in.Mode == "" {
		in.Mode = "create_only"
	}
	if in.Mode != "create_only" && in.Mode != "upsert_by_id" {
		return ImportResult{}, domain.Validation("mode must be create_only or upsert_by_id")
	}
	res := ImportResult{DryRun: in.DryRun, Errors: []ImportError{}, Duplicates: []ImportDuplicate{}}
	type fixup struct {
		line         int
		id, replacer uuid.UUID
	}
	var supersede []fixup

	err := s.store.Tx(ctx, func(q *store.Queries) error {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
		line := 0
		fail := func(err error) {
			msg := err.Error()
			if de, ok := domain.AsError(err); ok {
				msg = de.Code + ": " + de.Detail
			}
			res.Errors = append(res.Errors, ImportError{Line: line, Error: msg})
		}
		for sc.Scan() {
			line++
			raw := sc.Bytes()
			if len(raw) == 0 {
				continue
			}
			var head exportRecord
			if err := json.Unmarshal(raw, &head); err != nil {
				fail(fmt.Errorf("invalid JSON: %w", err))
				continue
			}
			err := q.Savepoint(ctx, func(q *store.Queries) error {
				switch head.Type {
				case "project":
					var p ProjectRecord
					if err := json.Unmarshal(raw, &p); err != nil {
						return domain.Validation("invalid project record: %s", err)
					}
					created, err := s.importProject(ctx, q, c, p)
					if created {
						res.ProjectsCreated++
					}
					return err
				case "entry":
					var e EntryRecord
					if err := json.Unmarshal(raw, &e); err != nil {
						return domain.Validation("invalid entry record: %s", err)
					}
					dup, err := s.importTwin(ctx, q, c, e.Entry)
					if err != nil {
						return err
					}
					if dup != nil {
						dup.Line, dup.Skipped = line, in.SkipDuplicateContent
						res.Duplicates = append(res.Duplicates, *dup)
						if dup.Skipped {
							res.EntriesSkipped++
							return nil
						}
					}
					outcome, err := s.importEntry(ctx, q, c, e.Entry, in.Mode)
					if err != nil {
						return err
					}
					switch outcome {
					case "created":
						res.EntriesCreated++
					case "updated":
						res.EntriesUpdated++
					case "skipped":
						res.EntriesSkipped++
					}
					if e.Status == domain.StatusSuperseded && e.SupersededBy != nil && outcome != "skipped" {
						supersede = append(supersede, fixup{line: line, id: e.ID, replacer: *e.SupersededBy})
					}
					return nil
				case "link":
					var l LinkRecord
					if err := json.Unmarshal(raw, &l); err != nil {
						return domain.Validation("invalid link record: %s", err)
					}
					if !domain.LinkRels[l.Rel] {
						return domain.Validation("unknown link type %q", l.Rel)
					}
					if err := q.InsertLink(ctx, l.FromID, l.ToID, l.Rel, c.Principal.ID); err != nil {
						return err
					}
					res.LinksCreated++
					return nil
				default:
					return domain.Validation("unknown record type %q", head.Type)
				}
			})
			if err != nil {
				fail(err)
			}
		}
		if err := sc.Err(); err != nil {
			return domain.BadRequest("read input: %s", err)
		}
		// Superseded entries are imported as deprecated and switched once their replacement exists.
		for _, f := range supersede {
			line = f.line
			err := q.Savepoint(ctx, func(q *store.Queries) error {
				cur, err := s.getEntry(ctx, q, c, f.id, true)
				if err != nil {
					return err
				}
				w := writeFromRow(cur)
				w.Status, w.SupersededBy = domain.StatusSuperseded, &f.replacer
				_, err = s.saveRevision(ctx, q, c, cur, w, "import: restore superseded status", nil, "entry.import")
				return err
			})
			if err != nil {
				fail(err)
			}
		}
		if err := q.Audit(ctx, c.audit("import", "", nil, nil, map[string]any{
			"mode": in.Mode, "dry_run": in.DryRun, "created": res.EntriesCreated, "updated": res.EntriesUpdated,
			"errors": len(res.Errors)})); err != nil {
			return err
		}
		if in.DryRun {
			return errDryRun
		}
		return nil
	})
	if errors.Is(err, errDryRun) {
		err = nil
	}
	return res, err
}

// importTwin reports a live entry of the target space with the same text as a new (unknown id) live entry.
// Entries in the same file count too: earlier lines are already inserted in this transaction.
func (s *Service) importTwin(ctx context.Context, q *store.Queries, c Caller, e domain.Entry) (*ImportDuplicate, error) {
	if e.ID == uuid.Nil || !live(e.Status) {
		return nil, nil
	}
	if _, err := q.GetEntry(ctx, c.visibility(), e.ID, true); err == nil {
		return nil, nil // known id: handled by mode (skip or update), not a new copy
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	sp, err := q.SpaceBySlug(ctx, e.Space)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil // importEntry reports the unknown space
	} else if err != nil {
		return nil, err
	}
	twins, err := q.ContentTwins(ctx, sp.ID, e.Body, e.ID)
	if err != nil || len(twins) == 0 {
		return nil, err
	}
	return &ImportDuplicate{ID: e.ID, Title: e.Title, ExistingID: twins[0].ID}, nil
}

func (s *Service) importProject(ctx context.Context, q *store.Queries, c Caller, p ProjectRecord) (bool, error) {
	sp, err := requireSpace(ctx, q, c, p.Space, domain.LevelWrite)
	if err != nil {
		return false, err
	}
	if _, err := q.GetProject(ctx, c.visibility(), p.Space, p.Slug); err == nil {
		return false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	if !projectSlugRe.MatchString(p.Slug) {
		return false, domain.Validation("invalid project slug %q", p.Slug)
	}
	_, err = q.CreateProject(ctx, store.ProjectInput{SpaceID: sp.ID, Slug: p.Slug, Name: p.Name,
		Description: p.Description, RepoURLs: normalizeRepoURLs(p.RepoURLs), Metadata: p.Metadata})
	return err == nil, err
}

func (s *Service) importEntry(ctx context.Context, q *store.Queries, c Caller, e domain.Entry, mode string) (string, error) {
	if e.ID == uuid.Nil {
		return "", domain.Validation("entry id is required")
	}
	if !e.Status.Valid() {
		return "", domain.Validation("invalid status %q", e.Status)
	}
	status := e.Status
	if status == domain.StatusSuperseded {
		status = domain.StatusDeprecated
	}
	project := ""
	if e.Project != nil {
		project = *e.Project
	}

	cur, err := q.GetEntry(ctx, c.visibility(), e.ID, true)
	switch {
	case err == nil && mode == "create_only":
		return "skipped", nil
	case err == nil:
		if cur.Space != e.Space {
			return "", domain.Validation("entry %s exists in space %s, not %s", e.ID, cur.Space, e.Space)
		}
		w := writeFromRow(cur)
		w.Title, w.Summary, w.Body, w.Attributes, w.Tags = e.Title, e.Summary, e.Body, e.Attributes, e.Tags
		w.Status, w.Pinned, w.Confidence, w.ReviewAfter, w.SourceRef = status, e.Pinned, e.Confidence, e.ReviewAfter, e.SourceRef
		w.SupersededBy = nil
		w.ProjectID = nil
		if project != "" {
			p, _, err := resolveProject(ctx, q, c, project, domain.LevelWrite)
			if err != nil {
				return "", err
			}
			w.ProjectID = &p.ID
		}
		_, err := s.saveRevision(ctx, q, c, cur, w, "import", nil, "entry.import")
		return "updated", err
	case !errors.Is(err, store.ErrNotFound):
		return "", err
	}
	id := e.ID
	_, err = s.createEntry(ctx, q, c, CreateEntryInput{
		Space: e.Space, Project: project, Kind: e.Kind, Title: e.Title, Summary: e.Summary, Body: e.Body,
		Attributes: e.Attributes, Tags: e.Tags, Pinned: e.Pinned, Confidence: e.Confidence,
		ReviewAfter: e.ReviewAfter, SourceRef: e.SourceRef, id: &id, forceStatus: &status,
	})
	return "created", err
}
