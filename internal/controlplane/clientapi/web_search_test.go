package clientapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
// switches and the order are the gateway's, the keys the vault's, the rests
// the search's.
type webSearchOver struct {
	web     *websearch.Service
	gateway *modelgateway.Service
}

func (w webSearchOver) Chain(ctx context.Context) ([]websearch.Link, error) { return w.web.Chain(ctx) }

func (w webSearchOver) SetSearchProvider(ctx context.Context, provider string, on bool) error {
	return w.gateway.SetSearchProvider(ctx, provider, on)
}

func (w webSearchOver) SetSearchOrder(ctx context.Context, providers []string) error {
	return w.gateway.SetSearchOrder(ctx, providers)
}

// keptSecrets is a vault in memory: the values kept under each name.
type keptSecrets struct {
	noSecrets

	mu     sync.Mutex
	values map[string]string
}

func (k *keptSecrets) List(context.Context) ([]SecretInfo, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	var out []SecretInfo
	for name := range k.values {
		out = append(out, SecretInfo{Name: name, Kind: "generic", Version: 1})
	}

	return out, nil
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

// keys are the vault's web search keys, as the control plane reads them.
func (k *keptSecrets) keys(context.Context) ([]websearch.Key, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	var out []websearch.Key

	for name, value := range k.values {
		if p, _, ok := websearch.ParseKeyName(name); ok {
			out = append(out, websearch.Key{Provider: p, Name: name, Value: value})
		}
	}

	return out, nil
}

// Scenario: the owner opens Settings → Gateway → Web search (issue #393).
// The chain lists the keyed providers in the default order (Exa, Firecrawl,
// Parallel, Tavily, TinyFish), none with a key yet; the keyless ring, where
// Parallel answered 429 and rests until its Retry-After; then the Codex
// account signed in here and ready, the Claude account signed in on a
// machine that cannot search, and no Antigravity account. The owner adds two
// Exa keys, which go into the vault as web-search-exa and web-search-exa-2
// and never come back; removes the first, and again, which is fine; cannot
// remove a key under another provider's name; drags Tavily first; turns
// Codex off and a search may no longer name it, then on again. A
// coordinator reads the chain and changes nothing.
func TestTheOwnerSeesAndChangesTheWebSearchChain(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	g := gatewayOver(t, w.svc)

	now := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	failing := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)

	parallel := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Retry-After", "120")
		rw.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(parallel.Close)

	vault := &keptSecrets{values: map[string]string{}}
	web := websearch.New(websearch.Config{
		Keys: vault.keys, Settings: g.SearchSettings, ExaMCPURL: failing.URL, ParallelMCPURL: parallel.URL,
		TavilyURL: failing.URL, FirecrawlURL: failing.URL, YouMCPURL: failing.URL,
		Now: func() time.Time { return now },
	}).WithSubscriptions(searchingAccounts{})

	codex, err := w.svc.AddAccount(ctx, "codex", "main")
	require.NoError(t, err)
	_, err = w.svc.AddAccount(ctx, "claude", "laptop")
	require.NoError(t, err)

	w.srv.WithAccounts(&signedIn{
		svc: w.svc, states: map[string]subscription.State{codex.ID: subscription.StateActive},
		removed: map[string]bool{},
	}).WithSecrets(vault).WithWebSearch(webSearchOver{web: web, gateway: g})

	// Parallel's 429 rests it; the other keyless ones fail; Codex answers this search.
	a, err := web.Search(ctx, "go 1.27", 0, "")
	require.NoError(t, err)
	assert.Equal(t, websearch.ProviderCodex, a.Provider)

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	listed, err := pi.GetWebSearchWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))

	rest := now.Add(2 * time.Minute)
	none := []openapi.WebSearchKey{}
	link := func(provider, source, state string) openapi.WebSearchLink {
		l := openapi.WebSearchLink{
			Provider: openapi.WebSearchProvider(provider), Source: openapi.WebSearchLinkSource(source),
			State: openapi.WebSearchLinkState(state), On: true,
		}
		if source == "key" {
			l.Keys = &none
		}

		return l
	}
	resting := link("parallel", "public", "resting")
	resting.RestingUntil = &rest
	resting.Rest = ptr(openapi.RateLimit)

	assert.Equal(t, []openapi.WebSearchLink{
		link("exa", "key", "not_set_up"), link("firecrawl", "key", "not_set_up"),
		link("parallel", "key", "not_set_up"), link("tavily", "key", "not_set_up"),
		link("tinyfish", "key", "not_set_up"),
		link("exa", "public", "ready"), resting, link("firecrawl", "public", "ready"),
		link("tavily", "public", "ready"), link("you", "public", "ready"),
		link("codex", "subscription", "ready"), link("claude", "subscription", "not_set_up"),
		link("antigravity", "subscription", "not_set_up"),
	}, listed.JSON200.Chain)

	key := "exa-key"

	refused, err := pi.AddWebSearchKeyWithResponse(ctx, openapi.WebSearchKeyedProviderExa,
		openapi.WebSearchNewKey{Key: &key})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	padded := " exa-key \n"
	added, err := owner.AddWebSearchKeyWithResponse(ctx, openapi.WebSearchKeyedProviderExa,
		openapi.WebSearchNewKey{Key: &padded})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, added.StatusCode(), string(added.Body))
	assert.Equal(t, "web-search-exa", added.JSON201.Name, "the first Exa key keeps the name it always had")
	assert.Equal(t, "exa-key", vault.value("web-search-exa"))
	assert.NotContains(t, string(added.Body), `"exa-key"`, "the key is never answered")

	second := "exa-key-2"
	added, err = owner.AddWebSearchKeyWithResponse(ctx, openapi.WebSearchKeyedProviderExa,
		openapi.WebSearchNewKey{Key: &second})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, added.StatusCode(), string(added.Body))
	assert.Equal(t, "web-search-exa-2", added.JSON201.Name)

	exa := added.JSON201.WebSearch.Chain[0]
	assert.Equal(t, openapi.WebSearchLinkStateReady, exa.State)
	require.Len(t, val(exa.Keys), 2)
	assert.Equal(t, openapi.WebSearchKey{Name: "web-search-exa", State: openapi.WebSearchKeyStateReady},
		val(exa.Keys)[0])

	empty := ""
	blank, err := owner.AddWebSearchKeyWithResponse(ctx, openapi.WebSearchKeyedProviderTavily,
		openapi.WebSearchNewKey{Key: &empty})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, blank.StatusCode())

	removed, err := owner.RemoveWebSearchKeyWithResponse(ctx, openapi.WebSearchKeyedProviderExa, "web-search-exa")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, removed.StatusCode(), string(removed.Body))
	assert.Empty(t, vault.value("web-search-exa"))
	assert.Equal(t, "web-search-exa-2", val(removed.JSON200.Chain[0].Keys)[0].Name)

	again, err := owner.RemoveWebSearchKeyWithResponse(ctx, openapi.WebSearchKeyedProviderExa, "web-search-exa")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, again.StatusCode(), "a key that is not there is removed already")

	other, err := owner.RemoveWebSearchKeyWithResponse(ctx, openapi.WebSearchKeyedProviderTavily, "web-search-exa-2")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, other.StatusCode(), "a key is removed under its own provider")
	assert.Equal(t, "exa-key-2", vault.value("web-search-exa-2"))

	order := openapi.WebSearchOrder{Providers: []openapi.WebSearchKeyedProvider{
		"tavily", "exa", "firecrawl", "parallel", "tinyfish",
	}}

	notOrdered, err := pi.SetWebSearchOrderWithResponse(ctx, order)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, notOrdered.StatusCode())

	ordered, err := owner.SetWebSearchOrderWithResponse(ctx, order)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, ordered.StatusCode(), string(ordered.Body))

	var providers []openapi.WebSearchProvider
	for _, l := range ordered.JSON200.Chain[:5] {
		providers = append(providers, l.Provider)
	}

	assert.Equal(t, []openapi.WebSearchProvider{"tavily", "exa", "firecrawl", "parallel", "tinyfish"}, providers)

	twice, err := owner.SetWebSearchOrderWithResponse(ctx, openapi.WebSearchOrder{
		Providers: []openapi.WebSearchKeyedProvider{"exa", "exa", "firecrawl", "parallel", "tinyfish"},
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, twice.StatusCode(), "each keyed provider is named once")

	notSwitched, err := pi.SetWebSearchProviderWithResponse(ctx, openapi.WebSearchProviderCodex,
		openapi.WebSearchSwitch{On: false})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, notSwitched.StatusCode())

	switched, err := owner.SetWebSearchProviderWithResponse(ctx, openapi.WebSearchProviderCodex,
		openapi.WebSearchSwitch{On: false})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, switched.StatusCode(), string(switched.Body))

	codexAt := slices.IndexFunc(switched.JSON200.Chain, func(l openapi.WebSearchLink) bool {
		return l.Provider == openapi.WebSearchProviderCodex
	})
	assert.Equal(t, openapi.WebSearchLinkStateOff, switched.JSON200.Chain[codexAt].State)

	_, err = web.Search(ctx, "go", 0, websearch.ProviderCodex)
	require.ErrorIs(t, err, websearch.ErrInvalid, "a provider that is off may not be named")

	switched, err = owner.SetWebSearchProviderWithResponse(ctx, openapi.WebSearchProviderCodex,
		openapi.WebSearchSwitch{On: true})
	require.NoError(t, err)
	assert.Equal(t, openapi.WebSearchLinkStateReady, switched.JSON200.Chain[codexAt].State)
}
