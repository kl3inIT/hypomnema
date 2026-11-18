package subscription_test

import (
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

	assert.ElementsMatch(t, []string{pro.ID, ultra.ID}, accountsFor(t, w, "", 2), "no model: every account")

	// The Ultra account tried and refused: no other account is served Carbon.
	_, err := w.subs.TokenFor(t.Context(), "antigravity", "gemini-4-carbon-high", "", ultra.ID)
	require.ErrorIs(t, err, subscription.ErrNoAccount)

	// A model no account's list has, such as one a profile keeps (issue #639), is tried under every account.
	assert.ElementsMatch(t, []string{pro.ID, ultra.ID}, accountsFor(t, w, "gemini-4-argon-high", 2))
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
