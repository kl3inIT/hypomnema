package modelgateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// loginAccount adds an account logged in on the control plane.
func loginAccount(t *testing.T, g chatGateway, runtime, label string) orchestration.Account {
	t.Helper()

	a, err := orchestration.New(g.db, platform.SystemClock{}, platform.NewID).SaveAccountLogin(t.Context(),
		orchestration.AccountLogin{Runtime: runtime, ExternalID: label, Label: label},
		func(string) string { return vault.Ref(label) })
	require.NoError(t, err)

	return a
}

// Scenario (issue #1277): the owner limits a key to two Claude accounts and gives it its own search order. Both come
// back with the key, in every read of it; an account or a provider that is not one is refused. "*" sets the key back
// to every account and an empty order to the order of Settings. An account removed leaves the key with none, not
// with every one.
func TestAKeysOwnRoutingIsKeptAndChecked(t *testing.T) {
	t.Parallel()

	upstream, _ := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, upstream)
	first, second := loginAccount(t, g, "claude", "first"), loginAccount(t, g, "claude", "second")
	ctx := t.Context()

	k, _, err := g.svc.AddRoutedKey(ctx, "beyondpilot", nil, nil, modelgateway.KeyRouting{
		Accounts:    []string{first.ID, second.ID},
		SearchOrder: []string{websearch.ProviderCodex, websearch.ProviderClaude, websearch.ProviderExa},
	})
	require.NoError(t, err)
	assert.True(t, k.OwnAccounts())
	assert.ElementsMatch(t, []modelgateway.KeyAccount{
		{ID: first.ID, Runtime: "claude"}, {ID: second.ID, Runtime: "claude"},
	}, k.Accounts)
	assert.Equal(t, []string{"codex", "claude", "exa"}, k.SearchOrder)

	keys, err := g.svc.ListKeys(ctx)
	require.NoError(t, err)
	i := slices.IndexFunc(keys, func(l modelgateway.Key) bool { return l.ID == k.ID })
	require.GreaterOrEqual(t, i, 0)
	assert.ElementsMatch(t, k.Accounts, keys[i].Accounts)
	assert.Equal(t, k.SearchOrder, keys[i].SearchOrder)

	for _, bad := range []modelgateway.KeyRouting{
		{Accounts: []string{"acc_missing"}},
		{Accounts: []string{first.ID, first.ID}},
		{Accounts: []string{"*", first.ID}},
		{Accounts: []string{}},
		{SearchOrder: []string{"google"}},
		{SearchOrder: []string{"exa", "exa"}},
	} {
		_, _, err := g.svc.AddRoutedKey(ctx, "bad", nil, nil, bad)
		require.ErrorIs(t, err, modelgateway.ErrInvalid, "%+v", bad)
	}

	k, err = g.svc.UpdateKey(ctx, k.ID, modelgateway.KeyUpdate{
		Accounts: []string{second.ID}, SearchOrder: []string{websearch.ProviderTavily},
	})
	require.NoError(t, err)
	assert.Equal(t, []modelgateway.KeyAccount{{ID: second.ID, Runtime: "claude"}}, k.Accounts)
	assert.Equal(t, []string{"tavily"}, k.SearchOrder)

	require.NoError(t, orchestration.New(g.db, platform.SystemClock{}, platform.NewID).
		RemoveAccountLogin(ctx, second.ID))
	keys, err = g.svc.ListKeys(ctx)
	require.NoError(t, err)
	i = slices.IndexFunc(keys, func(l modelgateway.Key) bool { return l.ID == k.ID })
	assert.True(t, keys[i].OwnAccounts(), "a key whose accounts are gone uses none")
	assert.Empty(t, keys[i].Accounts)

	k, err = g.svc.UpdateKey(ctx, k.ID, modelgateway.KeyUpdate{
		Accounts: []string{modelgateway.AllAccounts}, SearchOrder: []string{},
	})
	require.NoError(t, err)
	assert.False(t, k.OwnAccounts())
	assert.Nil(t, k.Accounts)
	assert.Empty(t, k.SearchOrder)
}

// accountsSeen stands for the accounts' service: it keeps the accounts each request may use, as the gateway passes
// them, and has none logged in.
type accountsSeen struct {
	accounts

	mu   sync.Mutex
	seen [][]string
}

func (a *accountsSeen) Token(ctx context.Context, runtime, session string, skip ...string,
) (modelgateway.AccountToken, error) {
	ids, ok := modelgateway.KeyAccounts(ctx)
	if !ok {
		ids = []string{"*"}
	}

	a.mu.Lock()
	a.seen = append(a.seen, append([]string{runtime}, ids...))
	a.mu.Unlock()

	return modelgateway.AccountToken{}, modelgateway.ErrNoAccount
}

func (a *accountsSeen) asked() [][]string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.seen)
}

// Scenario (issue #1277): a key limited to one Claude account searches with its own order, Claude first. The
// search is asked of a Claude account among the key's alone, before Exa; with none of them able to search, Exa
// answers. Another key searches in the order of Settings: Exa answers first and no subscription is asked. The
// limited key still chats with a model of a provider that takes no account.
func TestAKeySearchesInItsOrderUnderItsAccounts(t *testing.T) {
	t.Parallel()

	var exaCalls atomic.Int32
	exa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exaCalls.Add(1)
		exaFound(w, r)
	}))
	t.Cleanup(exa.Close)

	off := websearch.Settings{Off: []string{"parallel", "firecrawl", "tavily", "you"}}
	web := websearch.New(websearch.Config{
		ExaMCPURL: exa.URL, Settings: func(context.Context) (websearch.Settings, error) { return off, nil },
	})
	upstream, _ := provider(t, plain(http.StatusOK, answer))
	subs := &accountsSeen{}
	g := newChatGateway(t, modelgateway.Config{Web: web, Subscriptions: subs}, upstream)
	web.WithSubscriptions(g.server.SubscriptionSearch())

	account := loginAccount(t, g, "claude", "first")
	_, key, err := g.svc.AddRoutedKey(t.Context(), "beyondpilot", []string{"gpt"}, nil, modelgateway.KeyRouting{
		Accounts: []string{account.ID}, SearchOrder: []string{websearch.ProviderClaude},
	})
	require.NoError(t, err)

	status, _, body := g.post(t, "/v1/search", key, `{"query":"hypomnema"}`)
	assert.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, `"provider":"exa"`)
	assert.Equal(t, [][]string{{"claude", account.ID}}, subs.asked(), "Claude is asked first, under the key's account")
	assert.Equal(t, int32(1), exaCalls.Load())

	status, _, body = g.post(t, "/v1/search", g.key, `{"query":"another"}`)
	assert.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, `"provider":"exa"`)
	assert.Len(t, subs.asked(), 1, "another key keeps the order of Settings")

	status, _, body = g.call(t, key, `{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`)
	assert.Equal(t, http.StatusOK, status, body)
}
