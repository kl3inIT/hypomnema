package subscription_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// The lists of models the owner's two accounts on zm were served on 2026-11-18 (issue #814), cut short: the Pro
// account has Carbon only as a disabled row, the Ultra account has it.
const (
	proRoster = `{"models":{
	  "gemini-4-hydrogen-sonar":{"maxTokens":1000000,"maxOutputTokens":64000},
	  "gemini-4-carbon-disabled":{"disabled":true}}}`
	ultraRoster = `{"models":{
	  "gemini-4-hydrogen-sonar":{"maxTokens":1000000,"maxOutputTokens":64000},
	  "gemini-4-carbon-high":{"maxTokens":1048576,"maxOutputTokens":65536}}}`
)

// twoAccounts logs in a Pro and an Ultra account, each served its own list, and returns them.
func twoAccounts(t *testing.T, w *antigravityWorld) (pro, ultra subscription.Account) {
	t.Helper()

	w.google.set(func(g *google) {
		g.user, g.email = "pro", "pro@gmail.example"
		g.rosters["pro"], g.rosters["ultra"] = proRoster, ultraRoster
	})

	pro, err := w.login()
	require.NoError(t, err)

	w.google.set(func(g *google) { g.user, g.email = "ultra", "ultra@gmail.example" })

	ultra, err = w.login()
	require.NoError(t, err)

	return pro, ultra
}

// accountsFor takes n tokens in turn for a model, as n requests without a conversation do, and returns whose each
// was.
func accountsFor(t *testing.T, w *antigravityWorld, model string, n int) []string {
	t.Helper()

	var got []string

	for range n {
		token, err := w.subs.TokenFor(t.Context(), "antigravity", model, "")
		require.NoError(t, err)

		got = append(got, token.AccountID)
	}

	return got
}

// Scenario: the owner has a Pro and an Ultra Antigravity account, and only the Ultra one is served Carbon (issue
// #814). The gateway lists the models of both, Carbon with them; Carbon's window and output are the Ultra account's.
func TestTheGatewayListsTheModelsOfEveryAccount(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	twoAccounts(t, w)

	names, ok := w.subs.ServedNames(t.Context(), "antigravity")
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"gemini-4-hydrogen-sonar", "gemini-4-carbon-high"}, names)

	window, output, ok := w.subs.ServedLimits(t.Context(), "antigravity", "gemini-4-carbon-high")
	require.True(t, ok)
	assert.Equal(t, [2]int64{1048576, 65536}, [2]int64{window, output})

	_, _, ok = w.subs.ServedLimits(t.Context(), "antigravity", "gemini-4-carbon-disabled")
	assert.False(t, ok, "no account is served it")

	w.google.set(func(g *google) { assert.Equal(t, 2, g.listed, "each account's list is read once and kept") })
}

// Scenario: a request for Carbon goes to the Ultra account, every time, whichever account comes first; a request
// for a model both have takes the accounts in turn, as before (issue #814).
func TestAModelGoesOnlyToTheAccountsServedIt(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	pro, ultra := twoAccounts(t, w)

	assert.Equal(t, []string{ultra.ID, ultra.ID, ultra.ID}, accountsFor(t, w, "gemini-4-carbon-high", 3))

	both := accountsFor(t, w, "gemini-4-hydrogen-sonar", 2)
	assert.ElementsMatch(t, []string{pro.ID, ultra.ID}, both, "in turn, as without a model")

	_, err := w.subs.TokenFor(t.Context(), "antigravity", "", "")
	require.ErrorIs(t, err, subscription.ErrNoModel, "a request names its model")

	// The Ultra account tried and refused: no other account is served Carbon.
	_, err = w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "", ultra.ID)
	require.ErrorIs(t, err, subscription.ErrNoAccount)
}

// Scenario: the Pro account's list cannot be read. The Ultra account's models are still listed and served, and the
// Pro account's are read again after the retry.
func TestAnAccountWhoseListFailsHidesNoOther(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	w.google.set(func(g *google) { g.rosterFails["pro"] = true })
	_, ultra := twoAccounts(t, w)

	names, ok := w.subs.ServedNames(t.Context(), "antigravity")
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"gemini-4-hydrogen-sonar", "gemini-4-carbon-high"}, names)
	assert.Equal(t, []string{ultra.ID, ultra.ID}, accountsFor(t, w, "gemini-4-carbon-high", 2))

	// Read once, the Pro account keeps what it was served while a later reading fails.
	w.google.set(func(g *google) {
		g.rosterFails["pro"] = false
		g.rosters["pro"] = `{"models":{"gemini-4-argon-high":{}}}`
	})
	w.clock.advance(5 * time.Minute)

	names, _ = w.subs.ServedNames(t.Context(), "antigravity")
	assert.Contains(t, names, "gemini-4-argon-high", "read again after the retry")

	w.google.set(func(g *google) { g.rosterFails["pro"] = true })
	w.clock.advance(time.Hour)

	names, _ = w.subs.ServedNames(t.Context(), "antigravity")
	assert.Contains(t, names, "gemini-4-argon-high", "the last reading is kept")
}

// Scenario (review of #832): the Ultra account's list is still being read when a request for Carbon comes, and the
// Pro account's is known. The request waits for the Ultra account's list and goes there, not to the Pro account,
// whose list has Carbon disabled; and the gateway's list, asked meanwhile, still has Carbon.
func TestARequestWaitsForAnAccountsListBeingRead(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	_, ultra := twoAccounts(t, w)

	hold, arrived := make(chan struct{}), make(chan string, 4)
	release := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(release) // a failing test does not leave Google's stand-in waiting
	w.google.set(func(g *google) { g.holds["ultra"], g.arrived = hold, arrived })

	first := make(chan []string, 1)

	go func() {
		names, _ := w.subs.ServedNames(t.Context(), "antigravity")
		first <- names
	}()

	select {
	case user := <-arrived:
		require.Equal(t, "ultra", user)
	case <-time.After(10 * time.Second):
		t.Fatal("the Ultra account's list was never asked for")
	}

	type pick struct {
		id  string
		err error
	}

	picked, listed := make(chan pick, 1), make(chan []string, 1)

	go func() {
		token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "")
		picked <- pick{token.AccountID, err}
	}()

	go func() {
		names, _ := w.subs.ServedNames(t.Context(), "antigravity")
		listed <- names
	}()

	select {
	case p := <-picked:
		release()
		t.Fatalf("an account was picked before the Ultra account's list was read: %+v", p)
	case names := <-listed:
		release()
		t.Fatalf("the list was given before the Ultra account's list was read: %v", names)
	case <-time.After(200 * time.Millisecond):
	}

	release()

	p := <-picked
	require.NoError(t, p.err)
	assert.Equal(t, ultra.ID, p.id)
	assert.Contains(t, <-listed, "gemini-4-carbon-high")
	assert.Contains(t, <-first, "gemini-4-carbon-high")

	w.google.set(func(g *google) { assert.Equal(t, 2, g.listed, "each list was read once") })
}

// Scenario (review of #832): reading the Pro account's list again finds its login refused. A request for a model
// both accounts were served goes to the Ultra account: the account is judged as it is after its list was read, not
// as it was before.
func TestAnAccountFoundUnusableWhileItsListIsReadIsNotPicked(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	pro, ultra := twoAccounts(t, w)

	_, ok := w.subs.ServedNames(t.Context(), "antigravity")
	require.True(t, ok)

	w.google.set(func(g *google) { g.revokedUsers["pro"] = true })
	w.clock.advance(time.Hour) // both lists are read again, and both tokens are due

	// With the Ultra account tried already, no account is left: the Pro account is not tried with its refused login.
	_, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-hydrogen-sonar", "", ultra.ID)
	require.ErrorIs(t, err, subscription.ErrNoAccount)

	for range 2 {
		token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-hydrogen-sonar", "")
		require.NoError(t, err)
		assert.Equal(t, ultra.ID, token.AccountID)
	}

	a, err := w.subs.Get(t.Context(), pro.ID)
	require.NoError(t, err)
	assert.Equal(t, subscription.StateLoginRequired, a.State)
}

// Scenario (review of #832, round 2): the Ultra account's first list is still being read after the wait, and the
// Ultra account is in reserve. A request for Carbon is not sent to the Pro account, whose list is known not to have
// it: it is told to try again soon. The gateway's list is not given meanwhile, so Carbon is not taken off it.
func TestAListNotKnownYetNeverLiftsTheFilter(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	w.subs.WaitForListsAtMost(50 * time.Millisecond)
	pro, ultra := twoAccounts(t, w)
	w.subs.NoteQuota(ultra.ID, 0.05)

	hold, arrived := make(chan struct{}), make(chan string, 4)
	release := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(release)
	w.google.set(func(g *google) { g.holds["ultra"], g.arrived = hold, arrived })

	// The lists are read in the accounts' order: the Pro account's is known once the Ultra account's is asked for.
	go func() { _, _ = w.subs.ServedNames(t.Context(), "antigravity") }()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the Ultra account's list was never asked for")
	}

	token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "")

	var cooling *subscription.CoolingError
	require.ErrorAs(t, err, &cooling, "to be tried again")
	assert.NotEqual(t, pro.ID, token.AccountID)
	assert.Equal(t, w.clock.Now().Add(30*time.Second), cooling.Until)
	assert.True(t, cooling.ListUnknown)

	_, ok := w.subs.ServedNames(t.Context(), "antigravity")
	assert.False(t, ok, "no list while one is being read")

	release()
}

// Scenario (issue #814): no account's list has the model, such as one a profile keeps (issue #639). While the Pro
// account's list cannot be read, the request is told to try again; once every list is known, it is told no account
// is served the model. No account is ever picked for it (owner, 2026-11-18).
func TestAModelNoListHasIsNotServed(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	w.google.set(func(g *google) { g.rosterFails["pro"] = true })
	twoAccounts(t, w)

	token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-argon-high", "")

	var cooling *subscription.CoolingError
	require.ErrorAs(t, err, &cooling, "the Pro account's list could not be read")
	assert.Empty(t, token.AccountID)

	w.google.set(func(g *google) { g.rosterFails["pro"] = false })
	w.clock.advance(5 * time.Minute) // past the retry

	token, err = w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-argon-high", "")
	require.ErrorIs(t, err, subscription.ErrModelNotServed)
	assert.Empty(t, token.AccountID)
}

// Scenario (review of #832, round 3): the Ultra account's first list is still being read past the wait, and it ends
// between two steps of a request's pick: after the accounts were read and before what is known of their lists. The
// Pro account, whose list is known not to have Carbon, is not picked: the pick sees the accounts and their lists at
// one moment.
func TestAListReadDuringAPickIsSeenWhole(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	w.subs.WaitForListsAtMost(50 * time.Millisecond)
	pro, ultra := twoAccounts(t, w)
	w.subs.NoteQuota(ultra.ID, 0.05)

	hold, arrived := make(chan struct{}), make(chan string, 4)
	release := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(release)
	w.google.set(func(g *google) { g.holds["ultra"], g.arrived = hold, arrived })

	go func() { _, _ = w.subs.ServedNames(t.Context(), "antigravity") }()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the Ultra account's list was never asked for")
	}

	w.subs.BetweenAccountsAndLists(func() {
		release()

		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			if _, ok := w.subs.ServedNames(context.Background(), "antigravity"); ok {
				return
			}
		}

		t.Error("the Ultra account's list was never read")
	})

	token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "")
	require.NoError(t, err)
	assert.NotEqual(t, pro.ID, token.AccountID)
	assert.Equal(t, ultra.ID, token.AccountID)
}

// Scenario (issue #814): when an account's first list is read, the gateway is told at once, so that its models are
// brought up to date then rather than at the next hourly reading; a list read again is not told.
func TestAFirstListIsToldAtOnce(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	twoAccounts(t, w)

	told := make(chan string, 4)
	w.subs.OnFirstList(func(runtime string) { told <- runtime })

	_, ok := w.subs.ServedNames(t.Context(), "antigravity")
	require.True(t, ok)

	for range 2 {
		select {
		case runtime := <-told:
			assert.Equal(t, "antigravity", runtime)
		case <-time.After(10 * time.Second):
			t.Fatal("a first list was not told")
		}
	}

	w.clock.advance(time.Hour)

	_, ok = w.subs.ServedNames(t.Context(), "antigravity")
	require.True(t, ok)

	select {
	case runtime := <-told:
		t.Fatalf("a list read again was told: %s", runtime)
	case <-time.After(100 * time.Millisecond):
	}
}

// Scenario (review of #832, round 4): between reading the accounts and their lists, the owner disables the Ultra
// account, or it starts to cool. A request for Carbon is not given the Ultra account: the account picked is read
// again right before its token is given.
func TestAnAccountChangedDuringAPickIsNotGiven(t *testing.T) {
	t.Parallel()

	changes := map[string]func(w *antigravityWorld, id string) error{
		"cools": func(w *antigravityWorld, id string) error {
			return w.subs.CoolDown(context.Background(), "antigravity", id, w.clock.Now().Add(time.Hour), "rate_limited")
		},
		"is disabled": func(w *antigravityWorld, id string) error {
			return w.orch.DisableAccount(context.Background(), id)
		},
	}

	for name, change := range changes {
		w := newAntigravityWorld(t, ideSecret)
		_, ultra := twoAccounts(t, w)

		_, ok := w.subs.ServedNames(t.Context(), "antigravity")
		require.True(t, ok)

		w.subs.BetweenAccountsAndLists(func() { require.NoError(t, change(w, ultra.ID)) })

		token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "")
		require.Error(t, err, name)
		assert.Empty(t, token.AccountID, name)

		if name == "cools" {
			var cooling *subscription.CoolingError
			require.ErrorAs(t, err, &cooling, name)
		}
	}
}

// Scenario (review of #832, round 4): the vendor refuses an account's token. It is refreshed for a request for a
// model only while the account's list has the model; otherwise the request is told so and goes on to another.
func TestARefusedTokenIsRefreshedOnlyForAModelItsListHas(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	pro, ultra := twoAccounts(t, w)

	_, ok := w.subs.ServedNames(t.Context(), "antigravity")
	require.True(t, ok)

	_, err := w.subs.TokenAfterRefusalFor(t.Context(), subscription.Token{AccountID: pro.ID}, "gemini-4-carbon-high")
	require.ErrorIs(t, err, subscription.ErrModelNotServed)

	token, err := w.subs.TokenAfterRefusalFor(t.Context(), subscription.Token{AccountID: ultra.ID},
		"gemini-4-carbon-high")
	require.NoError(t, err)
	assert.Equal(t, ultra.ID, token.AccountID)

	_, err = w.subs.TokenAfterRefusalFor(t.Context(), subscription.Token{AccountID: ultra.ID}, "")
	require.ErrorIs(t, err, subscription.ErrNoModel)
}

// Scenario (review of #832, round 4): the request that starts the Ultra account's first list read waits for it no
// longer than any other: past the wait it is told to try again, and the Pro account is not picked.
func TestTheRequestThatStartsAReadWaitsNoLonger(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	w.subs.WaitForListsAtMost(50 * time.Millisecond)
	pro, _ := twoAccounts(t, w)

	hold := make(chan struct{})
	release := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(release)
	w.google.set(func(g *google) { g.holds["ultra"], g.arrived = hold, make(chan string, 4) })

	type pick struct {
		id  string
		err error
	}

	picked := make(chan pick, 1)

	go func() {
		token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "")
		picked <- pick{token.AccountID, err}
	}()

	select {
	case p := <-picked:
		var cooling *subscription.CoolingError
		require.ErrorAs(t, p.err, &cooling)
		assert.NotEqual(t, pro.ID, p.id)
	case <-time.After(10 * time.Second):
		release()
		t.Fatal("the request that started the read waited for it past the bound")
	}
}

// Scenario (review of #832, round 4): an account's first list read is told when it ends, answered or not, so that
// the providers' models are brought up to date then even when another account's list cannot be read.
func TestAFirstReadThatFailsIsToldToo(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	w.google.set(func(g *google) { g.rosterFails["ultra"] = true })
	twoAccounts(t, w)

	told := make(chan string, 4)
	w.subs.OnFirstList(func(runtime string) { told <- runtime })

	_, _ = w.subs.ServedNames(t.Context(), "antigravity")

	for range 2 {
		select {
		case <-told:
		case <-time.After(10 * time.Second):
			t.Fatal("a first read was not told")
		}
	}
}

// Scenario (review of #832, round 5): the first lists of both accounts are still being read. A request waits for
// them once, not once for each: it is told to try again within the one wait.
func TestARequestWaitsOnceForEveryListBeingRead(t *testing.T) {
	t.Parallel()

	const wait = 500 * time.Millisecond

	w := newAntigravityWorld(t, ideSecret)
	w.subs.WaitForListsAtMost(wait)
	twoAccounts(t, w)

	w.google.set(func(g *google) { g.user, g.email = "third", "third@gmail.example" })
	_, err := w.login()
	require.NoError(t, err)

	hold := make(chan struct{})
	release := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(release)
	w.google.set(func(g *google) {
		g.holds["pro"], g.holds["ultra"], g.holds["third"], g.arrived = hold, hold, hold, make(chan string, 8)
	})

	// One wait for three lists, not three: the bound sits between them, wide enough for a loaded machine.
	started := time.Now()
	_, err = w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "")
	took := time.Since(started)

	var cooling *subscription.CoolingError
	require.ErrorAs(t, err, &cooling)
	assert.Less(t, took, 2*wait, "one wait for every list")
}

// Scenario (review of #832, round 6): a request has its own deadline for the accounts' lists, set when it entered the
// gateway. Past it, a pick does not wait again for a list still being read, however long the subscriptions' own wait.
func TestAPickWaitsNoLaterThanItsRequest(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	twoAccounts(t, w)

	hold := make(chan struct{})
	release := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(release)
	w.google.set(func(g *google) { g.holds["ultra"], g.arrived = hold, make(chan string, 4) })

	ctx := subscription.WithListDeadline(t.Context(), time.Now().Add(100*time.Millisecond))

	done := make(chan error, 1)

	go func() {
		_, err := w.subs.TokenFor(ctx, "antigravity", "gemini-4-carbon-high", "")
		done <- err
	}()

	select {
	case err := <-done:
		var cooling *subscription.CoolingError
		require.ErrorAs(t, err, &cooling)
	case <-time.After(10 * time.Second):
		release()
		t.Fatal("the pick waited past its request's deadline")
	}
}

// Scenario (review of #832, round 6): the Ultra account, the only one whose list has Carbon, loses it while its token
// is refreshed, and every list is known. The request is told no account serves Carbon, not that no account could be
// used.
func TestAModelLostDuringARefreshIsNotServed(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	_, ultra := twoAccounts(t, w)

	_, ok := w.subs.ServedNames(t.Context(), "antigravity")
	require.True(t, ok)

	w.google.set(func(g *google) {
		g.onRefresh = func(context.Context) { w.subs.ForgetModel(ultra.ID, "gemini-4-carbon-high") }
	})
	w.clock.advance(59*time.Minute + 30*time.Second) // the tokens are due, the lists are not

	token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "")
	require.ErrorIs(t, err, subscription.ErrModelNotServed)
	assert.Empty(t, token.AccountID)
}

// Scenario (review of #832, round 5): the Ultra account's token is due, and while it is refreshed the owner
// disables the account. The refreshed token is not given: the account is read again after the refresh, both for a
// pick and for the retry after the vendor refused a token.
func TestAnAccountDisabledDuringItsRefreshIsNotGiven(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	_, ultra := twoAccounts(t, w)

	_, ok := w.subs.ServedNames(t.Context(), "antigravity")
	require.True(t, ok)

	disable := func(ctx context.Context) { assert.NoError(t, w.orch.DisableAccount(ctx, ultra.ID)) }
	w.google.set(func(g *google) { g.onRefresh = disable })

	w.clock.advance(59*time.Minute + 30*time.Second) // the tokens are due, the lists are not

	// The Pro account, the only one left usable, has a known list without Carbon: no account serves it now.
	token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "")
	require.ErrorIs(t, err, subscription.ErrModelNotServed)
	assert.Empty(t, token.AccountID)

	require.NoError(t, w.orch.EnableAccount(t.Context(), ultra.ID))

	_, err = w.subs.TokenAfterRefusalFor(t.Context(), subscription.Token{AccountID: ultra.ID}, "gemini-4-carbon-high")
	require.ErrorIs(t, err, subscription.ErrModelNotServed)
}

// Scenario (review of #832, round 7): the Ultra account, the only one whose list has Carbon, loses it while its token
// is refreshed, and the Pro account's first list is still being read, so it may yet have Carbon. The request is told
// to try again soon, as when no list has the model and one is not known, not that no account could be used.
func TestAModelLostDuringARefreshWhileAListIsUnknownIsTriedAgain(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	w.subs.WaitForListsAtMost(50 * time.Millisecond)
	_, ultra := twoAccounts(t, w)

	hold := make(chan struct{})
	release := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(release)
	w.google.set(func(g *google) { g.holds["pro"], g.arrived = hold, make(chan string, 4) })

	// The Ultra account's list is read, with Carbon; the Pro account's never ends.
	require.Eventually(t, func() bool {
		token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "")
		return err == nil && token.AccountID == ultra.ID
	}, 10*time.Second, 10*time.Millisecond)

	w.google.set(func(g *google) {
		g.onRefresh = func(context.Context) { w.subs.ForgetModel(ultra.ID, "gemini-4-carbon-high") }
	})
	w.clock.advance(59*time.Minute + 30*time.Second) // the tokens are due, the Ultra account's list is not

	token, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "")

	var cooling *subscription.CoolingError
	require.ErrorAs(t, err, &cooling, "to be tried again, not ErrNoAccount")
	assert.Equal(t, w.clock.Now().Add(30*time.Second), cooling.Until)
	assert.True(t, cooling.ListUnknown, "whatever an account answered before")
	assert.Empty(t, token.AccountID)
}

// Scenario (review of PR #1298): a gateway key limited to the Pro account asks for a model. Only the Pro account's
// list is read for it: the Ultra account, outside the key's list, is not called.
func TestALimitedKeyReadsOnlyItsAccountsLists(t *testing.T) {
	t.Parallel()

	w := newAntigravityWorld(t, ideSecret)
	pro, _ := twoAccounts(t, w)

	w.google.set(func(g *google) { g.listed = 0 })

	token, err := w.subs.TokenFor(subscription.WithAccounts(t.Context(), []string{pro.ID}), "antigravity",
		"gemini-4-hydrogen-sonar", "")
	require.NoError(t, err)
	assert.Equal(t, pro.ID, token.AccountID)

	w.google.set(func(g *google) { assert.Equal(t, 1, g.listed, "the key's one account's list, and no other") })
}
