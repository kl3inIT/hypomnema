package listening_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
)

// Design row "Dedup": the same (source, native id) is one item; then the same address, in any of its forms and from
// any source, is one item that says where else it was seen; then near-duplicate text within a source, Jaccard 0.7 or
// more, is one item. Different text from different sources stays.
func TestDuplicatesAreFoldedIntoTheFirst(t *testing.T) {
	t.Parallel()

	items := []listening.Item{
		{Source: "reddit", NativeID: "abc", URL: "https://www.reddit.com/r/VietNam/comments/abc/vetc/", Title: "VETC trừ tiền",
			Engagement: map[string]float64{"score": 10}, Subqueries: []string{"q1"}},
		{Source: "reddit", NativeID: "abc", URL: "https://old.reddit.com/comments/abc", Title: "VETC trừ tiền",
			Engagement: map[string]float64{"score": 25, "num_comments": 4}, Subqueries: []string{"q2"}},
		{Source: "web", NativeID: "https://reddit.com/comments/abc", URL: "https://redd.it/abc", Title: "r/VietNam on Reddit",
			Subqueries: []string{"q1"}},
		{Source: "web", NativeID: "a", URL: "https://www.voz.vn/t/vetc.1/?utm_source=fb#post-2", Title: "Thớt VETC"},
		{Source: "web", NativeID: "b", URL: "https://voz.vn/t/vetc.1", Title: "Thớt VETC, trang 2", Text: "longer text"},
		{Source: "web", NativeID: "c", URL: "https://tinhte.vn/a", Title: "VETC trừ tiền hai lần ở trạm thu phí Long Thành"},
		{Source: "web", NativeID: "d", URL: "https://otofun.net/b", Title: "VETC trừ tiền hai lần ở trạm thu phí Long Thành!"},
		{Source: "reddit", NativeID: "e", URL: "https://www.reddit.com/comments/e/", Title: "VETC trừ tiền hai lần ở trạm thu phí Long Thành"},
	}

	got := listening.Dedupe(items)
	require.Len(t, got, 4)

	thread := got[0]
	assert.Equal(t, "abc", thread.NativeID)
	assert.Equal(t, map[string]float64{"score": 25, "num_comments": 4}, thread.Engagement, "the larger counts are kept")
	assert.Equal(t, []string{"q1", "q2"}, thread.Subqueries)
	assert.Equal(t, []string{"web"}, thread.SeenIn, "the web's copy of the thread folds into it")

	assert.Equal(t, "a", got[1].NativeID)
	assert.Equal(t, "longer text", got[1].Text, "the same page under another address form is one item")

	assert.Equal(t, "c", got[2].NativeID, "a near-duplicate of the same source is folded")
	assert.Equal(t, "e", got[3].NativeID, "the same words on another source stay")
}

func TestAddressesAreNormalized(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"HTTPS://WWW.Example.COM/Path/?b=2&a=1&utm_medium=x#frag":  "https://example.com/Path?a=1&b=2",
		"https://m.facebook.com/groups/1/posts/2/":                 "https://facebook.com/groups/1/posts/2",
		"https://old.reddit.com/r/VietNam/comments/1gzqsxk/title/": "https://reddit.com/comments/1gzqsxk",
		"https://redd.it/1gzqsxk":                                  "https://reddit.com/comments/1gzqsxk",
		"https://www.reddit.com/r/VietNam/":                        "https://reddit.com/r/VietNam",
		"https://www.reddit.com/r/VietNam/s/AbCdEf":                "https://reddit.com/r/VietNam/s/AbCdEf",
	} {
		assert.Equal(t, want, listening.NormalizeURL(in), in)
	}
}

// Design row "Ranking": streams are fused by weighted reciprocal rank: of equally relevant items, one two subqueries
// found outranks one only one found first, and a subquery's weight lifts its items.
func TestStreamsAreFusedByWeightedReciprocalRank(t *testing.T) {
	t.Parallel()

	c := newClock()
	at := start.AddDate(0, 0, -2)
	words := map[string]string{"alone": "long thành", "both": "ứng dụng", "other": "ví điện"}
	item := func(id string) listening.Item {
		return listening.Item{
			NativeID: id, URL: "https://example.com/" + id, Title: "VETC alpha beta " + words[id], PublishedAt: &at,
		}
	}

	byQuery := map[string][]listening.Item{
		"VETC alpha": {item("alone"), item("both")},
		"VETC beta":  {item("other"), item("both")},
	}
	s := service(c, listening.Platform{Name: "forum", Backends: []listening.Backend{{
		Name: "json", Host: "a.example",
		Search: func(_ context.Context, r listening.Request) (listening.Result, error) {
			return listening.Result{Items: byQuery[r.Query]}, nil
		},
	}}})

	ids := func(a listening.Answer) []string {
		out := make([]string, 0, len(a.Items))
		for _, it := range a.Items {
			out = append(out, it.NativeID)
		}

		return out
	}

	a, err := s.Search(t.Context(), plan([]string{"forum"}, "VETC alpha", "VETC beta"), nil)
	require.NoError(t, err)
	base := a
	assert.Equal(t, "both", ids(a)[0], "found by two subqueries")
	assert.Equal(t, []string{"q1", "q2"}, a.Items[0].Subqueries)

	p := plan([]string{"forum"}, "VETC alpha", "VETC beta")
	p.Subqueries[1].Weight = 3
	a, err = s.Search(t.Context(), p, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"both", "other", "alone"}, ids(a))

	p = plan([]string{"forum"}, "VETC alpha", "VETC beta")
	p.SourceWeights = map[string]float64{"forum": 2}
	weighted, err := s.Search(t.Context(), p, nil)
	require.NoError(t, err)
	assert.Greater(t, weighted.Items[0].Score, base.Items[0].Score, "a source's weight lifts its streams")
}

// Design row "Ranking": engagement is normalized per source on a log scale: a 250-upvote Reddit thread near 50, ten
// times the votes far less than ten times the score, and a web page, whose counts are not readers', has none.
func TestEngagementIsOnItsSourcesOwnLogScale(t *testing.T) {
	t.Parallel()

	score := func(source string, eng map[string]float64) int {
		s, ok := listening.EngagementScore(source, eng)
		require.True(t, ok)

		return s
	}

	mid := score("reddit", map[string]float64{"score": 250, "num_comments": 40})
	high := score("reddit", map[string]float64{"score": 2500, "num_comments": 400})
	low := score("reddit", map[string]float64{"score": 3})

	assert.InDelta(t, 55, mid, 10)
	assert.Less(t, high, 2*mid)
	assert.Less(t, low, mid)
	assert.Equal(t, 100, score("reddit", map[string]float64{"score": 1e7, "num_comments": 1e6}))

	_, ok := listening.EngagementScore("web", map[string]float64{"citations": 50})
	assert.False(t, ok)
	_, ok = listening.EngagementScore("reddit", nil)
	assert.False(t, ok)

	// In the ranking, the busier of two equally relevant threads comes first.
	c := newClock()
	at := start.AddDate(0, 0, -2)
	s := service(c, listening.Platform{Name: "reddit", Backends: []listening.Backend{{
		Name: "json", Host: "a.example",
		Search: func(context.Context, listening.Request) (listening.Result, error) {
			return listening.Result{Items: []listening.Item{
				{NativeID: "quiet", URL: "https://www.reddit.com/comments/quiet/", Title: "VETC lỗi app",
					PublishedAt: &at, Engagement: map[string]float64{"score": 2}},
				{NativeID: "busy", URL: "https://www.reddit.com/comments/busy/", Title: "VETC lỗi app hôm nay",
					PublishedAt: &at, Engagement: map[string]float64{"score": 900, "num_comments": 300}},
			}}, nil
		},
	}}})

	a, err := s.Search(t.Context(), plan([]string{"reddit"}, "VETC lỗi app"), nil)
	require.NoError(t, err)
	require.Len(t, a.Items, 2)
	assert.Equal(t, "busy", a.Items[0].NativeID)
	require.NotNil(t, a.Items[0].EngagementScore)
	assert.Greater(t, *a.Items[0].EngagementScore, 70)
}

// The answer lists the best PoolSize items and counts the rest.
func TestTheAnswerListsTheBestPool(t *testing.T) {
	t.Parallel()

	c := newClock()
	at := start.Add(-time.Hour)

	s := service(c, listening.Platform{Name: "forum", Backends: []listening.Backend{{
		Name: "json", Host: "a.example",
		Search: func(_ context.Context, r listening.Request) (listening.Result, error) {
			var items []listening.Item

			for i := range r.Limit {
				sum := sha256.Sum256([]byte(fmt.Sprint(r.Query, i)))
				id := hex.EncodeToString(sum[:8])
				items = append(items, listening.Item{
					NativeID: id, URL: "https://example.com/" + id, Title: id, PublishedAt: &at,
				})
			}

			return listening.Result{Items: items}, nil
		},
	}}})

	p := plan([]string{"forum"}, "one", "two", "three")
	p.MaxResults = 25
	a, err := s.Search(t.Context(), p, nil)
	require.NoError(t, err)
	assert.Len(t, a.Items, listening.PoolSize)
	assert.Equal(t, 3*25-listening.PoolSize, a.Omitted)
	assert.Equal(t, 75, coverageOf(t, a, "forum").Items)
}
