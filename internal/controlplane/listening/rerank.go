package listening

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// The model's relevance scores (layer 2's first call): last30days rerank_candidates (lib/rerank.py:277-358). The
// shortlist, the pool's first Limits.Shortlist items, is scored in one batched call; an item the model gave no score,
// and every item past the shortlist, keeps the code's local score. Every score says where it came from.

// The scores' provenance (last30days RERANK_PROVENANCE_*, lib/rerank.py:1203-1208).
const (
	ProvenanceLLM      = "llm"
	ProvenanceFallback = "fallback"
)

// rerankPrompt is the version of the scoring prompt: a cached score is kept for its version only.
const rerankPrompt = "rerank-1"

// rerankCliff is the model's score under which an item is cut to 0.3 of its final score; a local score is never cut
// so (last30days _final_score, lib/rerank.py:1256-1265: the cliff "demotes items a judge explicitly called
// irrelevant"; cliffing a weak local score buried on-topic threads, so it is the floor of the rule-based score).
const (
	rerankCliff   = 20
	cliffMultiple = 0.3
)

// scored is a cached judgment of one item's relevance.
type scored struct {
	relevance float64
	reason    string
}

// rerankSystem is last30days' prompt (_build_prompt, lib/rerank.py:456-482), its entity grounding (436-447) with the
// plan's topic as the primary entity, and obsei's rule on language (enrichers/classify.py:26-30).
func rerankSystem(plan Plan) string {
	var queries strings.Builder
	for _, sq := range plan.Subqueries {
		fmt.Fprintf(&queries, "- %s: %s\n", sq.Label, sq.Query)
	}

	topic := plan.topic()

	return "Judge search-result relevance for a social listening search: what people say about a topic.\n\n" +
		"Topic: " + topic + "\nRanking queries:\n" + queries.String() + "\n" +
		`Return JSON only:
{
  "scores": [
    {
      "id": "id",
      "relevance": 0-100,
      "reason": "short reason"
    }
  ]
}

Scoring guidance:
- 90 to 100: one of the strongest pieces of evidence
- 70 to 89: clearly relevant and useful
- 40 to 69: somewhat relevant but weaker
- 0 to 39: weak, redundant, or off-target

Primary entity grounding: the user's primary entity is "` + topic + `". A candidate that does NOT mention this ` +
		`entity (or a clear synonym/abbreviation) in its title or text should score no higher than 30, regardless ` +
		`of other signals. Do not let a candidate match the topic vicinity without matching the entity itself.
Score every candidate by its id. Read each post in its own language; do not translate it.`
}

// candidateBlock lists items for the model: the bounded, sanitized fields only (last30days _candidate_lines,
// lib/rerank.py:412-430; jev-social synthesisEvidence, src/report.js:351-391): id, source, title of 220 and text of
// 420 characters without links, date, the subqueries that found it, author and where it was said.
func candidateBlock(items []Item, withCluster bool) string {
	var b strings.Builder

	for _, it := range items {
		sources := it.Source
		if len(it.SeenIn) > 0 {
			sources += " (also " + strings.Join(it.SeenIn, ", ") + ")"
		}

		date := "unknown"
		if it.PublishedAt != nil {
			date = it.PublishedAt.UTC().Format(time.DateOnly)
		}

		fmt.Fprintf(&b, "- id: %s\n", it.ID)

		if withCluster {
			fmt.Fprintf(&b, "  cluster: %s\n", it.Cluster)
		}

		fmt.Fprintf(&b, "  sources: %s\n  title: %s\n  text: %s\n  date: %s\n  matched_subqueries: %s\n", sources,
			excerpt(it.Title, 220), excerpt(it.Text, 420), date, strings.Join(it.Subqueries, ", "))

		if it.Author != "" {
			fmt.Fprintf(&b, "  author: %s\n", excerpt(it.Author, 80))
		}

		if it.Container != "" {
			fmt.Fprintf(&b, "  where: %s\n", excerpt(it.Container, 80))
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// scoreKey is the cache's key of an item's score: the item, its text, the question (topic and subqueries), the
// prompt's version and the model.
func scoreKey(plan Plan, it Item, model string) string {
	var q strings.Builder
	for _, sq := range plan.Subqueries {
		q.WriteString(sq.Label + "\x1e" + sq.Query + "\x1e")
	}

	return cacheKey("score", it.ID, it.Title, it.Text, plan.topic(), q.String(), rerankPrompt, model)
}

// rerank scores the shortlist with the model and orders the items again, the best first.
func (s *Service) rerank(ctx context.Context, plan Plan, items []Item, c Caller, l Limits) ([]Item, Step) {
	shortlist := min(l.Shortlist, len(items))

	st := s.step(ctx, "rerank", s.reviewModel, DefaultReviewModel, c, func(model string, t *tally, st *Step) error {
		if shortlist == 0 {
			st.Detail = "nothing to score"

			return nil
		}

		got := map[string]scored{}

		var ask []Item

		for _, it := range items[:shortlist] {
			if v, ok := s.scores.get(scoreKey(plan, it, model)); ok {
				got[it.ID] = v

				continue
			}

			ask = append(ask, it)
		}

		st.Cached = len(got)

		var refused refusedRows

		if len(ask) > 0 {
			fresh, r, err := s.askScores(ctx, plan, ask, model, c, t)
			if err != nil {
				return err
			}

			refused = r

			for _, it := range ask {
				if v, ok := fresh[it.ID]; ok {
					got[it.ID] = v
					s.scores.put(scoreKey(plan, it, model), v)
				}
			}
		}

		for i := range items[:shortlist] {
			if v, ok := got[items[i].ID]; ok {
				items[i].rerank = v.relevance
				items[i].Provenance = ProvenanceLLM
				items[i].Reason = v.reason
			}
		}

		st.Detail = joinReason(fmt.Sprintf("%d of %d shortlisted items scored by the model", len(got), shortlist),
			refused.note()) + "; the rest keep the local score"

		return nil
	})

	for i := range items {
		items[i].Score = math.Round(finalScore(items[i])*10) / 10
	}

	byScore(items)

	return items, st
}

// scoreRow is a score's schema: an id and a relevance from 0 to 100, its reason optional (last30days _build_prompt's
// JSON, lib/rerank.py:464-473).
type scoreRow struct {
	ID        string   `json:"id"`
	Relevance *float64 `json:"relevance"`
	Reason    string   `json:"reason"`
}

func (r scoreRow) check() error {
	switch {
	case strings.TrimSpace(r.ID) == "":
		return errors.New("has no id")
	case r.Relevance == nil:
		return errors.New("has no relevance")
	case *r.Relevance < 0 || *r.Relevance > 100:
		return fmt.Errorf("has the relevance %g, not from 0 to 100", *r.Relevance)
	}

	return nil
}

// askScores asks the model to score items and reads its scores, each row checked on its own: an id that is not one of
// the items is dropped (last30days _apply_llm_scores, lib/rerank.py:497-538; obsei ask.py:151-152).
func (s *Service) askScores(ctx context.Context, plan Plan, items []Item, model string, c Caller, t *tally,
) (map[string]scored, refusedRows, error) {
	var refused refusedRows

	body, err := chatRequest(model, rerankSystem(plan), fenced("Candidates", candidateBlock(items, false)))
	if err != nil {
		return nil, refused, err
	}

	text, err := ask(ctx, c.Model, body, reviewDeadline, reviewAttempts, t)
	if err != nil {
		return nil, refused, err
	}

	rows, err := answerRows(text, "scores")
	if err != nil {
		return nil, refused, err
	}

	known := map[string]bool{}
	for _, it := range items {
		known[it.ID] = true
	}

	out := map[string]scored{}

	for n, raw := range rows {
		var row scoreRow

		if err := decodeRow(raw, &row); err != nil {
			refused.add("scores", n, err)

			continue
		}

		if err := row.check(); err != nil {
			refused.add("scores", n, err)

			continue
		}

		if id := strings.TrimSpace(row.ID); known[id] {
			out[id] = scored{relevance: *row.Relevance, reason: clip(row.Reason, 200)}
		}
	}

	if len(out) == 0 {
		why := fmt.Sprintf("it scored none of the %d items it was given", len(items))
		if refused.n > 0 {
			why = refused.note()
		}

		return nil, refused, fmt.Errorf("the model gave no valid score: %s", why)
	}

	return out, refused, nil
}
