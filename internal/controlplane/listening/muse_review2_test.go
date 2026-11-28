package listening_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
)

// Review round 2 of PR #1325: each test is a reviewer's probe.

// P1 1: an Instagram description is "N likes, M comments - name on date: "caption"": only the caption is matched,
// never the account name or the date before it (trend-opportunity-radar run_instagram_opencli_capture.py:55-71).
func TestReviewTwoInstagramMetadataIsNoCaption(t *testing.T) {
	t.Parallel()

	c := newClock()
	p := newPages(t)
	u := "https://www.instagram.com/p/" + codeNov4 + "/"
	p.set(u, `<html><head><meta property="og:url" content="`+u+`"><meta property="og:description" content="12 likes, `+
		`3 comments - vetc.official on November 4, 2026: &quot;Một bài khác hẳn&quot;"></head></html>`)

	m := &fakeMuse{answer: func(n int, _ muse.Question) (muse.Result, error) {
		if n == 0 {
			return answeredWith(block(1, u, "VETC", "VETC", "2026-11-04", "caption",
				"vetc.official on November 4, 2026", "none")), nil
		}

		return answeredWith("NO POSTS FOUND"), nil
	}}

	got, err := museService(c, m, p).Search(t.Context(), plan([]string{listening.SourceInstagram}, "VETC"),
		listening.Caller{})
	require.NoError(t, err)
	assert.Empty(t, got.Items)
	assert.Empty(t, got.Clusters)
	require.Len(t, got.Unverified, 1)
	assert.Contains(t, got.Unverified[0].Check, "does not hold the quote")
}

// P1 2: a list none of whose posts is of the platform never ends the question: Muse is asked again within the
// search's time until a usable list or exactly NO POSTS FOUND comes, and "NO POSTS FOUND." is not that.
func TestReviewTwoWrongListsAndANearEmptyAnswerAreAskedAgain(t *testing.T) {
	t.Parallel()

	u := threadsURL("coccoc.official", codeNov8)
	list := block(1, u, "@coccoc.official", "@coccoc.official", "2026-11-08", "caption",
		"Vào web đọc truyện mà quên mở bằng Cốc Cốc 🤡", "75")
	wrong := block(1, "https://vnexpress.net/vetc-1.html", "VnExpress", "VnExpress", "2026-11-08", "caption",
		"VETC", "none")

	for name, before := range map[string][]string{
		"two wrong-platform lists":     {wrong, wrong},
		"NO POSTS FOUND with a period": {"NO POSTS FOUND."},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := newClock()
			p := newPages(t)
			p.post(u, u, "Vào web đọc truyện mà quên mở bằng Cốc Cốc 🤡")

			m := &fakeMuse{answer: func(n int, _ muse.Question) (muse.Result, error) {
				switch {
				case n < len(before):
					return answeredWith(before[n]), nil
				case n == len(before):
					return answeredWith(list), nil
				default:
					return answeredWith("NO POSTS FOUND"), nil
				}
			}}

			got, err := museService(c, m, p).Search(t.Context(), plan([]string{listening.SourceThreads}, "Cốc Cốc"),
				listening.Caller{})
			require.NoError(t, err)
			assert.GreaterOrEqual(t, len(m.questions()), len(before)+1)
			require.Len(t, got.Items, 1)
			assert.Equal(t, listening.StateOK, coverageOf(t, got, listening.SourceThreads).State)
		})
	}
}

// P1 2: when the search's time runs out on lists of another platform, the source was not read, and the answer says
// why (last30days grok_x.py:1223-1234 keeps the rejection's reason), never a clean zero.
func TestReviewTwoWrongListsUntilTheTimeEndsAreNotAZero(t *testing.T) {
	t.Parallel()

	c := newClock()
	wrong := block(1, "https://vnexpress.net/vetc-1.html", "VnExpress", "VnExpress", "2026-11-08", "caption", "VETC",
		"none")
	m := &fakeMuse{clock: c, perAnswer: 2*time.Minute + 30*time.Second,
		answer: func(int, muse.Question) (muse.Result, error) { return answeredWith(wrong), nil }}

	got, err := museService(c, m, newPages(t)).Search(t.Context(), plan([]string{listening.SourceThreads}, "VETC"),
		listening.Caller{})
	require.NoError(t, err)

	cov := coverageOf(t, got, listening.SourceThreads)
	assert.Equal(t, listening.StateNotRead, cov.State)
	assert.Equal(t, listening.CodeTimeout, cov.Code)
	require.NotEmpty(t, cov.Streams[0].Tried)
	assert.Contains(t, cov.Streams[0].Tried[0].Detail, "none with a link to a Threads post")
}

// P1 3: every Muse call of the extra questions, continuations included, counts toward the three of a search
// (last30days grok_x.py:1353-1368: each handle is asked with attempts=1).
func TestReviewTwoContinuationsOfExtraQuestionsCountTowardTheThree(t *testing.T) {
	t.Parallel()

	c := newClock()
	p := newPages(t)

	var first string

	for i, h := range []string{"a.one", "b.two", "c.three"} {
		code := []string{codeNov4, codeNov8, codeNov2}[i]
		u := threadsURL(h, code)
		p.post(u, u, "VETC "+code)
		first += block(i+1, u, "@"+h, "@"+h, "", "caption", "VETC "+code, "none")
	}

	m := &fakeMuse{answer: func(n int, _ muse.Question) (muse.Result, error) {
		if n == 0 {
			return answeredWith(first), nil
		}

		return answeredWith("I'm still opening those posts; I'll send them once the check returns."), nil
	}}

	_, err := museService(c, m, p).Search(t.Context(), plan([]string{listening.SourceThreads}, "VETC"), listening.Caller{})
	require.NoError(t, err)

	extra := 0

	for _, q := range m.questions() {
		if q.Thread != "" {
			extra++
		}
	}

	assert.Equal(t, 3, extra)
}

// P2: the wait before a continuation ends with the search's Muse time, as the wait to open a page does.
func TestReviewTwoTheContinuationWaitEndsWithTheMuseTime(t *testing.T) {
	t.Parallel()

	c := newClock()
	m := &fakeMuse{clock: c, perAnswer: 2*time.Minute + 32*time.Second,
		answer: func(int, muse.Question) (muse.Result, error) {
			return answeredWith("I'll send the verified list once that check returns."), nil
		}}

	got, err := museService(c, m, newPages(t)).Search(t.Context(), plan([]string{listening.SourceThreads}, "VETC"),
		listening.Caller{})
	require.NoError(t, err)

	assert.Len(t, m.questions(), 2, "the second continuation would wait past the questions' end")
	assert.LessOrEqual(t, c.Now().Sub(start), 6*time.Minute)
	assert.Equal(t, listening.CodeTimeout, coverageOf(t, got, listening.SourceThreads).Code)
}

// P2: a date that does not exist, 2026-11-31, is no date, not 2026-12-01.
func TestReviewTwoAnImpossibleDateIsNoDate(t *testing.T) {
	t.Parallel()

	c := newClock()
	c.Advance(5 * 24 * time.Hour)

	p := newPages(t)
	u := "https://www.facebook.com/groups/tasco.vmap/posts/1234567890123/"
	p.set(u, `<html><head><meta property="og:url" content="`+u+`"><meta property="og:description" `+
		`content="Bản đồ 3D của VMAP đẹp quá"></head><body><time datetime="2026-12-01T09:00:00Z">1 Dec</time>`+
		`</body></html>`)

	m := &fakeMuse{answer: func(n int, _ muse.Question) (muse.Result, error) {
		if n == 0 {
			return answeredWith(block(1, u, "A", "VMAP", "2026-11-31", "caption", "Bản đồ 3D của VMAP đẹp quá", "none")),
				nil
		}

		return answeredWith("NO POSTS FOUND"), nil
	}}

	got, err := museService(c, m, p).Search(t.Context(), plan([]string{listening.SourceFacebook}, "VMAP"),
		listening.Caller{})
	require.NoError(t, err)
	assert.Empty(t, got.Items)
	require.Len(t, got.Unverified, 1)
	assert.Contains(t, got.Unverified[0].Check, "Muse gave no date")
}
