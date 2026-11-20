// Package export renders entries as Markdown files with YAML frontmatter (Obsidian-compatible).
package export

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"kn.local/api/internal/domain"
)

// FileName is deterministic from id and title, so links can point to files of entries
// that are exported separately (Obsidian resolves [[name]] across folders).
func FileName(id, title string) string {
	return Slug(title) + "-" + id[:8]
}

// Path places an entry under <space>/<project-slug | _no-project>/<kind>/.
func Path(e domain.Entry) string {
	project := "_no-project"
	if e.Project != nil {
		_, slug, _ := strings.Cut(*e.Project, "/")
		project = slug
	}
	return fmt.Sprintf("%s/%s/%s/%s.md", e.Space, project, e.Kind, FileName(e.ID.String(), e.Title))
}

// Slug keeps letters (any script) and digits, turns the rest into single dashes.
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if utf8.RuneCountInString(out) > 80 {
		out = strings.TrimRight(string([]rune(out)[:80]), "-")
	}
	if out == "" {
		out = "entry"
	}
	return out
}

// Markdown renders one entry. Outgoing links become wikilinks to the target files.
func Markdown(e domain.Entry, links []domain.Link) string {
	var b strings.Builder
	q := func(v any) string { j, _ := json.Marshal(v); return string(j) } // JSON strings are valid YAML
	b.WriteString("---\n")
	fmt.Fprintf(&b, "kn_id: %s\n", e.ID)
	fmt.Fprintf(&b, "kn_version: %d\n", e.Version)
	fmt.Fprintf(&b, "kind: %s\n", e.Kind)
	fmt.Fprintf(&b, "status: %s\n", e.Status)
	fmt.Fprintf(&b, "space: %s\n", e.Space)
	if e.Project != nil {
		fmt.Fprintf(&b, "project: %s\n", q(*e.Project))
	}
	fmt.Fprintf(&b, "tags: %s\n", q(e.Tags))
	if e.Pinned {
		b.WriteString("pinned: true\n")
	}
	if e.Confidence != nil {
		fmt.Fprintf(&b, "confidence: %g\n", *e.Confidence)
	}
	if e.SourceRef != nil {
		fmt.Fprintf(&b, "source_ref: %s\n", q(*e.SourceRef))
	}
	if len(e.Attributes) > 0 && string(e.Attributes) != "{}" {
		fmt.Fprintf(&b, "attributes: %s\n", string(e.Attributes))
	}
	fmt.Fprintf(&b, "created_by: %s\n", q(e.CreatedBy))
	fmt.Fprintf(&b, "updated_at: %s\n", e.UpdatedAt.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", e.Title)
	if e.Summary != nil && *e.Summary != "" {
		fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(*e.Summary, "\n", "\n> "))
	}
	if body := strings.TrimSpace(e.Body); body != "" {
		b.WriteString(body)
		b.WriteString("\n")
	}
	if len(links) > 0 {
		b.WriteString("\n## Связи\n\n")
		for _, l := range links {
			fmt.Fprintf(&b, "- %s: [[%s|%s]]\n", l.Rel, FileName(l.ToID.String(), l.Title), l.Title)
		}
	}
	return b.String()
}

const VaultReadme = `# Knowledge (generated)

Эта папка сгенерирована командой ` + "`kn export --format markdown`" + ` из базы знаний.
Источник истины — база знаний; правки в этих файлах будут перезаписаны при следующем экспорте.
`
