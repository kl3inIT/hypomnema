package subscription_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

const verifyLink = "https://accounts.google.com/signin/continue?sarp=1&plt=first"

// refusals is how many times Google answered 403 VALIDATION_REQUIRED.
func (g *google) refusals() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.refused
}

// verification is an account's mark, which must be there.
func (w *antigravityWorld) verification(id string) subscription.Account {
	w.t.Helper()

	a, err := w.subs.Get(w.t.Context(), id)
	require.NoError(w.t, err)
	require.NotNil(w.t, a.Verification, "the account is marked")

	return a
}

// Scenario (issue #822): Google wants an Antigravity account verified and answers the models list with 403
// VALIDATION_REQUIRED. The account is marked with Google's link, its message and the time it asked; it is not asked
// again before its next try, and each answer that asks again puts the next try further: 10 minutes, 1 hour, 6 hours,
// then every day. The gateway is given the other account meanwhile. Once Google answers, the mark is cleared.
func TestAnAccountGoogleWantsVerifiedRestsOnAGrowingSchedule(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)

	marked, err := w.login()
	require.NoError(t, err)

	w.google.set(func(g *google) { g.user, g.email = "99", "second@gmail.example" })

	other, err := w.login()
	require.NoError(t, err)

	w.google.set(func(g *google) { g.asks = verifyLink })

	_, err = w.subs.Models(t.Context(), marked.ID)

	var verify *subscription.VerificationError
	require.ErrorAs(t, err, &verify)
	assert.Equal(t, verifyLink, verify.URL)
	assert.Equal(t, 1, w.google.refusals(), "the other host would say the same: it is not asked")

	v := w.verification(marked.ID).Verification
	assert.Equal(t, verifyLink, v.URL)
	assert.Equal(t, "Verify your account to continue.", v.Message)
	assert.Equal(t, t0, v.AskedAt)
	assert.Equal(t, t0.Add(10*time.Minute), v.RetryAt)

	for range 4 {
		token, err := w.subs.Token(t.Context(), "antigravity", "")
		require.NoError(t, err)
		assert.Equal(t, other.ID, token.AccountID, "the marked account is passed over")
	}

	// Before its next try nothing asks Google under the account: not the models list, not the limits.
	_, err = w.subs.Models(t.Context(), marked.ID)
	require.ErrorAs(t, err, &verify)
	_, err = w.subs.Limits(t.Context(), marked.ID, true)
	require.ErrorAs(t, err, &verify)
	assert.Equal(t, verifyLink, verify.URL)
	assert.Equal(t, t0.Add(10*time.Minute), verify.RetryAt)
	assert.Equal(t, 1, w.google.refusals())

	// Each try that Google answers the same way waits longer; the quota read marks the account as the list does.
	for i, wait := range []time.Duration{time.Hour, 6 * time.Hour, 24 * time.Hour, 24 * time.Hour} {
		at := w.verification(marked.ID).Verification.RetryAt
		w.clock.advance(at.Sub(w.clock.Now()))

		if i%2 == 0 {
			_, err = w.subs.Limits(t.Context(), marked.ID, true)
		} else {
			_, err = w.subs.Models(t.Context(), marked.ID)
		}

		require.ErrorAs(t, err, &verify)

		v = w.verification(marked.ID).Verification
		assert.Equal(t, at.Add(wait), v.RetryAt, "try %d", i+2)
		assert.Equal(t, i+2, v.Failures)
		assert.Equal(t, t0, v.AskedAt, "when Google first asked")
	}

	// Its next time come, the gateway may try it, and says so; Google no longer asks, so it works and is cleared.
	w.clock.advance(w.verification(marked.ID).Verification.RetryAt.Sub(w.clock.Now()))
	w.google.set(func(g *google) { g.asks = "" })

	token, err := w.subs.Token(t.Context(), "antigravity", "", other.ID)
	require.NoError(t, err)
	assert.Equal(t, marked.ID, token.AccountID)
	assert.True(t, token.Verifying, "this is the marked account's try")

	_, err = w.subs.Models(t.Context(), marked.ID)
	require.NoError(t, err)

	a, err := w.subs.Get(t.Context(), marked.ID)
	require.NoError(t, err)
	assert.Nil(t, a.Verification, "a call that works clears the mark")

	token, err = w.subs.Token(t.Context(), "antigravity", "", other.ID)
	require.NoError(t, err)
	assert.False(t, token.Verifying)
}

// Scenario (issue #822): the only Antigravity account is marked. The gateway is told when it is free again, as for a
// cooling account; the gateway's own call marks and clears it through NeedsVerification and Verified; a second
// answer from a request sent before the mark does not climb the schedule.
func TestTheGatewayMarksAndClearsAnAccountToVerify(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)

	a, err := w.login()
	require.NoError(t, err)

	retry, err := w.subs.NeedsVerification(t.Context(), a.ID, verifyLink, "Verify your account to continue.")
	require.NoError(t, err)
	assert.Equal(t, t0.Add(10*time.Minute), retry)

	// A request in flight when the account was marked answers the same, with a newer link.
	retry, err = w.subs.NeedsVerification(t.Context(), a.ID, verifyLink+"-2", "Verify your account to continue.")
	require.NoError(t, err)
	assert.Equal(t, t0.Add(10*time.Minute), retry, "not a new try: the schedule does not climb")

	v := w.verification(a.ID).Verification
	assert.Equal(t, 1, v.Failures)
	assert.Equal(t, verifyLink+"-2", v.URL, "the latest link")

	_, err = w.subs.Token(t.Context(), "antigravity", "")

	var cooling *subscription.CoolingError
	require.ErrorAs(t, err, &cooling)
	assert.Equal(t, t0.Add(10*time.Minute), cooling.Until)

	require.NoError(t, w.subs.Verified(t.Context(), a.ID))

	token, err := w.subs.Token(t.Context(), "antigravity", "")
	require.NoError(t, err)
	assert.False(t, token.Verifying)
}

// Scenario (issue #822): the owner verified the account through Google's link, or not, and signs it in again. The
// new sign-in clears the mark and its schedule: the account is used at once (opencode-antigravity-auth #589).
func TestSigningInAgainClearsTheMark(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)

	a, err := w.login()
	require.NoError(t, err)

	w.google.set(func(g *google) { g.asks = verifyLink })

	_, err = w.subs.Limits(t.Context(), a.ID, true)
	require.ErrorAs(t, err, new(*subscription.VerificationError), "the quota read marks the account")
	w.verification(a.ID)

	w.google.set(func(g *google) { g.asks = "" })

	again, err := w.login()
	require.NoError(t, err)
	assert.Equal(t, a.ID, again.ID)
	assert.Nil(t, again.Verification)

	token, err := w.subs.Token(t.Context(), "antigravity", "")
	require.NoError(t, err)
	assert.Equal(t, a.ID, token.AccountID)
}

// Scenario (issue #822, review of #826): two requests run under one account; one is answered VALIDATION_REQUIRED and
// marks it, then the other's token is refused with a 401. Its retry gets no new token before the account's next
// try: every path that hands out a token keeps the rest, not only Token.
func TestTheRetryAfterARefusalWaitsForTheNextTry(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)

	a, err := w.login()
	require.NoError(t, err)

	token, err := w.subs.Token(t.Context(), "antigravity", "")
	require.NoError(t, err)

	_, err = w.subs.NeedsVerification(t.Context(), a.ID, verifyLink, "Verify your account to continue.")
	require.NoError(t, err)

	_, err = w.subs.TokenAfterRefusal(t.Context(), token)

	var verify *subscription.VerificationError
	require.ErrorAs(t, err, &verify)
	assert.Equal(t, t0.Add(10*time.Minute), verify.RetryAt)
	assert.Equal(t, verifyLink, verify.URL)

	_, err = w.subs.Roster(t.Context(), a.ID)
	require.ErrorAs(t, err, &verify, "the full list waits too")

	w.clock.advance(10 * time.Minute)

	again, err := w.subs.TokenAfterRefusal(t.Context(), token)
	require.NoError(t, err, "its next try has come")
	assert.True(t, again.Verifying)
}

func TestReview826MarkDuringRefreshMustBlockToken(t *testing.T) {
	t.Parallel()

	world := newAntigravityWorld(t, ideSecret)
	account, err := world.login()
	require.NoError(t, err)

	refused, err := world.subs.Token(t.Context(), "antigravity", "")
	require.NoError(t, err)

	world.google.set(func(fake *google) {
		fake.onRefresh = func(ctx context.Context) {
			_, err := world.subs.NeedsVerification(ctx, account.ID, verifyLink, "Verify your account to continue.")
			assert.NoError(t, err)
		}
	})

	_, err = world.subs.TokenAfterRefusal(t.Context(), refused)

	var verify *subscription.VerificationError
	require.ErrorAs(t, err, &verify)
	assert.Equal(t, verifyLink, verify.URL)
	assert.Equal(t, t0.Add(10*time.Minute), verify.RetryAt)
}

func TestReview826MarkedAccountMustNotRefreshBeforeGate(t *testing.T) {
	t.Parallel()

	world := newAntigravityWorld(t, ideSecret)
	account, err := world.login()
	require.NoError(t, err)

	refused, err := world.subs.Token(t.Context(), "antigravity", "")
	require.NoError(t, err)

	var refreshed atomic.Bool
	world.google.set(func(fake *google) {
		fake.revoked = true
		fake.onRefresh = func(context.Context) { refreshed.Store(true) }
	})
	_, err = world.subs.NeedsVerification(t.Context(), account.ID, verifyLink, "Verify your account to continue.")
	require.NoError(t, err)

	_, err = world.subs.TokenAfterRefusal(t.Context(), refused)

	var verify *subscription.VerificationError
	require.ErrorAs(t, err, &verify)
	assert.Equal(t, t0.Add(10*time.Minute), verify.RetryAt)
	assert.False(t, refreshed.Load(), "the account must not be refreshed while waiting for verification")
}

type verificationRead func(context.Context, *subscription.Service, string) error

var verificationReads = map[string]verificationRead{
	"models": func(ctx context.Context, subs *subscription.Service, id string) error {
		_, err := subs.Models(ctx, id)

		return err
	},
	"roster": func(ctx context.Context, subs *subscription.Service, id string) error {
		_, err := subs.Roster(ctx, id)

		return err
	},
	"limits": func(ctx context.Context, subs *subscription.Service, id string) error {
		_, err := subs.Limits(ctx, id, true)

		return err
	},
}

func markDuringCall(t *testing.T, world *antigravityWorld, id string) func(context.Context) {
	t.Helper()

	return func(ctx context.Context) {
		_, err := world.subs.NeedsVerification(ctx, id, verifyLink, "Verify your account to continue.")
		assert.NoError(t, err)
	}
}

func TestReview826MarkDuringRefreshStopsTokenAndReads(t *testing.T) {
	t.Parallel()

	reads := map[string]verificationRead{
		"token": func(ctx context.Context, subs *subscription.Service, _ string) error {
			_, err := subs.Token(ctx, "antigravity", "")

			return err
		},
	}
	for name, read := range verificationReads {
		reads[name] = read
	}

	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			world := newAntigravityWorld(t, ideSecret)
			account, err := world.login()
			require.NoError(t, err)
			world.clock.advance(time.Hour)
			world.google.set(func(fake *google) { fake.onRefresh = markDuringCall(t, world, account.ID) })

			err = read(t.Context(), world.subs, account.ID)
			if name == "token" {
				var cooling *subscription.CoolingError
				require.ErrorAs(t, err, &cooling)
			} else {
				var verify *subscription.VerificationError
				require.ErrorAs(t, err, &verify)
				assert.Equal(t, verifyLink, verify.URL)
			}

			world.google.set(func(fake *google) { assert.Equal(t, []string{"", "daily"}, fake.hosts) })
		})
	}
}

func TestReview826MarkDuringFailedRefreshWinsAfterRefusal(t *testing.T) {
	t.Parallel()

	world := newAntigravityWorld(t, ideSecret)
	account, err := world.login()
	require.NoError(t, err)
	refused, err := world.subs.Token(t.Context(), "antigravity", "")
	require.NoError(t, err)
	world.google.set(func(fake *google) {
		fake.revoked = true
		fake.onRefresh = markDuringCall(t, world, account.ID)
	})

	_, err = world.subs.TokenAfterRefusal(t.Context(), refused)
	var verify *subscription.VerificationError
	require.ErrorAs(t, err, &verify)
	assert.Equal(t, verifyLink, verify.URL)
}

func TestReview826MarkedAccountDoesNotRefreshThroughAnyEntryPoint(t *testing.T) {
	t.Parallel()

	reads := map[string]verificationRead{
		"models": verificationReads["models"],
		"roster": verificationReads["roster"],
		"manual": func(ctx context.Context, subs *subscription.Service, id string) error {
			_, err := subs.Refresh(ctx, id)

			return err
		},
		"background": func(ctx context.Context, subs *subscription.Service, _ string) error {
			return subs.RefreshDue(ctx)
		},
	}

	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			world := newAntigravityWorld(t, ideSecret)
			account, err := world.login()
			require.NoError(t, err)
			world.clock.advance(time.Hour)
			markDuringCall(t, world, account.ID)(t.Context())
			var refreshed atomic.Bool
			world.google.set(func(fake *google) {
				fake.revoked = true
				fake.onRefresh = func(context.Context) { refreshed.Store(true) }
			})

			err = read(t.Context(), world.subs, account.ID)
			assert.False(t, refreshed.Load())
			if name == "background" {
				require.NoError(t, err)
			} else {
				var verify *subscription.VerificationError
				require.ErrorAs(t, err, &verify)
			}
		})
	}
}

func TestReview826MarkedAccountDoesNotReachSecondHost(t *testing.T) {
	t.Parallel()

	for name, read := range verificationReads {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			world := newAntigravityWorld(t, ideSecret)
			account, err := world.login()
			require.NoError(t, err)
			world.google.set(func(fake *google) { fake.onDailyRead = markDuringCall(t, world, account.ID) })

			err = read(t.Context(), world.subs, account.ID)
			world.google.set(func(fake *google) { assert.Equal(t, []string{"", "daily", "daily"}, fake.hosts) })
			var verify *subscription.VerificationError
			require.ErrorAs(t, err, &verify)
			assert.Equal(t, 1, world.verification(account.ID).Verification.Failures)
		})
	}
}
