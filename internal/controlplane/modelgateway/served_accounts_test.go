package modelgateway_test

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// rosterGoogle are Antigravity accounts that say which models each is served (modelgateway.ModelTokens).
type rosterGoogle struct {
	googleAccount

	served map[string][]string
	asked  []string
}

func (a *rosterGoogle) TokenFor(
	ctx context.Context, runtime, model, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	a.mu.Lock()
	a.asked = append(a.asked, model)

	var others []string

	for _, id := range a.ids {
		if !slices.Contains(a.served[id], model) {
			others = append(others, id)
		}
	}

	none := len(others) == len(a.ids)
	a.mu.Unlock()

	if none {
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
	assert.Contains(t, body, "gemini-3.8-flash-high")
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
