package listening_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The owner's additions to issue #1306 (2026-11-28): the full read dates each item by its own page, the cluster
// audit asks for a direct source, a judgment not given is not cached, and the model's answers are checked against
// their schema, code owning the numbers.

// pages is a web fetch that answers each address's page, with the date its metadata gives.
type pages map[string]websearch.Page

func (p pages) fetch(_ context.Context, address string) (websearch.Page, error) {
	page, ok := p[address]
	if !ok {
		return websearch.Page{}, fmt.Errorf("%w: it answered 404", websearch.ErrUnreadable)
	}

	page.URL = address

	return page, nil
}

func day(daysAgo int) *time.Time {
	at := start.AddDate(0, 0, -daysAgo)

	return &at
}

func webPost(id, title, author string, daysAgo int) listening.Item {
	it := authored(id, title, author, daysAgo)
	if daysAgo < 0 {
		it.PublishedAt = nil
	}

	return it
}

// webSource is a web source of the items, read in full through the web fetch as the real one is.
func webSource(items ...listening.Item) listening.Platform {
	return listening.Platform{
		Name: listening.SourceWeb, Backends: []listening.Backend{(&backend{items: items}).spec("fake", "web")},
		Read: listening.Sources(listening.Endpoints{}, http.DefaultClient)[0].Read,
	}
}

// readsAll answers a read call with a quote for each item, the first line of its text, and a date when asked.
func readsAll(published map[string]string) func(modelCall) (string, error) {
	return func(call modelCall) (string, error) {
		var rows []string

		for id, doc := range documents(call.User) {
			lines := strings.Split(strings.TrimSpace(doc), "\n")
			quote := strings.TrimSpace(lines[len(lines)-1])
			row := fmt.Sprintf(`{"id":%q,"quotes":[%q],"claims":[]`, id, quote)

			for marker, date := range published {
				if strings.Contains(doc, marker) {
					row += fmt.Sprintf(`,"published":{"date":%q,"quote":%q}`, strings.Split(date, "|")[0],
						strings.Split(date, "|")[1])
				}
			}

			rows = append(rows, row+"}")
		}

		return `{"items":[` + strings.Join(rows, ",") + `]}`, nil
	}
}

// Addition row 1: the full read dates each item by its own page. The date its metadata gives replaces a search
// snippet's that disagrees, and one outside the window drops the item, counted as outside_window and gone from its
// cluster; an undated page is dated by the model only when the words it quotes for the date are on the page and hold
// its year and day. The page's own date is applied even when the read's model fails.
func TestThePageDatesTheItemAndDropsItOutsideTheWindow(t *testing.T) {
	t.Parallel()

	items := []listening.Item{
		webPost("https://a.example/old", "VETC ra mắt thẻ dán", "", 2),
		webPost("https://a.example/moved", "VETC đổi giá thẻ", "", 1),
		webPost("https://a.example/said", "VETC mở thêm làn", "", -1),
		webPost("https://a.example/lied", "VETC nâng cấp ứng dụng", "", -1),
	}
	for i := range items {
		items[i].URL = items[i].NativeID
	}

	web := pages{
		"https://a.example/old":   {Content: "VETC ra mắt thẻ dán cho xe con.", Published: new(time.Date(2019, 3, 2, 0, 0, 0, 0, time.UTC))},
		"https://a.example/moved": {Content: "VETC đổi giá thẻ từ tháng này.", Published: day(10)},
		"https://a.example/said":  {Content: "Đăng ngày 22/11/2026\n\nVETC mở thêm làn ở trạm Long Thành."},
		"https://a.example/lied":  {Content: "Cập nhật hôm qua\n\nVETC nâng cấp ứng dụng lên bản mới."},
	}

	read := readsAll(map[string]string{
		"Đăng ngày 22/11/2026": "2026-11-22|Đăng ngày 22/11/2026",
		"Cập nhật hôm qua":     "2026-11-20|Cập nhật hôm qua",
	})

	p := plan([]string{listening.SourceWeb}, "VETC")
	p.Topic = "VETC"

	m := &fakeModel{answer: steps(nil, nil, read)}
	a, err := withModel(newClock(), listening.ModelOff, "", webSource(items...)).
		Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
	require.NoError(t, err)

	require.Equal(t, listening.StepModel, a.Pass.Read.State, a.Pass.Read.Reason)
	assert.Equal(t, 1, a.Pass.Read.OutsideWindow)
	assert.Contains(t, a.Pass.Read.Detail, "dropped: its page dates it 2019-03-02, outside the window")
	require.Len(t, a.Items, 3)

	for _, it := range a.Items {
		assert.NotEqual(t, "VETC ra mắt thẻ dán", it.Title)
		assert.True(t, it.Direct, "read in full: %s", it.Title)
	}

	for _, cl := range a.Clusters {

		for _, id := range cl.Items {
			found := false
			for _, it := range a.Items {
				found = found || it.ID == id
			}

			assert.True(t, found, "cluster %s names %s, which is not answered", cl.ID, id)
		}
	}

	moved := itemByTitle(t, a, "VETC đổi giá thẻ")
	require.NotNil(t, moved.PublishedAt)
	assert.Equal(t, day(10).Format(time.DateOnly), moved.PublishedAt.Format(time.DateOnly), "the page's date wins")
	assert.Equal(t, "high", moved.DateConfidence)

	said := itemByTitle(t, a, "VETC mở thêm làn")
	require.NotNil(t, said.PublishedAt, "the model's date, its words on the page")
	assert.Equal(t, "2026-11-22", said.PublishedAt.Format(time.DateOnly))

	assert.Nil(t, itemByTitle(t, a, "VETC nâng cấp ứng dụng").PublishedAt,
		"a date whose words do not hold it is not taken")

	// With the read's model failing, the pages' own dates still stand.
	failing := &fakeModel{answer: steps(nil, nil, nil)}
	a, err = withModel(newClock(), listening.ModelOff, "", webSource(items...)).
		Search(t.Context(), p, listening.Caller{Model: failing.Model, Fetch: web.fetch})
	require.NoError(t, err)
	assert.Equal(t, listening.StepFallback, a.Pass.Read.State)
	assert.Equal(t, 1, a.Pass.Read.OutsideWindow)
	assert.Len(t, a.Items, 3)
	assert.Equal(t, day(10).Format(time.DateOnly), itemByTitle(t, a, "VETC đổi giá thẻ").PublishedAt.Format(time.DateOnly))
}

// Addition row 1, Reddit: a thread only the web search showed is read from Arctic Shift's archive, its post and its
// date, which replaces the snippet's.
func TestAThreadOnlySearchedIsDatedByTheArchive(t *testing.T) {
	t.Parallel()

	arctic, arcticURL := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/api/posts/ids":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{
				"id": "abc123", "title": "VETC trừ tiền", "selftext": "Bị trừ 2 lần ở trạm Long Thành.",
				"author": "an", "subreddit": "VietNam", "created_utc": day(20).Unix(),
			}}})
		case "/api/comments/tree":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	reddit := listening.Sources(listening.Endpoints{ArcticURL: arcticURL}, http.DefaultClient)[1]
	thread := authored("abc123", "VETC trừ tiền", "", 1)

	m := &fakeModel{answer: steps(nil, nil, readsAll(nil))}
	p := plan([]string{listening.SourceReddit}, "VETC")

	a, err := withModel(newClock(), listening.ModelOff, "", listening.Platform{
		Name: listening.SourceReddit, Read: reddit.Read,
		Backends: []listening.Backend{(&backend{items: []listening.Item{thread}}).spec("fake", "r")},
	}).Search(t.Context(), p, listening.Caller{Model: m.Model})
	require.NoError(t, err)

	require.Len(t, a.Items, 1)
	assert.Equal(t, day(20).Format(time.DateOnly), a.Items[0].PublishedAt.Format(time.DateOnly))
	assert.True(t, a.Items[0].Direct)
	assert.Contains(t, documents(m.Calls("read")[0].User)[a.Items[0].ID], "Bị trừ 2 lần ở trạm Long Thành.")
	assert.Len(t, arctic.Requests(), 2, "the post, then its comments")
}

// Addition row 2: a reviewed cluster needs a direct source; search cards alone fail it, and the audit is made again
// once the full read has read them, which makes them direct.
func TestTheAuditAsksForADirectSourceAndIsMadeAgainAfterTheRead(t *testing.T) {
	t.Parallel()

	items := []listening.Item{
		webPost("https://b.example/1", "VETC trừ phí hai lần ở trạm", "Lan", 1),
		webPost("https://b.example/2", "Tài khoản VETC bị tính phí gấp đôi", "Minh", 2),
		webPost("https://b.example/3", "VETC thu hai lượt phí một chuyến", "Hoa", 3),
	}
	web := pages{}

	for i := range items {
		items[i].URL = items[i].NativeID
		web[items[i].URL] = websearch.Page{Content: items[i].Title + "\n\n" + items[i].Title}
	}

	oneCluster := func(call modelCall) (string, error) {
		var ids []string
		for _, c := range candidates(call.User) {
			ids = append(ids, c.ID)
		}

		return assign("Bị trừ phí hai lần", ids, "core", "same story"), nil
	}

	p := plan([]string{listening.SourceWeb}, "VETC")
	p.Topic = "VETC"

	cards := &fakeModel{answer: steps(scoresBy(nil), oneCluster, nil)}
	a, err := withModel(newClock(), "", listening.ModelOff, webSource(items...)).
		Search(t.Context(), p, listening.Caller{Model: cards.Model, Fetch: web.fetch})
	require.NoError(t, err)
	require.Len(t, a.Clusters, 1)
	assert.Equal(t, listening.AuditFailed, a.Clusters[0].Audit.Status)
	assert.Equal(t, []string{"direct_source"}, a.Clusters[0].Audit.Failed, "search cards are not direct")

	read := &fakeModel{answer: steps(scoresBy(nil), oneCluster, readsAll(nil))}
	a, err = withModel(newClock(), "", "", webSource(items...)).
		Search(t.Context(), p, listening.Caller{Model: read.Model, Fetch: web.fetch})
	require.NoError(t, err)
	require.Len(t, a.Clusters, 1)
	assert.Equal(t, "Bị trừ phí hai lần", a.Clusters[0].Title)
	assert.Equal(t, &listening.Audit{Status: listening.AuditPassed, Members: 3, Authors: 3, Direct: 3, Core: 3},
		a.Clusters[0].Audit, "the read made them direct")
}

// Addition row 3: a judgment the model did not give falls back and is not cached, so the next search asks for it
// again (OpinionLens report_sentiment.py:1-14); those it gave are kept by a hash of their text.
func TestAJudgmentNotGivenIsNotKept(t *testing.T) {
	t.Parallel()

	items := []listening.Item{post("a", "VETC một", 1), post("b", "VETC hai", 2)}
	onlyFirst := func(call modelCall) (string, error) {
		var rows []string

		for id, doc := range documents(call.User) {
			if strings.Contains(doc, "VETC một") {
				rows = append(rows, fmt.Sprintf(`{"id":%q,"quotes":["VETC một"],"claims":[]}`, id))
			}
		}

		return `{"items":[` + strings.Join(rows, ",") + `]}`, nil
	}
	scoreFirst := func(call modelCall) (string, error) {
		for _, c := range candidates(call.User) {
			if c.Title == "VETC một" {
				return fmt.Sprintf(`{"scores":[{"id":%q,"relevance":80,"reason":"x"}]}`, c.ID), nil
			}
		}

		return `{"scores":[]}`, nil
	}

	m := &fakeModel{answer: steps(scoreFirst, keepClusters, onlyFirst)}
	s := withModel(newClock(), "", "", forum("forum", items...))
	p := plan([]string{"forum"}, "VETC")

	_, err := s.Search(t.Context(), p, listening.Caller{Model: m.Model})
	require.NoError(t, err)

	again, err := s.Search(t.Context(), p, listening.Caller{Model: m.Model})
	require.NoError(t, err)

	require.Len(t, m.Calls("rerank"), 2)
	assert.Equal(t, []string{"VETC hai"}, titlesOf(candidates(m.Calls("rerank")[1].User)), "only the unscored again")
	assert.Equal(t, 1, again.Pass.Rerank.Cached)

	require.Len(t, m.Calls("read"), 2)
	assert.Len(t, documents(m.Calls("read")[1].User), 1, "only the unread again")
	assert.Equal(t, 1, again.Pass.Read.Cached)
}

func titlesOf(cs []candidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Title)
	}

	return out
}

// Addition row 4: the model's answers are checked against their schema, and one that does not fit is refused whole,
// the step falling back with the reason (OpinionLens schema_validator.py, report_manager.py:15-17); code owns the
// numbers, so a claim with a number its post does not have is dropped, and a cluster title with one is refused
// (OpinionLens narrator.py:9-11).
func TestAnswersAreCheckedAgainstTheirSchemaAndCodeOwnsTheNumbers(t *testing.T) {
	t.Parallel()

	items := []listening.Item{post("a", "VETC hẹn 7 ngày mới hoàn tiền", 1), post("b", "VETC trừ tiền hai lần", 2)}
	p := plan([]string{"forum"}, "VETC")

	for name, tc := range map[string]struct {
		rerank, read func(modelCall) (string, error)
		step         func(listening.Pass) listening.Step
		reason       string
	}{
		"relevance as text": {
			rerank: func(call modelCall) (string, error) {
				return fmt.Sprintf(`{"scores":[{"id":%q,"relevance":"high"}]}`, candidates(call.User)[0].ID), nil
			},
			step: func(p listening.Pass) listening.Step { return p.Rerank }, reason: "not the JSON asked for",
		},
		"relevance out of range": {
			rerank: func(call modelCall) (string, error) {
				return fmt.Sprintf(`{"scores":[{"id":%q,"relevance":150}]}`, candidates(call.User)[0].ID), nil
			},
			step: func(p listening.Pass) listening.Step { return p.Rerank }, reason: "is not from 0 to 100",
		},
		"no relevance": {
			rerank: func(call modelCall) (string, error) {
				return fmt.Sprintf(`{"scores":[{"id":%q,"reason":"x"}]}`, candidates(call.User)[0].ID), nil
			},
			step: func(p listening.Pass) listening.Step { return p.Rerank }, reason: "has no relevance",
		},
		"a read item without id": {
			rerank: scoresBy(nil),
			read:   func(modelCall) (string, error) { return `{"items":[{"quotes":["VETC"]}]}`, nil },
			step:   func(p listening.Pass) listening.Step { return p.Read }, reason: "has no id",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m := &fakeModel{answer: steps(tc.rerank, keepClusters, tc.read)}
			a, err := withModel(newClock(), "", "", forum("forum", items...)).
				Search(t.Context(), p, listening.Caller{Model: m.Model})
			require.NoError(t, err)

			st := tc.step(a.Pass)
			assert.Equal(t, listening.StepFallback, st.State)
			assert.Contains(t, st.Reason, tc.reason)

			if st == a.Pass.Rerank {
				for _, it := range a.Items {
					assert.Equal(t, listening.ProvenanceFallback, it.Provenance, "refused whole")
				}
			}
		})
	}

	numbers := &fakeModel{answer: steps(scoresBy(nil), keepClusters, func(call modelCall) (string, error) {
		var rows []string
		for id := range documents(call.User) {
			rows = append(rows, fmt.Sprintf(`{"id":%q,"quotes":["VETC"],`+
				`"claims":["Phải chờ 7 ngày mới được hoàn tiền","Phải chờ 15 ngày mới được hoàn tiền"]}`, id))
		}

		return `{"items":[` + strings.Join(rows, ",") + `]}`, nil
	})}

	a, err := withModel(newClock(), "", "", forum("forum", items...)).
		Search(t.Context(), p, listening.Caller{Model: numbers.Model})
	require.NoError(t, err)
	assert.Equal(t, []string{"Phải chờ 7 ngày mới được hoàn tiền"},
		itemByTitle(t, a, "VETC hẹn 7 ngày mới hoàn tiền").Claims, "15 is not the post's number")
	assert.Empty(t, itemByTitle(t, a, "VETC trừ tiền hai lần").Claims, "neither number is this post's")

	titled := &fakeModel{answer: steps(scoresBy(nil), func(call modelCall) (string, error) {
		var ids []string
		for _, c := range candidates(call.User) {
			ids = append(ids, c.ID)
		}

		return assign("Trừ phí 3 lần", ids, "core", "same"), nil
	}, nil)}

	a, err = withModel(newClock(), "", listening.ModelOff, forum("forum", items...)).
		Search(t.Context(), p, listening.Caller{Model: titled.Model})
	require.NoError(t, err)
	assert.Equal(t, listening.StepFallback, a.Pass.Clusters.State)
	assert.Contains(t, a.Pass.Clusters.Reason, "has the number 3, which none of its items says")
}
