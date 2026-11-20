package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"kn.local/api/internal/domain"
	"kn.local/api/internal/service"
	"kn.local/api/internal/store"
)

// ── Request helpers ───────────────────────────────────────────────────────────

// decode reads a JSON body strictly: unknown fields are an error, which catches agent typos early.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		if errors.Is(err, io.EOF) {
			return domain.BadRequest("request body is required")
		}
		return domain.BadRequest("invalid JSON body: %s", err)
	}
	return nil
}

func pathID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, domain.BadRequest("%s must be a UUID", name)
	}
	return id, nil
}

func queryList(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// reportLang picks the language of report texts: ?lang= first, then the first Accept-Language tag;
// a tag starting with "ru" means Russian, anything else (or nothing) English.
func reportLang(r *http.Request) service.Lang {
	tag := strings.TrimSpace(r.URL.Query().Get("lang"))
	if tag == "" {
		tag, _, _ = strings.Cut(r.Header.Get("Accept-Language"), ",")
		tag, _, _ = strings.Cut(tag, ";")
		tag = strings.TrimSpace(tag)
	}
	if strings.HasPrefix(strings.ToLower(tag), "ru") {
		return service.LangRU
	}
	return service.LangEN
}

func queryInt(r *http.Request, name string) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, domain.BadRequest("%s must be an integer", name)
	}
	return n, nil
}

func queryBool(r *http.Request, name string) (*bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, domain.BadRequest("%s must be true or false", name)
	}
	return &b, nil
}

// ifMatch parses If-Match: "3" (quotes optional). required=false returns 0 when absent.
func ifMatch(r *http.Request, required bool) (int, error) {
	v := strings.Trim(strings.TrimPrefix(r.Header.Get("If-Match"), "W/"), `"`)
	if v == "" {
		if required {
			return 0, domain.PreconditionRequired("If-Match header with the current entry version is required")
		}
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, domain.BadRequest("If-Match must be an entry version number")
	}
	return n, nil
}

func writeEntry(w http.ResponseWriter, status int, res service.EntryResult) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, res.Entry.Version))
	if status == http.StatusCreated {
		w.Header().Set("Location", "/v1/entries/"+res.Entry.ID.String())
	}
	if res.Warnings == nil {
		res.Warnings = []service.Warning{}
	}
	writeJSON(w, status, res)
}

func items[T any](v []T) map[string]any {
	if v == nil {
		v = []T{}
	}
	return map[string]any{"items": v}
}

// ── Identity ──────────────────────────────────────────────────────────────────

func (s *Server) me(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	me, err := s.svc.Me(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, 200, me)
	return nil
}

func (s *Server) spaces(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	sp, err := s.svc.Spaces(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, 200, items(sp))
	return nil
}

func (s *Server) kinds(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	ks, err := s.svc.ListKinds(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, 200, items(ks))
	return nil
}

// ── Projects ──────────────────────────────────────────────────────────────────

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	arch, err := queryBool(r, "include_archived")
	if err != nil {
		return err
	}
	ps, err := s.svc.ListProjects(r.Context(), c, service.ListProjectsInput{
		Space: r.URL.Query().Get("space"), RepoURL: r.URL.Query().Get("repo_url"), IncludeArchived: arch != nil && *arch,
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, items(ps))
	return nil
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in service.CreateProjectInput
	if err := decode(r, &in); err != nil {
		return err
	}
	p, err := s.svc.CreateProject(r.Context(), c, in)
	if err != nil {
		return err
	}
	writeJSON(w, 201, p)
	return nil
}

func (s *Server) resolveProject(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in struct {
		RepoURL string `json:"repo_url"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	p, err := s.svc.ResolveProject(r.Context(), c, in.RepoURL)
	if err != nil {
		return err
	}
	writeJSON(w, 200, p)
	return nil
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	p, err := s.svc.GetProject(r.Context(), c, r.PathValue("space"), r.PathValue("slug"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, p)
	return nil
}

func (s *Server) patchProject(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in service.PatchProjectInput
	if err := decode(r, &in); err != nil {
		return err
	}
	p, err := s.svc.PatchProject(r.Context(), c, r.PathValue("space"), r.PathValue("slug"), in)
	if err != nil {
		return err
	}
	writeJSON(w, 200, p)
	return nil
}

// ── Entries ───────────────────────────────────────────────────────────────────

func (s *Server) listEntries(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	q := r.URL.Query()
	limit, err := queryInt(r, "limit")
	if err != nil {
		return err
	}
	pinned, err := queryBool(r, "pinned")
	if err != nil {
		return err
	}
	in := service.ListEntriesInput{
		Space: q.Get("space"), Project: q.Get("project"), Kinds: queryList(r, "kind"),
		Statuses: queryList(r, "status"), Tags: queryList(r, "tag"), Pinned: pinned,
		Limit: limit, Cursor: q.Get("cursor"),
	}
	if v := q.Get("updated_since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return domain.BadRequest("updated_since must be RFC 3339")
		}
		in.UpdatedSince = &t
	}
	page, err := s.svc.ListEntries(r.Context(), c, in)
	if err != nil {
		return err
	}
	writeJSON(w, 200, page)
	return nil
}

func (s *Server) createEntry(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in service.CreateEntryInput
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.svc.CreateEntry(r.Context(), c, in)
	if err != nil {
		return err
	}
	writeEntry(w, 201, res)
	return nil
}

func (s *Server) getEntry(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	e, err := s.svc.ReadEntry(r.Context(), c, id)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, e.Version))
	writeJSON(w, 200, e)
	return nil
}

func (s *Server) patchEntry(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ver, err := ifMatch(r, true)
	if err != nil {
		return err
	}
	var in service.PatchEntryInput
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.svc.PatchEntry(r.Context(), c, id, ver, in)
	if err != nil {
		return err
	}
	writeEntry(w, 200, res)
	return nil
}

func (s *Server) transition(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ver, err := ifMatch(r, false)
	if err != nil {
		return err
	}
	var in service.TransitionInput
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.svc.Transition(r.Context(), c, id, ver, in)
	if err != nil {
		return err
	}
	writeEntry(w, 200, res)
	return nil
}

func (s *Server) promote(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in service.PromoteInput
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.svc.Promote(r.Context(), c, id, in)
	if err != nil {
		return err
	}
	writeEntry(w, 201, res)
	return nil
}

func (s *Server) versions(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	vs, err := s.svc.Versions(r.Context(), c, id)
	if err != nil {
		return err
	}
	writeJSON(w, 200, items(vs))
	return nil
}

func (s *Server) version(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		return domain.BadRequest("version must be an integer")
	}
	v, err := s.svc.Version(r.Context(), c, id, n)
	if err != nil {
		return err
	}
	writeJSON(w, 200, v)
	return nil
}

func (s *Server) revert(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	ver, err := ifMatch(r, true)
	if err != nil {
		return err
	}
	var in struct {
		ToVersion int `json:"to_version"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.svc.Revert(r.Context(), c, id, ver, in.ToVersion)
	if err != nil {
		return err
	}
	writeEntry(w, 200, res)
	return nil
}

func (s *Server) links(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	e, err := s.svc.GetEntry(r.Context(), c, id)
	if err != nil {
		return err
	}
	writeJSON(w, 200, e.Links)
	return nil
}

func (s *Server) addLink(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		ToID uuid.UUID      `json:"to_id"`
		Rel  domain.LinkRel `json:"rel"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if err := s.svc.AddLink(r.Context(), c, id, in.ToID, in.Rel); err != nil {
		return err
	}
	writeJSON(w, 201, map[string]any{"from_id": id, "to_id": in.ToID, "rel": in.Rel})
	return nil
}

func (s *Server) deleteLink(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	to, err := pathID(r, "to")
	if err != nil {
		return err
	}
	if err := s.svc.DeleteLink(r.Context(), c, id, to, domain.LinkRel(r.PathValue("rel"))); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) review(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in service.ReviewInput
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.svc.Review(r.Context(), c, id, in)
	if err != nil {
		return err
	}
	writeEntry(w, 200, res)
	return nil
}

func (s *Server) reviewQueue(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	limit, err := queryInt(r, "limit")
	if err != nil {
		return err
	}
	es, err := s.svc.ReviewQueue(r.Context(), c, limit)
	if err != nil {
		return err
	}
	writeJSON(w, 200, items(es))
	return nil
}

// ── Search and context ────────────────────────────────────────────────────────

func (s *Server) search(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in service.SearchInput
	if err := decode(r, &in); err != nil {
		return err
	}
	page, err := s.svc.Search(r.Context(), c, in)
	if err != nil {
		return err
	}
	writeJSON(w, 200, page)
	return nil
}

func (s *Server) context(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in service.ContextInput
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.svc.Context(r.Context(), c, in)
	if err != nil {
		return err
	}
	if r.URL.Query().Get("format") == "markdown" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, err := io.WriteString(w, res.Markdown)
		return err
	}
	writeJSON(w, 200, res)
	return nil
}

// ── Sessions ──────────────────────────────────────────────────────────────────

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in service.StartSessionInput
	if err := decode(r, &in); err != nil {
		return err
	}
	sess, err := s.svc.StartSession(r.Context(), c, in)
	if err != nil {
		return err
	}
	writeJSON(w, 201, sess)
	return nil
}

func (s *Server) endSession(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in service.EndSessionInput
	if err := decode(r, &in); err != nil {
		return err
	}
	res, err := s.svc.EndSession(r.Context(), c, id, in)
	if err != nil {
		return err
	}
	writeJSON(w, 200, res)
	return nil
}

// ── Export / import ───────────────────────────────────────────────────────────

func (s *Server) export(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	hist, err := queryBool(r, "include_history")
	if err != nil {
		return err
	}
	in := service.ExportInput{Spaces: queryList(r, "space"), IncludeHistory: hist != nil && *hist}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	switch f := r.URL.Query().Get("format"); f {
	case "", "jsonl":
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="knowledge-`+stamp+`.jsonl"`)
		return s.svc.ExportJSONL(r.Context(), c, in, w)
	case "markdown":
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="knowledge-`+stamp+`.tar.gz"`)
		return s.svc.ExportMarkdown(r.Context(), c, in, w)
	default:
		return domain.BadRequest("format must be jsonl or markdown, got %q", f)
	}
}

func (s *Server) importJSONL(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	dry, err := queryBool(r, "dry_run")
	if err != nil {
		return err
	}
	skip, err := queryBool(r, "skip_duplicate_content")
	if err != nil {
		return err
	}
	res, err := s.svc.Import(r.Context(), c, r.Body, service.ImportInput{
		Mode: r.URL.Query().Get("mode"), DryRun: dry != nil && *dry, SkipDuplicateContent: skip != nil && *skip,
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, res)
	return nil
}

// ── Admin ─────────────────────────────────────────────────────────────────────

func (s *Server) listPrincipals(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	ps, err := s.svc.ListPrincipals(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, 200, items(ps))
	return nil
}

func (s *Server) createPrincipal(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in service.CreatePrincipalInput
	if err := decode(r, &in); err != nil {
		return err
	}
	p, err := s.svc.CreatePrincipal(r.Context(), c, in)
	if err != nil {
		return err
	}
	writeJSON(w, 201, p)
	return nil
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	ts, err := s.svc.ListTokens(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, 200, items(ts))
	return nil
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in service.CreateTokenInput
	if err := decode(r, &in); err != nil {
		return err
	}
	t, err := s.svc.CreateToken(r.Context(), c, in)
	if err != nil {
		return err
	}
	writeJSON(w, 201, t)
	return nil
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	t, err := s.svc.RevokeToken(r.Context(), c, id)
	if err != nil {
		return err
	}
	writeJSON(w, 200, t)
	return nil
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	q := r.URL.Query()
	limit, err := queryInt(r, "limit")
	if err != nil {
		return err
	}
	before, err := queryInt(r, "before_id")
	if err != nil {
		return err
	}
	f := store.AuditFilter{Principal: q.Get("principal"), Action: q.Get("action"), Limit: limit, BeforeID: int64(before)}
	if v := q.Get("target_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return domain.BadRequest("target_id must be a UUID")
		}
		f.TargetID = &id
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return domain.BadRequest("since must be RFC 3339")
		}
		f.Since = &t
	}
	recs, err := s.svc.Audit(r.Context(), c, f)
	if err != nil {
		return err
	}
	writeJSON(w, 200, items(recs))
	return nil
}

func (s *Server) putKind(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in service.PutKindInput
	if err := decode(r, &in); err != nil {
		return err
	}
	k, err := s.svc.PutKind(r.Context(), c, r.PathValue("kind"), in)
	if err != nil {
		return err
	}
	writeJSON(w, 200, k)
	return nil
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	st, err := s.svc.Stats(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, 200, st)
	return nil
}

func (s *Server) disablePrincipal(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	p, err := s.svc.SetPrincipalDisabled(r.Context(), c, r.PathValue("name"), true)
	if err != nil {
		return err
	}
	writeJSON(w, 200, p)
	return nil
}

func (s *Server) enablePrincipal(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	p, err := s.svc.SetPrincipalDisabled(r.Context(), c, r.PathValue("name"), false)
	if err != nil {
		return err
	}
	writeJSON(w, 200, p)
	return nil
}

func (s *Server) graph(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	g, err := s.svc.Graph(r.Context(), c, queryList(r, "status"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, g)
	return nil
}

func (s *Server) healthReport(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	limit, err := queryInt(r, "limit")
	if err != nil {
		return err
	}
	rep, err := s.svc.Health(r.Context(), c, service.HealthInput{Project: r.URL.Query().Get("project"), Limit: limit, Lang: reportLang(r)})
	if err != nil {
		return err
	}
	writeJSON(w, 200, rep)
	return nil
}

func (s *Server) eval(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	var in service.EvalInput
	if err := decode(r, &in); err != nil {
		return err
	}
	rep, err := s.svc.Eval(r.Context(), c, in)
	if err != nil {
		return err
	}
	writeJSON(w, 200, rep)
	return nil
}

func (s *Server) linkSuggestions(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	res, err := s.svc.LinkSuggestions(r.Context(), c, id)
	if err != nil {
		return err
	}
	writeJSON(w, 200, items(res))
	return nil
}

func (s *Server) impact(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	res, err := s.svc.Impact(r.Context(), c, id)
	if err != nil {
		return err
	}
	writeJSON(w, 200, res)
	return nil
}

func (s *Server) healthHistory(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	days, err := queryInt(r, "days")
	if err != nil {
		return err
	}
	res, err := s.svc.HealthHistory(r.Context(), c, days)
	if err != nil {
		return err
	}
	writeJSON(w, 200, items(res))
	return nil
}

func (s *Server) projectSkill(w http.ResponseWriter, r *http.Request, c service.Caller) error {
	res, err := s.svc.Skill(r.Context(), c, service.SkillInput{
		Project: r.PathValue("space") + "/" + r.PathValue("slug"), Format: r.URL.Query().Get("format")})
	if err != nil {
		return err
	}
	writeJSON(w, 200, res)
	return nil
}
