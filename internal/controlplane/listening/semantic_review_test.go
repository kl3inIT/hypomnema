package listening_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Review round 1 of PR #1320: each test is a reviewer's probe.

// changingPages is a web fetch whose pages the test changes between searches.
type changingPages struct {
	mu    sync.Mutex
	pages pages
}

func (c *changingPages) set(address string, page websearch.Page) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.pages[address] = page
}

func (c *changingPages) fetch(ctx context.Context, address string) (websearch.Page, error) {
	c.mu.Lock()
	p := c.pages
	c.mu.Unlock()

	return p.fetch(ctx, address)
}

// readWith answers a read call with what the test gives each item, by a phrase of its text.
func readWith(rows map[string]string) func(modelCall) (string, error) {
	return func(call modelCall) (string, error) {
		var out []string

		for id, doc := range documents(call.User) {
			for marker, row := range rows {
				if strings.Contains(doc, marker) {
					out = append(out, fmt.Sprintf(`{"id":%q,%s}`, id, row))
				}
			}
		}

		return `{"items":[` + strings.Join(out, ",") + `]}`, nil
	}
}

// Finding 1: a model's date is taken only when it is the date the quoted words give, day, month and year; "22/11/2026"
// read as 2026-01-22 does not date, nor drop, the item.
func TestAModelDateMustBeTheDateItsWordsGive(t *testing.T) {
	t.Parallel()

	// Since #1319 an undated web item from an unbounded provider is dropped at the search: this one has the
	// search snippet's date, 2026-11-26, and no metadata date on its page.
	it := webPost("https://c.example/lane", "VETC mở thêm làn", "", 1)
	it.URL = it.NativeID
	web := pages{it.URL: {Content: "Đăng ngày 22/11/2026\n\nVETC mở thêm làn ở trạm Long Thành."}}
	p := plan([]string{listening.SourceWeb}, "VETC")

	for _, tc := range []struct {
		date, quote string
		want        string
	}{
		{"2026-01-22", "Đăng ngày 22/11/2026", "2026-11-26"},
		{"2026-11-22", "Đăng ngày 22/11/2026", "2026-11-22"},
		{"2026-11-21", "Đăng ngày 22/11/2026", "2026-11-26"},
	} {
		m := &fakeModel{answer: steps(nil, nil, readWith(map[string]string{
			"VETC mở thêm làn": fmt.Sprintf(`"quotes":["VETC mở thêm làn"],"claims":[],`+
				`"published":{"date":%q,"quote":%q}`, tc.date, tc.quote),
		}))}

		a, err := withModel(newClock(), listening.ModelOff, "", webSource(it)).
			Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
		require.NoError(t, err)
		require.Len(t, a.Items, 1, tc.date)
		assert.Zero(t, a.Pass.Read.OutsideWindow, tc.date)

		got := ""
		if a.Items[0].PublishedAt != nil {
			got = a.Items[0].PublishedAt.Format(time.DateOnly)
		}

		assert.Equal(t, tc.want, got, "the model said %s for %q", tc.date, tc.quote)
	}

	for date, quote := range map[string]string{
		"2026-11-22": "ngày 22 tháng 11 năm 2026",
		"2026-11-05": "2026-11-05 08:30",
	} {
		page := websearch.Page{Content: quote + "\n\nVETC mở thêm làn ở trạm Long Thành."}
		m := &fakeModel{answer: steps(nil, nil, readWith(map[string]string{
			"VETC mở thêm làn": fmt.Sprintf(`"quotes":["VETC mở thêm làn"],"claims":[],`+
				`"published":{"date":%q,"quote":%q}`, date, quote),
		}))}

		a, err := withModel(newClock(), listening.ModelOff, "", webSource(it)).
			Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: pages{it.URL: page}.fetch})
		require.NoError(t, err)
		require.NotNil(t, a.Items[0].PublishedAt, quote)
		assert.Equal(t, date, a.Items[0].PublishedAt.Format(time.DateOnly), quote)
	}
}

// Finding 2: when Arctic Shift refuses the post (429), the thread was not read: a search card stays one, not direct,
// with no quote checked against its snippet, and a cluster of such cards fails its direct-source audit however many
// authors it has.
func TestAThreadNotReadStaysASearchCard(t *testing.T) {
	t.Parallel()

	_, arcticURL := startSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	reddit := listening.Sources(listening.Endpoints{ArcticURL: arcticURL}, http.DefaultClient)[1]
	threads := []listening.Item{
		authored("aaa111", "VETC trừ tiền hai lần ở trạm", "u/an", 1),
		authored("bbb222", "VETC trừ phí gấp đôi một chuyến", "u/binh", 2),
		authored("ccc333", "VETC thu hai lượt phí cùng trạm", "u/chi", 3),
	}

	for i := range threads {
		threads[i].Text = "Snippet: " + threads[i].Title
	}

	oneCluster := func(call modelCall) (string, error) {
		var ids []string
		for _, c := range candidates(call.User) {
			ids = append(ids, c.ID)
		}

		return assign("Bị trừ phí hai lần", ids, "core", "same story"), nil
	}

	m := &fakeModel{answer: steps(scoresBy(nil), oneCluster, readsAll(nil))}
	a, err := withModel(newClock(), "", "", listening.Platform{
		Name: listening.SourceReddit, Read: reddit.Read,
		Backends: []listening.Backend{(&backend{items: threads}).spec("fake", "r")},
	}).Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC"), listening.Caller{Model: m.Model})
	require.NoError(t, err)

	for _, it := range a.Items {
		assert.False(t, it.Direct, "%s was not read", it.Title)
		assert.Empty(t, it.Quotes, "no quote is checked against a snippet")
	}

	assert.Empty(t, m.Calls("read"), "nothing was read, so the model is not asked")
	assert.Equal(t, listening.StepFallback, a.Pass.Read.State)
	require.Len(t, a.Clusters, 1)
	assert.Equal(t, listening.AuditFailed, a.Clusters[0].Audit.Status)
	assert.Zero(t, a.Clusters[0].Audit.Direct)
	assert.Contains(t, a.Clusters[0].Audit.Failed, "direct_source")
}

// Finding 3: the reading is kept by the text the model was given; a page whose text and date changed since is read
// again, and its new date drops it.
func TestAReadingIsKeptByTheTextTheModelWasGiven(t *testing.T) {
	t.Parallel()

	it := webPost("https://d.example/price", "VETC đổi giá thẻ", "", 1)
	it.URL = it.NativeID
	web := &changingPages{pages: pages{it.URL: {Content: "VETC đổi giá thẻ từ tháng này."}}}

	m := &fakeModel{answer: steps(nil, nil, readsAll(nil))}
	s := withModel(newClock(), listening.ModelOff, "", webSource(it))
	p := plan([]string{listening.SourceWeb}, "VETC")

	first, err := s.Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
	require.NoError(t, err)
	require.Len(t, first.Items, 1)

	again, err := s.Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
	require.NoError(t, err)
	assert.Equal(t, 1, again.Pass.Read.Cached, "the same text is not read twice")
	assert.Len(t, m.Calls("read"), 1)

	web.set(it.URL, websearch.Page{
		Content:   "Bài cũ: VETC giữ giá thẻ.",
		Published: new(time.Date(2019, 3, 2, 0, 0, 0, 0, time.UTC)),
	})

	changed, err := s.Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
	require.NoError(t, err)
	assert.Zero(t, changed.Pass.Read.Cached, "a changed page is not answered from the cache")
	assert.Len(t, m.Calls("read"), 2)
	assert.Equal(t, 1, changed.Pass.Read.OutsideWindow, "its page now dates it 2019")
	assert.Empty(t, changed.Items)
}

// Finding 4: numbers are compared whole, so "1,5 triệu" needs 1,5 in the post, not a 1 and a 5; and a reviewed
// cluster's title is checked again on the members left once the full read dropped some.
func TestNumbersAreComparedWholeAndTitlesCheckedAgain(t *testing.T) {
	t.Parallel()

	items := []listening.Item{
		post("a", "Ngày 1 tháng 5 VETC trừ tiền", 1),
		post("b", "VETC trừ tiền lần nữa", 2),
	}
	p := plan([]string{"forum"}, "VETC")

	claims := &fakeModel{answer: steps(scoresBy(nil), keepClusters, readWith(map[string]string{
		"Ngày 1 tháng 5": `"quotes":["VETC trừ tiền"],"claims":["Được hoàn 1,5 triệu đồng","Trừ tiền ngày 1 tháng 5"]`,
	}))}

	a, err := withModel(newClock(), "", "", forum("forum", items...)).
		Search(t.Context(), p, listening.Caller{Model: claims.Model})
	require.NoError(t, err)
	assert.Equal(t, []string{"Trừ tiền ngày 1 tháng 5"}, itemByTitle(t, a, "Ngày 1 tháng 5 VETC trừ tiền").Claims)

	titled := &fakeModel{answer: steps(scoresBy(nil), func(call modelCall) (string, error) {
		var ids []string
		for _, c := range candidates(call.User) {
			ids = append(ids, c.ID)
		}

		return assign("Hoàn 1,5 triệu", ids, "core", "same"), nil
	}, nil)}

	a, err = withModel(newClock(), "", listening.ModelOff, forum("forum", items...)).
		Search(t.Context(), p, listening.Caller{Model: titled.Model})
	require.NoError(t, err)
	assert.Equal(t, listening.StepFallback, a.Pass.Clusters.State)
	assert.Contains(t, a.Pass.Clusters.Reason, "has the number 1,5")

	// The only member saying 99 is dropped by its page's date: the title "99 ngày" no longer holds.
	web := []listening.Item{
		webPost("https://e.example/1", "VETC hoàn tiền chậm 99 ngày", "Lan", 1),
		webPost("https://e.example/2", "VETC hoàn tiền rất chậm", "Minh", 2),
		webPost("https://e.example/3", "VETC chậm hoàn tiền", "Hoa", 3),
	}
	served := pages{}

	for i := range web {
		web[i].URL = web[i].NativeID
		served[web[i].URL] = websearch.Page{Content: web[i].Title + "."}
	}

	served["https://e.example/1"] = websearch.Page{
		Content: "VETC hoàn tiền chậm 99 ngày.", Published: new(time.Date(2019, 3, 2, 0, 0, 0, 0, time.UTC)),
	}

	review := &fakeModel{answer: steps(scoresBy(nil), func(call modelCall) (string, error) {
		var ids []string
		for _, c := range candidates(call.User) {
			ids = append(ids, c.ID)
		}

		return assign("Hoàn tiền chậm 99 ngày", ids, "core", "same"), nil
	}, readsAll(nil))}

	a, err = withModel(newClock(), "", "", webSource(web...)).
		Search(t.Context(), plan([]string{listening.SourceWeb}, "VETC"),
			listening.Caller{Model: review.Model, Fetch: served.fetch})
	require.NoError(t, err)
	require.Equal(t, 1, a.Pass.Read.OutsideWindow)
	require.Len(t, a.Clusters, 1)
	assert.NotContains(t, a.Clusters[0].Title, "99", "no member left says 99")
	assert.Contains(t, a.Pass.Clusters.Detail, "title")
}

// Finding 5: a full-read answer with no valid item is a fallback with its reason and is not kept, and a row with only
// an id is not an answer; the pages' own dates still stand. Two empty arrays are a valid answer: nothing on topic.
func TestAnEmptyReadIsAFallbackAndNotKept(t *testing.T) {
	t.Parallel()

	it := webPost("https://f.example/1", "VETC đổi giá thẻ", "", 1)
	it.URL = it.NativeID
	web := pages{it.URL: {Content: "VETC đổi giá thẻ từ tháng này.", Published: day(10)}}
	p := plan([]string{listening.SourceWeb}, "VETC")

	for name, answer := range map[string]string{
		"no item": `{"items":[]}`,
		"id only": `{"items":[{"id":"%s"}]}`,
	} {
		m := &fakeModel{}
		m.answer = steps(nil, nil, func(call modelCall) (string, error) {
			for id := range documents(call.User) {
				return strings.ReplaceAll(answer, "%s", id), nil
			}

			return answer, nil
		})

		s := withModel(newClock(), listening.ModelOff, "", webSource(it))

		a, err := s.Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
		require.NoError(t, err)
		assert.Equal(t, listening.StepFallback, a.Pass.Read.State, name)
		assert.NotEmpty(t, a.Pass.Read.Reason, name)
		require.Len(t, a.Items, 1)
		assert.Equal(t, day(10).Format(time.DateOnly), a.Items[0].PublishedAt.Format(time.DateOnly),
			"%s: the page's own date stands", name)

		_, err = s.Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
		require.NoError(t, err)
		assert.Len(t, m.Calls("read"), 2, "%s: not kept, asked again", name)
	}

	nothing := &fakeModel{answer: steps(nil, nil, readWith(map[string]string{
		"VETC đổi giá thẻ": `"quotes":[],"claims":[]`,
	}))}
	s := withModel(newClock(), listening.ModelOff, "", webSource(it))

	a, err := s.Search(t.Context(), p, listening.Caller{Model: nothing.Model, Fetch: web.fetch})
	require.NoError(t, err)
	assert.Equal(t, listening.StepModel, a.Pass.Read.State, "two empty arrays: read, nothing on the topic")

	_, err = s.Search(t.Context(), p, listening.Caller{Model: nothing.Model, Fetch: web.fetch})
	require.NoError(t, err)
	assert.Len(t, nothing.Calls("read"), 1, "a valid empty reading is kept")
}

// Follow-up: a row that does not fit the scores' schema is dropped, the others kept (OpinionLens filters sentiment
// rows one by one, report_sentiment.py:300-345; last30days keeps the valid scores, rerank.py:497-538).
func TestABadScoreRowLeavesTheOthers(t *testing.T) {
	t.Parallel()

	items := []listening.Item{post("a", "VETC một", 1), post("b", "VETC hai", 2), post("c", "VETC ba", 3)}
	m := &fakeModel{answer: steps(func(call modelCall) (string, error) {
		cs := candidates(call.User)

		return fmt.Sprintf(`{"scores":[{"id":%q,"relevance":80,"reason":"x"},{"id":%q,"relevance":70},`+
			`{"id":%q,"relevance":"high"}]}`, cs[0].ID, cs[1].ID, cs[2].ID), nil
	}, keepClusters, nil)}

	a, err := withModel(newClock(), "", listening.ModelOff, forum("forum", items...)).
		Search(t.Context(), plan([]string{"forum"}, "VETC"), listening.Caller{Model: m.Model})
	require.NoError(t, err)
	assert.Equal(t, listening.StepModel, a.Pass.Rerank.State)
	assert.Contains(t, a.Pass.Rerank.Detail, "2 of 3")
	assert.Contains(t, a.Pass.Rerank.Detail, "1 row refused")

	llm := 0

	for _, it := range a.Items {
		if it.Provenance == listening.ProvenanceLLM {
			llm++
		}
	}

	assert.Equal(t, 2, llm)
}
