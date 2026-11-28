package listening

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// The model's review of the clusters (layer 2's second call): trend-opportunity-radar's clustering contract
// (references/clustering-contract.md:29) and its audit (scripts/audit_clusters.py). The model is given the shortlist
// as the code clustered it and answers a cluster plan: it merges two clusters that tell one story in other words,
// splits one that joins two stories, and assigns every item once with its fit and a reason. The code then checks the
// plan: a plan that names an unknown item, names one twice, leaves one out, or gives a fit or reason that is not
// valid, or a title holding a number none of its items says, is refused whole, and the code's clusters stand
// (audit_clusters.py:43-57, 99-101). A cluster of the plan passes its audit with three members or more, two authors
// or more, one direct source or more and two core members or more (audit_clusters.py:58-74; owner, 2026-11-28; its
// task-transition and subject-bridge checks need fields these items do not have); one that fails has its score capped
// at 54, under thin evidence's 55, and keeps its uncapped score (references/scoring-contract.md:32). The audit is
// made again after the full read, which can make an item direct and drop one dated outside the window.

// The fits a member has (audit_clusters.py VALID_FITS).
const (
	FitCore       = "core"
	FitSupporting = "supporting"
	FitCounter    = "counter"
)

// Audit is the code's check of a reviewed cluster.
type Audit struct {
	// Status is passed or failed; Failed names the checks it failed: member_count, author_diversity, direct_source,
	// core_members.
	Status string   `json:"status"`
	Failed []string `json:"failed,omitempty"`
	// Members, Authors, Direct and Core are what was counted.
	Members int `json:"members"`
	Authors int `json:"authors"`
	Direct  int `json:"direct"`
	Core    int `json:"core"`
	// UncappedScore is the cluster's score before a failed audit capped it.
	UncappedScore *float64 `json:"uncapped_score,omitempty"`
}

// The audit's thresholds and cap.
const (
	AuditPassed = "passed"
	AuditFailed = "failed"

	auditMembers = 3
	auditAuthors = 2
	auditDirect  = 1
	auditCore    = 2
	auditCap     = 54
	// maxClusterTitle is the longest title (audit_clusters.py:45).
	maxClusterTitle = 120
)

// clusterSystem is radar's contract, as the model is told it.
func clusterSystem(topic string) string {
	return "Review the clusters of a social listening search about " + topic + `: posts grouped by the story they tell.
Clustering is a semantic review step, not a text-label rewrite. Merge two clusters that tell one story in different ` +
		`words, split a cluster that joins different stories, and assign every item exactly once.
Give each assignment its fit: core (it tells the cluster's story), supporting (it adds to it) or counter (it ` +
		`disagrees with it or tells it otherwise), and a short reason why the item belongs.
Keep questionable members as supporting or counter, or split the cluster. Never split a coherent cluster to fill a ` +
		`display target.
Write every cluster title for the report reader, in the posts' language, at most 120 characters: the members' ` +
		`shared issue, experience or question. Do not reuse a post's headline, a URL or Markdown.

Return JSON only:
{
  "clusters": [
    {
      "title": "title",
      "assignments": [
        {"id": "id", "fit": "core|supporting|counter", "reason": "short reason"}
      ]
    }
  ]
}`
}

// clusterPlan is the model's answer.
type clusterPlan struct {
	Clusters []struct {
		Title       string `json:"title"`
		Assignments []struct {
			ID     string `json:"id"`
			Fit    string `json:"fit"`
			Reason string `json:"reason"`
		} `json:"assignments"`
	} `json:"clusters"`
}

// reviewClusters asks the model to review the code's clusters and answers the reviewed ones; on any failure, the
// code's.
func (s *Service) reviewClusters(ctx context.Context, topic string, items []Item, lexical []Cluster, c Caller,
) ([]Cluster, Step) {
	clusters := lexical

	st := s.step(ctx, "clusters", s.reviewModel, DefaultReviewModel, c, func(model string, t *tally, st *Step) error {
		if len(items) == 0 {
			st.Detail = "nothing to review"

			return nil
		}

		body, err := chatRequest(model, clusterSystem(topic), fenced("Items", candidateBlock(items, true)))
		if err != nil {
			return err
		}

		text, err := ask(ctx, c.Model, body, reviewDeadline, reviewAttempts, t)
		if err != nil {
			return err
		}

		var plan clusterPlan
		if err := jsonObject(text, &plan); err != nil {
			return err
		}

		reviewed, err := applyPlan(plan, items, topic)
		if err != nil {
			return fmt.Errorf("the model's cluster plan was refused, so the code's clusters stand: %w", err)
		}

		st.Detail = fmt.Sprintf("%d clusters reviewed from %d", len(reviewed), len(lexical))
		clusters = reviewed

		return nil
	})

	return clusters, st
}

// applyPlan checks the model's plan against the items and makes its clusters, audited, the best first; an error says
// why the plan is refused.
func applyPlan(plan clusterPlan, items []Item, topic string) ([]Cluster, error) {
	index := map[string]int{}
	for i, it := range items {
		index[it.ID] = i
	}

	if len(plan.Clusters) == 0 {
		return nil, fmt.Errorf("the plan has no cluster")
	}

	type proposed struct {
		title   string
		members []int
	}

	seen := map[int]bool{}
	fits := make([]struct{ fit, reason string }, len(items))
	groups := make([]proposed, 0, len(plan.Clusters))

	for _, pc := range plan.Clusters {
		title := strings.Join(strings.Fields(pc.Title), " ")
		switch {
		case title == "":
			return nil, fmt.Errorf("a cluster has no title")
		case len([]rune(title)) > maxClusterTitle || strings.Contains(title, "**") || strings.Contains(title, "__") ||
			strings.Contains(title, "http://") || strings.Contains(title, "https://"):
			return nil, fmt.Errorf("the title %q is not a reader's label of at most %d characters", clip(title, 60),
				maxClusterTitle)
		case len(pc.Assignments) == 0:
			return nil, fmt.Errorf("the cluster %q has no member", clip(title, 60))
		}

		g := proposed{title: title}

		for _, a := range pc.Assignments {
			i, ok := index[strings.TrimSpace(a.ID)]
			fit := strings.ToLower(strings.TrimSpace(a.Fit))
			reason := strings.TrimSpace(a.Reason)

			switch {
			case !ok:
				return nil, fmt.Errorf("it names an item that is not in the search: %q", clip(a.ID, 40))
			case seen[i]:
				return nil, fmt.Errorf("it assigns the item %s twice", items[i].ID)
			case fit != FitCore && fit != FitSupporting && fit != FitCounter:
				return nil, fmt.Errorf("the item %s's fit %q is not core, supporting or counter", items[i].ID,
					clip(a.Fit, 20))
			case reason == "":
				return nil, fmt.Errorf("the item %s has no reason", items[i].ID)
			}

			seen[i] = true
			fits[i].fit, fits[i].reason = fit, clip(reason, 200)
			g.members = append(g.members, i)
		}

		groups = append(groups, g)
	}

	if len(seen) < len(items) {
		var left []string

		for i, it := range items {
			if !seen[i] {
				left = append(left, it.ID)
			}
		}

		return nil, fmt.Errorf("it leaves out %d items: %s", len(left), strings.Join(left[:min(len(left), 10)], ", "))
	}

	for _, g := range groups {
		if n := foreignNumber(g.title, g.members, items); n != "" {
			return nil, fmt.Errorf("the title %q has the number %s, which none of its items says", clip(g.title, 60), n)
		}
	}

	for i := range items {
		items[i].Fit, items[i].FitReason = fits[i].fit, fits[i].reason
	}

	planned := make([]group, 0, len(groups))
	for _, g := range groups {
		planned = append(planned, group{title: g.title, members: g.members, reviewed: true})
	}

	return finishClusters(planned, items, topic), nil
}

// group is a cluster's members, by their index in the items, and its title; reviewed when the model made it.
type group struct {
	title    string
	members  []int
	reviewed bool
}

// finishClusters makes the groups clusters: a reviewed one with its title and its audit, a failed audit capping its
// score; the code's with its best item's title. They are ordered best first, numbered in that order, and each item
// marked with its cluster.
func finishClusters(groups []group, items []Item, topic string) []Cluster {
	alike := alikeIn(items, topic)
	out := make([]Cluster, 0, len(groups))

	for _, g := range groups {
		slices.Sort(g.members)

		cl := makeCluster("", g.members, items, alike)

		cl.Title = g.title
		if !g.reviewed {
			cl.Title = items[g.members[0]].Title
			if cl.Title == "" {
				cl.Title = clip(items[g.members[0]].Text, 120)
			}
		}

		if g.reviewed {
			cl.Audit = audit(g.members, items)

			if cl.Audit.Status == AuditFailed && cl.Score > auditCap {
				cl.Audit.UncappedScore = new(cl.Score)
				cl.Score = auditCap
			}
		}

		cl.tag()
		out = append(out, cl)
	}

	slices.SortStableFunc(out, func(a, b Cluster) int { return cmp.Compare(b.Score, a.Score) })

	index := map[string]int{}
	for i, it := range items {
		index[it.ID] = i
	}

	for n := range out {
		out[n].ID = fmt.Sprintf("c%d", n+1)

		for _, id := range out[n].Items {
			items[index[id]].Cluster = out[n].ID
		}
	}

	return out
}

// audited says how many reviewed clusters passed their audit, as the pass reports it once the full read has made the
// last audit; empty for the code's clusters. Thin data is reported as thin (last30days passes_discovery_floor,
// lib/rerank.py:106-154: "nothing solid this window" is an outcome).
func audited(clusters []Cluster) string {
	reviewed, passed := 0, 0

	for _, cl := range clusters {
		if cl.Audit != nil {
			reviewed++

			if cl.Audit.Status == AuditPassed {
				passed++
			}
		}
	}

	switch {
	case reviewed == 0:
		return ""
	case passed == 0:
		return "0 passed the audit; nothing solid: no cluster has 3 members, 2 authors, 1 direct source and 2 core " +
			"members"
	default:
		return fmt.Sprintf("%d passed the audit", passed)
	}
}

// regroup makes the clusters again from the items left, as each is marked, after some were dropped or changed: a
// cluster keeps its title and, when the model made it, is audited again.
func regroup(clusters []Cluster, items []Item, topic string) []Cluster {
	byCluster := map[string][]int{}
	for i, it := range items {
		byCluster[it.Cluster] = append(byCluster[it.Cluster], i)
	}

	var groups []group

	for _, cl := range clusters {
		if members := byCluster[cl.ID]; len(members) > 0 {
			groups = append(groups, group{title: cl.Title, members: members, reviewed: cl.Audit != nil})
		}
	}

	return finishClusters(groups, items, topic)
}

// numbers are the runs of digits in a text.
var numbers = regexp.MustCompile(`\d+`)

// foreignNumber is a number of the model's words that none of the items' own text has, or empty: code owns the
// numbers and the model the words (OpinionLens report_pipeline/narrator.py:9-11, every number in the narrative comes
// from the computed stats).
func foreignNumber(words string, members []int, items []Item) string {
	var own strings.Builder
	for _, i := range members {
		own.WriteString(" " + items[i].Title + " " + items[i].Text + " " + items[i].body)
	}

	return numberNotIn(words, own.String())
}

// numberNotIn is a number of words that text does not have as a number of its own, or empty.
func numberNotIn(words, text string) string {
	have := map[string]bool{}
	for _, n := range numbers.FindAllString(norm.NFKC.String(text), -1) {
		have[n] = true
	}

	for _, n := range numbers.FindAllString(norm.NFKC.String(words), -1) {
		if !have[n] {
			return n
		}
	}

	return ""
}

// audit is radar's check of one cluster: members, distinct authors (case folded; an item without one counts none),
// direct sources (radar's detail_captured or direct_post: the source's own record or a post read in full, not a search
// card) and core members.
func audit(members []int, items []Item) *Audit {
	a := &Audit{Members: len(members)}
	authors := map[string]bool{}

	for _, i := range members {
		if author := strings.ToLower(strings.TrimSpace(items[i].Author)); author != "" {
			authors[author] = true
		}

		if items[i].Direct {
			a.Direct++
		}

		if items[i].Fit == FitCore {
			a.Core++
		}
	}

	a.Authors = len(authors)

	if a.Members < auditMembers {
		a.Failed = append(a.Failed, "member_count")
	}

	if a.Authors < auditAuthors {
		a.Failed = append(a.Failed, "author_diversity")
	}

	if a.Direct < auditDirect {
		a.Failed = append(a.Failed, "direct_source")
	}

	if a.Core < auditCore {
		a.Failed = append(a.Failed, "core_members")
	}

	a.Status = AuditPassed
	if len(a.Failed) > 0 {
		a.Status = AuditFailed
	}

	return a
}
