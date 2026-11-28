package listening

import (
	"cmp"
	"math"
	"slices"
	"time"
)

// The ranking (last30days, lib/signals.py, lib/fusion.py and the local score of lib/rerank.py, the path it takes
// without a reranking model): each stream is ordered by its own items' relevance, freshness and engagement; streams
// are fused by weighted reciprocal rank; each fused item is scored by its relevance, its fused rank, its freshness and
// its engagement on its source's own scale.

const (
	// rrfK is RRF's constant (Cormack et al. 2009; last30days fusion.RRF_K).
	rrfK = 60
	// rrfCeiling is the fused score read as 100 (last30days rerank._normalized_rrf).
	rrfCeiling = 0.08
	// sourceQuality is last30days' default (signals.SOURCE_QUALITY): Reddit's and the web's are both 0.6 there.
	sourceQuality = 0.6
	// maxPerAuthor keeps one author from filling the answer (last30days fusion._MAX_ITEMS_PER_AUTHOR,
	// last30days-cn score.apply_per_author_cap).
	maxPerAuthor = 3
	// outOfWindowMultiplier is last30days rerank.OUT_OF_WINDOW_FINAL_MULTIPLIER.
	outOfWindowMultiplier = 0.35
)

// engagementWeights are each source's counts and their weights, on log1p, and the value read as 100: a very high
// item of that source (last30days signals.ENGAGEMENT_WEIGHTS, _reddit_engagement and _youtube_engagement without their
// top-comment slot, and _ENGAGEMENT_LOG_REFERENCE, lib/signals.py:197-454; trend-opportunity-radar
// engagement-weight-registry.json: weights hold within a platform only). YouTube's counts run on a view scale from a
// floor (_VIEW_ENGAGEMENT_SOURCES). A source not listed has no score: the web's counts are not readers', and arXiv,
// Techmeme, careers pages and the forums have none (_NO_ENGAGEMENT_SCORE_SOURCES); an app store's are a rating and
// thumbs, which no reference scores.
var engagementWeights = map[string]struct {
	fields    []weighted
	reference float64
	floor     float64
}{
	SourceReddit: {
		fields: []weighted{{"score", 0.50}, {"num_comments", 0.35}, {"upvote_ratio_x10", 0.05}}, reference: 7.6,
	},
	SourceYouTube: {
		fields: []weighted{{"views", 0.45}, {"likes", 0.32}, {"comments", 0.13}}, reference: 11.5, floor: 2.1,
	},
	SourceHackerNews: {fields: []weighted{{"points", 0.55}, {"comments", 0.45}}, reference: 6.2},
	SourceGitHub:     {fields: []weighted{{"reactions", 0.60}, {"comments", 0.40}}, reference: 9.2},
	SourceDevTo:      {fields: []weighted{{"reactions", 0.60}, {"comments", 0.40}}, reference: 3.0},
	SourceBluesky: {
		fields: []weighted{{"likes", 0.40}, {"reposts", 0.30}, {"replies", 0.20}, {"quotes", 0.10}}, reference: 9.2,
	},
}

// commentReference is the log1p of a very high comment's votes on each platform: a comment is scored on it alone
// (last30days _VOTE_LOG_REFERENCE and normalized_comment_vote, lib/signals.py:141-166).
var commentReference = map[string]float64{
	SourceReddit: 7.6, SourceHackerNews: 6.2, SourceDevTo: 3.0, SourceYouTube: 10.3, SourceBluesky: 9.2,
}

// sourceQualities are last30days' (signals.SOURCE_QUALITY, lib/signals.py:14-41); a source not listed is 0.6, its
// default (the web and Reddit, the app stores, GitHub, careers pages and the forums).
var sourceQualities = map[string]float64{
	SourceHackerNews: 0.8, SourceDevTo: 0.78, SourceYouTube: 0.85, SourceArxiv: 0.9, SourceTechmeme: 0.85,
	SourceBluesky: 0.66, SourceCareers: 0.72,
}

func quality(source string) float64 {
	if q, ok := sourceQualities[source]; ok {
		return q
	}

	return sourceQuality
}

type weighted struct {
	field  string
	weight float64
}

// EngagementScore is a source's counts on a 0-100 log scale of that source: a 250-upvote Reddit thread near 50
// (last30days signals.engagement_score); false for a source without one or an item without counts.
func EngagementScore(source string, engagement map[string]float64) (int, bool) {
	spec, ok := engagementWeights[source]
	if !ok || len(engagement) == 0 {
		return 0, false
	}

	raw, counted := 0.0, false

	for _, f := range spec.fields {
		v := engagement[f.field]
		if f.field == "upvote_ratio_x10" {
			// The ratio counts on a linear scale of 0 to 10, not a log one (last30days: 0.05 * ratio * 10).
			if r, ok := engagement["upvote_ratio"]; ok && r > 0 {
				raw += f.weight * r * 10
				counted = true
			}

			continue
		}

		if v > 0 {
			raw += f.weight * math.Log1p(v)
			counted = true
		}
	}

	if !counted {
		return 0, false
	}

	return int(math.Round(100 * math.Max(0, math.Min(1, (raw-spec.floor)/(spec.reference-spec.floor))))), true
}

// CommentScore is a comment's votes on a 0-100 log scale of its platform; false for a platform without one or a
// comment without votes (last30days normalized_comment_vote).
func CommentScore(source string, engagement map[string]float64) (int, bool) {
	ref, ok := commentReference[source]
	if !ok {
		return 0, false
	}

	votes := 0.0
	for _, field := range []string{"likes", "points", "score"} {
		votes = max(votes, engagement[field])
	}

	if votes <= 0 {
		return 0, false
	}

	return int(math.Round(100 * math.Min(1, math.Log1p(votes)/ref))), true
}

// freshness is an item's recency, 10 to 90: 100 today, 0 at the window's start, then 0.8x + 10 (last30days
// dates.recency_score and signals.freshness, balanced_recent); 10 for an item with no date.
func freshness(published *time.Time, to time.Time, windowDays int) int {
	if published == nil {
		return 10
	}

	age := to.Sub(*published).Hours() / 24
	recency := 100.0

	switch {
	case age >= float64(windowDays):
		recency = 0
	case age > 0:
		recency = 100 * (1 - age/float64(windowDays))
	}

	return int(recency*0.8 + 10)
}

// localRank orders a stream: 0.65 relevance, 0.25 freshness, 0.10 engagement (last30days signals.annotate_stream).
func localRank(it Item) float64 {
	eng := 0
	if it.EngagementScore != nil {
		eng = *it.EngagementScore
	}

	return 0.65*it.relevance + 0.25*float64(it.freshness)/100 + 0.10*float64(eng)/100
}

// finalScore is a fused item's score: last30days rerank._final_score with the local score in place of a model's
// (rerank._fallback_tuple: relevance 0.7, quality 0.1), the fused rank, freshness and quality, plus engagement at
// 0.15 (rerank.ENGAGEMENT_FINAL_WEIGHT).
func finalScore(it Item) float64 {
	q := quality(it.Source)
	rerank := math.Max(0, math.Min(100, it.relevance*100*0.7+q*100*0.1))
	rrf := math.Max(0, math.Min(100, it.rrf/rrfCeiling*100))
	known := 0.60*rerank + 0.20*rrf + 0.10*float64(it.freshness) + 0.05*q*100

	eng := 0.0
	if it.EngagementScore != nil {
		eng = float64(*it.EngagementScore)
	}

	return known + 0.15*eng
}

// rank scores, fuses, deduplicates and orders the streams' items, the best first.
func rank(plan Plan, streams []stream, to time.Time) []Item {
	type candidate struct {
		item    Item
		streams map[string]bool
	}

	var fused []*candidate

	byKey := map[string]*candidate{}

	for _, st := range streams {
		items := make([]Item, len(st.items))
		for i, it := range st.items {
			it.relevance = relevance(st.subquery.Query, it.Title+" "+it.Text)
			it.freshness = freshness(it.PublishedAt, to, plan.WindowDays)

			score := EngagementScore
			if it.Parent != "" {
				score = CommentScore
			}

			if s, ok := score(it.Source, it.Engagement); ok {
				it.EngagementScore = &s
			}

			items[i] = it
		}

		slices.SortStableFunc(items, func(a, b Item) int { return cmp.Compare(localRank(b), localRank(a)) })
		items = Dedupe(items)

		weight := plan.weight(st.subquery, st.source)
		streamKey := st.subquery.Label + ":" + st.source

		// Weighted RRF: each stream adds weight / (k + rank) to the items it holds, once per stream (last30days
		// weighted_rrf, lib/fusion.py:889-1027).
		for r, it := range items {
			score := weight / float64(rrfK+r+1)

			key := identity(it)
			if key == "" {
				key = it.Source + "\x1f" + it.NativeID
			}

			c, ok := byKey[key]
			if !ok {
				it.rrf = score
				c = &candidate{item: it, streams: map[string]bool{streamKey: true}}
				byKey[key] = c
				fused = append(fused, c)

				continue
			}

			if !c.streams[streamKey] {
				c.streams[streamKey] = true
				c.item.rrf += score
			}

			rrf, rel, fresh, eng := c.item.rrf, max(c.item.relevance, it.relevance), max(c.item.freshness, it.freshness),
				c.item.EngagementScore
			c.item = merge(c.item, it)
			c.item.rrf, c.item.relevance, c.item.freshness = rrf, rel, fresh

			if eng == nil || it.EngagementScore != nil && *it.EngagementScore > *eng {
				eng = it.EngagementScore
			}

			c.item.EngagementScore = eng
		}
	}

	from := to.AddDate(0, 0, -plan.WindowDays)
	out := make([]Item, 0, len(fused))
	for _, c := range fused {
		score := finalScore(c.item)
		if !inDays(c.item.PublishedAt, from, to) {
			score *= outOfWindowMultiplier
		}

		c.item.Score = math.Round(score*10) / 10
		out = append(out, c.item)
	}

	slices.SortStableFunc(out, func(a, b Item) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}

		return cmp.Compare(engagementOf(b), engagementOf(a))
	})

	return capAuthors(Dedupe(out))
}

func engagementOf(it Item) int {
	if it.EngagementScore == nil {
		return -1
	}

	return *it.EngagementScore
}

// capAuthors keeps at most maxPerAuthor items of one author on one source, the best first.
func capAuthors(items []Item) []Item {
	count := map[string]int{}

	var out []Item

	for _, it := range items {
		if it.Author != "" {
			key := it.Source + "\x1f" + it.Author
			if count[key] >= maxPerAuthor {
				continue
			}

			count[key]++
		}

		out = append(out, it)
	}

	return out
}
