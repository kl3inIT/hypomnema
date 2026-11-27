package websearch_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// subscriptions stands for the gateway's search under the logged-in
// accounts: each provider answers with its own function; one without is not
// logged in. It keeps the providers asked, in order.
type subscriptions struct {
	mu      sync.Mutex
	answers map[string]func() ([]websearch.Result, error)
	asked   []string
}

func (s *subscriptions) Search(_ context.Context, provider, query string, limit int) ([]websearch.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.asked = append(s.asked, fmt.Sprintf("%s:%s:%d", provider, query, limit))

	answer, ok := s.answers[provider]
	if !ok {
		return nil, fmt.Errorf("%w: none of %s", websearch.ErrNoAccount, provider)
	}

	return answer()
}

func (s *subscriptions) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.asked...)
}

func found(title string) func() ([]websearch.Result, error) {
	return func() ([]websearch.Result, error) {
		return []websearch.Result{{Title: title, URL: "https://" + title + ".example/"}}, nil
	}
}

// failingFree is a world whose one free provider, Exa's public MCP, fails,
// so a search reaches the subscriptions, which off turns off in part.
func failingFree(t *testing.T, subs *subscriptions, off ...string) *websearch.Service {
	t.Helper()

	w := newWorld(t, options{})
	w.exaMCP.set(refuses(http.StatusInternalServerError, ""))

	set := websearch.Settings{Off: append(keylessOnly("exa"), off...)}

	svc := websearch.New(websearch.Config{
		ExaMCPURL: w.exaMCP.url, Now: w.clock.Now,
		Settings: func(context.Context) (websearch.Settings, error) { return set, nil },
	})

	return svc.WithSubscriptions(subs)
}

// Scenario: the free providers fail, so the logged-in subscriptions are
// asked, last and in the order Codex, Claude, Antigravity. No Codex account
// is logged in: Codex is not in the chain and is not named among those that
// failed; Claude answers.
func TestTheSubscriptionsAreAskedLastOnlyWhenLoggedIn(t *testing.T) {
	t.Parallel()

	subs := &subscriptions{answers: map[string]func() ([]websearch.Result, error){
		websearch.ProviderClaude: found("claude"), websearch.ProviderAntigravity: found("antigravity"),
	}}
	svc := failingFree(t, subs)

	a, err := svc.Search(t.Context(), "go 1.27", 3, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderClaude, a.Provider)
	assert.Equal(t, []websearch.Result{{Title: "claude", URL: "https://claude.example/"}}, a.Results)
	assert.Equal(t, []string{"codex:go 1.27:3", "claude:go 1.27:3"}, subs.calls(), "Antigravity is not reached")

	var failed []string
	for _, f := range a.Errors {
		failed = append(failed, f.Provider)
	}

	assert.Equal(t, []string{websearch.ProviderExa}, failed,
		"a subscription with no account is not a failure")

	// Named, Codex without an account cannot be asked.
	_, err = svc.Search(t.Context(), "go", 0, websearch.ProviderCodex)
	require.ErrorIs(t, err, websearch.ErrInvalid)
	assert.Contains(t, err.Error(), "no codex account is logged in")

	// Named, Antigravity is the only one asked.
	a, err = svc.Search(t.Context(), "go", 0, "Antigravity")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderAntigravity, a.Provider)
	assert.Empty(t, a.Errors)
}

// Scenario: Codex's search answers 429 with a wait of 30 seconds. Claude
// answers instead; until the wait is over Codex is not asked and the answer
// says it rests. A subscription the owner turned off is neither tried nor
// may be named, and none is when the control plane has no subscriptions.
func TestASubscriptionsSearchRestsAfterA429AndCanBeTurnedOff(t *testing.T) {
	t.Parallel()

	subs := &subscriptions{answers: map[string]func() ([]websearch.Result, error){
		websearch.ProviderCodex: func() ([]websearch.Result, error) {
			return nil, websearch.Refused(http.StatusTooManyRequests, 30*time.Second, "usage limit")
		},
		websearch.ProviderClaude: found("claude"),
	}}
	svc := failingFree(t, subs)

	a, err := svc.Search(t.Context(), "first", 0, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderClaude, a.Provider)
	require.Len(t, a.Errors, 2)
	assert.Equal(t, websearch.Failure{Provider: "codex", Reason: "its subscription: rate limited (429)"}, a.Errors[1])

	a, err = svc.Search(t.Context(), "second", 0, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"codex:first:5", "claude:first:5", "claude:second:5"}, subs.calls(),
		"a resting search is not asked")
	assert.Contains(t, a.Errors[1].Reason, "its subscription: rate limited until")

	off := &subscriptions{answers: map[string]func() ([]websearch.Result, error){
		websearch.ProviderCodex: found("codex"), websearch.ProviderClaude: found("claude"),
	}}
	offSvc := failingFree(t, off, websearch.ProviderCodex)

	a, err = offSvc.Search(t.Context(), "go", 0, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderClaude, a.Provider)
	assert.Equal(t, []string{"claude:go:5"}, off.calls())

	_, err = offSvc.Search(t.Context(), "go", 0, websearch.ProviderCodex)
	require.ErrorIs(t, err, websearch.ErrInvalid)
	assert.Contains(t, err.Error(), "turned off")

	_, err = websearch.New(websearch.Config{}).Search(t.Context(), "go", 0, websearch.ProviderClaude)
	require.ErrorIs(t, err, websearch.ErrInvalid)
}

// scoped is a world whose one free provider is Exa's public MCP, which answers, and whose subscriptions answer as
// subs says; off turns providers off.
func scoped(t *testing.T, subs *subscriptions, off ...string) (*websearch.Service, *world) {
	t.Helper()

	w := newWorld(t, options{})
	set := websearch.Settings{Off: append(keylessOnly("exa"), off...)}

	svc := websearch.New(websearch.Config{
		ExaMCPURL: w.exaMCP.url, Now: w.clock.Now,
		Settings: func(context.Context) (websearch.Settings, error) { return set, nil },
	})

	return svc.WithSubscriptions(subs), w
}

// Scenario (issue #1277): a gateway key's own order is Codex, Claude, then Exa. Its search asks Codex, which is at
// its limit, then Claude, which answers: Exa is not asked. The same search without the key's order is Exa's, first
// in the owner's order. A key whose order names only Claude, which fails, goes on in the owner's order: Exa answers
// and Codex, last there, is not asked. A provider switched off stays off for a key that names it.
func TestAKeysOwnSearchOrderAsksItsProvidersFirst(t *testing.T) {
	t.Parallel()

	keyOrder := websearch.Scope{Order: []string{websearch.ProviderCodex, websearch.ProviderClaude, websearch.ProviderExa}}
	subs := &subscriptions{answers: map[string]func() ([]websearch.Result, error){
		websearch.ProviderCodex: func() ([]websearch.Result, error) {
			return nil, websearch.Refused(http.StatusTooManyRequests, 30*time.Second, "usage limit")
		},
		websearch.ProviderClaude: found("claude"),
	}}
	svc, w := scoped(t, subs)

	a, err := svc.SearchIn(t.Context(), keyOrder, "go 1.27", 3, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderClaude, a.Provider)
	assert.Equal(t, []string{"codex:go 1.27:3", "claude:go 1.27:3"}, subs.calls())
	assert.Zero(t, w.exaMCP.calls.Load(), "Exa comes after the key's subscriptions")

	a, err = svc.Search(t.Context(), "go 1.27", 3, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderExa, a.Provider, "another key keeps the owner's order")
	assert.False(t, a.Cached, "the key's answer is not another key's")
	assert.Len(t, subs.calls(), 2)

	failing := &subscriptions{answers: map[string]func() ([]websearch.Result, error){
		websearch.ProviderClaude: func() ([]websearch.Result, error) {
			return nil, websearch.Refused(http.StatusServiceUnavailable, 0, "overloaded")
		},
		websearch.ProviderCodex: found("codex"),
	}}
	svc, _ = scoped(t, failing)

	a, err = svc.SearchIn(t.Context(), websearch.Scope{Order: []string{websearch.ProviderClaude}}, "go", 0, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderExa, a.Provider, "the providers it leaves out follow in the owner's order")
	assert.Equal(t, []string{"claude:go:5"}, failing.calls())

	off := &subscriptions{answers: map[string]func() ([]websearch.Result, error){
		websearch.ProviderCodex: found("codex"), websearch.ProviderClaude: found("claude"),
	}}
	svc, _ = scoped(t, off, websearch.ProviderCodex)

	a, err = svc.SearchIn(t.Context(), keyOrder, "go", 0, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderClaude, a.Provider)
	assert.Equal(t, []string{"claude:go:5"}, off.calls(), "a provider off is skipped though the key names it")
}

// Scenario (issue #1277): Codex's search under a key's own accounts answers 429. It rests for that key's accounts
// alone: the owner's next search, under every account, asks Codex.
func TestASubscriptionRestsForTheKeysAccountsAlone(t *testing.T) {
	t.Parallel()

	limited := true
	subs := &subscriptions{answers: map[string]func() ([]websearch.Result, error){
		websearch.ProviderCodex: func() ([]websearch.Result, error) {
			if limited {
				return nil, websearch.Refused(http.StatusTooManyRequests, time.Minute, "usage limit")
			}

			return found("codex")()
		},
		websearch.ProviderClaude: found("claude"),
	}}
	svc, _ := scoped(t, subs, websearch.ProviderExa)
	key := websearch.Scope{Order: []string{websearch.ProviderCodex}, Accounts: "only:acc_1"}

	a, err := svc.SearchIn(t.Context(), key, "first", 0, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderClaude, a.Provider)

	limited = false

	a, err = svc.Search(t.Context(), "second", 0, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderCodex, a.Provider, "Codex rests only for the key's accounts")

	a, err = svc.SearchIn(t.Context(), key, "third", 0, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderClaude, a.Provider, "the key's accounts still rest")
}
