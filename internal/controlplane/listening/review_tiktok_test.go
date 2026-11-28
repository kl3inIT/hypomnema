package listening_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
)

// Review round 1 of PR #1363: each finding's probe, failing before its fix.

// P1-1: TikTok answers the sign-in check's search 429 with Retry-After 120. The owner's tab asks every two seconds;
// until the 120 seconds end, no ask sends TikTok anything, and the dialog says why.
func TestReviewTikTokOwnerTabRespectsRetryAfter(t *testing.T) {
	rig := newTikTokRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceTikTok))

	rig.browser.mu.Lock()
	rig.browser.profile = []browser.Cookie{
		{Name: "sessionid", Value: "made-up-session", Domain: ".tiktok.com", Path: "/"},
		{Name: "msToken", Value: "made-up-token", Domain: ".tiktok.com", Path: "/"},
	}
	rig.browser.mu.Unlock()

	rig.check.answer = func(string) map[string]any {
		return map[string]any{"status": 429, "retry_after": "120", "body": ""}
	}

	first, err := rig.svc.SignInCheck(t.Context(), listening.SourceTikTok, "")
	require.NoError(t, err)
	assert.False(t, first.Done)
	assert.Contains(t, first.Waiting, "slow down")
	require.Len(t, rig.check.fetched(), 1)

	rig.clock.Advance(2 * time.Second)

	second, err := rig.svc.SignInCheck(t.Context(), listening.SourceTikTok, "")
	require.NoError(t, err)
	assert.Contains(t, second.Waiting, "slow down")
	assert.Len(t, rig.check.fetched(), 1, "nothing is sent to TikTok while its Retry-After lasts")

	rig.clock.Advance(2 * time.Minute)

	_, err = rig.svc.SignInCheck(t.Context(), listening.SourceTikTok, "")
	require.NoError(t, err)
	assert.Len(t, rig.check.fetched(), 2, "once it ends, the session is checked again")
}

// P1-2: a not_found met by one route of TikTok is that route's endpoint or video, not the question's: the source's
// next route is still asked.
func TestReviewTikTokNotFoundStillFallsBack(t *testing.T) {
	found := "https://www.tiktok.com/@viewer.one/video/" + tiktokID(start.Add(-72*time.Hour), 7)

	t.Run("the signed-in search's 404 moves on to yt-dlp", func(t *testing.T) {
		rig := newTikTokRig(t)
		rig.signIn(t)
		rig.tab.answer = func(string) map[string]any { return map[string]any{"status": 404, "body": "Not Found"} }
		rig.yt.videos[found] = map[string]any{
			"id": tiktokID(start.Add(-72*time.Hour), 7), "description": "VETC trừ tiền hai lần",
			"uploader": "viewer.one", "timestamp": start.Add(-72 * time.Hour).Unix(), "webpage_url": found,
		}

		cov := coverageOf(t, rig.search(t), listening.SourceTikTok)
		assert.Equal(t, listening.BackendYtDlp, cov.Streams[0].ServedBy)
	})

	t.Run("yt-dlp's Video unavailable moves on to the web cards", func(t *testing.T) {
		rig := newTikTokRig(t)
		require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceTikTok))
		rig.yt.failure = func(address string) (string, error) {
			return "ERROR: [TikTok] " + address + ": Video unavailable", assert.AnError
		}

		a := rig.search(t)
		cov := coverageOf(t, a, listening.SourceTikTok)
		assert.Equal(t, listening.BackendSiteSearch, cov.Streams[0].ServedBy)
		assert.Len(t, a.Items, 1)
	})
}

// Review round 2 of PR #1363, P1: a Bot's search passed the registry's check and waits for TikTok's turn while a
// sign-in's check holds it; the check meets a 429 with Retry-After 120 and keeps TikTok's rest; the search, canceled
// while it waits, sent nothing to TikTok and must not replace that rest with its own timeout. The owner's next check,
// two seconds later, then sends nothing either.
func TestReviewTikTokCancelledSearchKeepsTheRetryAfter(t *testing.T) {
	rig := newTikTokRig(t)
	rig.signIn(t)
	rig.clock.Advance(time.Hour)

	fetching, release := make(chan struct{}), make(chan struct{})
	var first sync.Once

	rig.check.answer = func(string) map[string]any {
		first.Do(func() { close(fetching) })
		<-release

		return map[string]any{"status": 429, "retry_after": "120", "body": ""}
	}

	checked := make(chan error, 1)
	go func() {
		checked <- rig.svc.PasteCookies(context.Background(), listening.SourceTikTok,
			"sessionid=made-up-2; msToken=made-up-2", "")
	}()
	<-fetching

	// The Bot's search: its tab is opened once the registry let it through, then it waits for TikTok's turn.
	opened, waiting := make(chan struct{}, 1), make(chan struct{}, 1)
	rig.onPage = func(key string) {
		if key == "listening:tiktok" {
			opened <- struct{}{}
		}
	}
	var once sync.Once

	rig.sleep = func(ctx context.Context, _ time.Duration) error {
		held := false
		once.Do(func() { held = true })

		if !held {
			return nil
		}

		waiting <- struct{}{}
		<-ctx.Done()

		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	searched := make(chan struct{})
	go func() {
		defer close(searched)

		p := plan([]string{listening.SourceTikTok}, "VETC")
		p.WindowDays = 30
		_, _ = rig.svc.Search(ctx, p, listening.Caller{Web: rig.web})
	}()
	<-opened

	close(release)
	require.ErrorIs(t, <-checked, listening.ErrInvalid, "the check met TikTok's rate limit")

	asked := len(rig.check.fetched())

	<-waiting
	cancel()
	<-searched

	assert.Empty(t, rig.tab.fetched(), "the canceled search sent TikTok nothing")

	rig.clock.Advance(2 * time.Second)

	err := rig.svc.PasteCookies(t.Context(), listening.SourceTikTok, "sessionid=made-up-2; msToken=made-up-2", "")
	require.ErrorIs(t, err, listening.ErrInvalid)
	assert.Contains(t, err.Error(), "slow down")
	assert.Len(t, rig.check.fetched(), asked, "nothing is sent to TikTok while its Retry-After lasts")
}

// Review round 2 of PR #1363, P2: has_more 1 without item_list is no read, so a sign-in is not kept on it.
func TestReviewTikTokMoreWithoutAListKeepsNoSignIn(t *testing.T) {
	rig := newTikTokRig(t)
	require.NoError(t, rig.svc.Consent(t.Context(), listening.SourceTikTok))
	rig.check.answer = func(string) map[string]any {
		return tiktokBody(map[string]any{"status_code": 0, "has_more": 1})
	}

	err := rig.svc.PasteCookies(t.Context(), listening.SourceTikTok, "sessionid=made-up; msToken=made-up", "")
	require.ErrorIs(t, err, listening.ErrInvalid)
	assert.False(t, rig.vault.has("listening-tiktok"))
}
