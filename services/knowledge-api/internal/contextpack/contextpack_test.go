package contextpack

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPackRespectsBudgetAndOrder(t *testing.T) {
	long := strings.Repeat("Длинное описание решения. ", 200)
	sections := []Section{
		{Name: "rules", Title: "Правила", Full: true, Items: []Item{
			{ID: "r1", Kind: "rule", Title: "Всегда pnpm", Body: "Не используем npm и yarn."},
		}},
		{Name: "decisions", Title: "Решения", Items: []Item{
			{ID: "d1", Kind: "decision", Title: "Postgres", Summary: "Одна БД на всё."},
			{ID: "d2", Kind: "decision", Title: "Очереди", Body: long},
		}},
	}
	res := Pack("# Контекст\n", sections, 2000)
	if res.Truncated || res.OmittedCount != 0 {
		t.Fatalf("should fit: %+v", res)
	}
	if !strings.Contains(res.Markdown, "Не используем npm") {
		t.Fatal("rules should include full body")
	}
	if strings.Contains(res.Markdown, long) {
		t.Fatal("non-full sections must use excerpts")
	}
	if !res.Sections[1].Items[1].Truncated {
		t.Fatal("excerpted item should be marked truncated")
	}
	if strings.Index(res.Markdown, "Правила") > strings.Index(res.Markdown, "Решения") {
		t.Fatal("sections out of order")
	}

	small := Pack("# Контекст\n", sections, 30)
	if !small.Truncated || small.TokenEstimate > 30 {
		t.Fatalf("budget not respected: %+v", small)
	}
}

func TestPackFallsBackToCompactForRules(t *testing.T) {
	sections := []Section{{Name: "rules", Title: "Правила", Full: true, Items: []Item{
		{ID: "r1", Kind: "rule", Title: "Большое правило", Summary: "Коротко.", Body: strings.Repeat("слово ", 2000)},
	}}}
	res := Pack("", sections, 200)
	if len(res.Sections[0].Items) != 1 || res.Sections[0].Items[0].Content != "Коротко." || !res.Truncated {
		t.Fatalf("expected compact fallback: %+v", res.Sections)
	}
}

func TestPackCapsSectionAndIndexesTheRest(t *testing.T) {
	var rules, items []Item
	for i := range 6 {
		rules = append(rules, Item{ID: fmt.Sprintf("r%d", i), Kind: "rule", Title: fmt.Sprintf("Правило %d", i),
			Summary: "Суть правила.", Body: strings.Repeat("подробности правила ", 40)})
	}
	for i := range 40 {
		items = append(items, Item{ID: fmt.Sprintf("d%02d", i), Kind: "decision", Title: fmt.Sprintf("Решение %d", i),
			Summary: strings.Repeat("суть решения ", 12)})
	}
	res := Pack("", []Section{
		{Name: "rules", Title: "Правила", Full: true, MaxShare: 0.35, Items: rules},
		{Name: "task", Title: "По задаче", Items: items},
	}, 1500)

	budget := int(1500 * charsPerToken)
	rulesEnd := strings.Index(res.Markdown, "## По задаче")
	if rulesEnd < 0 || utf8.RuneCountInString(res.Markdown[:rulesEnd]) > int(float64(budget)*0.35)+40 {
		t.Fatalf("rules must stay within 35%% of the budget (%d chars): %d", budget, utf8.RuneCountInString(res.Markdown[:rulesEnd]))
	}
	if len(res.Sections[0].Items) != 6 {
		t.Fatalf("all rules must be present, compact when over the cap: %d", len(res.Sections[0].Items))
	}
	more := res.Sections[len(res.Sections)-1]
	if more.Name != "more" || len(more.Items) != indexMax || res.IndexedCount != indexMax {
		t.Fatalf("overflow must be indexed up to %d lines: %+v", indexMax, more)
	}
	shown := len(res.Sections[1].Items)
	if shown+res.IndexedCount+res.OmittedCount != 40 || res.OmittedCount == 0 || !res.Truncated {
		t.Fatalf("every item is shown, indexed or counted: shown %d indexed %d omitted %d", shown, res.IndexedCount, res.OmittedCount)
	}
	if !strings.Contains(res.Markdown, "`"+more.Items[0].ID+"`") || !strings.Contains(res.Markdown, "kn_search") {
		t.Fatalf("index lines and the search hint must be in markdown:\n%s", res.Markdown)
	}
	if res.TokenEstimate > 1500 {
		t.Fatalf("budget exceeded: %d", res.TokenEstimate)
	}
}
