package listening

import (
	"fmt"
	"slices"
)

// Clusters (last30days cluster.cluster_candidates, lib/cluster.py:61-176): each item, the best first, joins the first
// cluster whose leader it is alike enough, else leads a new one; a cluster shows up to three representatives picked
// for both score and difference (MMR); it is tagged single-source when one source holds it all, thin-evidence when its
// best score is weak (lib/cluster.py:337-346).
const (
	// clusterSimilarity is the similarity, with the topic's words taken out, at which an item joins a leader. It is
	// where the Vietnamese test of cluster_test.go put it: last30days' 0.48 (0.42 for breaking news) joined none of
	// thirteen same-story pairs of short Vietnamese posts, 0.20 joined twelve and no pair of different stories.
	clusterSimilarity = 0.20
	// thinEvidence is the best score under which a cluster's evidence is thin (last30days _cluster_uncertainty: 55).
	thinEvidence = 55
	// representatives a cluster shows, and MMR's weight of score against difference (last30days
	// _mmr_representatives: 3 and 0.75).
	representatives = 3
	mmrLambda       = 0.75
)

// Cluster is a group of items that tell the same story.
type Cluster struct {
	ID string `json:"id"`
	// Title is its best item's title.
	Title string `json:"title"`
	// Items are its items' ids, the best first; Representatives up to three of them, to read first.
	Items           []string `json:"items"`
	Representatives []string `json:"representatives"`
	// Sources are the sources its items came from, or were seen in.
	Sources []string `json:"sources"`
	// Score is its best item's score.
	Score float64 `json:"score"`
	// Uncertainty is "single-source", "thin-evidence" or empty.
	Uncertainty string `json:"uncertainty,omitempty"`
}

// The uncertainty tags (last30days schema.Cluster.uncertainty).
const (
	SingleSource = "single-source"
	ThinEvidence = "thin-evidence"
)

// clusterItems groups items, ordered best first, and marks each with its cluster.
func clusterItems(items []Item, topic string) []Cluster {
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = withoutTopic(it.Title+" "+it.Text, topic)
	}

	alike := func(i, j int) float64 {
		if len(words(texts[i])) < 2 || len(words(texts[j])) < 2 {
			return 0
		}

		return prepare(texts[i]).similarity(prepare(texts[j]))
	}

	var groups [][]int

	for i := range items {
		joined := false

		for g, group := range groups {
			if alike(i, group[0]) >= clusterSimilarity {
				groups[g] = append(group, i)
				joined = true

				break
			}
		}

		if !joined {
			groups = append(groups, []int{i})
		}
	}

	out := make([]Cluster, 0, len(groups))

	for n, group := range groups {
		c := Cluster{ID: fmt.Sprintf("c%d", n+1), Title: items[group[0]].Title, Score: items[group[0]].Score}
		if c.Title == "" {
			c.Title = clip(items[group[0]].Text, 120)
		}

		for _, i := range group {
			items[i].Cluster = c.ID
			c.Items = append(c.Items, items[i].ID)

			for _, src := range append([]string{items[i].Source}, items[i].SeenIn...) {
				if !slices.Contains(c.Sources, src) {
					c.Sources = append(c.Sources, src)
				}
			}
		}

		slices.Sort(c.Sources)
		c.Representatives = mmr(group, items, alike)

		switch {
		case len(c.Sources) == 1:
			c.Uncertainty = SingleSource
		case c.Score < thinEvidence:
			c.Uncertainty = ThinEvidence
		}

		out = append(out, c)
	}

	return out
}

// mmr picks a cluster's representatives: its best item, then each time the one whose score least repeats what is
// picked (last30days _mmr_representatives: lambda * score - (1 - lambda) * similarity * 100).
func mmr(group []int, items []Item, alike func(i, j int) float64) []string {
	picked := []int{group[0]}
	left := slices.Clone(group[1:])

	for len(left) > 0 && len(picked) < representatives {
		best, bestScore := 0, 0.0

		for k, i := range left {
			penalty := 0.0
			for _, p := range picked {
				penalty = max(penalty, alike(i, p))
			}

			score := mmrLambda*items[i].Score - (1-mmrLambda)*penalty*100
			if k == 0 || score > bestScore {
				best, bestScore = k, score
			}
		}

		picked = append(picked, left[best])
		left = slices.Delete(left, best, best+1)
	}

	out := make([]string, 0, len(picked))
	for _, i := range picked {
		out = append(out, items[i].ID)
	}

	return out
}
