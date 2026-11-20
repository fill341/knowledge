package service

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"kn.local/api/internal/domain"
	"kn.local/api/internal/store"
)

// Link suggestions and impact analysis (roadmap P2 #7). Suggestions are only proposals: links are made by an
// agent (kn_link) or a person (UI), never automatically.

// linkableKinds are the structured kinds worth linking to each other; documents and logs are raw material.
var linkableKinds = []string{"decision", "rule", "lesson", "fact"}

const linkSuggestionsMax = 5

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func isLinkable(kind string) bool {
	for _, k := range linkableKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// linkCandidates finds live structured entries of the same space related to e by its title and summary,
// skipping e itself, entries already linked to it and ids in skip.
func (s *Service) linkCandidates(ctx context.Context, q *store.Queries, c Caller, e store.EntryRow, skip map[uuid.UUID]bool) ([]store.Similar, error) {
	query := e.Title
	if e.Summary != nil {
		query += " " + *e.Summary
	}
	// The query is built from the entry's own text, not typed by a person: strip punctuation so "--max-tokens"
	// or quotes are not read as search operators (which would disable the any-word fallback).
	query = strings.TrimSpace(nonWord.ReplaceAllString(query, " "))
	out, in, err := q.Links(ctx, c.visibility(), e.ID)
	if err != nil {
		return nil, err
	}
	linked := map[uuid.UUID]bool{e.ID: true}
	for _, l := range out {
		linked[l.ToID] = true
	}
	for _, l := range in {
		linked[l.FromID] = true
	}
	hits, err := s.search(ctx, q, c, store.SearchParams{Query: query, BoostProjectID: e.ProjectID, Filter: store.EntryFilter{
		Spaces: []string{e.Space}, Kinds: linkableKinds, Statuses: []string{string(domain.StatusActive)}, Limit: 20}})
	if err != nil {
		return nil, err
	}
	res := []store.Similar{}
	for _, h := range hits {
		if linked[h.ID] || skip[h.ID] {
			continue
		}
		res = append(res, store.Similar{ID: h.ID, Title: h.Title, Status: string(h.Status), Similarity: round(h.Score)})
		if len(res) == linkSuggestionsMax {
			break
		}
	}
	return res, nil
}

// suggestLinksWarning adds a suggested_links warning for a structured entry, excluding entries the other
// warnings already point at (similar titles, same text).
func (s *Service) suggestLinksWarning(ctx context.Context, q *store.Queries, c Caller, e store.EntryRow, warnings []Warning) ([]Warning, error) {
	if !isLinkable(e.Kind) {
		return warnings, nil
	}
	skip := map[uuid.UUID]bool{}
	for _, w := range warnings {
		for _, it := range w.Items {
			skip[it.ID] = true
		}
	}
	cands, err := s.linkCandidates(ctx, q, c, e, skip)
	if err != nil || len(cands) == 0 {
		return warnings, err
	}
	return append(warnings, Warning{
		Code:    "suggested_links",
		Message: "related knowledge in this space — link it if the new entry depends on, refines or contradicts it",
		Items:   cands,
	}), nil
}

// LinkSuggestions returns candidates to link an entry to (GET /v1/entries/{id}/link-suggestions).
func (s *Service) LinkSuggestions(ctx context.Context, c Caller, id uuid.UUID) ([]store.Similar, error) {
	q := s.store.Q()
	e, err := s.getEntry(ctx, q, c, id, false)
	if err != nil {
		return nil, err
	}
	return s.linkCandidates(ctx, q, c, e, nil)
}

type Impact struct {
	Count      int               `json:"count"`
	Direct     int               `json:"direct"`
	Dependents []store.Dependent `json:"dependents"`
}

// impactDepth bounds the walk: three steps cover "a lesson about a fact a decision depends on".
const impactDepth = 3

// Impact lists what relies on an entry, so replacing or retiring it is a conscious step (GET /v1/entries/{id}/impact).
func (s *Service) Impact(ctx context.Context, c Caller, id uuid.UUID) (Impact, error) {
	q := s.store.Q()
	if _, err := s.getEntry(ctx, q, c, id, false); err != nil {
		return Impact{}, err
	}
	deps, err := q.Dependents(ctx, c.visibility(), id, impactDepth)
	if err != nil {
		return Impact{}, err
	}
	res := Impact{Count: len(deps), Dependents: deps}
	if res.Dependents == nil {
		res.Dependents = []store.Dependent{}
	}
	for _, d := range deps {
		if d.Depth == 1 {
			res.Direct++
		}
	}
	return res, nil
}

// ── Implicit links ───────────────────────────────────────────────────────────

// Affinity is an implicit link: two entries that talk about the same thing without an explicit link.
// The galaxy uses it as weak gravity; it is computed on read and never stored.
type Affinity struct {
	From  uuid.UUID `json:"from"`
	To    uuid.UUID `json:"to"`
	Score float64   `json:"score"` // cosine similarity, 0..1
}

const (
	affinityTerms    = 48   // characteristic lexemes per entry
	affinityPerEntry = 3    // strongest neighbours kept per entry
	affinityMin      = 0.12 // weaker neighbours are kept only as the single nearest one
	affinityFloor    = 0.02 // below this two entries share nothing meaningful
	affinityPosting  = 400  // terms shared by more entries are too common to tell anything
	affinitySameProj = 1.3  // neighbours from the same project are preferred
)

// affinityLinks finds implicit links between live entries of the same space by TF-IDF cosine similarity of
// their characteristic lexemes, tags and sources (same document or chat, same folder). Each entry keeps its
// strongest neighbours; an entry without any live explicit link always gets its nearest neighbour, so no
// knowledge floats unconnected unless it shares nothing at all with the rest. Pairs already linked explicitly
// are skipped. Deterministic: the same input gives the same links.
func affinityLinks(nodes []store.GraphNode, terms []store.GraphTerm, links []store.GraphLink) []Affinity {
	idx := map[uuid.UUID]int{}
	var live []store.GraphNode
	for _, n := range nodes {
		if n.Status != string(domain.StatusSuperseded) && n.Status != string(domain.StatusArchived) {
			idx[n.ID] = len(live)
			live = append(live, n)
		}
	}
	n := len(live)
	vec := make([]map[string]float64, n)
	for i := range vec {
		vec[i] = map[string]float64{}
	}
	for _, t := range terms {
		if i, ok := idx[t.ID]; ok {
			vec[i][t.Lexeme] = t.Weight
		}
	}
	// Tags and sources are labels put on purpose: weighted by rarity, twice as strong as a word.
	labels := make([][]string, n)
	df := map[string]int{}
	for i, e := range live {
		for _, tag := range e.Tags {
			labels[i] = append(labels[i], "#"+tag)
		}
		if e.SourceRef != nil && *e.SourceRef != "" {
			ref := *e.SourceRef
			labels[i] = append(labels[i], "@"+ref)
			if k := strings.LastIndex(ref, "/"); k > 0 {
				labels[i] = append(labels[i], "@dir:"+ref[:k])
			}
		}
		for _, l := range labels[i] {
			df[l]++
		}
	}
	for i := range live {
		for _, l := range labels[i] {
			if df[l] > 1 {
				vec[i][l] = 2 * math.Log(float64(n)/float64(df[l]))
			}
		}
	}

	// Normalize and build postings; terms are visited in sorted order for determinism.
	postings := map[string][]int{}
	for i, v := range vec {
		var norm float64
		for _, w := range v {
			norm += w * w
		}
		norm = math.Sqrt(norm)
		for t, w := range v {
			if w <= 0 || norm == 0 {
				delete(v, t)
				continue
			}
			v[t] = w / norm
			postings[t] = append(postings[t], i)
		}
	}

	linked := make([]map[int]bool, n)
	hasLink := make([]bool, n)
	for _, l := range links {
		a, okA := idx[l.From]
		b, okB := idx[l.To]
		if !okA || !okB {
			continue
		}
		if linked[a] == nil {
			linked[a] = map[int]bool{}
		}
		if linked[b] == nil {
			linked[b] = map[int]bool{}
		}
		linked[a][b], linked[b][a] = true, true
		hasLink[a], hasLink[b] = true, true
	}

	type cand struct {
		j     int
		score float64
	}
	pairs := map[[2]int]float64{}
	keep := func(a, b int, s float64) {
		if a > b {
			a, b = b, a
		}
		if s > pairs[[2]int{a, b}] {
			pairs[[2]int{a, b}] = s
		}
	}
	scores := make([]float64, n)
	for i := range live {
		var touched []int
		for t, w := range vec[i] {
			p := postings[t]
			if len(p) > affinityPosting {
				continue
			}
			for _, j := range p {
				if j == i || live[j].Space != live[i].Space || linked[i][j] {
					continue
				}
				if scores[j] == 0 {
					touched = append(touched, j)
				}
				scores[j] += w * vec[j][t]
			}
		}
		cands := make([]cand, 0, len(touched))
		for _, j := range touched {
			s := scores[j]
			scores[j] = 0
			if s < affinityFloor {
				continue
			}
			rank := s
			if ptrEq(live[i].Project, live[j].Project) {
				rank *= affinitySameProj
			}
			cands = append(cands, cand{j, rank})
		}
		sort.Slice(cands, func(a, b int) bool {
			if cands[a].score != cands[b].score {
				return cands[a].score > cands[b].score
			}
			return live[cands[a].j].ID.String() < live[cands[b].j].ID.String()
		})
		for k, c := range cands {
			if k >= affinityPerEntry {
				break
			}
			if k == 0 && !hasLink[i] || c.score >= affinityMin {
				keep(i, c.j, math.Min(1, c.score))
			}
		}
	}

	out := make([]Affinity, 0, len(pairs))
	for p, s := range pairs {
		out = append(out, Affinity{From: live[p[0]].ID, To: live[p[1]].ID, Score: math.Round(s*1000) / 1000})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].From != out[b].From {
			return out[a].From.String() < out[b].From.String()
		}
		return out[a].To.String() < out[b].To.String()
	})
	return out
}

func ptrEq(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
