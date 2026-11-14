package clientapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// searchingAccounts stands for the gateway's search under the accounts: each
// answers with one result.
type searchingAccounts struct{}

func (searchingAccounts) Search(_ context.Context, provider, _ string, _ int) ([]websearch.Result, error) {
	return []websearch.Result{{Title: provider, URL: "https://" + provider + ".example/"}}, nil
}

// webSearchOver wires the search chain as the control plane does: the
// switches are the gateway's, the rests the search's.
type webSearchOver struct {
	web     *websearch.Service
	gateway *modelgateway.Service
}

func (w webSearchOver) Chain(ctx context.Context) ([]websearch.Link, error) { return w.web.Chain(ctx) }

func (w webSearchOver) SetSearchProvider(ctx context.Context, provider string, on bool) error {
	return w.gateway.SetSearchProvider(ctx, provider, on)
}

func (w webSearchOver) SetSearXNG(ctx context.Context, address string) error {
	return w.gateway.SetSearXNG(ctx, address)
}

// keptSecrets is a vault in memory: the values kept under each name.
type keptSecrets struct {
	noSecrets

	mu     sync.Mutex
	values map[string]string
}

func (k *keptSecrets) Put(_ context.Context, name string, values map[string]string) (SecretInfo, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.values[name] = values["value"]

	return SecretInfo{Name: name, Kind: "generic", Version: 1}, nil
}

func (k *keptSecrets) Delete(_ context.Context, name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if _, ok := k.values[name]; !ok {
		return fmt.Errorf("%w: secret %s", orchestration.ErrNotFound, name)
	}

	delete(k.values, name)

	return nil
}

func (k *keptSecrets) value(name string) string {
	k.mu.Lock()
	defer k.mu.Unlock()

	return k.values[name]
}

// Scenario: the owner opens Settings → Gateway → Web search (issue #354). The
// chain lists every provider in the order a search tries them, each with
// where its search comes from: Exa's key is not in the vault yet and no
// SearXNG is set, so those are not set up; Exa's public MCP failed and
// Parallel answered 429, so Parallel rests until its Retry-After; the Codex
// account is signed in here and is ready, the Claude account was signed in on
// a machine and cannot search, and no Antigravity account exists. The owner
// enters Exa's key, which goes into the vault and never comes back, and Exa
// with its key is ready first; he gives SearXNG's address, a wrong one
// refused; he turns Codex off and a search may no longer name it, then on
// again; he removes the key and the address. A coordinator reads the chain
// and changes nothing.
func TestTheOwnerSeesAndSwitchesTheWebSearchChain(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	g := gatewayOver(t, w.svc)

	now := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	exaMCP := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(exaMCP.Close)

	parallel := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Retry-After", "120")
		rw.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(parallel.Close)

	vault := &keptSecrets{values: map[string]string{}}
	web := websearch.New(websearch.Config{
		ExaKey:   func(context.Context) (string, error) { return vault.value("web-search-exa"), nil },
		Settings: g.SearchSettings, ExaMCPURL: exaMCP.URL, ParallelMCPURL: parallel.URL,
		Now: func() time.Time { return now },
	}).WithSubscriptions(searchingAccounts{})

	codex, err := w.svc.AddAccount(ctx, "codex", "main")
	require.NoError(t, err)
	_, err = w.svc.AddAccount(ctx, "claude", "laptop")
	require.NoError(t, err)

	w.srv.WithAccounts(&signedIn{
		svc: w.svc, states: map[string]subscription.State{codex.ID: subscription.StateActive},
		removed: map[string]bool{},
	}).WithSecrets(vault).WithWebSearch(webSearchOver{web: web, gateway: g}, "web-search-exa")

	// Parallel's 429 rests it; Codex answers this search.
	a, err := web.Search(ctx, "go 1.27", 0, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderCodex, a.Provider)

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	listed, err := pi.GetWebSearchWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))
	assert.Equal(t, "web-search-exa", listed.JSON200.ExaSecret)
	assert.Nil(t, listed.JSON200.SearxngUrl)

	rest := now.Add(2 * time.Minute)
	link := func(provider, source, state string, on bool) openapi.WebSearchLink {
		return openapi.WebSearchLink{
			Provider: openapi.WebSearchProvider(provider), Source: openapi.WebSearchLinkSource(source),
			State: openapi.WebSearchLinkState(state), On: on,
		}
	}
	resting := link("parallel", "public", "resting", true)
	resting.RestingUntil = &rest

	assert.Equal(t, []openapi.WebSearchLink{
		link("exa", "key", "not_set_up", true),
		link("searxng", "self_hosted", "not_set_up", true),
		link("exa", "public", "ready", true),
		resting,
		link("codex", "subscription", "ready", true),
		link("claude", "subscription", "not_set_up", true),
		link("antigravity", "subscription", "not_set_up", true),
	}, listed.JSON200.Chain)

	key := "exa-key"

	refused, err := pi.PutWebSearchExaKeyWithResponse(ctx, openapi.WebSearchExaKey{Key: &key})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	padded := " exa-key \n"
	changed, err := owner.PutWebSearchExaKeyWithResponse(ctx, openapi.WebSearchExaKey{Key: &padded})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, changed.StatusCode(), string(changed.Body))
	assert.Equal(t, "exa-key", vault.value("web-search-exa"))
	assert.NotContains(t, string(changed.Body), `"exa-key"`, "the key is never answered")
	assert.Equal(t, link("exa", "key", "ready", true), changed.JSON200.Chain[0])

	wrong, err := owner.PutWebSearchSearXNGWithResponse(ctx, openapi.WebSearchSearXNG{Url: "searx.lan:8888"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, wrong.StatusCode())

	searxng, err := owner.PutWebSearchSearXNGWithResponse(ctx, openapi.WebSearchSearXNG{Url: "http://10.0.0.5:8888/"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, searxng.StatusCode(), string(searxng.Body))
	assert.Equal(t, "http://10.0.0.5:8888", val(searxng.JSON200.SearxngUrl))
	assert.Equal(t, link("searxng", "self_hosted", "ready", true), searxng.JSON200.Chain[1])

	notSwitched, err := pi.SetWebSearchProviderWithResponse(ctx, openapi.WebSearchProviderCodex,
		openapi.WebSearchSwitch{On: false})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, notSwitched.StatusCode())

	switched, err := owner.SetWebSearchProviderWithResponse(ctx, openapi.WebSearchProviderCodex,
		openapi.WebSearchSwitch{On: false})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, switched.StatusCode(), string(switched.Body))
	assert.Equal(t, link("codex", "subscription", "off", false), switched.JSON200.Chain[4])

	_, err = web.Search(ctx, "go", 0, websearch.ProviderCodex)
	require.ErrorIs(t, err, websearch.ErrInvalid, "a provider that is off may not be named")

	switched, err = owner.SetWebSearchProviderWithResponse(ctx, openapi.WebSearchProviderCodex,
		openapi.WebSearchSwitch{On: true})
	require.NoError(t, err)
	assert.Equal(t, openapi.WebSearchLinkStateReady, switched.JSON200.Chain[4].State)

	removed, err := owner.DeleteWebSearchExaKeyWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, removed.StatusCode(), string(removed.Body))
	assert.Empty(t, vault.value("web-search-exa"))
	assert.Equal(t, openapi.WebSearchLinkStateNotSetUp, removed.JSON200.Chain[0].State)

	again, err := owner.DeleteWebSearchExaKeyWithResponse(ctx)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, again.StatusCode(), "a key that is not there is removed already")

	forgot, err := owner.DeleteWebSearchSearXNGWithResponse(ctx)
	require.NoError(t, err)
	assert.Nil(t, forgot.JSON200.SearxngUrl)
	assert.Equal(t, openapi.WebSearchLinkStateNotSetUp, forgot.JSON200.Chain[1].State)
}
