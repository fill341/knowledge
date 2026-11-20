package service

import (
	"testing"

	"github.com/google/uuid"

	"kn.local/api/internal/auth"
	"kn.local/api/internal/store"
)

func TestNormalizeRepoURL(t *testing.T) {
	want := "github.com/acme/billing"
	for _, in := range []string{
		"git@github.com:acme/billing.git",
		"https://github.com/acme/billing",
		"https://github.com/acme/billing.git/",
		"ssh://git@github.com/acme/billing.git",
		"https://user:token@GitHub.com/acme/billing",
		"github.com/acme/billing",
	} {
		if got := NormalizeRepoURL(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"file:///Users/me/Code/Health/": "file:///Users/me/Code/Health",
		"file:///Users/me/My%20Repo":    "file:///Users/me/My Repo",
	} {
		if got := NormalizeRepoURL(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	if NormalizeRepoURL("  ") != "" {
		t.Error("blank should normalize to empty")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	tok, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := auth.Parse(tok.String())
	if err != nil || parsed != tok {
		t.Fatalf("parse(%q) = %+v, %v", tok, parsed, err)
	}
	if !auth.Verify(tok.Secret, tok.Hash()) || auth.Verify(tok.Secret+"x", tok.Hash()) {
		t.Fatal("verify broken")
	}
	for _, bad := range []string{"", "kn_", "kn_short_secret", "xx_AAAAAAAA_" + tok.Secret, "kn_AAAAAAA!_" + tok.Secret} {
		if _, err := auth.Parse(bad); err == nil {
			t.Errorf("expected parse error for %q", bad)
		}
	}
}

func TestSessionTitle(t *testing.T) {
	if got := sessionTitle("codex", "## Починил ретраи\nдетали"); got != "Сессия codex: Починил ретраи" {
		t.Fatalf("got %q", got)
	}
}

func TestAffinityLinks(t *testing.T) {
	id := func(b byte) uuid.UUID { return uuid.UUID{b} }
	proj := "work/billing"
	node := func(b byte, status string, tags ...string) store.GraphNode {
		return store.GraphNode{ID: id(b), Space: "work", Project: &proj, Status: status, Tags: tags}
	}
	nodes := []store.GraphNode{
		node(1, "active"), node(2, "active"), node(3, "active", "webhooks"), node(4, "active", "webhooks"),
		node(5, "superseded"), node(6, "active"),
		{ID: id(7), Space: "personal", Status: "active"},
	}
	terms := []store.GraphTerm{
		{ID: id(1), Lexeme: "ретра", Weight: 2}, {ID: id(1), Lexeme: "очеред", Weight: 1},
		{ID: id(2), Lexeme: "ретра", Weight: 2}, {ID: id(2), Lexeme: "очеред", Weight: 1},
		{ID: id(5), Lexeme: "ретра", Weight: 2}, // retired copy: never linked implicitly
		{ID: id(6), Lexeme: "очеред", Weight: 0.1}, {ID: id(6), Lexeme: "кэш", Weight: 3},
		{ID: id(7), Lexeme: "ретра", Weight: 2}, // other space: never linked
	}
	got := map[[2]byte]float64{}
	for _, a := range affinityLinks(nodes, terms, []store.GraphLink{{From: id(3), To: id(6), Rel: "relates_to"}}) {
		got[[2]byte{a.From[0], a.To[0]}] = a.Score
	}
	if s := got[[2]byte{1, 2}]; s < 0.99 {
		t.Errorf("same words must give a strong link 1–2, got %v (%v)", s, got)
	}
	if got[[2]byte{3, 4}] == 0 {
		t.Errorf("a shared rare tag must link 3–4: %v", got)
	}
	if got[[2]byte{3, 6}] != 0 {
		t.Errorf("explicitly linked pair must not get an implicit link: %v", got)
	}
	weak := false
	for p := range got {
		if p[0] == 6 || p[1] == 6 {
			weak = true
		}
		if p[0] == 5 || p[1] == 5 || p[0] == 7 || p[1] == 7 {
			t.Errorf("superseded or other-space entry linked: %v", got)
		}
	}
	if weak {
		t.Errorf("6 has an explicit link and only weak similarity — no forced neighbour expected: %v", got)
	}
}
