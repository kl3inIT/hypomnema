package modelgateway_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// rosterGoogle are Antigravity accounts that say which models each is served (modelgateway.ModelTokens).
type rosterGoogle struct {
	googleAccount

	served map[string][]string
	// unknown are the accounts whose list is not known yet: while no account has the model, TokenFor says to try
	// again in 30 s, as subscription.TokenFor does.
	unknown []string
	asked   []string
	// waits are the moments each TokenFor's request stops waiting for the lists (AccountWaitEnds), zero when none.
	waits []time.Time
}

func (a *rosterGoogle) TokenFor(
	ctx context.Context, runtime, model, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	a.mu.Lock()
	a.asked = append(a.asked, model)

	wait, _ := modelgateway.AccountWaitEnds(ctx)
	a.waits = append(a.waits, wait)

	var others []string

	for _, id := range a.ids {
		if !slices.Contains(a.served[id], model) {
			others = append(others, id)
		}
	}

	none, unknown := len(others) == len(a.ids), len(a.unknown) > 0
	a.mu.Unlock()

	switch {
	case none && unknown:
		return modelgateway.AccountToken{}, &modelgateway.CoolingError{
			Until: time.Now().Add(30 * time.Second), ListUnknown: true,
		}
	case none:
		return modelgateway.AccountToken{}, modelgateway.ErrModelNotServed
	}

	return a.Token(ctx, runtime, session, append(skip, others...)...)
}

// Scenario (issue #814): the first Antigravity account is not served the model asked for, the second is. The chat
// is served under the second, asked for by the model.
func TestAChatGoesToAnAntigravityAccountServedItsModel(t *testing.T) {
	t.Parallel()

	backend, got := cloudCode(t, stream(geminiEvents...))
	account := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_pro", "acc_ultra"}}},
		served:        map[string][]string{"acc_ultra": {"gemini-3.8-flash-high"}},
	}
	g := newAntigravityGateway(t, backend, account)

	status, _, body := g.call(t, g.key, askFlash+`,"stream":true}`)
	require.Equal(t, http.StatusOK, status, body)

	sentHeaders, _ := got.last(t)
	assert.Equal(t, "Bearer acc_ultra-access-0", sentHeaders.Get("Authorization"))

	account.mu.Lock()
	defer account.mu.Unlock()

	assert.Equal(t, []string{"gemini-3.8-flash-high"}, account.asked)
}

// Scenario (issue #814): no Antigravity account is served the model asked for. The client is told so, with a 404,
// and no account is called.
func TestAChatForAModelNoAccountIsServedIsRefused(t *testing.T) {
	t.Parallel()

	backend, got := cloudCode(t, stream(geminiEvents...))
	account := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_pro", "acc_ultra"}}},
		served:        map[string][]string{"acc_ultra": {"gemini-4-carbon-high"}},
	}
	g := newAntigravityGateway(t, backend, account)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusNotFound, status, body)
	assert.Contains(t, body, "model_not_served")
	assert.Contains(t, body, `\"flash\"`)
	assert.Zero(t, got.calls(), "no account was called")
}

// Scenario (issue #814): a request for a Codex or Antigravity account's token names the model it will call; one that
// does not is refused before any account is picked.
func TestATokenWithoutAModelIsRefused(t *testing.T) {
	t.Parallel()

	backend, _ := cloudCode(t, stream(geminiEvents...))
	account := &rosterGoogle{googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_1"}}}}
	g := newAntigravityGateway(t, backend, account)

	for _, runtime := range []string{"codex", "antigravity"} {
		require.ErrorIs(t, g.server.TokenWithoutModel(t.Context(), runtime), modelgateway.ErrNoModel, runtime)
	}

	account.mu.Lock()
	defer account.mu.Unlock()

	assert.Empty(t, account.asked)
}

func (a *rosterGoogle) TokenAfterRefusalFor(
	ctx context.Context, refused modelgateway.AccountToken, model string,
) (modelgateway.AccountToken, error) {
	a.mu.Lock()
	serves := slices.Contains(a.served[refused.AccountID], model)
	a.mu.Unlock()

	if !serves {
		return modelgateway.AccountToken{}, modelgateway.ErrModelNotServed
	}

	return a.TokenAfterRefusal(ctx, refused)
}

// refusingRosters are accounts whose lists name no model (modelgateway.ModelTokens): every TokenFor is refused, and
// recorded.
type refusingRosters struct {
	accounts

	asked []string
}

func (a *refusingRosters) TokenFor(
	_ context.Context, _, model, _ string, _ ...string,
) (modelgateway.AccountToken, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.asked = append(a.asked, model)

	return modelgateway.AccountToken{}, modelgateway.ErrModelNotServed
}

func (a *refusingRosters) TokenAfterRefusalFor(
	context.Context, modelgateway.AccountToken, string,
) (modelgateway.AccountToken, error) {
	return modelgateway.AccountToken{}, modelgateway.ErrModelNotServed
}

// Scenario (issue #814; owner, 2026-11-18): Codex's lists name only the models of its picker, not its search
// model. A search through a Codex account goes to its accounts as before, not by their lists.
func TestACodexSearchIsNotRoutedByTheAccountsLists(t *testing.T) {
	t.Parallel()

	backend, _ := codexBackend(t, stream(codexSearchEvents...))
	subs := &refusingRosters{}
	search := searchGateway(t, modelgateway.Config{Subscriptions: subs, CodexBaseURL: backend.URL})

	_, err := search.Search(t.Context(), websearch.ProviderCodex, "go 1.27", 5)
	require.NoError(t, err)

	subs.mu.Lock()
	defer subs.mu.Unlock()

	assert.Empty(t, subs.asked)
}

// Scenario (issue #814; owner, 2026-11-18): Codex's lists do not name its image model. An image is made under the
// Codex accounts as before, not by their lists.
func TestACodexImageIsNotRoutedByTheAccountsLists(t *testing.T) {
	t.Parallel()

	backend, _ := imageBackend(t, "/images/generations", answerWith(http.StatusOK, codexImageAnswer))
	subs := &rosterAccounts{
		imageAccounts: imageAccounts{all: []imageAccount{{id: "acc_1", runtime: "codex", plan: "plus"}}},
	}
	g := newImageGateway(t, subs, backend.URL, "")

	status, _, body := g.post(t, "/v1/images/generations", g.key, `{"prompt":"a red fox"}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Empty(t, subs.askedFor())
}

// The fakes say which models each account is served, as the control plane's accounts do.
var (
	_ modelgateway.ModelTokens = (*rosterGoogle)(nil)
	_ modelgateway.ModelTokens = (*rosterAccounts)(nil)
	_ modelgateway.ModelTokens = (*refusingRosters)(nil)
)

// Scenario (review of #832, round 4): Google refuses the first account's token, and by then that account's list no
// longer has the model. The account is not refreshed and asked again: the request goes on to the second account,
// whose list has it.
func TestARefusedAccountIsAskedAgainOnlyForAModelItsListHas(t *testing.T) {
	t.Parallel()

	account := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_a", "acc_b"}}},
		served: map[string][]string{
			"acc_a": {"gemini-3.8-flash-high"}, "acc_b": {"gemini-3.8-flash-high"},
		},
	}

	answer := stream(geminiEvents...)
	backend, got := cloudCode(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer acc_a-access-0" {
			account.mu.Lock()
			account.served["acc_a"] = nil
			account.mu.Unlock()

			http.Error(w, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`, http.StatusUnauthorized)

			return
		}

		answer(w, r)
	})
	g := newAntigravityGateway(t, backend, account)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusOK, status, body)

	header, _ := got.last(t)
	assert.Equal(t, "Bearer acc_b-access-0", header.Get("Authorization"))
	assert.Equal(t, 2, got.calls(), "the first account was not asked again")
}

// Scenario (review of #832, round 4): an Antigravity image is asked for by name and no account's list has the
// image model. The client is told the model is not served, not that the image was refused.
func TestAnAntigravityImageNoAccountIsServedIsNotServed(t *testing.T) {
	t.Parallel()

	backend, got := imageBackend(t, "/v1internal:generateContent", answerWith(http.StatusOK, antigravityImageAnswer))
	subs := &rosterAccounts{
		imageAccounts: imageAccounts{all: []imageAccount{
			{id: "g_pro", runtime: "antigravity", plan: "g1-pro-tier", project: "bright-wave-1234"},
		}},
	}
	g := newImageGateway(t, subs, "", backend.URL)

	status, _, body := g.post(t, "/v1/images/generations", g.key, `{"prompt":"a fox","model":"gemini-3-pro-image"}`)
	assert.Equal(t, http.StatusNotFound, status, body)
	assert.Contains(t, body, `"code":"model_not_served"`)
	assert.Zero(t, got.calls())
}

// Scenario (review of #832, round 4): on /responses, Codex refuses the first account's token, and by then that
// account's list no longer has the model. The request goes on to the second account instead of refreshing the
// first and asking it again.
func TestARefusedCodexAccountIsAskedAgainOnlyForAModelItsListHas(t *testing.T) {
	t.Parallel()

	two := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_a", "acc_b"}}},
		served:        map[string][]string{"acc_a": {"gpt-6.1-sol"}, "acc_b": {"gpt-6.1-sol"}},
	}

	backend, got := codexBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer acc_a-access-0" {
			two.mu.Lock()
			two.served["acc_a"] = nil
			two.mu.Unlock()

			plain(http.StatusUnauthorized, `{"error":{"code":"token_expired"}}`)(w, r)

			return
		}

		stream(helloEvents...)(w, r)
	})
	g := newCodexGateway(t, backend, two)

	status, _, body := g.respond(t, g.key, codexAsks)
	require.Equal(t, http.StatusOK, status, body)

	header, _ := got.last(t)
	assert.Equal(t, "Bearer acc_b-access-0", header.Get("Authorization"))
	assert.Equal(t, 2, got.calls())
	assert.Zero(t, two.refreshes, "the first account was not refreshed")
}

// Scenario (review of #832, round 5): the first Antigravity account is near its quota, so it is held while the
// second is tried. The second answers 503, and by then the first account's list no longer has the model. The held
// account is asked again before it is sent to, and is not called.
func TestAHeldAccountIsAskedAgainBeforeItIsSentTo(t *testing.T) {
	t.Parallel()

	var armed atomic.Bool

	account := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{
			ids:   []string{"acc_1", "acc_2"},
			quota: map[string]map[string]float64{"acc_1": {"gemini-3.8-flash-high": 0.05}, "acc_2": {}},
		}},
		served: map[string][]string{"acc_1": {"gemini-3.8-flash-high"}, "acc_2": {"gemini-3.8-flash-high"}},
	}

	answer := stream(geminiEvents...)
	backend, got := cloudCode(t, func(w http.ResponseWriter, r *http.Request) {
		if armed.Load() && r.Header.Get("Authorization") == "Bearer acc_2-access-0" {
			account.mu.Lock()
			account.served["acc_1"] = nil
			account.mu.Unlock()

			http.Error(w, `{"error":{"code":503,"status":"UNAVAILABLE"}}`, http.StatusServiceUnavailable)

			return
		}

		answer(w, r)
	})
	g := newAntigravityGateway(t, backend, account)

	// The first account serves until its quota is read; then the second serves while the first is held.
	status, _, body := g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Eventually(t, func() bool {
		g.call(t, g.key, askFlash+`}`)
		h, _ := got.last(t)

		return h.Get("Authorization") == "Bearer acc_2-access-0"
	}, 5*time.Second, 20*time.Millisecond)

	armed.Store(true)
	before := got.calls()

	status, _, body = g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusServiceUnavailable, status, body)
	assert.Equal(t, before+1, got.calls(), "only the second account was called")
}

func (a *rosterGoogle) Serves(_ context.Context, accountID, model string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Contains(a.served[accountID], model)
}

func (a *rosterAccounts) Serves(_ context.Context, accountID, model string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Contains(a.served[accountID], model)
}

func (a *refusingRosters) Serves(context.Context, string, string) bool { return false }

// Scenario (review of #832, round 6): one chat request picks twice, the first account answering 503. Both picks
// share the one moment the request stops waiting for the accounts' lists, set when it entered the gateway.
func TestEveryPickOfARequestSharesItsWait(t *testing.T) {
	t.Parallel()

	account := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_a", "acc_b"}}},
		served: map[string][]string{
			"acc_a": {"gemini-3.8-flash-high"}, "acc_b": {"gemini-3.8-flash-high"},
		},
	}

	answer := stream(geminiEvents...)
	backend, _ := cloudCode(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer acc_a-access-0" {
			http.Error(w, `{"error":{"code":503,"status":"UNAVAILABLE"}}`, http.StatusServiceUnavailable)

			return
		}

		answer(w, r)
	})
	g := newAntigravityGateway(t, backend, account)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusOK, status, body)

	account.mu.Lock()
	defer account.mu.Unlock()

	require.Len(t, account.waits, 2)
	assert.False(t, account.waits[0].IsZero())
	assert.Equal(t, account.waits[0], account.waits[1], "one wait for the whole request")
}

// Scenario (review of #832, round 6): a held account is sent the very token it was given, not asked for a new one,
// so that what came with it (such as a retry it claimed) stays.
func TestAHeldAccountIsSentItsOwnToken(t *testing.T) {
	t.Parallel()

	account := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{
			ids:   []string{"acc_1", "acc_2"},
			quota: map[string]map[string]float64{"acc_1": {"gemini-3.8-flash-high": 0.05}, "acc_2": {}},
		}},
		served: map[string][]string{"acc_1": {"gemini-3.8-flash-high"}, "acc_2": {"gemini-3.8-flash-high"}},
	}

	var armed atomic.Bool

	answer := stream(geminiEvents...)
	backend, got := cloudCode(t, func(w http.ResponseWriter, r *http.Request) {
		if armed.Load() && r.Header.Get("Authorization") == "Bearer acc_2-access-0" {
			http.Error(w, `{"error":{"code":503,"status":"UNAVAILABLE"}}`, http.StatusServiceUnavailable)

			return
		}

		answer(w, r)
	})
	g := newAntigravityGateway(t, backend, account)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusOK, status, body)
	require.Eventually(t, func() bool {
		g.call(t, g.key, askFlash+`}`)
		h, _ := got.last(t)

		return h.Get("Authorization") == "Bearer acc_2-access-0"
	}, 5*time.Second, 20*time.Millisecond)

	armed.Store(true)

	account.mu.Lock()
	before := len(account.asked)
	account.mu.Unlock()

	status, _, body = g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusOK, status, body)

	h, _ := got.last(t)
	assert.Equal(t, "Bearer access-0", h.Get("Authorization"), "the held account served")

	account.mu.Lock()
	defer account.mu.Unlock()

	assert.Len(t, account.asked, before+3, "two picks and the end of the accounts; none for the held account")
}

// Scenario (review of #832, round 7): Google refuses the only account whose list has the model, and by then its list
// no longer has it. The account is not refreshed and asked again, and the pick that follows finds that no account's
// list has the model: the client is told the model is not served, not given the account's 401.
func TestARefusedAccountThatLostTheModelLeavesItNotServed(t *testing.T) {
	t.Parallel()

	account := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_a", "acc_b"}}},
		served:        map[string][]string{"acc_a": {"gemini-3.8-flash-high"}},
	}

	backend, got := cloudCode(t, func(w http.ResponseWriter, _ *http.Request) {
		account.mu.Lock()
		account.served["acc_a"] = nil
		account.mu.Unlock()

		http.Error(w, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`, http.StatusUnauthorized)
	})
	g := newAntigravityGateway(t, backend, account)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusNotFound, status, body)
	assert.Contains(t, body, `"code":"model_not_served"`)
	assert.Equal(t, 1, got.calls(), "the account was not asked again")
}

// Scenario (review of #832, round 7): a model has two targets, and no Antigravity account's list has the first's
// model. The gateway's own 404 for that target does not answer for the model: its next target serves the request.
func TestATargetNoAccountServesGivesWayToTheNext(t *testing.T) {
	t.Parallel()

	backend, got := cloudCode(t, stream(geminiEvents...))
	account := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_pro", "acc_ultra"}}},
		served:        map[string][]string{"acc_ultra": {"gemini-4-carbon-high"}},
	}
	second, served := provider(t, plain(http.StatusOK, answer))
	g := newAntigravityGateway(t, backend, account, second)

	status, _, body := g.call(t, g.key, askFlash+`}`)
	require.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, answer, body) //nolint:testifylint // byte for byte: the answer is passed on, not re-encoded
	assert.Zero(t, got.calls(), "no Antigravity account was called")
	assert.Equal(t, 1, served.calls(), "the next target served")
}

// Scenario (review of #832, round 7): a subscription model asked for by its provider's name, with no account's list
// having it, is answered alike whether the provider still lists it or has taken it off since (the hourly reading,
// issue #814): 404 model_not_served, on chat and on Responses, and no account is called.
func TestAModelNoAccountServesIsAnsweredAlikeOnOrOffTheList(t *testing.T) {
	t.Parallel()

	google, googleCalls := cloudCode(t, stream(geminiEvents...))
	chat := newAntigravityGateway(t, google, &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_pro", "acc_ultra"}}},
		served:        map[string][]string{"acc_ultra": {"gemini-3.1-pro-high"}},
	})

	for _, model := range []string{
		"antigravity-subscription/gemini-3.8-flash-high", // on the provider's list
		"antigravity-subscription/gemini-4-carbon-high",  // taken off it
	} {
		status, _, body := chat.call(t, chat.key, `{"model":"`+model+`","messages":[{"role":"user","content":"Hi"}]}`)
		assert.Equal(t, http.StatusNotFound, status, model)
		assert.Contains(t, body, `"code":"model_not_served"`, model)
	}

	openAI, codexCalls := codexBackend(t, stream(helloEvents...))
	responses := newCodexGateway(t, openAI, &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_a", "acc_b"}}},
		served:        map[string][]string{"acc_a": {"gpt-6.1-sol"}},
	})

	_, anyModel, err := responses.svc.AddKey(t.Context(), "tool", nil, nil)
	require.NoError(t, err)

	for _, model := range []string{
		"codex-subscription/gpt-6-astra", // on the provider's list
		"codex-subscription/gpt-6-nova",  // taken off it
	} {
		status, _, body := responses.respond(t, anyModel, strings.Replace(codexAsks, `"sol"`, `"`+model+`"`, 1))
		assert.Equal(t, http.StatusNotFound, status, model)
		assert.Contains(t, body, `"code":"model_not_served"`, model)
	}

	assert.Zero(t, googleCalls.calls(), "no account was called")
	assert.Zero(t, codexCalls.calls(), "no account was called")
}

// Scenario (review of #949): Google refuses the only account whose list has the model, and by then its list no longer
// has it, while the other account's list is not known yet. The client is told to try again in 30 s, as at the start of
// a pick, not given the account's 401.
func TestARefusedAccountThatLostTheModelWhileAListIsUnknownIsTriedAgain(t *testing.T) {
	t.Parallel()

	account := &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_a", "acc_b"}}},
		served:        map[string][]string{"acc_a": {"gemini-3.8-flash-high"}},
		unknown:       []string{"acc_b"},
	}

	backend, got := cloudCode(t, func(w http.ResponseWriter, _ *http.Request) {
		account.mu.Lock()
		account.served["acc_a"] = nil
		account.mu.Unlock()

		http.Error(w, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`, http.StatusUnauthorized)
	})
	g := newAntigravityGateway(t, backend, account)

	status, header, body := g.call(t, g.key, askFlash+`}`)
	assert.Equal(t, http.StatusTooManyRequests, status, body)
	assert.Equal(t, "30", header.Get("Retry-After"))
	assert.Equal(t, 1, got.calls(), "the account was not asked again")
}

// Scenario (review of #949): while an Antigravity account's list is not known yet and no known list has the model,
// a model asked for by its provider's name is told to try again in 30 s, whether the provider still publishes it or
// not; a name the provider never published is not refused before the lists are known.
func TestAModelOnOrOffTheListWaitsForAListNotKnownYet(t *testing.T) {
	t.Parallel()

	google, calls := cloudCode(t, stream(geminiEvents...))
	chat := newAntigravityGateway(t, google, &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_pro", "acc_ultra"}}},
		served:        map[string][]string{"acc_pro": {"gemini-3.1-pro-high"}},
		unknown:       []string{"acc_ultra"},
	})

	for _, model := range []string{
		"antigravity-subscription/gemini-3.8-flash-high", // on the provider's list
		"antigravity-subscription/gemini-4-carbon-high",  // not on it
	} {
		status, header, body := chat.call(t, chat.key,
			`{"model":"`+model+`","messages":[{"role":"user","content":"Hi"}]}`)
		assert.Equal(t, http.StatusTooManyRequests, status, model+": "+body)
		assert.Equal(t, "30", header.Get("Retry-After"), model)
	}

	assert.Zero(t, calls.calls(), "no account was called")
}

// Scenario (review of #949): a model the provider does not publish yet, but an account's own list has (the hourly
// reading has not caught up), is served by that account.
func TestAModelAnAccountHasIsServedBeforeItIsPublished(t *testing.T) {
	t.Parallel()

	google, got := cloudCode(t, stream(geminiEvents...))
	chat := newAntigravityGateway(t, google, &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_pro", "acc_ultra"}}},
		served:        map[string][]string{"acc_ultra": {"gemini-4-carbon-high"}},
	})

	status, _, body := chat.call(t, chat.key,
		`{"model":"antigravity-subscription/gemini-4-carbon-high","messages":[{"role":"user","content":"Hi"}]}`)
	require.Equal(t, http.StatusOK, status, body)

	header, _ := got.last(t)
	assert.Equal(t, "Bearer acc_ultra-access-0", header.Get("Authorization"))
}

// Scenario (review of #949, round 2): the Antigravity provider keeps Gemini 3.8 and up (min_versions, issue #642) and
// publishes only gemini-3.8-flash-high. An account's own list still has gemini-3.1-pro-high, but a request for it by
// its provider's name is told the model is not served, and no account is called: an unpublished name is routed by the
// accounts' lists only past the provider's version floor.
func TestAnUnpublishedModelBelowTheVersionFloorIsNotServed(t *testing.T) {
	t.Parallel()

	google, calls := cloudCode(t, stream(geminiEvents...))
	chat := newAntigravityGateway(t, google, &rosterGoogle{
		googleAccount: googleAccount{accounts: accounts{ids: []string{"acc_pro"}}},
		served:        map[string][]string{"acc_pro": {"gemini-3.8-flash-high", "gemini-3.1-pro-high"}},
	})

	orch := orchestration.New(chat.db, platform.SystemClock{}, platform.NewID)
	_, err := orch.Profiles().ApplyProvider(t.Context(), "antigravity-subscription", "name: antigravity-subscription\n"+
		"kind: antigravity_subscription\nmin_versions: {gemini: \"3.8\"}\nmodels: [gemini-3.8-flash-high]\n")
	require.NoError(t, err)

	status, _, body := chat.call(t, chat.key,
		`{"model":"antigravity-subscription/gemini-3.1-pro-high","messages":[{"role":"user","content":"Hi"}]}`)
	assert.Equal(t, http.StatusNotFound, status, body)
	assert.Contains(t, body, `"code":"model_not_served"`)
	assert.Zero(t, calls.calls(), "no account was called")
}
