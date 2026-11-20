package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"kn.local/api/internal/domain"
)

// Retrieval evaluation (roadmap P1 #3): a labelled set of queries with expected entries is run through
// the same search and context code agents use, and scored with recall@k and MRR. Read-only; the runs are
// not recorded as searches, so they do not distort usage statistics or the health report.

type EvalExpect struct {
	ID        string `json:"id,omitempty"`
	SourceRef string `json:"source_ref,omitempty"`
	Title     string `json:"title,omitempty"`
}

func (e EvalExpect) ref() (field, value string) {
	switch {
	case e.ID != "":
		return "id", e.ID
	case e.SourceRef != "":
		return "source_ref", e.SourceRef
	default:
		return "title", e.Title
	}
}

type EvalCase struct {
	ID      string `json:"id"`
	Query   string `json:"query"`
	Project string `json:"project,omitempty"`
	Mode    string `json:"mode,omitempty"` // search | context | both (default)
	// NoTask runs the context without a task, like a session-start hook: only the project is known.
	NoTask bool         `json:"no_task,omitempty"`
	Expect []EvalExpect `json:"expect"`
	Note   string       `json:"note,omitempty"`
}

type EvalInput struct {
	K         int        `json:"k"`          // search depth, default 10
	MaxTokens int        `json:"max_tokens"` // context budget, default 3000
	Cases     []EvalCase `json:"cases"`
}

type EvalSearchResult struct {
	Results   int       `json:"results"`
	FirstRank int       `json:"first_rank"` // 1-based rank of the first expected entry, 0 = not in top k
	Ranks     []int     `json:"ranks"`      // per expectation, 0 = not in top k
	Recall5   float64   `json:"recall_at_5"`
	RecallK   float64   `json:"recall_at_k"`
	RR        float64   `json:"rr"`
	Top       []EvalHit `json:"top"` // first results, to see what outranked the expectation
}

type EvalHit struct {
	ID    uuid.UUID `json:"id"`
	Title string    `json:"title"`
}

type EvalContextResult struct {
	Found   int     `json:"found"`   // in the packed sections
	Indexed int     `json:"indexed"` // only in the index of entries that did not fit
	Of      int     `json:"of"`
	Hit     float64 `json:"hit"`
	Tokens  int     `json:"tokens"`
}

type EvalCaseResult struct {
	ID      string             `json:"id"`
	Query   string             `json:"query"`
	Errors  []string           `json:"errors,omitempty"` // label problems: the case is excluded from metrics
	Search  *EvalSearchResult  `json:"search,omitempty"`
	Context *EvalContextResult `json:"context,omitempty"`
}

type EvalSummary struct {
	Cases        int     `json:"cases"`
	LabelErrors  int     `json:"label_errors"`
	SearchCases  int     `json:"search_cases"`
	Recall5      float64 `json:"recall_at_5"`
	RecallK      float64 `json:"recall_at_k"`
	MRR          float64 `json:"mrr"`
	EmptyRate    float64 `json:"empty_rate"`
	ContextCases int     `json:"context_cases"`
	ContextHit   float64 `json:"context_hit"`
	// ContextReach counts an expectation found in the packed sections or in the index (reachable by kn_get).
	ContextReach float64 `json:"context_reach"`
}

type EvalReport struct {
	K         int              `json:"k"`
	MaxTokens int              `json:"max_tokens"`
	Summary   EvalSummary      `json:"summary"`
	Cases     []EvalCaseResult `json:"cases"`
}

const maxEvalCases = 500

func (s *Service) Eval(ctx context.Context, c Caller, in EvalInput) (EvalReport, error) {
	if in.K <= 0 {
		in.K = 10
	}
	if in.K > 50 {
		in.K = 50
	}
	if in.MaxTokens <= 0 {
		in.MaxTokens = DefaultContextTokens
	}
	if len(in.Cases) == 0 || len(in.Cases) > maxEvalCases {
		return EvalReport{}, domain.Validation("cases: 1 to %d required", maxEvalCases)
	}
	rep := EvalReport{K: in.K, MaxTokens: in.MaxTokens, Cases: make([]EvalCaseResult, 0, len(in.Cases))}
	sum := &rep.Summary
	q := s.store.Q()
	v := c.visibility()

	for i, tc := range in.Cases {
		r := EvalCaseResult{ID: tc.ID, Query: strings.TrimSpace(tc.Query)}
		if r.ID == "" {
			r.ID = fmt.Sprintf("#%d", i+1)
		}
		mode := tc.Mode
		if mode == "" {
			mode = "both"
		}
		if mode != "search" && mode != "context" && mode != "both" {
			r.Errors = append(r.Errors, "mode must be search, context or both")
		}
		if r.Query == "" {
			r.Errors = append(r.Errors, "empty query")
		}
		if len(tc.Expect) == 0 {
			r.Errors = append(r.Errors, "no expected entries")
		}
		// Each expectation is satisfied by any of the entries it resolves to (one document imported twice, …).
		expect := make([]map[uuid.UUID]bool, len(tc.Expect))
		for j, e := range tc.Expect {
			field, value := e.ref()
			ids, err := q.EntriesByRef(ctx, v, field, value)
			if err != nil {
				return EvalReport{}, err
			}
			if len(ids) == 0 {
				r.Errors = append(r.Errors, fmt.Sprintf("expected %s %q: no visible live entry", field, value))
			}
			expect[j] = map[uuid.UUID]bool{}
			for _, id := range ids {
				expect[j][id] = true
			}
		}
		if len(r.Errors) > 0 {
			sum.LabelErrors++
			rep.Cases = append(rep.Cases, r)
			continue
		}

		if mode != "context" {
			page, err := s.runSearch(ctx, c, SearchInput{Q: r.Query, Project: tc.Project, Limit: in.K})
			if err != nil {
				r.Errors = append(r.Errors, "search: "+errText(err))
				sum.LabelErrors++
				rep.Cases = append(rep.Cases, r)
				continue
			}
			sr := &EvalSearchResult{Results: len(page.Items), Ranks: make([]int, len(expect))}
			for rank, it := range page.Items {
				if rank < 5 {
					sr.Top = append(sr.Top, EvalHit{it.ID, it.Title})
				}
				for j, set := range expect {
					if sr.Ranks[j] == 0 && set[it.ID] {
						sr.Ranks[j] = rank + 1
					}
				}
			}
			at5, atK := 0, 0
			for _, rk := range sr.Ranks {
				if rk > 0 {
					atK++
					if rk <= 5 {
						at5++
					}
					if sr.FirstRank == 0 || rk < sr.FirstRank {
						sr.FirstRank = rk
					}
				}
			}
			sr.Recall5 = float64(at5) / float64(len(expect))
			sr.RecallK = float64(atK) / float64(len(expect))
			if sr.FirstRank > 0 {
				sr.RR = 1 / float64(sr.FirstRank)
			}
			r.Search = sr
			sum.SearchCases++
			sum.Recall5 += sr.Recall5
			sum.RecallK += sr.RecallK
			sum.MRR += sr.RR
			if sr.Results == 0 {
				sum.EmptyRate++
			}
		}

		if mode != "search" {
			task := r.Query
			if tc.NoTask {
				task = ""
			}
			res, err := s.buildContext(ctx, c, ContextInput{Project: tc.Project, Task: task, MaxTokens: in.MaxTokens})
			if err != nil {
				r.Errors = append(r.Errors, "context: "+errText(err))
				sum.LabelErrors++
				rep.Cases = append(rep.Cases, r)
				continue
			}
			packed, indexed := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
			for _, sec := range res.Sections {
				for _, it := range sec.Items {
					if id, err := uuid.Parse(it.ID); err == nil {
						if sec.Name == "more" {
							indexed[id] = true
						} else {
							packed[id] = true
						}
					}
				}
			}
			cr := &EvalContextResult{Of: len(expect), Tokens: res.TokenEstimate}
			for _, set := range expect {
				inPack, inIndex := false, false
				for id := range set {
					inPack = inPack || packed[id]
					inIndex = inIndex || indexed[id]
				}
				if inPack {
					cr.Found++
				} else if inIndex {
					cr.Indexed++
				}
			}
			cr.Hit = float64(cr.Found) / float64(cr.Of)
			r.Context = cr
			sum.ContextCases++
			sum.ContextHit += cr.Hit
			sum.ContextReach += float64(cr.Found+cr.Indexed) / float64(cr.Of)
		}
		rep.Cases = append(rep.Cases, r)
	}

	sum.Cases = len(in.Cases)
	if n := float64(sum.SearchCases); n > 0 {
		sum.Recall5, sum.RecallK, sum.MRR, sum.EmptyRate = round(sum.Recall5/n), round(sum.RecallK/n), round(sum.MRR/n), round(sum.EmptyRate/n)
	}
	if n := float64(sum.ContextCases); n > 0 {
		sum.ContextHit, sum.ContextReach = round(sum.ContextHit/n), round(sum.ContextReach/n)
	}
	// One audit record for the run, not per query: evaluation is not usage.
	_ = q.Audit(ctx, c.audit("eval", "", nil, nil, map[string]any{"cases": sum.Cases, "label_errors": sum.LabelErrors}))
	return rep, nil
}

func errText(err error) string {
	if de, ok := domain.AsError(err); ok {
		return de.Code + ": " + de.Detail
	}
	return err.Error()
}
