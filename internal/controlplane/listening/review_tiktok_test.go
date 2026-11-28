package listening_test

import (
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
