package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"kn.local/kclient"
)

// fakeAPI records requests and answers like the Knowledge API for the paths the tools use.
type fakeAPI struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string]map[string]any
	linked   string // repo id attached by PATCH /v1/projects/personal/demo; resolves afterwards
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	f.bodies[r.Method+" "+r.URL.Path] = body
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch r.Method + " " + r.URL.Path {
	case "POST /v1/projects/resolve":
		if strings.Contains(fmt.Sprint(body["repo_url"]), "known") || (f.linked != "" && body["repo_url"] == f.linked) {
			_, _ = io.WriteString(w, `{"ref":"personal/known","space":"personal","slug":"known","name":"Known"}`)
			return
		}
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"status":404,"code":"not_found","detail":"no project"}`)
	case "POST /v1/projects":
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"ref":"personal/`+fmt.Sprint(body["slug"])+`","space":"personal","slug":"`+fmt.Sprint(body["slug"])+`","name":"N"}`)
	case "POST /v1/sessions/sess-1/end":
		_, _ = io.WriteString(w, `{"session":{"id":"sess-1"},"session_log":{"id":"log-1","status":"active","attributes":{"status":"in_progress"}},"outcomes":[]}`)
	case "GET /v1/entries/e-5":
		_, _ = io.WriteString(w, `{"id":"e-5","title":"Факт","kind":"fact","status":"active","space":"work","version":1,"review_due":"2020-01-01","links":{"outgoing":[],"incoming":[]}}`)
	case "GET /v1/entries/e-5/impact":
		_, _ = io.WriteString(w, `{"count":2,"direct":1,"dependents":[{"id":"d-1","title":"Решение","kind":"decision","depth":1,"rel":"depends_on"},{"id":"d-2","title":"Урок","kind":"lesson","depth":2,"rel":"depends_on"}]}`)
	case "PATCH /v1/projects/personal/demo":
		urls := body["repo_urls"].([]any)
		f.linked = fmt.Sprint(urls[len(urls)-1])
		_, _ = io.WriteString(w, `{"ref":"personal/demo","space":"personal","slug":"demo","name":"demo","repo_urls":["`+f.linked+`"]}`)
	case "GET /v1/health":
		_, _ = io.WriteString(w, `{"score":63,"severity":"critical","summary":"842 живых записей","project":"`+r.URL.Query().Get("project")+`","checks":[
			{"id":"duplicate_content","title":"Дубли по содержимому","severity":"critical","count":1,"message":"1 копия","suggestion":"оставьте одну",
			 "groups":[[{"id":"keep-1","title":"docs/a.md"},{"id":"copy-1","title":"import:docs/a.md"}]]},
			{"id":"contradictions","title":"Противоречия","severity":"ok","count":0,"message":"нет"}]}`)
	case "GET /v1/projects":
		_, _ = io.WriteString(w, `{"items":[{"ref":"personal/known","space":"personal","slug":"known","name":"Known"},{"ref":"personal/demo","space":"personal","slug":"demo","name":"demo"}]}`)
	case "GET /v1/me":
		_, _ = io.WriteString(w, `{"principal":{"name":"a","kind":"agent"},"spaces":[{"slug":"personal","level":"write"},{"slug":"shared","level":"read"}]}`)
	case "POST /v1/sessions":
		_, _ = io.WriteString(w, `{"id":"sess-1","space":"work","agent":"test"}`)
	case "POST /v1/entries":
		if strings.Contains(body["title"].(string), "дубль") {
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"status":409,"code":"duplicate","detail":"exists","existing_id":"e-0"}`)
			return
		}
		w.WriteHeader(201)
		project := "null"
		if p, ok := body["project"].(string); ok {
			project = `"` + p + `"`
		}
		_, _ = io.WriteString(w, `{"entry":{"id":"e-1","status":"proposed","version":1,"space":"personal","project":`+project+`},
			"warnings":[{"code":"similar_entries","message":"m","items":[{"id":"e-9","title":"Похожая","similarity":0.7}]},
				{"code":"duplicate_content","message":"m","items":[{"id":"e-8","title":"docs/a.md","status":"active","similarity":1}]},
				{"code":"suggested_links","message":"m","items":[{"id":"e-7","title":"Решение про очередь","status":"active","similarity":0.4}]}]}`)
	case "POST /v1/context":
		_, _ = io.WriteString(w, `{"markdown":"# Контекст: X\n","sections":[{"name":"rules","items":[{"id":"r"}]}]}`)
	case "POST /v1/search":
		_, _ = io.WriteString(w, `{"items":[{"id":"e-2","space":"work","kind":"lesson","title":"Урок","status":"active","highlight":"**ретраи** вебхуков"}]}`)
	case "PATCH /v1/entries/e-1":
		w.WriteHeader(412)
		_, _ = io.WriteString(w, `{"status":412,"code":"version_conflict","detail":"x","current_version":4}`)
	default:
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"status":404,"code":"not_found","detail":"no route"}`)
	}
}

func connect(t *testing.T, cfg Config, roots ...string) (*mcp.ClientSession, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{bodies: map[string]map[string]any{}}
	ts := httptest.NewServer(api)
	t.Cleanup(ts.Close)

	state := NewState(cfg, kclient.New(ts.URL, "kn_test"))
	srv := mcp.NewServer(&mcp.Implementation{Name: "knowledge", Version: "test"}, Options(state))
	Register(srv, state)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	var sessOpts *mcp.ClientSessionOptions
	if len(roots) > 0 {
		// Roots are deprecated in the newest protocol (SEP-2577); editors that report them use older versions.
		sessOpts = &mcp.ClientSessionOptions{ProtocolVersion: "2025-06-18"}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	for _, r := range roots {
		client.AddRoots(&mcp.Root{URI: "file://" + r})
	}
	cs, err := client.Connect(ctx, ct, sessOpts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, api
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestToolsAreRegisteredWithSchemas(t *testing.T) {
	cs, _ := connect(t, Config{Agent: "test", Project: "work/billing"})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"kn_context": true, "kn_search": true, "kn_get": true, "kn_save": true,
		"kn_record_decision": true, "kn_update": true, "kn_link": true, "kn_log_session": true, "kn_project": true, "kn_health": true}
	for _, tool := range res.Tools {
		delete(want, tool.Name)
		if tool.InputSchema == nil {
			t.Errorf("%s has no input schema", tool.Name)
		}
	}
	if len(want) > 0 {
		t.Fatalf("missing tools: %v", want)
	}
	if cs.InitializeResult().Instructions == "" {
		t.Fatal("server instructions not sent")
	}
}

func TestSaveStartsSessionAndUsesProject(t *testing.T) {
	cs, api := connect(t, Config{Agent: "test", Project: "work/billing"})
	out, isErr := call(t, cs, "kn_save", map[string]any{"kind": "lesson", "title": "Урок про ретраи", "body": "текст"})
	if isErr || !strings.Contains(out, "e-1") || !strings.Contains(out, "проверки человеком") || !strings.Contains(out, "Похожая") {
		t.Fatalf("unexpected output (err=%v): %s", isErr, out)
	}
	body := api.bodies["POST /v1/entries"]
	if body["project"] != "work/billing" || body["session_id"] != "sess-1" {
		t.Fatalf("entry not attached to project/session: %v", body)
	}
	// Second save reuses the session.
	call(t, cs, "kn_save", map[string]any{"kind": "note", "title": "Ещё одна", "body": "x"})
	n := 0
	for _, r := range api.requests {
		if r == "POST /v1/sessions" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected one session, got %d: %v", n, api.requests)
	}
}

func TestErrorsAreActionable(t *testing.T) {
	cs, _ := connect(t, Config{Agent: "test", Project: "work/billing"})
	out, isErr := call(t, cs, "kn_save", map[string]any{"kind": "note", "title": "дубль", "body": "x"})
	if !isErr || !strings.Contains(out, "e-0") || !strings.Contains(out, "kn_update") {
		t.Fatalf("duplicate should point to existing entry: %s", out)
	}
	out, isErr = call(t, cs, "kn_update", map[string]any{"id": "e-1", "expected_version": 1, "change_note": "x", "body": "y"})
	if !isErr || !strings.Contains(out, "expected_version=4") {
		t.Fatalf("version conflict should tell the current version: %s", out)
	}
	out, isErr = call(t, cs, "kn_save", map[string]any{"kind": "decision", "title": "x", "body": "y"})
	if !isErr || !strings.Contains(out, "kn_record_decision") {
		t.Fatalf("decision via kn_save should redirect: %s", out)
	}
}

func TestDecisionAndSearchAndContext(t *testing.T) {
	cs, api := connect(t, Config{Agent: "test", Project: "work/billing"})
	_, isErr := call(t, cs, "kn_record_decision", map[string]any{
		"title": "Backoff", "context": "теряли события", "decision": "экспоненциальный", "alternatives": []string{"фиксированный"},
	})
	if isErr {
		t.Fatal("decision failed")
	}
	body := api.bodies["POST /v1/entries"]
	attrs := body["attributes"].(map[string]any)
	if body["kind"] != "decision" || attrs["decision"] != "экспоненциальный" || !strings.Contains(body["body"].(string), "## Альтернативы") {
		t.Fatalf("decision payload: %v", body)
	}
	out, _ := call(t, cs, "kn_search", map[string]any{"q": "ретраи"})
	if !strings.Contains(out, "e-2") || api.bodies["POST /v1/search"]["project"] != "work/billing" {
		t.Fatalf("search: %s / %v", out, api.bodies["POST /v1/search"])
	}
	out, _ = call(t, cs, "kn_context", map[string]any{"task": "ретраи"})
	if !strings.HasPrefix(out, "# Контекст: X") {
		t.Fatalf("context: %s", out)
	}
}

// gitRepo creates a repository with an origin remote, like a project opened in an editor.
func gitRepo(t *testing.T, remote string) string {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

// Cursor starts MCP servers from $HOME: the project must come from the client's workspace roots.
func TestProjectFromClientRoots(t *testing.T) {
	repo := gitRepo(t, "git@github.com:me/known.git")
	cs, api := connect(t, Config{Agent: "cursor", WorkDir: t.TempDir()}, repo)
	call(t, cs, "kn_save", map[string]any{"kind": "lesson", "title": "Урок", "body": "x"})
	if api.bodies["POST /v1/entries"]["project"] != "personal/known" {
		t.Fatalf("project not resolved from roots: %v", api.bodies["POST /v1/entries"])
	}
}

func TestKnProjectRegistersRepository(t *testing.T) {
	repo := gitRepo(t, "git@github.com:me/new-repo.git")
	cs, api := connect(t, Config{Agent: "cursor"})
	out, _ := call(t, cs, "kn_context", map[string]any{})
	if !strings.Contains(out, "kn_project") {
		t.Fatalf("context must tell the agent to call kn_project: %s", out)
	}
	out, isErr := call(t, cs, "kn_project", map[string]any{"path": repo})
	if !isErr || !strings.Contains(out, "create=true") {
		t.Fatalf("unknown repo without create should ask for create: %s", out)
	}
	out, isErr = call(t, cs, "kn_project", map[string]any{"path": repo, "create": true})
	slug := strings.ToLower(filepath.Base(repo))
	if isErr || !strings.Contains(out, "Зарегистрирован проект") {
		t.Fatalf("register: %s", out)
	}
	if api.bodies["POST /v1/projects"]["space"] != "personal" {
		t.Fatalf("project must go to the only writable space: %v", api.bodies["POST /v1/projects"])
	}
	call(t, cs, "kn_save", map[string]any{"kind": "note", "title": "После выбора проекта", "body": "x"})
	if got := api.bodies["POST /v1/entries"]["project"]; got != "personal/"+slugInvalid.ReplaceAllString(slug, "-") {
		t.Fatalf("entry must go to the selected project, got %v", got)
	}
}

func TestSaveWithoutProjectWarns(t *testing.T) {
	cs, _ := connect(t, Config{Agent: "cursor"}) // no project, only writable space → personal
	out, isErr := call(t, cs, "kn_save", map[string]any{"kind": "note", "title": "Без проекта", "body": "x"})
	if isErr || !strings.Contains(out, "без проекта") || !strings.Contains(out, "kn_project") {
		t.Fatalf("saving without a project must warn: %s", out)
	}
}

// ChatGPT has no working directory: kn_context must list projects and accept one directly.
func TestContextListsProjectsAndAcceptsProject(t *testing.T) {
	cs, api := connect(t, Config{Agent: "chatgpt"})
	out, isErr := call(t, cs, "kn_context", map[string]any{})
	if isErr || !strings.Contains(out, "`personal/demo` — demo") || !strings.Contains(out, `project="<space>/<slug>"`) {
		t.Fatalf("context without a project must list projects: %s", out)
	}
	out, isErr = call(t, cs, "kn_context", map[string]any{"project": "personal/known"})
	if isErr || strings.Contains(out, "Доступные проекты") {
		t.Fatalf("context with a project must not show the hint: %s", out)
	}
	if api.bodies["POST /v1/context"]["project"] != "personal/known" {
		t.Fatalf("context must be requested for the chosen project: %v", api.bodies["POST /v1/context"])
	}
	call(t, cs, "kn_save", map[string]any{"kind": "note", "title": "После выбора", "body": "x"})
	if api.bodies["POST /v1/entries"]["project"] != "personal/known" {
		t.Fatalf("the session must stay on the chosen project: %v", api.bodies["POST /v1/entries"])
	}
	_, isErr = call(t, cs, "kn_context", map[string]any{"project": "personal/nope"})
	if !isErr {
		t.Fatal("unknown project must be an error")
	}
}

func TestHealthToolUsesCurrentProject(t *testing.T) {
	cs, api := connect(t, Config{Agent: "test", Project: "work/billing"})
	out, isErr := call(t, cs, "kn_health", map[string]any{})
	if isErr || !strings.Contains(out, "63/100") || !strings.Contains(out, "оставить «docs/a.md» (`keep-1`)") ||
		!strings.Contains(out, "`copy-1`") || !strings.Contains(out, "В порядке: Противоречия") {
		t.Fatalf("health output: %s", out)
	}
	_ = api
	out, _ = call(t, cs, "kn_health", map[string]any{"project": "*"})
	if strings.Contains(out, "Область: work/billing") {
		t.Fatalf("project=* must report all spaces: %s", out)
	}
}

func TestSaveWarnsAboutSameText(t *testing.T) {
	cs, _ := connect(t, Config{Agent: "test", Project: "work/billing"})
	out, isErr := call(t, cs, "kn_save", map[string]any{"kind": "note", "title": "Копия", "body": "x"})
	if isErr || !strings.Contains(out, "Такой же текст уже есть") || !strings.Contains(out, "`e-8`") || !strings.Contains(out, "kn_update") {
		t.Fatalf("duplicate_content warning must tell the agent to update the existing entry: %s", out)
	}
}

// gitRepoNoRemote creates a git repository without any remote in a folder with the given name.
func gitRepoNoRemote(t *testing.T, name string) string {
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	real, _ := filepath.EvalSymlinks(dir)
	return real
}

func TestRepoWithoutRemoteResolvesByPath(t *testing.T) {
	repo := gitRepoNoRemote(t, "known-local")
	cs, api := connect(t, Config{Agent: "test", WorkDir: repo})
	out, _ := call(t, cs, "kn_context", map[string]any{})
	if api.bodies["POST /v1/projects/resolve"]["repo_url"] != "file://"+repo || api.bodies["POST /v1/context"]["project"] != "personal/known" {
		t.Fatalf("a repository without a remote must resolve by its path: %v / %s", api.bodies["POST /v1/projects/resolve"], out)
	}
}

func TestKnProjectLinksRepoWithoutRemote(t *testing.T) {
	repo := gitRepoNoRemote(t, "health")
	cs, api := connect(t, Config{Agent: "test", WorkDir: repo})
	out, _ := call(t, cs, "kn_context", map[string]any{})
	if !strings.Contains(out, "project=<space>/<slug>") {
		t.Fatalf("an unlinked repository must suggest linking it: %s", out)
	}
	if _, isErr := call(t, cs, "kn_project", map[string]any{"project": "personal/demo", "path": repo}); isErr {
		t.Fatal("kn_project with project and path must succeed")
	}
	urls := api.bodies["PATCH /v1/projects/personal/demo"]["repo_urls"]
	if !strings.Contains(fmt.Sprint(urls), "file://"+repo) {
		t.Fatalf("the repository path must be linked to the project: %v", urls)
	}
	// Linking again is a no-op: the repository already resolves to a project.
	api.mu.Lock()
	delete(api.bodies, "PATCH /v1/projects/personal/demo")
	api.mu.Unlock()
	call(t, cs, "kn_project", map[string]any{"project": "personal/demo", "path": repo})
	if api.bodies["PATCH /v1/projects/personal/demo"] != nil {
		t.Fatal("an already linked repository must not be linked again")
	}
}

func TestLogSessionSendsHandoff(t *testing.T) {
	cs, api := connect(t, Config{Agent: "test", Project: "work/billing"})
	call(t, cs, "kn_save", map[string]any{"kind": "note", "title": "Чтобы открылась сессия", "body": "x"})
	out, isErr := call(t, cs, "kn_log_session", map[string]any{
		"summary": "Очередь вебхуков", "goal": "надёжная доставка", "next_steps": []string{"dead letter"},
		"open_questions": []string{"лимит партнёра?"}, "commits": []string{"a1b2c3d"}, "continues": []string{"log-0"},
	})
	body := api.bodies["POST /v1/sessions/sess-1/end"]
	if isErr || body["goal"] != "надёжная доставка" || fmt.Sprint(body["next_steps"]) != "[dead letter]" ||
		fmt.Sprint(body["continues"]) != "[log-0]" || fmt.Sprint(body["commits"]) != "[a1b2c3d]" {
		t.Fatalf("structured fields must reach the API: %v / %s", body, out)
	}
	if !strings.Contains(out, "незавершённая") {
		t.Fatalf("an unfinished log must be announced: %s", out)
	}
}

func TestGetShowsDueAndImpact(t *testing.T) {
	cs, _ := connect(t, Config{Agent: "test"})
	out, isErr := call(t, cs, "kn_get", map[string]any{"id": "e-5"})
	if isErr || !strings.Contains(out, "Срок перепроверки прошёл") || !strings.Contains(out, "От этой записи зависят (2)") ||
		!strings.Contains(out, "`d-2`") {
		t.Fatalf("kn_get must show the overdue re-check and the impact: %s", out)
	}
	out, _ = call(t, cs, "kn_save", map[string]any{"kind": "lesson", "title": "Урок", "body": "x", "project": "work/billing"})
	if !strings.Contains(out, "свяжи через kn_link") || !strings.Contains(out, "`e-7`") {
		t.Fatalf("link suggestions must reach the agent: %s", out)
	}
}
