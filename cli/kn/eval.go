package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kn.local/kclient"
)

// evalSet is the query set file (see eval/README.md). Project is the default for cases without one.
type evalSet struct {
	Comment   string             `json:"comment,omitempty"`
	Project   string             `json:"project,omitempty"`
	K         int                `json:"k,omitempty"`
	MaxTokens int                `json:"max_tokens,omitempty"`
	Cases     []kclient.EvalCase `json:"cases"`
}

type evalBaseline struct {
	SavedAt time.Time           `json:"saved_at"`
	Set     string              `json:"set"`
	Summary kclient.EvalSummary `json:"summary"`
	Ranks   map[string]int      `json:"ranks"` // case id → first rank in search (0 = miss)
}

var errRegression = errors.New("retrieval quality regressed beyond tolerance")

func (a *app) eval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	file := fs.String("f", "eval/local/queries.json", "query set")
	baseline := fs.String("baseline", "", "baseline to compare with (default: baseline.json next to the set, if present)")
	save := fs.Bool("save-baseline", false, "write the result as the new baseline")
	tolerance := fs.Float64("tolerance", 0.02, "allowed drop of each metric before failing")
	all := fs.Bool("all", false, "list every case, not only misses")
	asJSON := fs.Bool("json", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var set evalSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return fmt.Errorf("%s: %w", *file, err)
	}
	for i := range set.Cases {
		if set.Cases[i].Project == "" {
			set.Cases[i].Project = set.Project
		}
	}
	rep, err := a.client.Eval(ctx, kclient.EvalInput{K: set.K, MaxTokens: set.MaxTokens, Cases: set.Cases})
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(rep)
	}

	s := rep.Summary
	fmt.Fprintf(a.out, "Eval %s: %d запросов, ошибок разметки %d, k=%d\n", *file, s.Cases, s.LabelErrors, rep.K)
	fmt.Fprintf(a.out, "  поиск (%d):   recall@5 %.3f   recall@%d %.3f   MRR %.3f   пустых %.1f%%\n",
		s.SearchCases, s.Recall5, rep.K, s.RecallK, s.MRR, s.EmptyRate*100)
	fmt.Fprintf(a.out, "  контекст (%d): ожидаемые записи в контексте %.3f, в контексте или оглавлении %.3f (бюджет %d токенов)\n",
		s.ContextCases, s.ContextHit, s.ContextReach, rep.MaxTokens)

	notes := map[string]string{}
	for _, c := range set.Cases {
		notes[c.ID] = c.Note
	}
	fmt.Fprintln(a.out)
	for _, c := range rep.Cases {
		switch {
		case len(c.Errors) > 0:
			fmt.Fprintf(a.out, "  ? [%s] %q — разметка: %s\n", c.ID, c.Query, strings.Join(c.Errors, "; "))
		case c.Search != nil && c.Search.RecallK < 1, c.Context != nil && c.Context.Found < c.Context.Of, *all:
			// ✓ everything found, ✗ nothing found by any retrieval that ran, ~ partly.
			searchMiss := c.Search != nil && c.Search.FirstRank == 0
			contextMiss := c.Context != nil && c.Context.Found == 0
			mark := "~"
			switch {
			case (c.Search == nil || c.Search.RecallK == 1) && (c.Context == nil || c.Context.Found == c.Context.Of):
				mark = "✓"
			case (c.Search == nil || searchMiss) && (c.Context == nil || contextMiss):
				mark = "✗"
			}
			fmt.Fprintf(a.out, "  %s [%s] %q", mark, c.ID, c.Query)
			if c.Search != nil {
				fmt.Fprintf(a.out, "  поиск: ранги %v из %d", c.Search.Ranks, c.Search.Results)
			}
			if c.Context != nil {
				fmt.Fprintf(a.out, "  контекст: %d/%d", c.Context.Found, c.Context.Of)
				if c.Context.Indexed > 0 {
					fmt.Fprintf(a.out, " (+%d в оглавлении)", c.Context.Indexed)
				}
			}
			fmt.Fprintln(a.out)
			if c.Search != nil && c.Search.FirstRank != 1 && len(c.Search.Top) > 0 {
				fmt.Fprintf(a.out, "      выше: %s\n", c.Search.Top[0].Title)
			}
			if n := notes[c.ID]; n != "" {
				fmt.Fprintf(a.out, "      заметка: %s\n", n)
			}
		}
	}

	path := *baseline
	if path == "" {
		path = filepath.Join(filepath.Dir(*file), "baseline.json")
	}
	var regressed bool
	if b, err := os.ReadFile(path); err == nil {
		var base evalBaseline
		if err := json.Unmarshal(b, &base); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		fmt.Fprintf(a.out, "\nСравнение с базовой линией %s (%s):\n", path, base.SavedAt.Format("2006-01-02"))
		for _, m := range []struct {
			name      string
			now, then float64
			lowerBest bool
		}{
			{"recall@5", s.Recall5, base.Summary.Recall5, false},
			{"recall@k", s.RecallK, base.Summary.RecallK, false},
			{"MRR", s.MRR, base.Summary.MRR, false},
			{"пустые", s.EmptyRate, base.Summary.EmptyRate, true},
			{"контекст", s.ContextHit, base.Summary.ContextHit, false},
			{"досягаемо", s.ContextReach, base.Summary.ContextReach, false},
		} {
			d := m.now - m.then
			worse := d < -*tolerance
			if m.lowerBest {
				worse = d > *tolerance
			}
			flag := ""
			if worse {
				flag, regressed = "  ← хуже", true
			}
			fmt.Fprintf(a.out, "  %-9s %.3f → %.3f (%+.3f)%s\n", m.name, m.then, m.now, d, flag)
		}
		for _, c := range rep.Cases {
			if c.Search == nil {
				continue
			}
			if was, ok := base.Ranks[c.ID]; ok && was > 0 && (c.Search.FirstRank == 0 || c.Search.FirstRank > was) {
				fmt.Fprintf(a.out, "  [%s] ранг ухудшился: %d → %d\n", c.ID, was, c.Search.FirstRank)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if *save {
		base := evalBaseline{SavedAt: time.Now().UTC(), Set: *file, Summary: s, Ranks: map[string]int{}}
		for _, c := range rep.Cases {
			if c.Search != nil {
				base.Ranks[c.ID] = c.Search.FirstRank
			}
		}
		out, _ := json.MarshalIndent(base, "", "  ")
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "\nБазовая линия сохранена: %s\n", path)
		return nil
	}
	if regressed {
		return errRegression
	}
	return nil
}
