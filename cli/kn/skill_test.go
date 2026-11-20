package main

import "testing"

func TestReplaceSection(t *testing.T) {
	sec := "<!-- kn:begin x -->\nnew\n<!-- kn:end -->\n"
	for name, c := range map[string]struct{ in, want string }{
		"empty":   {"", sec},
		"append":  {"# Agents\n\nhand-written\n", "# Agents\n\nhand-written\n\n" + sec},
		"replace": {"# A\n\n<!-- kn:begin old -->\nold\n<!-- kn:end -->\n\n## Tail\n", "# A\n\n" + sec + "\n## Tail\n"},
	} {
		if got := replaceSection(c.in, sec); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}
