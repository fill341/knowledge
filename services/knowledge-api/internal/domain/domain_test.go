package domain

import "testing"

func TestParseScopes(t *testing.T) {
	s, err := ParseScopes([]string{"space:work:write", "space:shared:read", "space:work:read"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Level("work") != LevelWrite || s.Level("shared") != LevelRead || s.Level("personal") != LevelNone {
		t.Fatalf("unexpected levels: %+v", s)
	}
	if !s.Can("work", LevelRead) || s.Can("shared", LevelWrite) {
		t.Fatal("hierarchy broken")
	}
	admin, _ := ParseScopes([]string{"admin"})
	if !admin.Can("anything", LevelAdmin) || !admin.AllSpaces() {
		t.Fatal("global admin must have every level everywhere")
	}
	for _, bad := range []string{"space:work", "space::read", "space:work:owner", "root"} {
		if _, err := ParseScopes([]string{bad}); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestCheckTransition(t *testing.T) {
	agent := Actor{Principal: Principal{Kind: PrincipalAgent}, Level: LevelWrite}
	human := Actor{Principal: Principal{Kind: PrincipalUser}, Level: LevelWrite}
	reviewer := Actor{Principal: Principal{Kind: PrincipalUser}, Level: LevelReview}

	cases := []struct {
		from, to Status
		actor    Actor
		ok       bool
	}{
		{StatusProposed, StatusActive, agent, false},
		{StatusProposed, StatusActive, human, false},
		{StatusProposed, StatusActive, reviewer, true},
		{StatusActive, StatusDeprecated, agent, true},
		{StatusActive, StatusSuperseded, agent, true},
		{StatusActive, StatusArchived, agent, false},
		{StatusActive, StatusArchived, human, true},
		{StatusDraft, StatusActive, agent, false},
		{StatusDraft, StatusProposed, agent, true},
		{StatusArchived, StatusActive, human, false},
		{StatusArchived, StatusActive, reviewer, true},
		{StatusSuperseded, StatusActive, reviewer, false},
	}
	for _, c := range cases {
		err := CheckTransition(c.from, c.to, c.actor)
		if (err == nil) != c.ok {
			t.Errorf("%s → %s by %s/%s: got err=%v, want ok=%v", c.from, c.to, c.actor.Principal.Kind, c.actor.Level, err, c.ok)
		}
	}
}

func TestInitialStatusAndReview(t *testing.T) {
	agent := Actor{Principal: Principal{Kind: PrincipalAgent}}
	human := Actor{Principal: Principal{Kind: PrincipalUser}}
	decision := Kind{Kind: "decision", AgentRequiresReview: true}
	note := Kind{Kind: "note"}
	work := Space{Slug: "work"}
	shared := Space{Slug: "shared", Settings: SpaceSettings{AgentAlwaysReview: true}}
	custom := Space{Slug: "x", Settings: SpaceSettings{ReviewKinds: []string{"note"}}}

	if InitialStatus(false, agent, RequiresReview(decision, work)) != StatusProposed {
		t.Error("agent decision should be proposed")
	}
	if InitialStatus(false, human, RequiresReview(decision, work)) != StatusActive {
		t.Error("human decision should be active")
	}
	if InitialStatus(false, agent, RequiresReview(note, work)) != StatusActive {
		t.Error("agent note should be active")
	}
	if !RequiresReview(note, shared) {
		t.Error("shared should review everything from agents")
	}
	if !RequiresReview(note, custom) || RequiresReview(decision, custom) {
		t.Error("space review_kinds must override kind defaults")
	}
	if InitialStatus(true, human, false) != StatusDraft {
		t.Error("draft flag ignored")
	}
}
