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

// failingFree is a world whose free providers all fail, so a search reaches
// the subscriptions, which off turns off in part.
func failingFree(t *testing.T, subs *subscriptions, off ...string) *websearch.Service {
	t.Helper()

	c := &clock{now: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	_, exaMCPURL := newProvider(t, refuses(http.StatusInternalServerError, ""))
	_, parallelURL := newProvider(t, refuses(http.StatusBadGateway, ""))

	svc := websearch.New(websearch.Config{
		ExaMCPURL: exaMCPURL, ParallelMCPURL: parallelURL, Now: c.Now,
		Settings: func(context.Context) (websearch.Settings, error) { return websearch.Settings{Off: off}, nil },
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

	assert.Equal(t, []string{websearch.ProviderExa, websearch.ProviderParallel}, failed,
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
	require.Len(t, a.Errors, 3)
	assert.Equal(t, websearch.Failure{Provider: "codex", Reason: "its subscription: rate limited (429)"}, a.Errors[2])

	a, err = svc.Search(t.Context(), "second", 0, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"codex:first:5", "claude:first:5", "claude:second:5"}, subs.calls(),
		"a resting search is not asked")
	assert.Contains(t, a.Errors[2].Reason, "its subscription: rate limited until")

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
