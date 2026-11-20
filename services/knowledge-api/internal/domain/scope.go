package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Level is a hierarchical permission level within a space: read < write < review < admin.
type Level int

const (
	LevelNone Level = iota
	LevelRead
	LevelWrite
	LevelReview
	LevelAdmin
)

var levelNames = map[string]Level{"read": LevelRead, "write": LevelWrite, "review": LevelReview, "admin": LevelAdmin}

func (l Level) String() string {
	for n, v := range levelNames {
		if v == l {
			return n
		}
	}
	return "none"
}

// Scope is the set of permissions granted to a token.
// Scope strings: "admin" (global) or "space:<slug>:<level>".
type Scope struct {
	Admin  bool
	Spaces map[string]Level
}

func ParseScopes(raw []string) (Scope, error) {
	s := Scope{Spaces: map[string]Level{}}
	for _, r := range raw {
		if r == "admin" {
			s.Admin = true
			continue
		}
		parts := strings.Split(r, ":")
		if len(parts) != 3 || parts[0] != "space" || parts[1] == "" {
			return Scope{}, fmt.Errorf("invalid scope %q: want admin or space:<slug>:<read|write|review|admin>", r)
		}
		lvl, ok := levelNames[parts[2]]
		if !ok {
			return Scope{}, fmt.Errorf("invalid scope level %q in %q", parts[2], r)
		}
		if lvl > s.Spaces[parts[1]] {
			s.Spaces[parts[1]] = lvl
		}
	}
	return s, nil
}

// Level returns the effective level in a space. Global admin has admin everywhere.
func (s Scope) Level(space string) Level {
	if s.Admin {
		return LevelAdmin
	}
	return s.Spaces[space]
}

func (s Scope) Can(space string, lvl Level) bool { return s.Level(space) >= lvl }

// Readable lists spaces readable with this scope. For global admin, it returns nil and
// AllSpaces reports true — the caller substitutes the full space list.
func (s Scope) Readable() []string {
	var out []string
	for sp, l := range s.Spaces {
		if l >= LevelRead {
			out = append(out, sp)
		}
	}
	sort.Strings(out)
	return out
}

func (s Scope) AllSpaces() bool { return s.Admin }

// Strings renders the scope back to its canonical string form.
func (s Scope) Strings() []string {
	var out []string
	if s.Admin {
		out = append(out, "admin")
	}
	for _, sp := range s.Readable() {
		out = append(out, fmt.Sprintf("space:%s:%s", sp, s.Spaces[sp]))
	}
	return out
}
