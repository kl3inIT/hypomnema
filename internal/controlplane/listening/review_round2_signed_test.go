package listening_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Review round 2 of PR #1359: each finding's probe, failing before its fix.

func failingWeb(context.Context, string, int) (websearch.Answer, error) {
	return websearch.Answer{}, errors.New("every provider answered 500")
}

// P1-1: the owner signs LinkedIn in again while a search holds LinkedIn's turn and meets a 999: the queued
// verification sends nothing, the stop the search wrote being read once its turn comes.
func TestRound2QueuedLinkedInVerificationRespectsNewStop(t *testing.T) {
	rig := newSignedRig(t)

	var (
		queued  atomic.Bool
		pasted  = make(chan error, 1)
		started = false
	)

	search := &pageFake{}
	search.snapshot = func(address string, _ int) map[string]any {
		if !started {
			started = true
			queued.Store(true)

			go func() {
				pasted <- rig.svc.PasteCookies(context.Background(), listening.SourceLinkedIn, "li_at=session-2", "")
			}()

			// Long enough for the verification to pass its first check and wait for LinkedIn's turn.
			time.Sleep(300 * time.Millisecond)
		}

		return map[string]any{"href": address, "status": 999, "ready": "complete", "posts": []any{}}
	}
	signInLinkedIn(t, rig, search)
	before := visits(rig, "listening-check:linkedin")

	_ = rig.search(t, listening.SourceLinkedIn)
	require.True(t, queued.Load())

	err := <-pasted
	require.ErrorIs(t, err, listening.ErrInvalid)
	assert.Equal(t, before, visits(rig, "listening-check:linkedin"), "the queued verification sends nothing")
	assert.Equal(t, start.Add(24*time.Hour), stateOf(t, rig.svc, listening.SourceLinkedIn).Signed.StoppedUntil)
}

// P1-2: a post shows, then LinkedIn refuses the request a scroll sends for more: the post read is kept, and the read
// is rate limited with its 24-hour stop.
func TestRound2LinkedInPaginationThrottleKeepsStop(t *testing.T) {
	urn := fmt.Sprintf("urn:li:activity:%d", time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC).UnixMilli()<<22)
	refused := browser.Response{URL: "https://www.linkedin.com/flagship-web/rsc-action/actions/pagination", Status: 999}

	for _, c := range []struct {
		name        string
		afterScroll bool
	}{{"refused from the start", false}, {"refused by the scroll", true}} {
		t.Run(c.name, func(t *testing.T) {
			rig := newSignedRig(t)
			page := &pageFake{}
			if !c.afterScroll {
				page.responses = []browser.Response{refused}
			}

			page.snapshot = func(address string, scrolls int) map[string]any {
				if c.afterScroll && scrolls > 0 {
					page.responses = []browser.Response{refused}
				}

				return map[string]any{"href": address, "status": 200, "ready": "complete", "empty": false,
					"posts": []any{map[string]any{"text": "a post about VETC", "urn": urn}}}
			}
			signInLinkedIn(t, rig, page)

			a := rig.search(t, listening.SourceLinkedIn)

			cov := coverageOf(t, a, listening.SourceLinkedIn)
			assert.Equal(t, listening.CodeRateLimited, cov.Streams[0].Code, "read in part, rate limited")
			assert.Equal(t, 1, cov.Streams[0].Items, "the post read before the refusal is kept")
			assert.Equal(t, start.Add(24*time.Hour),
				stateOf(t, rig.svc, listening.SourceLinkedIn).Signed.StoppedUntil)
		})
	}
}

// P1-3: X's session ends while the web fails (one notice); the owner signs in again; the new session ends the same
// way: he hears of it again.
func TestRound2NewSessionExpiryAfterOldFailureNotifies(t *testing.T) {
	rig := newSignedRig(t)
	rig.signInX(t)
	rig.web = failingWeb
	rig.x.status = http.StatusUnauthorized

	_ = rig.search(t, listening.SourceX)
	require.Equal(t, 1, rig.told.Count())

	rig.x.status = 0
	rig.x.tweets = []map[string]any{tweet(snowflake(start.Add(-time.Hour)), "a", "VETC now", 1)}
	require.NoError(t, rig.svc.PasteCookies(t.Context(), listening.SourceX, "auth_token=token-2; ct0=csrf-1", ""))

	rig.x.status = http.StatusUnauthorized
	_ = rig.search(t, listening.SourceX)

	assert.Equal(t, listening.AccountNeedLogin, stateOf(t, rig.svc, listening.SourceX).Signed.Account.State)
	assert.Equal(t, 2, rig.told.Count(), "a new session's end is a new notice")
}

// P2: X's session ends while the web serves (one notice); a later search finds the web failing: the same expired
// session is not told again.
func TestRound2RepeatExpiryNoticeAfterFallbackRecovers(t *testing.T) {
	rig := newSignedRig(t)
	rig.signInX(t)
	rig.x.status = http.StatusUnauthorized

	_ = rig.search(t, listening.SourceX)
	require.Equal(t, 1, rig.told.Count())

	rig.web = failingWeb
	_ = rig.search(t, listening.SourceX)

	assert.Equal(t, 1, rig.told.Count(), "the same expired session, one notice: %+v", rig.told.all)
}

// P2: a snapshot of the same keywords under another date filter, the last search's, is not this one's.
func TestRound2LinkedInIgnoresTheLastDateFacet(t *testing.T) {
	rig := newSignedRig(t)
	urn := fmt.Sprintf("urn:li:activity:%d", time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC).UnixMilli()<<22)
	looks := 0
	page := &pageFake{snapshot: func(address string, _ int) map[string]any {
		looks++
		if looks <= 3 {
			week := strings.Replace(address, "past-month", "past-week", 1)

			return map[string]any{"href": week, "status": 200, "ready": "complete", "empty": false,
				"posts": []any{map[string]any{"text": "last week's post", "urn": urn}}}
		}

		return map[string]any{"href": address, "status": 999, "ready": "complete", "posts": []any{}}
	}}
	signInLinkedIn(t, rig, page)

	a := rig.search(t, listening.SourceLinkedIn)

	assert.Equal(t, string(listening.CodeRateLimited),
		coverageOf(t, a, listening.SourceLinkedIn).Streams[0].Tried[0].Result)
}

// P2: X's bundle answers a redirect to another host: it is not followed, as X's page's redirects are not.
func TestRound2XDiscoveryRedirectPolicyCoversBundles(t *testing.T) {
	var hits atomic.Int32

	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(evil.Close)

	rig := newSignedRigWith(t, map[string]string{"evil.example": evil.URL})
	rig.x.bundleTo = "https://evil.example/main.js"

	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceX))
	_ = rig.svc.PasteCookies(t.Context(), listening.SourceX, "auth_token=token-1; ct0=csrf-1", "")

	assert.Zero(t, hits.Load(), "a bundle's redirect off x.com and abs.twimg.com is not followed")
}
