package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"kn.local/api/internal/domain"
	"kn.local/api/internal/store"
)

func itoa(n int) string { return strconv.Itoa(n) }

var projectSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// scpLike matches git's scp-style remotes: user@host:path
var scpLike = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// NormalizeRepoURL maps the many spellings of a git remote to host/path:
// git@github.com:acme/x.git, https://github.com/acme/x, ssh://git@github.com/acme/x.git → github.com/acme/x
func NormalizeRepoURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	var host, path string
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return strings.ToLower(s)
		}
		// A local repository without a remote is identified by its path (kclient.RepoIDs); paths keep their case.
		if u.Scheme == "file" {
			if p := strings.TrimRight(u.Path, "/"); p != "" {
				return "file://" + p
			}
			return ""
		}
		host, path = u.Hostname(), u.Path
	} else if m := scpLike.FindStringSubmatch(s); m != nil {
		host, path = m[1], m[2]
	} else {
		return strings.ToLower(strings.TrimSuffix(strings.Trim(s, "/"), ".git"))
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	return strings.ToLower(host) + "/" + path
}

func normalizeRepoURLs(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, u := range in {
		n := NormalizeRepoURL(u)
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

type ListProjectsInput struct {
	Space           string
	RepoURL         string
	IncludeArchived bool
}

func (s *Service) ListProjects(ctx context.Context, c Caller, in ListProjectsInput) ([]domain.Project, error) {
	q := s.store.Q()
	if in.Space != "" {
		if _, err := requireSpace(ctx, q, c, in.Space, domain.LevelRead); err != nil {
			return nil, err
		}
	}
	return q.ListProjects(ctx, c.visibility(), store.ProjectFilter{
		Space: in.Space, RepoURL: NormalizeRepoURL(in.RepoURL), IncludeArchived: in.IncludeArchived,
	})
}

func (s *Service) GetProject(ctx context.Context, c Caller, space, slug string) (domain.Project, error) {
	p, _, err := resolveProject(ctx, s.store.Q(), c, space+"/"+slug, domain.LevelRead)
	return p, err
}

// ResolveProject finds the project for a git remote among the caller's readable spaces.
// When the same repo is registered in several spaces, the one where the caller can write wins.
func (s *Service) ResolveProject(ctx context.Context, c Caller, repoURL string) (domain.Project, error) {
	norm := NormalizeRepoURL(repoURL)
	if norm == "" {
		return domain.Project{}, domain.Validation("repo_url is required")
	}
	ps, err := s.store.Q().ListProjects(ctx, c.visibility(), store.ProjectFilter{RepoURL: norm})
	if err != nil {
		return domain.Project{}, err
	}
	if len(ps) == 0 {
		return domain.Project{}, domain.NotFound("no project registered for %s", norm)
	}
	for _, p := range ps {
		if c.Scope.Can(p.Space, domain.LevelWrite) {
			return p, nil
		}
	}
	return ps[0], nil
}

type CreateProjectInput struct {
	Space       string          `json:"space"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	RepoURLs    []string        `json:"repo_urls"`
	Metadata    json.RawMessage `json:"metadata"`
}

func (s *Service) CreateProject(ctx context.Context, c Caller, in CreateProjectInput) (domain.Project, error) {
	if err := s.checkWriteRate(c); err != nil {
		return domain.Project{}, err
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if !projectSlugRe.MatchString(in.Slug) {
		return domain.Project{}, domain.Validation("slug must match %s", projectSlugRe)
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = in.Slug
	}
	var out domain.Project
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		sp, err := requireSpace(ctx, q, c, in.Space, domain.LevelWrite)
		if err != nil {
			return err
		}
		id, err := q.CreateProject(ctx, store.ProjectInput{
			SpaceID: sp.ID, Slug: in.Slug, Name: strings.TrimSpace(in.Name), Description: in.Description,
			RepoURLs: normalizeRepoURLs(in.RepoURLs), Metadata: in.Metadata,
		})
		if store.IsUniqueViolation(err, "") {
			return domain.Conflict("project %s/%s already exists", in.Space, in.Slug)
		} else if err != nil {
			return err
		}
		if err := q.Audit(ctx, c.audit("project.create", "project", &sp.ID, &id, map[string]any{"slug": in.Slug})); err != nil {
			return err
		}
		out, err = q.GetProject(ctx, c.visibility(), in.Space, in.Slug)
		return err
	})
	return out, err
}

type PatchProjectInput struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	RepoURLs    *[]string       `json:"repo_urls"`
	Metadata    json.RawMessage `json:"metadata"`
	Archived    *bool           `json:"archived"`
}

func (s *Service) PatchProject(ctx context.Context, c Caller, space, slug string, in PatchProjectInput) (domain.Project, error) {
	var out domain.Project
	err := s.store.Tx(ctx, func(q *store.Queries) error {
		p, sp, err := resolveProject(ctx, q, c, space+"/"+slug, domain.LevelWrite)
		if err != nil {
			return err
		}
		patch := store.ProjectPatch{Name: in.Name, Description: in.Description, Metadata: in.Metadata, Archived: in.Archived}
		if in.RepoURLs != nil {
			n := normalizeRepoURLs(*in.RepoURLs)
			patch.RepoURLs = &n
		}
		if err := q.UpdateProject(ctx, p.ID, patch); err != nil {
			return err
		}
		if err := q.Audit(ctx, c.audit("project.update", "project", &sp.ID, &p.ID, nil)); err != nil {
			return err
		}
		out, err = q.GetProject(ctx, c.visibility(), space, slug)
		if errors.Is(err, store.ErrNotFound) {
			return domain.NotFound("project not found")
		}
		return err
	})
	return out, err
}
