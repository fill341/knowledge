package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"kn.local/api/internal/contextpack"
	"kn.local/api/internal/domain"
	"kn.local/api/internal/store"
)

// Knowledge Skills (ADR-14, roadmap P3 #9): a project's approved rules and decisions rendered as a file an agent
// loads natively — a Claude Code skill, a Cursor rule or a section of AGENTS.md. Generated on request and
// written by `kn skill export`; the output is deterministic (no timestamps), so `--check` can compare files.

type SkillInput struct {
	Project string
	Format  string // claude | cursor | agents
}

type Skill struct {
	Project string   `json:"project"`
	Format  string   `json:"format"`
	Path    string   `json:"path"`    // where to put it, relative to the repository root
	Content string   `json:"content"` // for agents: the section between the kn markers
	Entries []string `json:"entries"` // ids of the entries it was built from
	Indexed int      `json:"indexed"` // listed by title only (did not fit)
}

// AgentsMarkers delimit the generated section inside AGENTS.md; the rest of the file belongs to people.
const (
	AgentsBegin = "<!-- kn:begin — сгенерировано `kn skill export`, не править вручную -->"
	AgentsEnd   = "<!-- kn:end -->"
)

// skillTokens bounds a skill: big enough for all rules and the summaries of decisions, small enough to stay
// cheap when a client loads it into every session.
const skillTokens = 8000

func (s *Service) Skill(ctx context.Context, c Caller, in SkillInput) (Skill, error) {
	q := s.store.Q()
	format := in.Format
	if format == "" {
		format = "claude"
	}
	if format != "claude" && format != "cursor" && format != "agents" {
		return Skill{}, domain.Validation("format must be claude, cursor or agents")
	}
	p, _, err := resolveProject(ctx, q, c, in.Project, domain.LevelRead)
	if err != nil {
		return Skill{}, err
	}
	active := []string{string(domain.StatusActive)} // approved knowledge only: proposals stay out
	list := func(f store.EntryFilter) ([]store.EntryRow, error) {
		f.Statuses, f.Limit = active, 100
		rows, err := s.listAll(ctx, q, c, f)
		sortByCreated(rows)
		return rows, err
	}
	seen := map[string]bool{}
	var entries []string
	collect := func(rows []store.EntryRow) []contextpack.Item {
		var items []contextpack.Item
		for _, r := range rows {
			if id := r.ID.String(); !seen[id] {
				seen[id] = true
				entries = append(entries, id)
				items = append(items, toItem(r))
			}
		}
		return items
	}
	pinned, err := list(store.EntryFilter{ProjectID: &p.ID, Pinned: ptr(true)})
	if err != nil {
		return Skill{}, err
	}
	rules, err := list(store.EntryFilter{ProjectID: &p.ID, Kinds: []string{"rule"}})
	if err != nil {
		return Skill{}, err
	}
	decisions, err := list(store.EntryFilter{ProjectID: &p.ID, Kinds: []string{"decision"}})
	if err != nil {
		return Skill{}, err
	}
	ruleItems := append(collect(pinned), collect(rules)...)
	decisionItems := collect(decisions)
	if len(entries) == 0 {
		return Skill{}, domain.Unprocessable("nothing_to_export", "project %s has no approved rules or decisions yet", p.Ref)
	}

	header := fmt.Sprintf("# %s — правила и решения из базы знаний kn\n\n"+
		"Сгенерировано из одобренных записей проекта `%s`. Не править вручную: исправления — в kn (`kn_update`), затем "+
		"`kn skill export`. Полный контекст и уроки — `kn_context`; у каждой записи указан id для `kn_get`.\n", p.Name, p.Ref)
	if p.Description != "" {
		header += "\n" + p.Description + "\n"
	}
	pack := contextpack.Pack(header, []contextpack.Section{
		{Name: "rules", Title: "Правила", Full: true, MaxShare: 0.6, Items: ruleItems},
		{Name: "decisions", Title: "Решения", Items: decisionItems},
	}, skillTokens)

	res := Skill{Project: p.Ref, Format: format, Entries: entries, Indexed: pack.IndexedCount}
	slug := "kn-" + p.Slug
	desc := fmt.Sprintf("Правила и принятые решения проекта %s из базы знаний kn. Используй при любой работе в этом "+
		"репозитории: перед изменением архитектуры, выбором подхода или библиотеки, правкой процессов сборки и деплоя.", p.Name)
	// Quoted: the text contains ": " (and the project name may too), which a plain YAML scalar cannot hold.
	desc = strconv.Quote(desc)
	body := strings.TrimRight(pack.Markdown, "\n") + "\n"
	switch format {
	case "claude":
		res.Path = ".claude/skills/" + slug + "/SKILL.md"
		res.Content = "---\nname: " + slug + "\ndescription: " + desc + "\n---\n\n" + body
	case "cursor":
		res.Path = ".cursor/rules/" + slug + ".mdc"
		res.Content = "---\ndescription: " + desc + "\nalwaysApply: false\n---\n\n" + body
	case "agents":
		res.Path = "AGENTS.md"
		res.Content = AgentsBegin + "\n" + body + AgentsEnd + "\n"
	}
	s.auditRead(ctx, c, "skill", p.Ref+" "+format, len(entries), nil)
	return res, nil
}

// sortByCreated orders oldest first, so a new entry appends to the file instead of reshuffling it.
func sortByCreated(rows []store.EntryRow) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
}
