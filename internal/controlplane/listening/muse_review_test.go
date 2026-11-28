package listening_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
)

// Review round 1 of PR #1325: each test is a reviewer's probe.

func answeredIn(thread, text string) muse.Result {
	r := answeredWith(text)
	r.Thread = thread

	return r
}

func reasons(items []listening.Item) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		out[it.URL] = it.Check
	}

	return out
}

// P1 1: a post counts only with a date Muse gave that parses and lies within a day of the link's or page's, and with
// its quote found in the field it was read from, the caption, as one field: a quote said to be a comment or image text
// cannot be checked on a page that shows the caption only, and a quote stitched from two meta fields is no caption
// (trend-opportunity-radar comment-evidence-contract.md:5).
func TestReviewOneAPostNeedsMusesDateAndItsQuoteInOneCaptionField(t *testing.T) {
	t.Parallel()

	c := newClock()
	p := newPages(t)

	noDate := threadsURL("a.one", codeNov4)
	badDate := threadsURL("b.two", codeNov8)
	comment := threadsURL("c.three", codeNov2)
	stitched := threadsURL("d.four", codeNov4b)
	good := threadsURL("e.five", codeNov14)

	p.post(noDate, noDate, "VETC trừ tiền sai")
	p.post(badDate, badDate, "VETC báo lỗi")
	p.post(comment, comment, "VETC dán thẻ nhanh")
	p.set(stitched, `<html><head><meta property="og:url" content="`+stitched+`"><meta property="og:description" `+
		`content="VETC trừ tiền hai lần"><meta name="description" content="tôi rất bực"></head></html>`)
	p.post(good, good, "VETC hoàn tiền rồi")

	text := block(1, noDate, "@a.one", "@a.one", "", "caption", "VETC trừ tiền sai", "none") +
		block(2, badDate, "@b.two", "@b.two", "early November", "caption", "VETC báo lỗi", "none") +
		block(3, comment, "@c.three", "@c.three", "2026-11-02", "comment", "VETC dán thẻ nhanh", "none") +
		block(4, stitched, "@d.four", "@d.four", "2026-11-04", "caption", "VETC trừ tiền hai lần tôi rất bực",
			"none") +
		block(5, good, "@e.five", "@e.five", "2026-11-14", "caption", "VETC hoàn tiền rồi", "none")
	m := &fakeMuse{answer: func(n int, _ muse.Question) (muse.Result, error) {
		if n == 0 {
			return answeredWith(text), nil
		}

		return answeredWith("NO POSTS FOUND"), nil
	}}

	got, err := museService(c, m, p).Search(t.Context(), plan([]string{listening.SourceThreads}, "VETC"), nil)
	require.NoError(t, err)

	require.Len(t, got.Items, 1, "%+v", got.Unverified)
	assert.Equal(t, good, got.Items[0].URL)
	assert.Len(t, got.Clusters, 1)

	why := reasons(got.Unverified)
	require.Len(t, why, 4)
	assert.Contains(t, why[noDate], "Muse gave no date")
	assert.Contains(t, why[badDate], "Muse gave no date")
	assert.Contains(t, why[comment], "comment")
	assert.Contains(t, why[stitched], "does not hold the quote")

	cov := coverageOf(t, got, listening.SourceThreads)
	assert.Equal(t, 1, cov.Items)
	assert.Equal(t, 4, cov.Unverified)
}

// P1 2: one deadline covers the wait for Muse and the checks of its links, so that a search returns what it checked
// before the tool's seven minutes: questions end early enough for their links to be opened, and no page is opened once
// the search's Muse time is spent (last30days pipeline.py:6908-6916: the supplements end with the run's cutoff).
func TestReviewOneTheSearchReturnsWhatItCheckedBeforeTheToolsLimit(t *testing.T) {
	t.Parallel()

	c := newClock()
	p := newPages(t)
	p.onHit = func() { c.Advance(15 * time.Second) }

	var text string

	codes := []string{codeNov4, codeNov8, codeNov2, codeNov4b, codeNov14}
	for i, code := range codes {
		u := threadsURL("h"+code, code)
		p.post(u, u, "VETC "+code)
		text += block(i+1, u, "@h"+code, "@h"+code, "", "caption", "VETC "+code, "none")
	}

	m := &fakeMuse{clock: c, perAnswer: 5*time.Minute + 59*time.Second,
		answer: func(int, muse.Question) (muse.Result, error) { return answeredWith(text), nil }}

	got, err := museService(c, m, p).Search(t.Context(), plan([]string{listening.SourceThreads}, "VETC"), nil)
	require.NoError(t, err)

	asked := m.questions()
	require.Len(t, asked, 1)
	assert.LessOrEqual(t, asked[0].Deadline, 6*time.Minute-25*time.Second,
		"the question leaves time to open its answer's links")
	assert.Len(t, p.opened(), 1, "no page is opened once the six minutes are spent")
	assert.Less(t, c.Now().Sub(start), 7*time.Minute, "the search answers before the tool's seven minutes")

	notChecked := 0

	for _, it := range got.Unverified {
		if strings.Contains(it.Check, "time ran out") {
			notChecked++
		}
	}

	assert.Equal(t, 4, notChecked, "%+v", got.Unverified)
}

// P1 3: only a usable list, a post of the platform with its quote, or an answer that is only NO POSTS FOUND ends the
// question; a draft block, or a draft that names NO POSTS FOUND, is continued (last30days grok_x.py:400-440 and
// 1223-1234: what was rejected is retried, apart from a clean empty).
func TestReviewOneADraftIsContinuedNotTakenForTheList(t *testing.T) {
	t.Parallel()

	for name, draft := range map[string]string{
		"draft block": "Đang mở các bài trên Threads.\n\npost: 1\nlink: (đang mở)\n",
		"draft naming the empty answer": "I won't send a list from memory or answer NO POSTS FOUND before the " +
			"posts have been opened and checked.",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := newClock()
			p := newPages(t)
			u := threadsURL("coccoc.official", codeNov8)
			p.post(u, u, "Vào web đọc truyện mà quên mở bằng Cốc Cốc 🤡")

			list := block(1, u, "@coccoc.official", "@coccoc.official", "2026-11-08", "caption",
				"Vào web đọc truyện mà quên mở bằng Cốc Cốc 🤡", "75")
			m := &fakeMuse{answer: func(n int, _ muse.Question) (muse.Result, error) {
				switch n {
				case 0:
					return answeredWith(draft), nil
				case 1:
					return answeredWith(list), nil
				default:
					return answeredWith("NO POSTS FOUND"), nil
				}
			}}

			got, err := museService(c, m, p).Search(t.Context(), plan([]string{listening.SourceThreads}, "Cốc Cốc"),
				nil)
			require.NoError(t, err)

			asked := m.questions()
			require.GreaterOrEqual(t, len(asked), 2)
			assert.Equal(t, museThread, asked[1].Thread)
			require.Len(t, got.Items, 1)
			assert.Equal(t, listening.StateOK, coverageOf(t, got, listening.SourceThreads).State)
		})
	}
}

// P1 4: the account asked again for more posts is the one observed, from the link or from the page, an Instagram
// /p/<shortcode>/ link naming none (trend-opportunity-radar instagram-account-adapter.md:21-22,
// run_instagram_opencli_capture.py:55-63: the name read from the page's description).
func TestReviewOneTheAccountForMorePostsIsReadFromThePage(t *testing.T) {
	t.Parallel()

	c := newClock()
	p := newPages(t)
	u := "https://www.instagram.com/p/" + codeNov4 + "/"
	p.set(u, `<html><head><meta property="og:url" content="`+u+`"><meta property="og:description" content="12 likes, `+
		`3 comments - vetc.official on November 4, 2026: &quot;VETC dán thẻ miễn phí&quot;"></head></html>`)

	m := &fakeMuse{answer: func(n int, _ muse.Question) (muse.Result, error) {
		if n == 0 {
			return answeredWith(block(1, u, "VETC", "VETC", "2026-11-04", "caption", "VETC dán thẻ miễn phí", "10")),
				nil
		}

		return answeredWith("NO POSTS FOUND"), nil
	}}

	got, err := museService(c, m, p).Search(t.Context(), plan([]string{listening.SourceInstagram}, "VETC"), nil)
	require.NoError(t, err)

	require.Len(t, got.Items, 1, "%+v", got.Unverified)
	assert.Equal(t, "@vetc.official", got.Items[0].Container)
	assert.Equal(t, map[string]float64{"likes": 12, "comments": 3}, got.Items[0].Engagement)

	asked := m.questions()
	require.Len(t, asked, 2)
	assert.Contains(t, asked[1].Prompt, "@vetc.official")
	assert.Equal(t, museThread, asked[1].Thread)
}

// P1 4: the extra questions come after every stream's first question, so each stream may get them within the budget,
// not only the last (last30days pipeline.py:6908-6916: the supplemental lanes run after the main collection); they go
// to the accounts the whole search saw most (entity_extract, max_handles=3, pipeline.py:6786-6789), each asked in the
// thread of the stream that saw it.
func TestReviewOneEveryStreamGetsItsExtraQuestions(t *testing.T) {
	t.Parallel()

	c := newClock()
	p := newPages(t)

	a1, a2 := threadsURL("a.one", codeNov4), threadsURL("a.one", codeNov8)
	b, d := threadsURL("b.two", codeNov2), threadsURL("c.three", codeNov4b)

	for _, u := range []string{a1, a2, b, d} {
		p.post(u, u, "VETC "+u[len(u)-11:])
	}

	first := block(1, a1, "@a.one", "@a.one", "2026-11-04", "caption", "VETC "+codeNov4, "none") +
		block(2, a2, "@a.one", "@a.one", "2026-11-08", "caption", "VETC "+codeNov8, "none")
	second := block(1, b, "@b.two", "@b.two", "2026-11-02", "caption", "VETC "+codeNov2, "none") +
		block(2, d, "@c.three", "@c.three", "2026-11-04", "caption", "VETC "+codeNov4b, "none")

	const t1, t2 = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"

	m := &fakeMuse{answer: func(n int, q muse.Question) (muse.Result, error) {
		switch {
		case n == 0:
			return answeredIn(t1, first), nil
		case n == 1:
			return answeredIn(t2, second), nil
		default:
			return answeredIn(q.Thread, "NO POSTS FOUND"), nil
		}
	}}

	_, err := museService(c, m, p).Search(t.Context(),
		plan([]string{listening.SourceThreads}, "VETC thu phí", "VETC dán thẻ"), nil)
	require.NoError(t, err)

	asked := m.questions()
	require.Len(t, asked, 5, "two first questions and three extra")
	assert.Equal(t, t1, asked[2].Thread)
	assert.Contains(t, asked[2].Prompt, "@a.one")
	assert.Equal(t, t2, asked[3].Thread)
	assert.Contains(t, asked[3].Prompt, "@b.two")
	assert.Equal(t, t2, asked[4].Thread)
	assert.Contains(t, asked[4].Prompt, "@c.three")
}

// P2: the extra questions are counted for the whole search, three at most, however many streams run at once.
func TestReviewOneExtraQuestionsAreCappedForTheWholeSearch(t *testing.T) {
	t.Parallel()

	c := newClock()
	p := newPages(t)

	threads := ""
	insta := ""

	for i, code := range []string{codeNov4, codeNov8, codeNov2, codeNov4b} {
		h := "t" + string(rune('a'+i))
		threads += block(i+1, threadsURL(h, code), "@"+h, "@"+h, "2026-11-04", "caption", "VETC "+code, "none")
		insta += block(i+1, "https://www.instagram.com/i"+string(rune('a'+i))+"/p/"+code+"/", "x", "x", "2026-11-04",
			"caption", "VETC "+code, "none")
	}

	m := &fakeMuse{answer: func(_ int, q muse.Question) (muse.Result, error) {
		switch {
		case q.Thread != "":
			return answeredIn(q.Thread, "NO POSTS FOUND"), nil
		case strings.Contains(q.Prompt, "Instagram"):
			return answeredIn("33333333-3333-4333-8333-333333333333", insta), nil
		default:
			return answeredIn("44444444-4444-4444-8444-444444444444", threads), nil
		}
	}}

	_, err := museService(c, m, p).Search(t.Context(),
		plan([]string{listening.SourceThreads, listening.SourceInstagram}, "VETC"), nil)
	require.NoError(t, err)

	extra := 0

	for _, q := range m.questions() {
		if q.Thread != "" {
			extra++
		}
	}

	assert.Equal(t, 3, extra)
}

// P2: every link is opened, even one whose shortcode dates it outside the window (issue #1292: open each link).
func TestReviewOneALinkDatedOutsideTheWindowIsStillOpened(t *testing.T) {
	t.Parallel()

	c := newClock()
	p := newPages(t)
	old := threadsURL("someone", codeOld)
	p.post(old, old, "VETC ngày xưa")

	m := &fakeMuse{answer: func(n int, _ muse.Question) (muse.Result, error) {
		if n == 0 {
			return answeredWith(block(1, old, "@someone", "@someone", "2026-11-20", "caption", "VETC ngày xưa", "none")),
				nil
		}

		return answeredWith("NO POSTS FOUND"), nil
	}}

	got, err := museService(c, m, p).Search(t.Context(), plan([]string{listening.SourceThreads}, "VETC"), nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"www.threads.com/@someone/post/" + codeOld}, p.opened())
	require.Len(t, got.Unverified, 1)
	assert.Contains(t, got.Unverified[0].Check, "outside the window")
}
