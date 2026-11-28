package listening_test

import (
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

// Review round 2 of PR #1320: each test is a reviewer's probe.

// Finding 1: a date written with its month's name, in English or Vietnamese, is read too, and still compared day,
// month and year: an old page so dated is dropped.
func TestADateWithItsMonthNamedIsRead(t *testing.T) {
	t.Parallel()

	// Dated by the search's snippet, 2026-11-26 (since #1319 an undated web item is dropped at the search).
	it := webPost("https://g.example/old", "VETC ra mắt", "", 1)
	it.URL = it.NativeID
	p := plan([]string{listening.SourceWeb}, "VETC")

	for _, tc := range []struct{ quote, date string }{
		{"Published November 22, 2019", "2019-11-22"},
		{"Published Nov. 22, 2019", "2019-11-22"},
		{"Posted 22 November 2019", "2019-11-22"},
		{"Đăng 22 tháng 11 năm 2019", "2019-11-22"},
	} {
		page := websearch.Page{Content: tc.quote + "\n\nVETC ra mắt thẻ dán cho xe con."}
		m := &fakeModel{answer: steps(nil, nil, readWith(map[string]string{
			"VETC ra mắt": fmt.Sprintf(`"quotes":["VETC ra mắt thẻ dán"],"claims":[],`+
				`"published":{"date":%q,"quote":%q}`, tc.date, tc.quote),
		}))}

		a, err := withModel(newClock(), listening.ModelOff, "", webSource(it)).
			Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: pages{it.URL: page}.fetch})
		require.NoError(t, err)
		assert.Equal(t, 1, a.Pass.Read.OutsideWindow, tc.quote)
		assert.Empty(t, a.Items, tc.quote)
	}

	// The month named must still be the model's month.
	page := websearch.Page{Content: "Published November 22, 2019\n\nVETC ra mắt thẻ dán cho xe con."}
	m := &fakeModel{answer: steps(nil, nil, readWith(map[string]string{
		"VETC ra mắt": `"quotes":["VETC ra mắt thẻ dán"],"claims":[],` +
			`"published":{"date":"2019-10-22","quote":"Published November 22, 2019"}`,
	}))}

	a, err := withModel(newClock(), listening.ModelOff, "", webSource(it)).
		Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: pages{it.URL: page}.fetch})
	require.NoError(t, err)
	assert.Zero(t, a.Pass.Read.OutsideWindow)
	require.Len(t, a.Items, 1)
	assert.Equal(t, "2026-11-26", a.Items[0].PublishedAt.Format(time.DateOnly), "the snippet's date stands")
}

// Finding 2: the reading is kept by everything the model is given for the item, its title included: a search title
// changed over the same page is read again.
func TestAReadingIsKeptByEveryFieldTheModelWasGiven(t *testing.T) {
	t.Parallel()

	b := &backend{items: []listening.Item{webPost("https://h.example/1", "VETC nạp tiền", "", 1)}}
	b.items[0].URL = b.items[0].NativeID
	platform := listening.Platform{
		Name: listening.SourceWeb, Backends: []listening.Backend{b.spec("fake", "web")},
		Read: listening.Sources(listening.Endpoints{}, http.DefaultClient)[0].Read,
	}
	web := pages{b.items[0].URL: {Content: "VETC nạp tiền chậm. VETC hoàn tiền chậm."}}

	m := &fakeModel{answer: steps(nil, nil, readsAll(nil))}
	s := withModel(newClock(), listening.ModelOff, "", platform)
	p := plan([]string{listening.SourceWeb}, "VETC")

	_, err := s.Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
	require.NoError(t, err)

	b.mu.Lock()
	b.items[0].Title = "VETC hoàn tiền"
	b.mu.Unlock()

	a, err := s.Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
	require.NoError(t, err)
	assert.Zero(t, a.Pass.Read.Cached, "the model is given another title, so another text")
	require.Len(t, m.Calls("read"), 2)
	assert.Contains(t, m.Calls("read")[1].User, "title: VETC hoàn tiền")
}

// Finding 3: a read whose newly asked items get no valid answer falls back with its reason for those items, while the
// kept readings of the others stand.
func TestAnEmptyAnswerLeavesTheKeptReadings(t *testing.T) {
	t.Parallel()

	first := webPost("https://i.example/a", "VETC trừ tiền", "", 1)
	first.URL = first.NativeID
	second := webPost("https://i.example/b", "VETC lỗi ứng dụng", "", 2)
	second.URL = second.NativeID
	web := pages{
		first.URL:  {Content: "VETC trừ tiền hai lần."},
		second.URL: {Content: "VETC lỗi ứng dụng sau cập nhật."},
	}

	b := &backend{items: []listening.Item{first}}
	platform := listening.Platform{
		Name: listening.SourceWeb, Backends: []listening.Backend{b.spec("fake", "web")},
		Read: listening.Sources(listening.Endpoints{}, http.DefaultClient)[0].Read,
	}

	empty := false
	m := &fakeModel{}
	m.answer = steps(nil, nil, func(call modelCall) (string, error) {
		if empty {
			return `{"items":[]}`, nil
		}

		return readsAll(nil)(call)
	})

	s := withModel(newClock(), listening.ModelOff, "", platform)
	p := plan([]string{listening.SourceWeb}, "VETC")

	a, err := s.Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
	require.NoError(t, err)
	require.NotEmpty(t, itemByTitle(t, a, "VETC trừ tiền").Quotes)

	b.mu.Lock()
	b.items = []listening.Item{first, second}
	b.mu.Unlock()

	empty = true

	a, err = s.Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: web.fetch})
	require.NoError(t, err)
	assert.Equal(t, 1, a.Pass.Read.Cached)
	assert.Equal(t, listening.StepFallback, a.Pass.Read.State)
	assert.Contains(t, a.Pass.Read.Reason, "no valid reading")
	assert.Equal(t, []string{"VETC trừ tiền hai lần."}, itemByTitle(t, a, "VETC trừ tiền").Quotes,
		"the kept reading stands")
	assert.Empty(t, itemByTitle(t, a, "VETC lỗi ứng dụng").Quotes)
}

// Follow-up: a thread whose post is read only at the full read is checked against that post's own title and body,
// never the search's title.
func TestAThreadReadLateIsCheckedAgainstItsOwnTitle(t *testing.T) {
	t.Parallel()

	_, arcticURL := startSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/api/posts/ids":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{
				"id": "abc123", "title": "VETC hoàn tiền", "selftext": "Đã được hoàn sau 7 ngày.",
				"author": "an", "subreddit": "VietNam", "created_utc": day(3).Unix(),
			}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		}
	})

	reddit := listening.Sources(listening.Endpoints{ArcticURL: arcticURL}, http.DefaultClient)[1]
	thread := authored("abc123", "VETC hoàn 99 triệu", "", 1)

	m := &fakeModel{answer: steps(nil, nil, func(call modelCall) (string, error) {
		var rows []string
		for id := range documents(call.User) {
			rows = append(rows, fmt.Sprintf(`{"id":%q,"quotes":["VETC hoàn 99 triệu","VETC hoàn tiền"],"claims":[]}`, id))
		}

		return `{"items":[` + strings.Join(rows, ",") + `]}`, nil
	})}

	a, err := withModel(newClock(), listening.ModelOff, "", listening.Platform{
		Name: listening.SourceReddit, Read: reddit.Read,
		Backends: []listening.Backend{(&backend{items: []listening.Item{thread}}).spec("fake", "r")},
	}).Search(t.Context(), plan([]string{listening.SourceReddit}, "VETC"), listening.Caller{Model: m.Model})
	require.NoError(t, err)
	require.Len(t, a.Items, 1)
	assert.Equal(t, []string{"VETC hoàn tiền"}, a.Items[0].Quotes, "the search's title is not the post")
	assert.Equal(t, day(3).Format(time.DateOnly), a.Items[0].PublishedAt.Format(time.DateOnly))
}

// Review round 3: a Vietnamese month written in words ("tháng mười một", "tháng Mười Một") is read too, day, month and
// year still compared; and a year is read whole, so "20190" is no 2019.
func TestAVietnameseMonthInWordsIsReadAndAYearWhole(t *testing.T) {
	t.Parallel()

	// Dated by the search's snippet, 2026-11-26 (since #1319 an undated web item is dropped at the search).
	it := webPost("https://j.example/old", "VETC ra mắt", "", 1)
	it.URL = it.NativeID
	p := plan([]string{listening.SourceWeb}, "VETC")

	read := func(quote, date string) listening.Answer {
		t.Helper()

		page := websearch.Page{Content: quote + "\n\nVETC ra mắt thẻ dán cho xe con."}
		m := &fakeModel{answer: steps(nil, nil, readWith(map[string]string{
			"VETC ra mắt": fmt.Sprintf(`"quotes":["VETC ra mắt thẻ dán"],"claims":[],`+
				`"published":{"date":%q,"quote":%q}`, date, quote),
		}))}

		a, err := withModel(newClock(), listening.ModelOff, "", webSource(it)).
			Search(t.Context(), p, listening.Caller{Model: m.Model, Fetch: pages{it.URL: page}.fetch})
		require.NoError(t, err)

		return a
	}

	for _, quote := range []string{
		"Đăng ngày 22 tháng mười một năm 2019",
		"22 tháng Mười Một, 2019 16:18",
		"Thứ sáu, ngày 22 tháng Mười một năm 2019",
	} {
		a := read(quote, "2019-11-22")
		assert.Equal(t, 1, a.Pass.Read.OutsideWindow, quote)
		assert.Empty(t, a.Items, quote)
	}

	for quote, date := range map[string]string{
		"Đăng ngày 22 tháng mười năm 2019":      "2019-11-22",
		"Published November 22, 20190":          "2019-11-22",
		"Đăng ngày 22 tháng mười một năm 20190": "2019-11-22",
	} {
		a := read(quote, date)
		assert.Zero(t, a.Pass.Read.OutsideWindow, quote)
		require.Len(t, a.Items, 1, quote)
		assert.Equal(t, "2026-11-26", a.Items[0].PublishedAt.Format(time.DateOnly), quote)
	}
}
