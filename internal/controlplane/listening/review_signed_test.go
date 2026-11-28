package listening_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Review round 1 of PR #1359: each finding's probe, failing before its fix.

// throttledPage is LinkedIn's search page served with its document's status, whose results request LinkedIn refused
// with 999 when refused is set.
func throttledPage(status int, refused bool) *pageFake {
	p := &pageFake{snapshot: func(address string, _ int) map[string]any {
		return map[string]any{"href": address, "status": status, "ready": "complete", "posts": []any{}, "empty": false}
	}}
	if refused {
		p.responses = []browser.Response{
			{URL: "https://www.linkedin.com/search/results/content/?keywords=VETC", Status: 200},
			{URL: "https://www.linkedin.com/flagship-web/rsc-action/actions/pagination", Status: 999},
		}
	}

	return p
}

func visits(rig *signedRig, key string) int {
	rig.browser.mu.Lock()
	defer rig.browser.mu.Unlock()

	p, ok := rig.browser.pages[key]
	if !ok {
		return 0
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.visits)
}

// P1-1: a 429 met while a pasted cookie is verified stops LinkedIn for a day: the next verification sends nothing.
func TestReviewLinkedInThrottledVerificationStopsTheNextOne(t *testing.T) {
	rig := newSignedRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceLinkedIn))

	rig.browser.mu.Lock()
	rig.browser.pages["listening-check:linkedin"] = throttledPage(429, false)
	rig.browser.mu.Unlock()

	err := rig.svc.PasteCookies(t.Context(), listening.SourceLinkedIn, "li_at=session-1", "")
	require.ErrorIs(t, err, listening.ErrInvalid)
	require.Equal(t, 1, visits(rig, "listening-check:linkedin"))

	rig.clock.Advance(time.Hour)

	err = rig.svc.PasteCookies(t.Context(), listening.SourceLinkedIn, "li_at=session-2", "")
	require.ErrorIs(t, err, listening.ErrInvalid)
	assert.Contains(t, err.Error(), "stopped")
	assert.Equal(t, 1, visits(rig, "listening-check:linkedin"), "no LinkedIn request within the 24-hour stop")
}

// P1-1: after a 999 stops the account, signing in again sends nothing before the day ends, and keeps the stop.
func TestReviewLinkedInSignInAgainKeepsTheStop(t *testing.T) {
	rig := newSignedRig(t)
	signInLinkedIn(t, rig, throttledPage(999, false))

	_ = rig.search(t, listening.SourceLinkedIn)
	acct := stateOf(t, rig.svc, listening.SourceLinkedIn).Signed.Account
	require.Equal(t, start.Add(24*time.Hour), acct.StoppedUntil)

	before := visits(rig, "listening-check:linkedin")
	err := rig.svc.PasteCookies(t.Context(), listening.SourceLinkedIn, "li_at=session-1; JSESSIONID=j", "")
	require.ErrorIs(t, err, listening.ErrInvalid)
	assert.Equal(t, before, visits(rig, "listening-check:linkedin"), "the stop holds for a sign-in again")
	assert.Equal(t, start.Add(24*time.Hour), stateOf(t, rig.svc, listening.SourceLinkedIn).Signed.Account.StoppedUntil)
}

// P1-2: the document answers 200 while LinkedIn refuses the request that loads its results with 999: a rate limit
// with its 24-hour stop, never a broken page.
func TestReviewLinkedInRefusedResultsRequestIsRateLimited(t *testing.T) {
	rig := newSignedRig(t)
	signInLinkedIn(t, rig, throttledPage(200, true))

	a := rig.search(t, listening.SourceLinkedIn)

	cov := coverageOf(t, a, listening.SourceLinkedIn)
	assert.Equal(t, string(listening.CodeRateLimited), cov.Streams[0].Tried[0].Result)
	assert.Equal(t, start.Add(24*time.Hour),
		stateOf(t, rig.svc, listening.SourceLinkedIn).Signed.Account.StoppedUntil)
}

// P1-3: X's session ends while the web fallback answers 500: the owner hears of it once.
func TestReviewXExpiryWithoutPinNotifiesOnceWhenWebReturns500(t *testing.T) {
	rig := newSignedRig(t)
	rig.signInX(t)
	rig.x.status = http.StatusUnauthorized
	rig.web = func(context.Context, string, int) (websearch.Answer, error) {
		return websearch.Answer{}, errors.New("every provider answered 500")
	}

	a := rig.search(t, listening.SourceX)

	assert.Equal(t, listening.StateNotRead, coverageOf(t, a, listening.SourceX).State)
	assert.Equal(t, 1, rig.told.Count(), "one expired session, one notice: %+v", rig.told.all)
}

// P1-3: a plan pinned to X's account meets the session's end: the owner hears of it once.
func TestReviewPinnedXExpiryNotifiesOnce(t *testing.T) {
	rig := newSignedRig(t)
	rig.signInX(t)
	rig.x.status = http.StatusUnauthorized

	p := plan([]string{listening.SourceX}, "VETC")
	p.Backends = map[string]string{listening.SourceX: listening.BackendXGraphQL}

	_, err := rig.svc.Search(t.Context(), p, listening.Caller{Web: rig.web})
	require.NoError(t, err)
	assert.Equal(t, 1, rig.told.Count(), "one expired session, one notice: %+v", rig.told.all)
}

// P2: a snapshot of the last query's page is not this search's results.
func TestReviewLinkedInIgnoresTheLastQuerysPage(t *testing.T) {
	rig := newSignedRig(t)
	urn := fmt.Sprintf("urn:li:activity:%d", time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC).UnixMilli()<<22)
	looks := 0
	page := &pageFake{snapshot: func(address string, _ int) map[string]any {
		looks++
		if looks <= 3 {
			stale := strings.Replace(address, "keywords=VETC", "keywords=older", 1)

			return map[string]any{"href": stale, "status": 200, "ready": "complete", "empty": false,
				"posts": []any{map[string]any{"text": "an older query's post", "urn": urn}}}
		}

		return map[string]any{"href": address, "status": 999, "ready": "complete", "posts": []any{}}
	}}
	signInLinkedIn(t, rig, page)

	a := rig.search(t, listening.SourceLinkedIn)

	cov := coverageOf(t, a, listening.SourceLinkedIn)
	assert.Equal(t, string(listening.CodeRateLimited), cov.Streams[0].Tried[0].Result,
		"the older query's posts are not taken; the page this query landed on is read")
}
