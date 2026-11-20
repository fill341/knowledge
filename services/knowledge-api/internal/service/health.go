package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"kn.local/api/internal/contextpack"
	"kn.local/api/internal/domain"
	"kn.local/api/internal/store"
)

// Knowledge health report (ADR-8): read-only detectors with severity, examples and a suggested fix.
// Fixes are applied by people through existing operations; nothing here changes data.

type Severity string

const (
	SevOK       Severity = "ok"
	SevInfo     Severity = "info"
	SevWarn     Severity = "warn"
	SevCritical Severity = "critical"
)

var sevRank = map[Severity]int{SevOK: 0, SevInfo: 1, SevWarn: 2, SevCritical: 3}

type HealthCheck struct {
	ID         string                `json:"id"`
	Title      string                `json:"title"`
	Severity   Severity              `json:"severity"`
	Count      int                   `json:"count"`
	Message    string                `json:"message"`
	Suggestion string                `json:"suggestion,omitempty"`
	Items      []store.HealthItem    `json:"items,omitempty"`
	Pairs      []store.HealthPair    `json:"pairs,omitempty"`
	Groups     [][]store.HealthItem  `json:"groups,omitempty"`
	Stats      []store.RetrievalStat `json:"stats,omitempty"`
	Sessions   []store.OpenSession   `json:"sessions,omitempty"`
	Tokens     []store.TokenRecord   `json:"tokens,omitempty"`
}

type HealthReport struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Project     *string       `json:"project"`
	Spaces      []string      `json:"spaces"`
	Score       int           `json:"score"`
	Severity    Severity      `json:"severity"`
	LiveEntries int           `json:"live_entries"`
	Summary     string        `json:"summary"`
	Checks      []HealthCheck `json:"checks"`
}

type HealthInput struct {
	Project string
	Limit   int  // examples per check
	Lang    Lang // language of the report texts; empty means Russian
}

// Lang is the language of user-visible health report texts.
type Lang string

const (
	LangRU Lang = "ru"
	LangEN Lang = "en"
)

// t picks the text for the language; anything but English falls back to Russian, the original language.
func (l Lang) t(ru, en string) string {
	if l == LangEN {
		return en
	}
	return ru
}

const (
	nearDupSimilarity = 0.8
	retrievalWindow   = 30 * 24 * time.Hour
	openSessionAge    = 24 * time.Hour
	staleTokenAge     = 30 * 24 * time.Hour
)

func (s *Service) Health(ctx context.Context, c Caller, in HealthInput) (HealthReport, error) {
	rep, err := s.healthReport(ctx, c, in)
	if err != nil {
		return rep, err
	}
	s.auditRead(ctx, c, "health", in.Project, len(rep.Checks), nil)
	return rep, nil
}

// healthReport builds the report without an audit record (also used by the daily snapshot).
func (s *Service) healthReport(ctx context.Context, c Caller, in HealthInput) (HealthReport, error) {
	q := s.store.Q()
	limit := in.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	l := in.Lang
	v := c.visibility()
	var projectID *uuid.UUID
	rep := HealthReport{GeneratedAt: time.Now().UTC(), Checks: []HealthCheck{}}
	if in.Project != "" {
		p, _, err := resolveProject(ctx, q, c, in.Project, domain.LevelRead)
		if err != nil {
			return HealthReport{}, err
		}
		projectID, rep.Project = &p.ID, &p.Ref
	}
	spaces, err := q.ListSpaces(ctx, v)
	if err != nil {
		return HealthReport{}, err
	}
	for _, sp := range spaces {
		rep.Spaces = append(rep.Spaces, sp.Slug)
	}
	if rep.LiveEntries, err = q.LiveCount(ctx, v, projectID, nil); err != nil {
		return HealthReport{}, err
	}
	live := rep.LiveEntries

	add := func(ch HealthCheck, err error) error {
		if err != nil {
			return fmt.Errorf("health check %s: %w", ch.ID, err)
		}
		rep.Checks = append(rep.Checks, ch)
		return nil
	}
	checks := []func() (HealthCheck, error){
		func() (HealthCheck, error) { return s.checkDuplicates(ctx, l, q, v, projectID, live, limit) },
		func() (HealthCheck, error) { return s.checkNearDuplicates(ctx, l, q, v, projectID, limit) },
		func() (HealthCheck, error) { return s.checkContradictions(ctx, l, q, v, projectID, limit) },
		func() (HealthCheck, error) { return s.checkDependsOnRetired(ctx, l, q, v, projectID, limit) },
		func() (HealthCheck, error) { return s.checkBrokenSupersede(ctx, l, q, v, projectID, limit) },
		func() (HealthCheck, error) { return s.checkOverdueReview(ctx, l, q, v, projectID, limit) },
		func() (HealthCheck, error) { return s.checkReviewBacklog(ctx, l, q, c) },
		func() (HealthCheck, error) { return s.checkOrphans(ctx, l, q, v, projectID, limit) },
		func() (HealthCheck, error) { return s.checkWeakEntries(ctx, l, q, v, projectID, limit) },
		func() (HealthCheck, error) { return s.checkUnused(ctx, l, q, v, projectID, limit) },
		func() (HealthCheck, error) { return s.checkRulesBudget(ctx, l, q, v, projectID) },
		func() (HealthCheck, error) { return s.checkRetrieval(ctx, l, q, c) },
		func() (HealthCheck, error) { return s.checkOpenSessions(ctx, l, q, v, projectID) },
		func() (HealthCheck, error) { return s.checkStaleHandoffs(ctx, l, q, v, projectID, limit) },
	}
	if c.Scope.Admin {
		checks = append(checks, func() (HealthCheck, error) { return s.checkAccess(ctx, l, q) })
	}
	for _, f := range checks {
		ch, err := f()
		if err := add(ch, err); err != nil {
			return HealthReport{}, err
		}
	}

	// Score: start at 100, subtract per finding by severity; overall severity is the worst one.
	rep.Score, rep.Severity = 100, SevOK
	counts := map[Severity]int{}
	for _, ch := range rep.Checks {
		counts[ch.Severity]++
		rep.Score -= map[Severity]int{SevCritical: 25, SevWarn: 10, SevInfo: 2}[ch.Severity]
		if sevRank[ch.Severity] > sevRank[rep.Severity] {
			rep.Severity = ch.Severity
		}
	}
	if rep.Score < 0 {
		rep.Score = 0
	}
	rep.Summary = fmt.Sprintf(l.t("%d живых записей; проблем: критичных %d, предупреждений %d, замечаний %d",
		"%d live entries; issues: %d critical, %d warnings, %d notices"),
		live, counts[SevCritical], counts[SevWarn], counts[SevInfo])
	return rep, nil
}

// SnapshotHealth stores today's health of the whole base (all spaces, as a global admin sees it) unless a
// snapshot for today exists (ADR-13). Called by the API's background loop.
func (s *Service) SnapshotHealth(ctx context.Context) (bool, error) {
	system := Caller{Scope: domain.Scope{Admin: true}} // no principal: nothing personal, the snapshot stores counts only
	rep, err := s.healthReport(ctx, system, HealthInput{Limit: 1})
	if err != nil {
		return false, err
	}
	snap := store.HealthSnapshot{Score: rep.Score, Severity: string(rep.Severity), LiveEntries: rep.LiveEntries,
		Checks: map[string]store.SnapshotCheck{}}
	for _, ch := range rep.Checks {
		snap.Checks[ch.ID] = store.SnapshotCheck{Severity: string(ch.Severity), Count: ch.Count}
	}
	return s.store.Q().SaveHealthSnapshot(ctx, snap)
}

// HealthHistory returns daily snapshots (global admin only: they aggregate every space).
func (s *Service) HealthHistory(ctx context.Context, c Caller, days int) ([]store.HealthSnapshot, error) {
	if err := requireAdmin(c); err != nil {
		return nil, err
	}
	if days <= 0 || days > 3650 {
		days = 90
	}
	out, err := s.store.Q().HealthSnapshots(ctx, days)
	if out == nil {
		out = []store.HealthSnapshot{}
	}
	return out, err
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) * 100 / float64(total)
}

func (s *Service) checkDuplicates(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID, live, limit int) (HealthCheck, error) {
	ch := HealthCheck{ID: "duplicate_content", Title: l.t("Дубли по содержимому", "Duplicate content")}
	groups, err := q.DuplicateContent(ctx, v, p)
	if err != nil {
		return ch, err
	}
	extra := 0 // entries beyond the keeper of each group
	for _, g := range groups {
		extra += len(g) - 1
	}
	ch.Count = extra
	share := pct(extra, live)
	switch {
	case extra == 0:
		ch.Severity, ch.Message = SevOK, l.t("Записей с одинаковым текстом нет.", "No entries with identical text.")
	case share > 10:
		ch.Severity = SevCritical
	default:
		ch.Severity = SevWarn
	}
	if extra > 0 {
		ch.Message = fmt.Sprintf(l.t("%d групп записей с одинаковым текстом: %d лишних копий (%.0f%% живых записей). "+
			"Копии засоряют поиск и съедают бюджет контекста.",
			"%d groups of entries with identical text: %d extra copies (%.0f%% of live entries). "+
				"Copies clutter search and eat the context budget."), len(groups), extra, share)
		ch.Suggestion = l.t("В каждой группе оставьте одну запись (первая — с большим числом связей, затем самая ранняя), "+
			"остальные переведите в superseded со ссылкой на неё: POST /v1/entries/{id}/transition {to: superseded, superseded_by}. "+
			"В веб-интерфейсе — кнопка «Оставить эту» на странице «Здоровье».",
			"Keep one entry per group (the first has the most links, then the oldest) and mark the others superseded by it: "+
				"POST /v1/entries/{id}/transition {to: superseded, superseded_by}. "+
				"In the web UI use the “Keep this one” button on the Health page.")
	}
	if len(groups) > limit {
		groups = groups[:limit]
	}
	ch.Groups = groups
	return ch, nil
}

func (s *Service) checkNearDuplicates(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID, limit int) (HealthCheck, error) {
	ch := HealthCheck{ID: "near_duplicate_titles", Title: l.t("Почти одинаковые заголовки", "Near-duplicate titles")}
	pairs, total, err := q.NearDuplicateTitles(ctx, v, p, nearDupSimilarity, limit)
	if err != nil {
		return ch, err
	}
	ch.Count, ch.Pairs = total, pairs
	switch {
	case total == 0:
		ch.Severity, ch.Message = SevOK, l.t("Несвязанных записей с почти одинаковыми заголовками нет.", "No unlinked entries with near-identical titles.")
	case total > 20:
		ch.Severity = SevWarn
	default:
		ch.Severity = SevInfo
	}
	if total > 0 {
		ch.Message = fmt.Sprintf(l.t("%d пар записей одного типа с похожими (≥ %.0f%%) заголовками, разным текстом и без связи между ними.",
			"%d pairs of same-kind entries with similar (≥ %.0f%%) titles, different text and no link between them."),
			total, nearDupSimilarity*100)
		ch.Suggestion = l.t("Если это одно знание — объедините текст в одну запись (kn_update) и замените вторую (superseded). "+
			"Если разные — свяжите relates_to, чтобы контекст показывал их вместе.",
			"If it is the same knowledge, merge the text into one entry (kn_update) and supersede the other. "+
				"If they differ, link them with relates_to so context shows them together.")
	}
	return ch, nil
}

func (s *Service) checkContradictions(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID, limit int) (HealthCheck, error) {
	ch := HealthCheck{ID: "contradictions", Title: l.t("Неразрешённые противоречия", "Unresolved contradictions")}
	pairs, total, err := q.Contradictions(ctx, v, p, limit)
	if err != nil {
		return ch, err
	}
	ch.Count, ch.Pairs = total, pairs
	if total == 0 {
		ch.Severity, ch.Message = SevOK, l.t("Активных противоречий (связей contradicts между живыми записями) нет.",
			"No active contradictions (contradicts links between live entries).")
		return ch, nil
	}
	ch.Severity = SevWarn
	ch.Message = fmt.Sprintf(l.t("%d пар живых записей помечены как противоречащие друг другу — агенты получают обе версии.",
		"%d pairs of live entries are marked as contradicting each other — agents get both versions."), total)
	ch.Suggestion = l.t("Решите, какая запись верна: вторую переведите в superseded (или deprecated), либо уточните обе и удалите связь contradicts.",
		"Decide which entry is right and mark the other superseded (or deprecated), or refine both and remove the contradicts link.")
	return ch, nil
}

func (s *Service) checkDependsOnRetired(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID, limit int) (HealthCheck, error) {
	ch := HealthCheck{ID: "depends_on_retired", Title: l.t("Опора на устаревшие знания", "Relying on retired knowledge")}
	pairs, total, err := q.DependsOnRetired(ctx, v, p, limit)
	if err != nil {
		return ch, err
	}
	ch.Count, ch.Pairs = total, pairs
	if total == 0 {
		ch.Severity, ch.Message = SevOK, l.t("Живые записи не зависят от устаревших, заменённых или архивных.",
			"No live entries depend on deprecated, superseded or archived ones.")
		return ch, nil
	}
	ch.Severity = SevWarn
	ch.Message = fmt.Sprintf(l.t("%d живых записей ссылаются (depends_on / references) на устаревшие, заменённые или архивные знания.",
		"%d live entries point (depends_on / references) to deprecated, superseded or archived knowledge."), total)
	ch.Suggestion = l.t("Перепроверьте зависящие записи: перенаправьте связь на актуальную замену или переведите запись в deprecated.",
		"Re-check the dependent entries: point the link to the current replacement or mark the entry deprecated.")
	return ch, nil
}

func (s *Service) checkBrokenSupersede(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID, limit int) (HealthCheck, error) {
	ch := HealthCheck{ID: "broken_supersede", Title: l.t("Замена на неактуальную запись", "Superseded by a retired entry")}
	items, total, err := q.HealthItems(ctx, v, p,
		store.X("e.status = 'superseded' AND EXISTS (SELECT 1 FROM entries r WHERE r.id = e.superseded_by AND r.status IN ('deprecated', 'superseded', 'archived'))"),
		store.X(`%s::text || (SELECT r.status FROM entries r WHERE r.id = e.superseded_by)`,
			l.t("заменена записью в статусе ", "superseded by an entry with status ")),
		"e.updated_at DESC", limit)
	if err != nil {
		return ch, err
	}
	ch.Count, ch.Items = total, items
	if total == 0 {
		ch.Severity, ch.Message = SevOK, l.t("Все заменённые записи указывают на живые замены.", "All superseded entries point to live replacements.")
		return ch, nil
	}
	ch.Severity = SevWarn
	ch.Message = fmt.Sprintf(l.t("%d записей заменены записями, которые сами устарели — цепочка замен ведёт в тупик.",
		"%d entries are superseded by entries that are retired themselves — the replacement chain is a dead end."), total)
	ch.Suggestion = l.t("Укажите актуальную замену (переход superseded с новым superseded_by) или верните запись в работу.",
		"Point to a current replacement (superseded transition with a new superseded_by) or bring the entry back into use.")
	return ch, nil
}

func (s *Service) checkOverdueReview(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID, limit int) (HealthCheck, error) {
	ch := HealthCheck{ID: "overdue_review", Title: l.t("Просроченная перепроверка", "Overdue re-check")}
	items, total, err := q.HealthItems(ctx, v, p, store.X(store.DueCond),
		store.X(`%s::text || to_char(`+store.DueExpr+`, 'YYYY-MM-DD')`, l.t("перепроверить до ", "re-check by ")), store.DueExpr, limit)
	if err != nil {
		return ch, err
	}
	ch.Count, ch.Items = total, items
	if total == 0 {
		ch.Severity, ch.Message = SevOK, l.t("Записей с истёкшим сроком перепроверки нет.", "No entries past their re-check date.")
		return ch, nil
	}
	ch.Severity = SevWarn
	ch.Message = fmt.Sprintf(l.t("%d активных записей прошли срок перепроверки (review_after или срок типа после последнего изменения). "+
		"В контексте агента они помечены «пора перепроверить».",
		"%d active entries are past their re-check date (review_after, or the kind's period since the last change). "+
			"Agent context marks them “due for re-check”."), total)
	ch.Suggestion = l.t("Подтвердите актуальность в «На проверке» (одобрение сбрасывает срок) или переведите в deprecated.",
		"Confirm they are still valid in Review (approval resets the date) or mark them deprecated.")
	return ch, nil
}

func (s *Service) checkReviewBacklog(ctx context.Context, l Lang, q *store.Queries, c Caller) (HealthCheck, error) {
	ch := HealthCheck{ID: "review_backlog", Title: l.t("Очередь проверки", "Review queue")}
	spaces, err := s.reviewSpaces(ctx, c)
	if err != nil {
		return ch, err
	}
	if len(spaces) == 0 {
		ch.Severity, ch.Message = SevOK, l.t("Нет прав на проверку записей — очередь не учитывается.", "No permission to review entries — the queue is not counted.")
		return ch, nil
	}
	n, oldest, err := q.ReviewBacklog(ctx, spaces)
	if err != nil {
		return ch, err
	}
	ch.Count = n
	switch {
	case n == 0:
		ch.Severity, ch.Message = SevOK, l.t("Очередь проверки пуста.", "The review queue is empty.")
	case oldest != nil && time.Since(*oldest) > 7*24*time.Hour:
		ch.Severity = SevWarn
		ch.Message = fmt.Sprintf(l.t("%d записей ждут проверки, самая старая — %d дн.: их знания не попадают в контекст агентов.",
			"%d entries await review, the oldest for %d days: their knowledge does not reach agent context."),
			n, int(time.Since(*oldest).Hours()/24))
	default:
		ch.Severity = SevInfo
		ch.Message = fmt.Sprintf(l.t("%d записей ждут проверки.", "%d entries await review."), n)
	}
	if n > 0 {
		ch.Suggestion = l.t("Разберите очередь на странице «На проверке» (клавиши a / r) или командой kn review.",
			"Work through the queue on the Review page (keys a / r) or with kn review.")
	}
	return ch, nil
}

// Orphans are only meaningful for structured knowledge; documents and session logs are naturally standalone.
var orphanKinds = []string{"decision", "rule", "lesson", "fact"}

func (s *Service) checkOrphans(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID, limit int) (HealthCheck, error) {
	ch := HealthCheck{ID: "orphans", Title: l.t("Знания без связей", "Unlinked knowledge")}
	items, total, err := q.HealthItems(ctx, v, p,
		store.X("e.status NOT IN ('archived', 'superseded') AND e.kind = ANY(%s::text[]) AND NOT EXISTS (SELECT 1 FROM entry_links l WHERE l.from_id = e.id OR l.to_id = e.id)", orphanKinds),
		store.X(`%s::text`, l.t("нет ни одной связи", "no links at all")),
		"array_position(ARRAY['decision','rule','lesson','fact'], e.kind), e.created_at", limit)
	if err != nil {
		return ch, err
	}
	base, err := q.LiveCount(ctx, v, p, orphanKinds)
	if err != nil {
		return ch, err
	}
	ch.Count, ch.Items = total, items
	if total == 0 {
		ch.Severity, ch.Message = SevOK, l.t("У всех решений, правил, уроков и фактов есть связи.", "Every decision, rule, lesson and fact has links.")
		return ch, nil
	}
	ch.Severity = SevInfo
	ch.Message = fmt.Sprintf(l.t("%d из %d решений, правил, уроков и фактов (%.0f%%) не связаны ни с чем: "+
		"зависимости, замены и противоречия между ними невидимы.",
		"%d of %d decisions, rules, lessons and facts (%.0f%%) are not linked to anything: "+
			"dependencies, replacements and contradictions between them are invisible."), total, base, pct(total, base))
	ch.Suggestion = l.t("Начните с решений: свяжите уроки и факты с решениями, к которым они относятся (relates_to / depends_on).",
		"Start with decisions: link lessons and facts to the decisions they belong to (relates_to / depends_on).")
	return ch, nil
}

func (s *Service) checkWeakEntries(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID, limit int) (HealthCheck, error) {
	ch := HealthCheck{ID: "weak_entries", Title: l.t("Неполные записи", "Incomplete entries")}
	cond := `e.status NOT IN ('archived', 'superseded') AND (
		(e.kind = 'decision' AND coalesce(jsonb_array_length(CASE WHEN jsonb_typeof(e.attributes->'alternatives') = 'array' THEN e.attributes->'alternatives' END), 0) = 0)
		OR (e.kind = 'lesson' AND coalesce(btrim(e.attributes->>'fix'), '') = '' AND char_length(btrim(e.body)) < 80)
		OR (e.summary IS NULL AND char_length(e.body) > 1500)
		OR (cb.kind = 'agent' AND e.session_id IS NULL AND coalesce(e.source_ref, '') = ''))`
	detail := store.X(`CASE
		WHEN e.kind = 'decision' AND coalesce(jsonb_array_length(CASE WHEN jsonb_typeof(e.attributes->'alternatives') = 'array' THEN e.attributes->'alternatives' END), 0) = 0 THEN %s::text
		WHEN e.kind = 'lesson' AND coalesce(btrim(e.attributes->>'fix'), '') = '' AND char_length(btrim(e.body)) < 80 THEN %s::text
		WHEN e.summary IS NULL AND char_length(e.body) > 1500 THEN %s::text
		ELSE %s::text END`,
		l.t("решение без альтернатив", "decision without alternatives"),
		l.t("урок без способа исправления", "lesson without a fix"),
		l.t("длинный текст без сути — в контекст попадёт только обрезок", "long text without a summary — context gets only a truncated part"),
		l.t("запись агента без провенанса", "agent entry without provenance"))
	items, total, err := q.HealthItems(ctx, v, p, store.X(cond), detail, "e.kind, e.created_at", limit)
	if err != nil {
		return ch, err
	}
	ch.Count, ch.Items = total, items
	if total == 0 {
		ch.Severity, ch.Message = SevOK, l.t("Неполных решений, уроков и длинных записей без сути нет.",
			"No incomplete decisions or lessons, and no long entries without a summary.")
		return ch, nil
	}
	ch.Severity = SevInfo
	ch.Message = fmt.Sprintf(l.t("%d записей неполны: решения без альтернатив, уроки без исправления, длинные тексты без сути, записи агентов без провенанса.",
		"%d entries are incomplete: decisions without alternatives, lessons without a fix, long texts without a summary, agent entries without provenance."), total)
	ch.Suggestion = l.t("Дополните записи через «Править» (kn_update): альтернативы и последствия у решений, fix у уроков, summary у длинных текстов.",
		"Complete them via Edit (kn_update): alternatives and consequences for decisions, a fix for lessons, a summary for long texts.")
	return ch, nil
}

func (s *Service) checkRulesBudget(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID) (HealthCheck, error) {
	ch := HealthCheck{ID: "rules_budget", Title: l.t("Правила не помещаются в контекст", "Rules do not fit the context")}
	sizes, err := q.RulesSizes(ctx, v, p)
	if err != nil {
		return ch, err
	}
	limit := int(float64(contextpack.Chars(DefaultContextTokens)) * rulesShare)
	var over []string
	for _, r := range sizes {
		if r.Chars > limit {
			over = append(over, fmt.Sprintf(l.t("%s (%d%% бюджета)", "%s (%d%% of budget)"), r.Project, r.Chars*100/contextpack.Chars(DefaultContextTokens)))
		}
	}
	ch.Count = len(over)
	if len(over) == 0 {
		ch.Severity, ch.Message = SevOK, fmt.Sprintf(l.t("Правила и закреплённые записи каждого проекта помещаются в %.0f%% бюджета контекста (%d токенов).",
			"Each project's rules and pinned entries fit in %.0f%% of the context budget (%d tokens)."),
			rulesShare*100, DefaultContextTokens)
		return ch, nil
	}
	ch.Severity = SevWarn
	ch.Message = fmt.Sprintf(l.t("Правила и закреплённые записи не помещаются целиком в %.0f%% бюджета контекста: %s. "+
		"Лишние уходят в краткий вид или оглавление — агент может не увидеть правило полностью.",
		"Rules and pinned entries do not fully fit in %.0f%% of the context budget: %s. "+
			"The overflow is shortened or listed as a table of contents — an agent may not see a rule in full."), rulesShare*100, strings.Join(over, ", "))
	ch.Suggestion = l.t("Сократите правила: суть — в summary, подробности — в отдельный документ; закрепляйте только то, что нужно в каждой сессии.",
		"Shorten the rules: the gist in summary, details in a separate document; pin only what every session needs.")
	return ch, nil
}

// Usage-based findings need some history first: an empty week says nothing about an entry.
const usageMinTracking = 14 * 24 * time.Hour

func (s *Service) checkUnused(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID, limit int) (HealthCheck, error) {
	ch := HealthCheck{ID: "unused", Title: l.t("Невостребованные знания", "Unused knowledge")}
	start, err := q.UsageTrackingStart(ctx)
	if err != nil {
		return ch, err
	}
	if start == nil || time.Since(*start) < usageMinTracking {
		since := l.t("ещё не начат", "has not started yet")
		if start != nil {
			since = l.t("идёт с ", "has been running since ") + start.Format("2006-01-02")
		}
		ch.Severity = SevOK
		ch.Message = l.t("Сбор статистики использования ", "Usage tracking ") + since +
			l.t("; оценка невостребованных знаний — после 14 дней сбора.", "; unused knowledge is assessed after 14 days of tracking.")
		return ch, nil
	}
	cond := fmt.Sprintf(`e.status = 'active' AND e.kind = ANY(%%s::text[]) AND e.created_at < now() - interval '%d days'
		AND NOT EXISTS (SELECT 1 FROM entry_usage u WHERE u.entry_id = e.id AND u.day > current_date - %d)`, usageWindow, usageWindow)
	items, total, err := q.HealthItems(ctx, v, p, store.X(cond, orphanKinds),
		store.X(`%s::text`, fmt.Sprintf(l.t("ни в поиске, ни в контексте, ни открыта за %d дней", "not found, used in context or opened in %d days"), usageWindow)),
		"array_position(ARRAY['decision','rule','lesson','fact'], e.kind), e.created_at", limit)
	if err != nil {
		return ch, err
	}
	base, err := q.LiveCount(ctx, v, p, orphanKinds)
	if err != nil {
		return ch, err
	}
	ch.Count, ch.Items = total, items
	if total == 0 {
		ch.Severity, ch.Message = SevOK, fmt.Sprintf(l.t("Все решения, правила, уроки и факты старше %d дней использовались за последние %d дней.",
			"All decisions, rules, lessons and facts older than %d days were used in the last %d days."), usageWindow, usageWindow)
		return ch, nil
	}
	ch.Severity = SevInfo
	ch.Message = fmt.Sprintf(l.t("%d из %d решений, правил, уроков и фактов (%.0f%%) за %d дней ни разу не попали в поиск или контекст и не открывались.",
		"%d of %d decisions, rules, lessons and facts (%.0f%%) have not appeared in search or context, or been opened, in %d days."),
		total, base, pct(total, base), usageWindow)
	ch.Suggestion = l.t("Проверьте каждую: устарела — переведите в deprecated; верна, но не находится — уточните заголовок, суть и теги "+
		"или свяжите с решением, рядом с которым её ищут.",
		"Check each one: outdated — mark it deprecated; valid but not found — refine the title, summary and tags "+
			"or link it to the decision people look for it next to.")
	return ch, nil
}

func (s *Service) checkRetrieval(ctx context.Context, l Lang, q *store.Queries, c Caller) (HealthCheck, error) {
	ch := HealthCheck{ID: "retrieval", Title: l.t("Поиск без результатов", "Searches without results")}
	var who *uuid.UUID
	if !c.Scope.Admin {
		who = &c.Principal.ID // the audit log is admin data: others see only their own calls
	}
	stats, err := q.RetrievalStats(ctx, time.Now().Add(-retrievalWindow), who)
	if err != nil {
		return ch, err
	}
	calls, empty := 0, 0
	for _, st := range stats {
		if st.Action == "search" {
			calls += st.Calls
			empty += st.Empty
		}
	}
	ch.Stats, ch.Count = stats, empty
	rate := pct(empty, calls)
	switch {
	case calls == 0:
		ch.Severity, ch.Message = SevOK, l.t("За 30 дней поисков не было.", "No searches in the last 30 days.")
		return ch, nil
	case rate > 20:
		ch.Severity = SevWarn
	case rate > 10:
		ch.Severity = SevInfo
	default:
		ch.Severity = SevOK
	}
	repeated, err := q.RepeatedEmptySearches(ctx, time.Now().Add(-retrievalWindow), who)
	if err != nil {
		return ch, err
	}
	ch.Message = fmt.Sprintf(l.t("За 30 дней %d поисков, без результатов %d (%.1f%%).", "%d searches in 30 days, %d without results (%.1f%%)."), calls, empty, rate)
	if repeated > 0 {
		ch.Message += fmt.Sprintf(l.t(" %d запросов повторялись без ответа (тот же запрос ≥ 2 раз) — знания по этим темам, похоже, нет.",
			" %d queries were repeated without an answer (same query ≥ 2 times) — there seems to be no knowledge on these topics."), repeated)
		if ch.Severity == SevOK {
			ch.Severity = SevInfo
		}
	}
	if ch.Severity != SevOK {
		ch.Suggestion = l.t("Высокая доля пустых поисков — сигнал добавить знания по этим темам или улучшить поиск "+
			"(следующий этап roadmap: тестовый набор запросов и kn eval, затем решение о гибридном поиске).",
			"A high share of empty searches is a signal to add knowledge on these topics or improve search "+
				"(next roadmap step: a test query set and kn eval, then a decision on hybrid search).")
	}
	return ch, nil
}

func (s *Service) checkOpenSessions(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID) (HealthCheck, error) {
	ch := HealthCheck{ID: "open_sessions", Title: l.t("Незакрытые сессии агентов", "Unclosed agent sessions")}
	sessions, err := q.OpenSessions(ctx, v, p, time.Now().Add(-openSessionAge))
	if err != nil {
		return ch, err
	}
	ch.Count, ch.Sessions = len(sessions), sessions
	if len(sessions) == 0 {
		ch.Severity, ch.Message = SevOK, l.t("Все сессии агентов старше суток закрыты итогом.", "All agent sessions older than a day are closed with a summary.")
		return ch, nil
	}
	ch.Severity = SevInfo
	ch.Message = fmt.Sprintf(l.t("%d сессий агентов старше суток без итога (kn_log_session): следующий агент не узнает, чем они закончились.",
		"%d agent sessions older than a day have no summary (kn_log_session): the next agent will not know how they ended."), len(sessions))
	ch.Suggestion = l.t("Попросите агента завершать значимую работу вызовом kn_log_session (это есть в его инструкции).",
		"Ask agents to finish significant work with kn_log_session (it is in their instructions).")
	return ch, nil
}

const staleHandoffAge = 14 * 24 * time.Hour

func (s *Service) checkStaleHandoffs(ctx context.Context, l Lang, q *store.Queries, v store.Visibility, p *uuid.UUID, limit int) (HealthCheck, error) {
	ch := HealthCheck{ID: "stale_handoffs", Title: l.t("Брошенная незавершённая работа", "Abandoned unfinished work")}
	items, total, err := q.StaleHandoffs(ctx, v, p, time.Now().Add(-staleHandoffAge), limit, l.t("начата ", "started "))
	if err != nil {
		return ch, err
	}
	ch.Count, ch.Items = total, items
	if total == 0 {
		ch.Severity, ch.Message = SevOK, l.t("Незавершённой работы старше 14 дней, которую никто не подхватил, нет.",
			"No unfinished work older than 14 days left without anyone picking it up.")
		return ch, nil
	}
	ch.Severity = SevInfo
	ch.Message = fmt.Sprintf(l.t("%d итогов сессий со статусом «в работе» или «заблокировано» старше 14 дней никто не продолжил. "+
		"Через 30 дней они перестанут попадать в контекст проекта.",
		"%d session summaries marked in progress or blocked for over 14 days were never continued. "+
			"After 30 days they drop out of the project context."), total)
	ch.Suggestion = l.t("Если работа неактуальна — переведите итог в archived; если нужна — продолжите её и укажите id в continues при kn_log_session.",
		"If the work is no longer relevant, archive the summary; if it is, continue it and pass its id in continues to kn_log_session.")
	return ch, nil
}

func (s *Service) checkAccess(ctx context.Context, l Lang, q *store.Queries) (HealthCheck, error) {
	ch := HealthCheck{ID: "access", Title: l.t("Риски доступа", "Access risks")}
	tokens, err := q.ListTokens(ctx)
	if err != nil {
		return ch, err
	}
	principals, err := q.ListPrincipals(ctx)
	if err != nil {
		return ch, err
	}
	kind := map[string]domain.PrincipalKind{}
	for _, p := range principals {
		kind[p.Name] = p.Kind
	}
	var risky, stale []store.TokenRecord
	for _, t := range tokens {
		if t.RevokedAt != nil {
			continue
		}
		if kind[t.Principal] == domain.PrincipalAgent {
			for _, sc := range t.Scopes {
				if sc == "space:shared:write" {
					risky = append(risky, t)
					break
				}
			}
		}
		unused := (t.LastUsedAt == nil && time.Since(t.CreatedAt) > 7*24*time.Hour) ||
			(t.LastUsedAt != nil && time.Since(*t.LastUsedAt) > staleTokenAge)
		if unused {
			stale = append(stale, t)
		}
	}
	ch.Tokens = append(risky, stale...)
	ch.Count = len(ch.Tokens)
	var msgs []string
	if len(risky) > 0 {
		ch.Severity = SevWarn
		msgs = append(msgs, fmt.Sprintf(l.t("%d токенов агентов могут писать в shared — через них знания одного профиля попадают в другой",
			"%d agent tokens can write to shared — through them knowledge leaks from one profile into another"), len(risky)))
	}
	if len(stale) > 0 {
		if ch.Severity == "" {
			ch.Severity = SevInfo
		}
		msgs = append(msgs, fmt.Sprintf(l.t("%d активных токенов давно не использовались", "%d active tokens have not been used for a long time"), len(stale)))
	}
	if ch.Count == 0 {
		ch.Severity, ch.Message = SevOK, l.t("Агентских токенов с записью в shared и давно неиспользуемых токенов нет.",
			"No agent tokens with write access to shared and no long-unused tokens.")
		return ch, nil
	}
	ch.Message = strings.Join(msgs, "; ") + "."
	ch.Suggestion = l.t("Отзовите лишние токены на странице «Доступ и токены»; агентам оставьте shared только на чтение.",
		"Revoke unneeded tokens on the Access & tokens page; give agents read-only access to shared.")
	return ch, nil
}
