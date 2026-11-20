// Package tools implements the kn-mcp tools as thin calls to the Knowledge REST API.
// No knowledge is cached here: every call goes to the API, which enforces all rules.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"kn.local/kclient"
)

// Config is how the MCP server was launched.
type Config struct {
	Agent        string // claude-code, cursor, codex, ...
	Project      string // explicit "<space>/<slug>", overrides detection
	DefaultSpace string // where to write when no project is known
	WorkDir      string // for git remote detection; empty disables it
	Version      string
}

// State is per-connection: the resolved project and the lazily started agent session.
type State struct {
	cfg    Config
	client *kclient.Client

	initOnce sync.Once

	// Workspace roots reported by the client. They are fetched when the client signals
	// "initialized" (servers may not send requests while handling a tool call), so init waits for them.
	rootsOnce  sync.Once
	rootsReady chan struct{}
	roots      []string

	mu        sync.Mutex
	project   string // resolved "<space>/<slug>" or ""
	repoURL   string
	workDir   string
	space     string // default write space
	initNote  string // explains why no project was found
	sessionID string
}

func NewState(cfg Config, client *kclient.Client) *State {
	return &State{cfg: cfg, client: client, rootsReady: make(chan struct{})}
}

// init resolves the project once. Order: explicit KN_PROJECT; the repository of the process working
// directory; the repository of a workspace root reported by the client (MCP roots). A repository is matched
// by its origin remote, then by its path (kclient.RepoIDs) — repositories without a remote are found too.
// Some clients (Cursor) start MCP servers from $HOME, so the roots step matters.
func (s *State) init(ctx context.Context, sess *mcp.ServerSession) {
	s.initOnce.Do(func() {
		var roots []string
		if rootsSupported(sess) {
			roots = s.waitRoots(ctx)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.project, s.space = s.cfg.Project, s.cfg.DefaultSpace
		var dirs []string
		if s.cfg.WorkDir != "" {
			dirs = append(dirs, s.cfg.WorkDir)
		}
		dirs = append(dirs, roots...)
		var lastErr error
		for _, dir := range dirs {
			p, ids, err := s.client.ResolveDir(ctx, dir)
			if len(ids) == 0 {
				continue // not a git repository
			}
			if s.repoURL == "" {
				s.repoURL, s.workDir = ids[0], dir
			}
			if err == nil {
				s.repoURL, s.workDir = ids[0], dir
				if s.project == "" {
					s.project = p.Ref
				}
				break
			}
			lastErr = err
		}
		if s.project == "" && s.repoURL != "" {
			if ae, ok := kclient.AsError(lastErr); ok && ae.Code == "not_found" {
				s.initNote = fmt.Sprintf("Репозиторий %s ещё не связан с проектом. Вызови kn_project с path=%q: с project=<space>/<slug> — "+
					"чтобы связать с существующим проектом, с create=true — чтобы зарегистрировать новый.", s.repoURL, s.workDir)
			}
		}
		if s.project == "" && s.repoURL == "" {
			s.initNote = "Проект не определён: MCP-сервер не видит рабочую папку."
		}
		if s.project != "" && s.space == "" {
			s.space, _, _ = strings.Cut(s.project, "/")
		}
		if s.space == "" {
			s.space = s.singleWritableSpace(ctx)
		}
	})
}

// FetchRoots asks the client for its workspace folders in the background. Called on the
// "initialized" and "roots changed" notifications.
func (s *State) FetchRoots(sess *mcp.ServerSession) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		dirs := clientRoots(ctx, sess)
		s.mu.Lock()
		if len(dirs) > 0 || s.roots == nil {
			s.roots = dirs
		}
		s.mu.Unlock()
		s.rootsOnce.Do(func() { close(s.rootsReady) })
	}()
}

// rootsSupported reports whether the client advertises roots on a protocol version where the
// server may still ask for them. Roots are deprecated from 2026-07-28 on (SEP-2577), and such
// clients do not send "initialized", so waiting for roots would only add latency.
func rootsSupported(sess *mcp.ServerSession) bool {
	if sess == nil {
		return false
	}
	ip := sess.InitializeParams()
	return ip != nil && ip.Capabilities != nil && ip.Capabilities.RootsV2 != nil && ip.ProtocolVersion < "2026-07-28"
}

// waitRoots returns the client's roots, waiting briefly if they have not arrived yet.
func (s *State) waitRoots(ctx context.Context) []string {
	select {
	case <-s.rootsReady:
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.roots...)
}

// clientRoots asks the client for its workspace folders, if it supports roots.
func clientRoots(ctx context.Context, sess *mcp.ServerSession) []string {
	ip := sess.InitializeParams()
	if ip == nil || ip.Capabilities == nil || ip.Capabilities.RootsV2 == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := sess.ListRoots(ctx, nil)
	if err != nil || res == nil {
		return nil
	}
	var dirs []string
	for _, r := range res.Roots {
		u, err := url.Parse(r.URI)
		if err == nil && u.Scheme == "file" && u.Path != "" {
			dirs = append(dirs, u.Path)
		}
	}
	return dirs
}

// singleWritableSpace returns the only space the token can write to, if there is exactly one.
func (s *State) singleWritableSpace(ctx context.Context) string {
	me, err := s.client.Me(ctx)
	if err != nil {
		return ""
	}
	var writable []string
	for _, sp := range me.Spaces {
		if sp.Level == "write" || sp.Level == "review" || sp.Level == "admin" {
			writable = append(writable, sp.Slug)
		}
	}
	if len(writable) == 1 {
		return writable[0]
	}
	return ""
}

func (s *State) current() (project, space, note string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.project, s.space, s.initNote
}

var slugInvalid = regexp.MustCompile(`[^a-z0-9._-]+`)

// selectProject switches the session to a project found by ref or by the repository of path.
// With ref and path, a repository not yet linked to that project is linked to it (its remote, or its path when
// it has no remote), so later sessions find the project by themselves. With create, an unregistered repository
// is registered in the default write space.
func (s *State) selectProject(ctx context.Context, ref, path string, create bool, name string) (kclient.Project, bool, error) {
	if ref != "" {
		space, slug, ok := strings.Cut(ref, "/")
		if !ok {
			return kclient.Project{}, false, fmt.Errorf("project должен быть в формате <space>/<slug>")
		}
		ps, err := s.client.ListProjects(ctx, space)
		if err != nil {
			return kclient.Project{}, false, explain(err)
		}
		for _, p := range ps {
			if p.Slug != slug {
				continue
			}
			if path != "" {
				if abs, err := filepath.Abs(path); err == nil {
					if found, ids, _ := s.client.ResolveDir(ctx, abs); len(ids) > 0 && found.Ref == "" {
						if linked, err := s.client.LinkRepo(ctx, p, ids[0]); err == nil {
							p = linked
						} else {
							return kclient.Project{}, false, explain(err)
						}
					}
				}
			}
			s.use(p, "", path)
			return p, false, nil
		}
		return kclient.Project{}, false, fmt.Errorf("проект %s не найден", ref)
	}
	if path == "" {
		return kclient.Project{}, false, fmt.Errorf("укажи path (абсолютный путь к репозиторию) или project")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return kclient.Project{}, false, err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return kclient.Project{}, false, fmt.Errorf("папка %s не найдена", abs)
	}
	p, ids, err := s.client.ResolveDir(ctx, abs)
	if len(ids) == 0 {
		return kclient.Project{}, false, fmt.Errorf("%s не git-репозиторий — укажи существующий project", abs)
	}
	remote := ids[0] // the origin remote, or the repository path when there is none
	if err == nil {
		s.use(p, remote, abs)
		return p, false, nil
	}
	if ae, ok := kclient.AsError(err); !ok || ae.Code != "not_found" {
		return kclient.Project{}, false, explain(err)
	}
	if !create {
		return kclient.Project{}, false, fmt.Errorf("репозиторий %s не зарегистрирован. Повтори с create=true, чтобы зарегистрировать проект", remote)
	}
	_, space, _ := s.current()
	if space == "" {
		return kclient.Project{}, false, fmt.Errorf("не понятно, в каком пространстве создать проект: у токена несколько пространств на запись. " +
			"Попроси пользователя создать проект (kn project create <space>/<slug> --repo-from-cwd) или задать KN_DEFAULT_SPACE")
	}
	slug := strings.Trim(slugInvalid.ReplaceAllString(strings.ToLower(filepath.Base(abs)), "-"), "-.")
	if slug == "" {
		slug = "project"
	}
	if name == "" {
		name = filepath.Base(abs)
	}
	p, err = s.client.CreateProject(ctx, space, slug, name, "", []string{remote})
	if err != nil {
		return kclient.Project{}, false, explain(err)
	}
	s.use(p, remote, abs)
	return p, true, nil
}

func (s *State) use(p kclient.Project, remote, dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.project, s.space, s.initNote = p.Ref, p.Space, ""
	if remote != "" {
		s.repoURL = remote
	}
	if dir != "" {
		s.workDir = dir
	}
	s.sessionID = "" // provenance: the next write opens a session bound to this project
}

// session returns the agent session id, starting one on first write.
func (s *State) session(ctx context.Context, sess *mcp.ServerSession) (string, error) {
	s.init(ctx, sess)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID != "" {
		return s.sessionID, nil
	}
	info, _ := json.Marshal(map[string]any{
		"mcp_server": "kn-mcp", "version": s.cfg.Version, "os": runtime.GOOS, "cwd": s.workDir, "pid": os.Getpid(),
	})
	space := ""
	if s.project == "" {
		space = s.space
	}
	sessn, err := s.client.StartSession(ctx, kclient.StartSession{
		Agent: s.cfg.Agent, Project: s.project, Space: space, RepoURL: s.repoURL, ClientInfo: info,
	})
	if err != nil {
		return "", err
	}
	s.sessionID = sessn.ID
	return s.sessionID, nil
}

func (s *State) endSession() {
	s.mu.Lock()
	s.sessionID = ""
	s.mu.Unlock()
}

// target picks where a new entry goes: explicit project/space, else the session's project/space.
func (s *State) target(project, space string) (string, string, error) {
	cur, curSpace, note := s.current()
	switch {
	case project != "":
		return project, "", nil
	case space != "":
		return "", space, nil
	case cur != "":
		return cur, "", nil
	case curSpace != "":
		return "", curSpace, nil
	}
	return "", "", fmt.Errorf("не понятно, куда сохранить запись: проект не определён. Укажи project (\"<space>/<slug>\") "+
		"или space, либо сначала вызови kn_project. %s", note)
}
