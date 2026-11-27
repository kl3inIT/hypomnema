package modelgateway

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// A key's own routing (issue #1277), stored next to the key (gateway_key_accounts, gateway_key_search_order) as
// gateway_search_providers keeps the global order. A key that sets none is routed as every other.
//
//   - Only these accounts: the key's requests go only to the accounts chosen, taken by the accounts' own rules among
//     them; when each is at its limit the request is answered 429 with when to retry, and no other account is tried.
//     A provider none of whose accounts is chosen is not used by the key: a model only it serves is refused. An
//     account added later is not added. OmniRoute narrows a provider's connections to an API key's
//     allowed_connections before it picks one (src/sse/services/auth.ts:1136-1138); Bifrost's virtual key names
//     the provider keys it may use, "*" being every one (tables/virtualkey.go).
//   - Its own web search order: those providers are asked first, in its order, the others after in the global
//     order; a provider switched off stays off, and the subscriptions' searches go to the key's accounts too.
//     OmniRoute's preferred_connections take the next eligible one; a per-key search order itself is the owner's
//     (2026-11-27).

// AllAccounts in an update sets a key back to every account, as AllEndpoints does its kinds.
const AllAccounts = "*"

// KeyAccount is an account a key may use.
type KeyAccount struct {
	ID      string
	Runtime string
}

// KeyRouting is what a new key sets of its routing.
type KeyRouting struct {
	// Accounts are the only accounts the key may use; nil is every account.
	Accounts []string
	// SearchOrder is the key's own web search order; empty is the global order.
	SearchOrder []string
}

// accountRuntimes are the provider kinds served under the accounts logged in on the control plane, with the runtime
// of their accounts.
var accountRuntimes = map[string]string{
	"codex_subscription":       codexRuntime,
	"claude_subscription":      claudeAccountRuntime,
	"antigravity_subscription": antigravityRuntime,
	openCodeGoKind:             openCodeGoRuntime,
	openCodeZenKind:            openCodeZenRuntime,
}

// OwnAccounts reports whether the key may use only the accounts chosen for it.
func (k Key) OwnAccounts() bool { return k.Accounts != nil }

// usesKind reports whether the key may use a provider of a kind: one served without accounts, or whose accounts
// include one chosen for the key.
func (k Key) usesKind(kind string) bool {
	runtime, ok := accountRuntimes[kind]
	if !ok || !k.OwnAccounts() {
		return true
	}

	return slices.ContainsFunc(k.Accounts, func(a KeyAccount) bool { return a.Runtime == runtime })
}

// KeyServes reports whether a key may use one of a model's targets (issue #1277): a key limited to some accounts is
// refused a model whose every provider has none of them. A provider that cannot be read is taken as usable: the
// request then fails as it would for any key.
func (s *Service) KeyServes(ctx context.Context, k Key, m Model) bool {
	if !k.OwnAccounts() {
		return true
	}

	for _, t := range m.Targets {
		p, err := s.providers(ctx, t.Provider)
		if err != nil || k.usesKind(p.Kind) {
			return true
		}
	}

	return len(m.Targets) == 0
}

// accountsRefuseMessage says why a key limited to some accounts may not use a model.
func accountsRefuseMessage(model string) string {
	return fmt.Sprintf("this key may not use model %q: none of the accounts it may use serves it", model)
}

type keyAccountsKey struct{}

// withKey gives a request the accounts its key may use (KeyAccounts).
func withKey(r *http.Request, k Key) *http.Request {
	return r.WithContext(keyContext(r.Context(), k))
}

// keyContext gives ctx the accounts a key may use (KeyAccounts).
func keyContext(ctx context.Context, k Key) context.Context {
	if !k.OwnAccounts() {
		return ctx
	}

	return context.WithValue(ctx, keyAccountsKey{}, slices.Clone(k.Accounts))
}

// KeyAccounts are the only accounts the request of ctx may be served under (issue #1277); ok is false when it may
// be served under every account. The control plane's wiring passes them to the accounts' service.
func KeyAccounts(ctx context.Context) ([]string, bool) {
	accounts, ok := ctx.Value(keyAccountsKey{}).([]KeyAccount)

	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}

	return ids, ok
}

// usesRuntime reports whether the request of ctx may be served under an account of a runtime: every request may,
// unless its key is limited to accounts none of which is of it.
func usesRuntime(ctx context.Context, runtime string) bool {
	accounts, ok := ctx.Value(keyAccountsKey{}).([]KeyAccount)

	return !ok || slices.ContainsFunc(accounts, func(a KeyAccount) bool { return a.Runtime == runtime })
}

// searchScope is the web search chain as a key sets it: its own order, and its accounts for the subscriptions.
func searchScope(ctx context.Context, k Key) websearch.Scope {
	scope := websearch.Scope{Order: k.SearchOrder}
	if ids, ok := KeyAccounts(ctx); ok {
		scope.Accounts = "only:" + strings.Join(ids, ",")
	}

	return scope
}

// withRouting fills a key's own routing from its rows.
func withRouting(ctx context.Context, q *store.Queries, k Key, own bool) (Key, error) {
	if own {
		rows, err := q.ListGatewayKeyAccounts(ctx, k.ID)
		if err != nil {
			return Key{}, fmt.Errorf("reading the accounts of key %s: %w", k.ID, err)
		}

		k.Accounts = make([]KeyAccount, 0, len(rows))
		for _, r := range rows {
			k.Accounts = append(k.Accounts, KeyAccount{ID: r.AccountID, Runtime: r.Runtime})
		}
	}

	order, err := q.ListGatewayKeySearchOrder(ctx, k.ID)
	if err != nil {
		return Key{}, fmt.Errorf("reading the search order of key %s: %w", k.ID, err)
	}

	for _, o := range order {
		k.SearchOrder = append(k.SearchOrder, o.Provider)
	}

	return k, nil
}

// validateRouting refuses accounts or a search order that cannot be set: an account named twice or mixed with
// AllAccounts, a provider that is not a web search provider or is named twice.
func validateRouting(accounts, order []string) error {
	if accounts != nil && len(accounts) == 0 {
		return fmt.Errorf("%w: choose at least one account, or every account", ErrInvalid)
	}

	for i, id := range accounts {
		switch {
		case id == AllAccounts && len(accounts) > 1:
			return fmt.Errorf("%w: every account (*) cannot be combined with named accounts", ErrInvalid)
		case id == "":
			return fmt.Errorf("%w: an account's id is empty", ErrInvalid)
		case slices.Contains(accounts[:i], id):
			return fmt.Errorf("%w: account %s is named twice", ErrInvalid, id)
		}
	}

	for i, p := range order {
		switch {
		case !slices.Contains(websearch.Providers(), p):
			return fmt.Errorf("%w: %q is not a web search provider: one of %s", ErrInvalid, p,
				strings.Join(websearch.Providers(), ", "))
		case slices.Contains(order[:i], p):
			return fmt.Errorf("%w: %s is named twice in the search order", ErrInvalid, p)
		}
	}

	return nil
}

// setAccounts replaces the accounts a key may use: nil or AllAccounts is every account.
func setAccounts(ctx context.Context, q *store.Queries, id string, accounts []string) error {
	if err := q.ClearGatewayKeyAccounts(ctx, id); err != nil {
		return fmt.Errorf("clearing the accounts of key %s: %w", id, err)
	}

	own := accounts != nil && !slices.Equal(accounts, []string{AllAccounts})
	if err := q.SetGatewayKeyOwnAccounts(ctx, store.SetGatewayKeyOwnAccountsParams{
		ID: id, OwnAccounts: boolInt(own),
	}); err != nil {
		return fmt.Errorf("setting the accounts of key %s: %w", id, err)
	}

	if !own {
		return nil
	}

	for _, account := range accounts {
		n, err := q.AccountExists(ctx, account)
		if err != nil {
			return fmt.Errorf("reading account %s: %w", account, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: no account %s", ErrInvalid, account)
		}

		if err := q.AddGatewayKeyAccount(ctx, store.AddGatewayKeyAccountParams{KeyID: id, AccountID: account}); err != nil {
			return fmt.Errorf("giving key %s account %s: %w", id, account, err)
		}
	}

	return nil
}

// setSearchOrder replaces a key's own web search order; empty is the global order.
func setSearchOrder(ctx context.Context, q *store.Queries, id string, order []string) error {
	if err := q.ClearGatewayKeySearchOrder(ctx, id); err != nil {
		return fmt.Errorf("clearing the search order of key %s: %w", id, err)
	}

	for i, p := range order {
		if err := q.AddGatewayKeySearchProvider(ctx, store.AddGatewayKeySearchProviderParams{
			KeyID: id, Provider: p, Position: int64(i),
		}); err != nil {
			return fmt.Errorf("setting the search order of key %s: %w", id, err)
		}
	}

	return nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}

	return 0
}
