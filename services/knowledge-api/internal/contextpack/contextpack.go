// Package contextpack packs knowledge into a token budget, deterministically and without an LLM.
//
// Token counts are estimated as characters / 3.5, which is model-independent and close enough
// for both Russian and English text to keep a context within budget.
package contextpack

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const charsPerToken = 3.5

// excerptChars is how much of the body is used when an entry has no summary.
const excerptChars = 300

// Entries that do not fit are not dropped silently: up to indexMax of them are listed as one-line references
// ("id — title") in a reserve of indexShare of the budget, so the agent can fetch what it needs with kn_get.
const (
	indexShare = 0.10
	indexMax   = 15
)

type Item struct {
	ID      string
	Kind    string
	Title   string
	Summary string
	Body    string
	Project string
	Status  string
	Stale   bool // past its re-check date: shown with a warning (ADR-12)
}

type Section struct {
	Name  string // machine name: rules, decisions, lessons, related
	Title string // heading in markdown
	// Full renders bodies (not just summaries) while the budget allows.
	Full bool
	// MaxShare caps the section's share of the budget (0 = no cap); the rest of its items go to the index.
	MaxShare float64
	Items    []Item
}

type OutItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Project   string `json:"project,omitempty"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type OutSection struct {
	Name  string    `json:"name"`
	Items []OutItem `json:"items"`
}

type Result struct {
	Sections      []OutSection `json:"sections"` // the last one, "more", is the index of entries that did not fit
	Markdown      string       `json:"markdown"`
	TokenEstimate int          `json:"token_estimate"`
	Truncated     bool         `json:"truncated"`
	IndexedCount  int          `json:"indexed_count"` // listed in the index only (title and id)
	OmittedCount  int          `json:"omitted_count"` // not shown at all
}

// Chars converts a token budget to characters with the same estimate.
func Chars(tokens int) int { return int(float64(tokens) * charsPerToken) }

func Estimate(s string) int {
	return int(float64(utf8.RuneCountInString(s))/charsPerToken + 0.999)
}

// Pack fills sections in order within the budget minus the index reserve. Each item first tries its full form
// (Full sections only), then a compact form, within the section's cap; items that do not fit go to the index.
func Pack(header string, sections []Section, maxTokens int) Result {
	budget := int(float64(maxTokens) * charsPerToken)
	main := budget - int(float64(budget)*indexShare)
	var md strings.Builder
	used := 0
	write := func(s string) {
		md.WriteString(s)
		used += utf8.RuneCountInString(s)
	}
	fitsIn := func(s string, limit int) bool { return used+utf8.RuneCountInString(s) <= limit }

	res := Result{Sections: []OutSection{}}
	var rest []Item
	if header != "" && fitsIn(header, main) {
		write(header)
	}
	for _, sec := range sections {
		out := OutSection{Name: sec.Name, Items: []OutItem{}}
		heading := fmt.Sprintf("\n## %s\n\n", sec.Title)
		if len(sec.Items) == 0 || !fitsIn(heading, main) {
			rest = append(rest, sec.Items...)
			res.Sections = append(res.Sections, out)
			continue
		}
		start := used
		limit := main
		if sec.MaxShare > 0 {
			limit = min(main, start+int(float64(budget)*sec.MaxShare))
		}
		write(heading)
		for _, it := range sec.Items {
			full := ""
			if sec.Full {
				full = renderFull(it)
			}
			compact, content, truncated := renderCompact(it)
			switch {
			case full != "" && fitsIn(full, limit):
				write(full)
				out.Items = append(out.Items, OutItem{ID: it.ID, Kind: it.Kind, Title: it.Title, Project: it.Project,
					Content: fullContent(it)})
			case fitsIn(compact, limit):
				write(compact)
				if sec.Full && strings.TrimSpace(it.Body) != "" {
					// The full body was wanted but did not fit: that is a budget cut, not a design choice.
					truncated = true
					res.Truncated = true
				}
				out.Items = append(out.Items, OutItem{ID: it.ID, Kind: it.Kind, Title: it.Title, Project: it.Project,
					Content: content, Truncated: truncated})
			default:
				rest = append(rest, it)
			}
		}
		res.Sections = append(res.Sections, out)
	}

	more := OutSection{Name: "more", Items: []OutItem{}}
	if len(rest) > 0 {
		res.Truncated = true
		heading := fmt.Sprintf("\n## Не поместились (%d) — открыть: `kn_get <id>`\n\n", len(rest))
		if fitsIn(heading, budget) {
			write(heading)
			for _, it := range rest {
				line := fmt.Sprintf("- `%s` %s (%s)\n", it.ID, it.Title, it.Kind)
				if len(more.Items) >= indexMax || !fitsIn(line, budget) {
					break
				}
				write(line)
				more.Items = append(more.Items, OutItem{ID: it.ID, Kind: it.Kind, Title: it.Title, Project: it.Project, Truncated: true})
			}
		}
		res.IndexedCount = len(more.Items)
		res.OmittedCount = len(rest) - len(more.Items)
		if res.OmittedCount > 0 {
			note := fmt.Sprintf("\n_И ещё %d — ищи через `kn_search`._\n", res.OmittedCount)
			if fitsIn(note, budget) {
				write(note)
			}
		}
	}
	res.Sections = append(res.Sections, more)
	res.Markdown = md.String()
	res.TokenEstimate = int(float64(used)/charsPerToken + 0.999)
	return res
}

func fullContent(it Item) string {
	if strings.TrimSpace(it.Body) != "" {
		return strings.TrimSpace(it.Body)
	}
	return it.Summary
}

func renderFull(it Item) string {
	return fmt.Sprintf("### %s\n`%s`%s\n\n%s\n\n", it.Title, it.ID, statusNote(it), fullContent(it))
}

func renderCompact(it Item) (line, content string, truncated bool) {
	content = strings.TrimSpace(it.Summary)
	if content == "" {
		content, truncated = excerpt(it.Body, excerptChars)
	} else {
		truncated = strings.TrimSpace(it.Body) != ""
	}
	content = strings.Join(strings.Fields(content), " ")
	line = fmt.Sprintf("- **%s** (%s, `%s`%s)", it.Title, it.Kind, it.ID, statusNote(it))
	if content != "" {
		line += " — " + content
	}
	return line + "\n", content, truncated
}

func statusNote(it Item) string {
	note := ""
	if it.Status != "" && it.Status != "active" {
		note = ", " + it.Status
	}
	if it.Stale {
		note += ", пора перепроверить"
	}
	return note
}

func excerpt(s string, n int) (string, bool) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s, false
	}
	r := []rune(s)
	cut := string(r[:n])
	if i := strings.LastIndexAny(cut, " \n"); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…", true
}
