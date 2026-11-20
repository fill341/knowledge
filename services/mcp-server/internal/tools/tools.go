package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"kn.local/kclient"
)

// Instructions are sent to the client at initialization; many clients show them to the model.
const Instructions = `База знаний (kn) — общая память между сессиями и агентами.
- В начале задачи вызови kn_context с кратким описанием задачи (если контекст не пришёл автоматически).
- Если kn_context пишет, что проект не определён: при работе в локальном репозитории вызови kn_project с path =
  абсолютный путь к его корню (create=true, если проект ещё не зарегистрирован); без локальной папки (например, ChatGPT)
  выбери проект из списка в ответе kn_context и вызови kn_context с project="<space>/<slug>".
- Перед архитектурным решением или выбором подхода — kn_search: возможно, решение уже есть.
- Сохраняй только то, что пригодится в будущих сессиях: решения с альтернативами (kn_record_decision),
  грабли и как их обойти (kn_save kind=lesson), устойчивые правила проекта (kind=rule), проверенные факты (kind=fact).
- Не сохраняй секреты, токены, персональные данные, сырые логи и то, что очевидно из кода или git-истории.
- Если похожая запись уже есть — обнови её (kn_update), а не создавай новую.
- В конце значимой сессии — kn_log_session: summary, goal, done, commits; если работа не закончена — next_steps и
  open_questions (статус станет in_progress), и следующий агент проекта увидит её в kn_context.
- Если kn_context показывает «Незавершённую работу» и ты её продолжаешь — укажи её id в continues при kn_log_session.`

// Options returns server options wired to the state: instructions, and fetching the client's
// workspace roots as soon as the session is initialized.
func Options(st *State) *mcp.ServerOptions {
	return &mcp.ServerOptions{
		Instructions:            Instructions,
		InitializedHandler:      func(_ context.Context, req *mcp.InitializedRequest) { st.FetchRoots(req.Session) },
		RootsListChangedHandler: func(_ context.Context, req *mcp.RootsListChangedRequest) { st.FetchRoots(req.Session) },
	}
}

// Register adds all tools, resources and prompts to the server.
func Register(srv *mcp.Server, st *State) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(srv, &mcp.Tool{
		Name: "kn_context",
		Description: "Контекст текущего проекта из базы знаний: правила, решения, уроки и знания, связанные с задачей. Вызывай в начале задачи. " +
			"Параметр project (<space>/<slug>) переключает сессию на проект — если проект не выбран, ответ покажет список доступных.",
		Annotations: readOnly,
	}, st.context)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "kn_project",
		Description: "Выбрать проект для этой сессии по пути к репозиторию (path: по git remote, а без remote — по пути) или по ссылке project. " +
			"project и path вместе — связать репозиторий с существующим проектом, чтобы дальше он определялся сам. " +
			"С create=true незарегистрированный репозиторий регистрируется как проект. Вызывай, если kn_context пишет, что проект не определён.",
	}, st.selectProjectTool)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "kn_health",
		Description: "Отчёт о здоровье базы знаний (только чтение): дубли, почти-дубли, противоречия, опора на устаревшие знания, " +
			"просроченные проверки, записи без связей, неполные записи, пустые поиски. По умолчанию — текущий проект. " +
			"Используй, чтобы предложить человеку конкретные исправления; массовые изменения сам не применяй.",
		Annotations: readOnly,
	}, st.health)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "kn_search",
		Description: "Полнотекстовый поиск по базе знаний (русский и английский, с учётом словоформ). Возвращает id, заголовки и фрагменты.",
		Annotations: readOnly,
	}, st.search)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "kn_get",
		Description: "Полный текст записи по id, с версией (нужна для kn_update) и связями.",
		Annotations: readOnly,
	}, st.get)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "kn_save",
		Description: "Сохранить знание. kind: lesson (грабли и как обойти), rule (правило проекта), fact (проверенный факт), " +
			"note (заметка), snippet (код/команда), document (длинный документ). Для решений используй kn_record_decision. " +
			"По умолчанию сохраняется в текущий проект.",
	}, st.save)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "kn_record_decision",
		Description: "Записать архитектурное/техническое решение (ADR): контекст, решение, альтернативы, последствия. Решения агентов проходят проверку человеком.",
	}, st.recordDecision)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "kn_update",
		Description: "Обновить существующую запись вместо создания дубля. Нужна expected_version из kn_get. Передавай только изменяемые поля.",
	}, st.update)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "kn_link",
		Description: "Связать две записи. rel: relates_to, depends_on, supersedes, derived_from, contradicts, references.",
	}, st.link)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "kn_log_session",
		Description: "Итог сессии: резюме, цель, что сделано, коммиты, открытые вопросы и следующие шаги — следующий агент проекта " +
			"увидит незаконченную работу в kn_context. continues — id незавершённых итогов, которые ты подхватил. " +
			"Опционально новые знания (outcomes), каждое сохраняется отдельной записью.",
	}, st.logSession)

	srv.AddResource(&mcp.Resource{
		URI: "kn://context/current", Name: "current-context", MIMEType: "text/markdown",
		Description: "Контекст текущего проекта (как kn_context без задачи)",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		res, err := st.contextMarkdown(ctx, req.Session, "", 10000)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: res}}}, nil
	})
	srv.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "kn://entry/{id}", Name: "entry", MIMEType: "text/markdown", Description: "Запись базы знаний по id",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		id := strings.TrimPrefix(req.Params.URI, "kn://entry/")
		e, err := st.client.GetEntry(ctx, id)
		if err != nil {
			if ae, ok := kclient.AsError(err); ok && ae.Status == 404 {
				return nil, mcp.ResourceNotFoundError(req.Params.URI)
			}
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/markdown", Text: renderEntry(e)}}}, nil
	})

	srv.AddPrompt(&mcp.Prompt{
		Name:        "save-session",
		Description: "Подвести итоги сессии и сохранить решения и уроки в базу знаний",
	}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Description: "Итоги сессии в базу знаний",
			Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "Подведи итоги этой сессии для базы знаний. " +
				"1) Найди через kn_search, нет ли уже похожих записей. 2) Принятые решения с альтернативами запиши через kn_record_decision. " +
				"3) Грабли и неочевидные находки — kn_save kind=lesson. 4) Вызови kn_log_session: summary, goal, done, commits, " +
				"а если работа не закончена — next_steps и open_questions; продолженную незавершённую работу укажи в continues. " +
				"Не сохраняй очевидное из кода, секреты и сырые логи."}}},
		}, nil
	})
}

// ── Inputs ────────────────────────────────────────────────────────────────────

type contextIn struct {
	Project   string `json:"project,omitempty" jsonschema:"проект <space>/<slug>: переключает сессию на него (нужно клиентам без локальной папки, например ChatGPT)"`
	Task      string `json:"task,omitempty" jsonschema:"кратко, над чем идёт работа — для подбора связанных знаний"`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"бюджет контекста в токенах, по умолчанию 10000"`
}

type projectIn struct {
	Path    string `json:"path,omitempty" jsonschema:"абсолютный путь к корню репозитория (проект определяется по git remote origin, без remote — по пути)"`
	Project string `json:"project,omitempty" jsonschema:"или явная ссылка на проект <space>/<slug>"`
	Create  bool   `json:"create,omitempty" jsonschema:"зарегистрировать репозиторий как проект, если его ещё нет"`
	Name    string `json:"name,omitempty" jsonschema:"название нового проекта (по умолчанию — имя папки)"`
}

type healthIn struct {
	Project string `json:"project,omitempty" jsonschema:"проект <space>/<slug>; по умолчанию текущий, \"*\" — все доступные пространства"`
	Limit   int    `json:"limit,omitempty" jsonschema:"примеров на проверку, по умолчанию 5"`
}

type searchIn struct {
	Q       string   `json:"q" jsonschema:"поисковый запрос; поддерживает \"фразы\", -исключение, or"`
	Kinds   []string `json:"kinds,omitempty" jsonschema:"фильтр по типам: decision, lesson, rule, fact, note, snippet, document, session_log"`
	Project string   `json:"project,omitempty" jsonschema:"проект <space>/<slug>, записи которого поднимаются выше; по умолчанию текущий"`
	Spaces  []string `json:"spaces,omitempty" jsonschema:"ограничить пространствами"`
	Limit   int      `json:"limit,omitempty" jsonschema:"сколько результатов, по умолчанию 10"`
}

type getIn struct {
	ID string `json:"id" jsonschema:"id записи"`
}

type saveIn struct {
	Kind       string         `json:"kind" jsonschema:"тип: lesson, rule, fact, note, snippet, document"`
	Title      string         `json:"title" jsonschema:"короткий конкретный заголовок (3-300 символов)"`
	Body       string         `json:"body" jsonschema:"текст в markdown"`
	Summary    string         `json:"summary,omitempty" jsonschema:"одна-две фразы сути; попадает в контекст вместо полного текста"`
	Tags       []string       `json:"tags,omitempty" jsonschema:"теги: строчные латиница/цифры/дефис"`
	Attributes map[string]any `json:"attributes,omitempty" jsonschema:"поля типа: lesson {symptom, cause, fix}; snippet {language, command}; fact {source, verified_at}; rule {applies_to: [glob]}"`
	Project    string         `json:"project,omitempty" jsonschema:"проект <space>/<slug>; по умолчанию текущий"`
	Space      string         `json:"space,omitempty" jsonschema:"пространство, если запись не относится к проекту"`
	Confidence float32        `json:"confidence,omitempty" jsonschema:"уверенность 0..1"`
	SourceRef  string         `json:"source_ref,omitempty" jsonschema:"откуда знание: путь к файлу, URL, коммит"`
}

type decisionIn struct {
	Title        string   `json:"title" jsonschema:"решение одной фразой"`
	Context      string   `json:"context" jsonschema:"почему понадобилось решение: проблема, ограничения"`
	Decision     string   `json:"decision" jsonschema:"что решили"`
	Alternatives []string `json:"alternatives,omitempty" jsonschema:"рассмотренные альтернативы и почему отвергнуты"`
	Consequences string   `json:"consequences,omitempty" jsonschema:"последствия и компромиссы"`
	Tags         []string `json:"tags,omitempty"`
	Project      string   `json:"project,omitempty" jsonschema:"проект <space>/<slug>; по умолчанию текущий"`
	SourceRef    string   `json:"source_ref,omitempty" jsonschema:"путь, URL или коммит"`
}

type updateIn struct {
	ID              string         `json:"id"`
	ExpectedVersion int            `json:"expected_version" jsonschema:"текущая версия записи из kn_get"`
	ChangeNote      string         `json:"change_note" jsonschema:"что и почему изменено"`
	Title           *string        `json:"title,omitempty"`
	Summary         *string        `json:"summary,omitempty"`
	Body            *string        `json:"body,omitempty"`
	Tags            []string       `json:"tags,omitempty" jsonschema:"новый полный список тегов"`
	Attributes      map[string]any `json:"attributes,omitempty" jsonschema:"новые атрибуты целиком"`
}

type linkIn struct {
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
	Rel    string `json:"rel" jsonschema:"relates_to | depends_on | supersedes | derived_from | contradicts | references"`
}

type outcomeIn struct {
	Kind    string `json:"kind" jsonschema:"lesson, fact, rule, note"`
	Title   string `json:"title"`
	Body    string `json:"body,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type logSessionIn struct {
	Summary       string      `json:"summary" jsonschema:"что сделано в сессии, первая строка — суть"`
	Goal          string      `json:"goal,omitempty" jsonschema:"цель сессии одной фразой"`
	Status        string      `json:"status,omitempty" jsonschema:"done | in_progress | blocked; по умолчанию in_progress, если есть next_steps, иначе done"`
	Done          []string    `json:"done,omitempty" jsonschema:"что сделано, по пунктам"`
	Commits       []string    `json:"commits,omitempty" jsonschema:"хэши коммитов (и ветка/PR, если есть)"`
	OpenQuestions []string    `json:"open_questions,omitempty" jsonschema:"нерешённые вопросы"`
	NextSteps     []string    `json:"next_steps,omitempty" jsonschema:"что сделать дальше — для следующего агента"`
	Continues     []string    `json:"continues,omitempty" jsonschema:"id незавершённых итогов из kn_context, которые эта сессия продолжила"`
	Outcomes      []outcomeIn `json:"outcomes,omitempty" jsonschema:"новые знания, каждое станет отдельной записью"`
	FilesTouched  []string    `json:"files_touched,omitempty"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func (s *State) context(ctx context.Context, req *mcp.CallToolRequest, in contextIn) (*mcp.CallToolResult, any, error) {
	if in.Project != "" {
		s.init(ctx, req.Session)
		if _, _, err := s.selectProject(ctx, in.Project, "", false, ""); err != nil {
			return nil, nil, err
		}
	}
	md, err := s.contextMarkdown(ctx, req.Session, in.Task, in.MaxTokens)
	if err != nil {
		return nil, nil, explain(err)
	}
	return text(md), nil, nil
}

func (s *State) contextMarkdown(ctx context.Context, sess *mcp.ServerSession, task string, maxTokens int) (string, error) {
	s.init(ctx, sess)
	project, _, note := s.current()
	if maxTokens <= 0 {
		maxTokens = 10000
	}
	res, err := s.client.Context(ctx, kclient.ContextRequest{Project: project, Task: task, MaxTokens: maxTokens})
	if err != nil {
		return "", err
	}
	md := res.Markdown
	total := 0
	for _, sec := range res.Sections {
		total += len(sec.Items)
	}
	if total == 0 {
		md += "\nВ базе знаний пока нет записей для этого контекста.\n"
	}
	if note != "" {
		md += "\n⚠ " + note + "\n"
	}
	if project == "" {
		md += s.projectHint(ctx)
	}
	return md, nil
}

// projectHint lists the projects a client can switch to — the only way to pick one for clients
// that have no local working directory (ChatGPT through a tunnel).
func (s *State) projectHint(ctx context.Context) string {
	ps, err := s.client.ListProjects(ctx, "")
	if err != nil || len(ps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nПроект не выбран — показаны только общие знания. Доступные проекты:\n")
	for i, p := range ps {
		if i == 25 {
			fmt.Fprintf(&b, "- … и ещё %d\n", len(ps)-i)
			break
		}
		fmt.Fprintf(&b, "- `%s` — %s\n", p.Ref, p.Name)
	}
	b.WriteString("Выбери проект: kn_context с project=\"<space>/<slug>\" (или kn_project с project=…). " +
		"Если работаешь в локальном репозитории — kn_project с path и create=true.\n")
	return b.String()
}

func (s *State) selectProjectTool(ctx context.Context, req *mcp.CallToolRequest, in projectIn) (*mcp.CallToolResult, any, error) {
	s.init(ctx, req.Session)
	p, created, err := s.selectProject(ctx, in.Project, in.Path, in.Create, in.Name)
	if err != nil {
		return nil, nil, err
	}
	verb := "Текущий проект"
	if created {
		verb = "Зарегистрирован проект"
	}
	return text(fmt.Sprintf("%s: %s (%s). Дальше записи и поиск привязываются к нему; вызови kn_context, чтобы получить его контекст.",
		verb, p.Ref, p.Name)), nil, nil
}

var sevIcon = map[string]string{"ok": "✅", "info": "ℹ️", "warn": "⚠️", "critical": "🛑"}

func (s *State) health(ctx context.Context, req *mcp.CallToolRequest, in healthIn) (*mcp.CallToolResult, any, error) {
	s.init(ctx, req.Session)
	project := in.Project
	if project == "" {
		project, _, _ = s.current()
	} else if project == "*" {
		project = ""
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}
	rep, err := s.client.Health(ctx, project, limit)
	if err != nil {
		return nil, nil, explain(err)
	}
	var b strings.Builder
	scope := "все доступные пространства"
	if rep.Project != nil {
		scope = *rep.Project
	}
	fmt.Fprintf(&b, "# Здоровье базы: %d/100 (%s)\n%s. Область: %s.\n", rep.Score, rep.Severity, rep.Summary, scope)
	ok := []string{}
	for _, c := range rep.Checks {
		if c.Severity == "ok" {
			ok = append(ok, c.Title)
			continue
		}
		fmt.Fprintf(&b, "\n## %s %s\n%s\n", sevIcon[c.Severity], c.Title, c.Message)
		for _, g := range c.Groups {
			keep := g[0]
			fmt.Fprintf(&b, "- оставить «%s» (`%s`), копии:", keep.Title, keep.ID)
			for _, it := range g[1:] {
				fmt.Fprintf(&b, " «%s» (`%s`)", it.Title, it.ID)
			}
			b.WriteString("\n")
		}
		for _, p := range c.Pairs {
			fmt.Fprintf(&b, "- «%s» (`%s`) ↔ «%s» (`%s`)\n", p.A.Title, p.A.ID, p.B.Title, p.B.ID)
		}
		for _, it := range c.Items {
			fmt.Fprintf(&b, "- «%s» (%s, `%s`) — %s\n", it.Title, it.Kind, it.ID, it.Detail)
		}
		if c.Suggestion != "" {
			fmt.Fprintf(&b, "Что сделать: %s\n", c.Suggestion)
		}
	}
	if len(ok) > 0 {
		fmt.Fprintf(&b, "\nВ порядке: %s.\n", strings.Join(ok, ", "))
	}
	return text(b.String()), nil, nil
}

func (s *State) search(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
	s.init(ctx, req.Session)
	project := in.Project
	if project == "" {
		project, _, _ = s.current()
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	page, err := s.client.Search(ctx, kclient.SearchRequest{Q: in.Q, Kinds: in.Kinds, Project: project, Spaces: in.Spaces, Limit: limit})
	if err != nil {
		return nil, nil, explain(err)
	}
	if len(page.Items) == 0 {
		return text("Ничего не найдено по запросу «" + in.Q + "»."), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Найдено: %d\n\n", len(page.Items))
	for _, it := range page.Items {
		where := it.Space
		if it.Project != nil {
			where = *it.Project
		}
		status := ""
		if it.Status != "active" {
			status = ", " + it.Status
		}
		fmt.Fprintf(&b, "- **%s** (%s, %s%s, `%s`)\n", it.Title, it.Kind, where, status, it.ID)
		snippet := strings.Join(strings.Fields(it.Highlight), " ")
		if snippet == "" && it.Summary != nil {
			snippet = *it.Summary
		}
		if snippet != "" {
			fmt.Fprintf(&b, "  %s\n", snippet)
		}
	}
	b.WriteString("\nПолный текст: kn_get <id>.")
	return text(b.String()), nil, nil
}

func (s *State) get(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, any, error) {
	e, err := s.client.GetEntry(ctx, in.ID)
	if err != nil {
		return nil, nil, explain(err)
	}
	var b strings.Builder
	b.WriteString(renderEntry(e))
	if e.ReviewDue != nil && *e.ReviewDue <= time.Now().Format(time.DateOnly) && e.Status == "active" {
		fmt.Fprintf(&b, "\n⚠ Срок перепроверки прошёл (%s): сверь с текущим состоянием, прежде чем опираться. "+
			"Если устарело — скажи человеку; если верно — обнови запись (kn_update), это продлит срок.\n", *e.ReviewDue)
	}
	// Impact: who relies on this entry — before changing or retiring it.
	if im, err := s.client.Impact(ctx, e.ID); err == nil && im.Count > 0 {
		fmt.Fprintf(&b, "\n## От этой записи зависят (%d)\nМеняешь или выводишь её из оборота — проверь их:\n", im.Count)
		for _, d := range im.Dependents {
			fmt.Fprintf(&b, "- %s«%s» (%s, `%s`) — %s\n", strings.Repeat("  ", d.Depth-1), d.Title, d.Kind, d.ID, d.Rel)
		}
	}
	return text(b.String()), nil, nil
}

func (s *State) save(ctx context.Context, req *mcp.CallToolRequest, in saveIn) (*mcp.CallToolResult, any, error) {
	if in.Kind == "decision" {
		return nil, nil, fmt.Errorf("для решений используй kn_record_decision")
	}
	var attrs json.RawMessage
	if len(in.Attributes) > 0 {
		attrs, _ = json.Marshal(in.Attributes)
	}
	var conf *float32
	if in.Confidence > 0 {
		conf = &in.Confidence
	}
	return s.create(ctx, req.Session, kclient.CreateEntry{
		Kind: in.Kind, Title: in.Title, Body: in.Body, Summary: ptr(in.Summary), Tags: in.Tags,
		Attributes: attrs, Confidence: conf, SourceRef: ptr(in.SourceRef),
	}, in.Project, in.Space)
}

func (s *State) recordDecision(ctx context.Context, req *mcp.CallToolRequest, in decisionIn) (*mcp.CallToolResult, any, error) {
	attrs := map[string]any{"context": in.Context, "decision": in.Decision}
	if len(in.Alternatives) > 0 {
		attrs["alternatives"] = in.Alternatives
	}
	if in.Consequences != "" {
		attrs["consequences"] = in.Consequences
	}
	raw, _ := json.Marshal(attrs)
	var body strings.Builder
	fmt.Fprintf(&body, "## Контекст\n\n%s\n\n## Решение\n\n%s\n", in.Context, in.Decision)
	if len(in.Alternatives) > 0 {
		body.WriteString("\n## Альтернативы\n\n")
		for _, a := range in.Alternatives {
			fmt.Fprintf(&body, "- %s\n", a)
		}
	}
	if in.Consequences != "" {
		fmt.Fprintf(&body, "\n## Последствия\n\n%s\n", in.Consequences)
	}
	return s.create(ctx, req.Session, kclient.CreateEntry{
		Kind: "decision", Title: in.Title, Body: body.String(), Summary: ptr(in.Decision), Tags: in.Tags,
		Attributes: raw, SourceRef: ptr(in.SourceRef),
	}, in.Project, "")
}

func (s *State) create(ctx context.Context, sess *mcp.ServerSession, e kclient.CreateEntry, project, space string) (*mcp.CallToolResult, any, error) {
	sid, err := s.session(ctx, sess)
	if err != nil {
		return nil, nil, explain(err)
	}
	e.Project, e.Space, err = s.target(project, space)
	if err != nil {
		return nil, nil, err
	}
	e.SessionID = &sid
	res, err := s.client.CreateEntry(ctx, e, "")
	if err != nil {
		return nil, nil, explain(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Сохранено: `%s` (%s, версия %d).", res.Entry.ID, res.Entry.Status, res.Entry.Version)
	if res.Entry.Status == "proposed" {
		b.WriteString(" Запись ждёт проверки человеком и попадёт в контекст после одобрения.")
	}
	if res.Entry.Project == nil {
		fmt.Fprintf(&b, "\n\n⚠ Запись сохранена без проекта (пространство %s). Если она относится к текущему репозиторию — "+
			"вызови kn_project с path к репозиторию, затем kn_update с project, чтобы привязать её.", res.Entry.Space)
	}
	writeWarnings(&b, res.Warnings)
	return text(b.String()), nil, nil
}

func writeWarnings(b *strings.Builder, ws []kclient.Warning) {
	for _, w := range ws {
		switch w.Code {
		case "duplicate_content":
			b.WriteString("\n\n⚠ Такой же текст уже есть в этом пространстве. Не держи копии: обнови существующую запись " +
				"(kn_update) и предложи человеку заменить новую ею; если новая точнее — опиши разницу в change_note.\n")
			for _, it := range w.Items {
				fmt.Fprintf(b, "- %s (`%s`, %s)\n", it.Title, it.ID, it.Status)
			}
		case "suggested_links":
			b.WriteString("\n\nСвязанные знания в этом пространстве — если новая запись опирается на них, уточняет или " +
				"противоречит им, свяжи через kn_link (depends_on / relates_to / contradicts):\n")
			for _, it := range w.Items {
				fmt.Fprintf(b, "- %s (`%s`)\n", it.Title, it.ID)
			}
		case "similar_entries":
			b.WriteString("\n\nПохожие записи — проверь, не стоит ли объединить (kn_update) или связать (kn_link):\n")
			for _, it := range w.Items {
				fmt.Fprintf(b, "- %s (`%s`, сходство %.2f)\n", it.Title, it.ID, it.Similarity)
			}
		}
	}
}

func (s *State) update(ctx context.Context, _ *mcp.CallToolRequest, in updateIn) (*mcp.CallToolResult, any, error) {
	p := kclient.PatchEntry{Title: in.Title, Summary: in.Summary, Body: in.Body, ChangeNote: in.ChangeNote}
	if in.Tags != nil {
		p.Tags = &in.Tags
	}
	if in.Attributes != nil {
		p.Attributes, _ = json.Marshal(in.Attributes)
	}
	res, err := s.client.PatchEntry(ctx, in.ID, in.ExpectedVersion, p)
	if err != nil {
		return nil, nil, explain(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Обновлено: `%s`, версия %d (%s).", res.Entry.ID, res.Entry.Version, res.Entry.Status)
	writeWarnings(&b, res.Warnings)
	return text(b.String()), nil, nil
}

func (s *State) link(ctx context.Context, _ *mcp.CallToolRequest, in linkIn) (*mcp.CallToolResult, any, error) {
	if err := s.client.AddLink(ctx, in.FromID, in.ToID, in.Rel); err != nil {
		return nil, nil, explain(err)
	}
	return text("Связь создана: " + in.FromID + " —" + in.Rel + "→ " + in.ToID), nil, nil
}

func (s *State) logSession(ctx context.Context, req *mcp.CallToolRequest, in logSessionIn) (*mcp.CallToolResult, any, error) {
	sid, err := s.session(ctx, req.Session)
	if err != nil {
		return nil, nil, explain(err)
	}
	end := kclient.EndSession{Summary: in.Summary, FilesTouched: in.FilesTouched, Goal: in.Goal, Status: in.Status,
		Done: in.Done, Commits: in.Commits, OpenQuestions: in.OpenQuestions, NextSteps: in.NextSteps, Continues: in.Continues}
	for _, o := range in.Outcomes {
		end.Outcomes = append(end.Outcomes, kclient.Outcome{Kind: o.Kind, Title: o.Title, Body: o.Body, Summary: ptr(o.Summary)})
	}
	res, err := s.client.EndSession(ctx, sid, end)
	if err != nil {
		return nil, nil, explain(err)
	}
	s.endSession() // the next write starts a new session
	var b strings.Builder
	if res.SessionLog != nil {
		fmt.Fprintf(&b, "Итог сессии сохранён: `%s`.\n", res.SessionLog.ID)
		var attrs struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(res.SessionLog.Attributes, &attrs)
		if attrs.Status == "in_progress" || attrs.Status == "blocked" {
			b.WriteString("Работа отмечена как незавершённая: следующий агент проекта увидит её в kn_context.\n")
		}
	}
	for _, o := range res.Outcomes {
		if o.Error != nil {
			fmt.Fprintf(&b, "- «%s»: не сохранено — %s\n", o.Title, explain(o.Error))
		} else {
			fmt.Fprintf(&b, "- «%s»: `%s` (%s)\n", o.Title, *o.ID, o.Status)
		}
	}
	return text(b.String()), nil, nil
}

// explain turns API errors into messages that tell the model what to do next.
func explain(err error) error {
	ae, ok := kclient.AsError(err)
	if !ok {
		return err
	}
	switch ae.Code {
	case "duplicate":
		return fmt.Errorf("такая запись уже есть: `%s`. Прочитай её (kn_get) и при необходимости дополни через kn_update", ae.ExistingID)
	case "version_conflict":
		return fmt.Errorf("запись изменилась, текущая версия %d. Перечитай её через kn_get и повтори kn_update с expected_version=%d",
			ae.CurrentVersion, ae.CurrentVersion)
	case "secret_detected":
		return fmt.Errorf("текст похож на секрет (%s). Удали секрет или замени его плейсхолдером и повтори", ae.Detail)
	case "forbidden_space":
		return fmt.Errorf("нет доступа: %s. Этот агент работает только со своими пространствами", ae.Detail)
	case "not_found":
		return fmt.Errorf("не найдено: %s", ae.Detail)
	case "rate_limited":
		return fmt.Errorf("слишком много записей подряд: %s. Сохраняй только важное", ae.Detail)
	case "unauthenticated":
		return fmt.Errorf("токен базы знаний недействителен — попроси пользователя выпустить новый (make token)")
	}
	return fmt.Errorf("%s: %s", ae.Code, ae.Detail)
}

func renderEntry(e kclient.EntryDetail) string {
	var b strings.Builder
	where := e.Space
	if e.Project != nil {
		where = *e.Project
	}
	fmt.Fprintf(&b, "# %s\n\n", e.Title)
	fmt.Fprintf(&b, "id: `%s` · %s · %s · версия %d · статус %s", e.ID, e.Kind, where, e.Version, e.Status)
	if len(e.Tags) > 0 {
		fmt.Fprintf(&b, " · теги: %s", strings.Join(e.Tags, ", "))
	}
	fmt.Fprintf(&b, "\nавтор: %s · обновлено %s\n", e.CreatedBy, e.UpdatedAt.Format("2006-01-02"))
	if e.SupersededBy != nil {
		fmt.Fprintf(&b, "\n⚠ Запись заменена: `%s`\n", *e.SupersededBy)
	}
	if e.Summary != nil {
		fmt.Fprintf(&b, "\n> %s\n", *e.Summary)
	}
	if len(e.Attributes) > 0 && string(e.Attributes) != "{}" {
		fmt.Fprintf(&b, "\nattributes: %s\n", string(e.Attributes))
	}
	if strings.TrimSpace(e.Body) != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(e.Body))
	}
	if len(e.Links.Outgoing)+len(e.Links.Incoming) > 0 {
		b.WriteString("\n## Связи\n")
		for _, l := range e.Links.Outgoing {
			fmt.Fprintf(&b, "- → %s: %s (`%s`)\n", l.Rel, l.Title, l.ToID)
		}
		for _, l := range e.Links.Incoming {
			fmt.Fprintf(&b, "- ← %s: %s (`%s`)\n", l.Rel, l.Title, l.FromID)
		}
	}
	return b.String()
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
