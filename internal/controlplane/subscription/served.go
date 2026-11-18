package subscription

import (
	"context"
	"slices"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// servedKept is how long a reading of the models an account is served is given again, and servedRetry how long
// after a failed reading the vendor is asked again. omp reads an account's roster once per discovery and keeps it
// (packages/catalog/src/discovery/antigravity.ts); a model's window changes rarely.
const (
	servedKept  = time.Hour
	servedRetry = 5 * time.Minute
)

// RosterRuntimes are the runtimes whose vendor's list of an account's models is authoritative: the provider's list
// is set to it (issue #639), and a request for a model goes only to the accounts whose list has it (issue #814), as
// omp takes Antigravity's roster (packages/catalog/src/discovery/antigravity.ts) and CLIProxyAPI routes a model only
// to an auth that registered it (sdk/cliproxy/auth/conductor_selection.go authSupportsRouteModel). Codex's and
// Claude's lists leave out models they serve (models.go), so they only add to the provider's.
var RosterRuntimes = map[string]bool{"antigravity": true}

// servedReading is the last reading of the models one account is served, in the vendor's order, with each one's
// window and largest output.
type servedReading struct {
	models []ServedModel
	at     time.Time
	tried  time.Time
}

// servedView is what a runtime's usable accounts are served, read from each account's own reading (issue #814).
type servedView struct {
	// names are every account's models, in the accounts' order, each once (issue #623).
	names []string
	// limits are each model's window and largest output, from the first account that serves it and gives them.
	limits map[string][2]int64
	// by are the accounts that serve each model.
	by map[string][]string
	// known says at least one account's models were read.
	known bool
}

// ServedLimits gives the context window and largest output the vendor serves a model with under a runtime's
// accounts (issue #587): the first usable account whose models, read as omp reads maxTokens and maxOutputTokens from
// fetchAvailableModels, have it says (omp packages/catalog/src/provider-models/account-access.ts, where the first
// catalog to list a model supplies its spec; issue #814). ok is false when nothing is known.
func (s *Service) ServedLimits(ctx context.Context, runtime, model string) (window, output int64, ok bool) {
	l, ok := s.servedOf(ctx, runtime).limits[model]

	return l[0], l[1], ok
}

// ServedNames are the models the vendor serves a runtime's accounts, by name: those of every usable account, each
// read and kept as ServedLimits reads them, as omp unions the catalogs of every account
// (packages/catalog/src/provider-models/account-access.ts unionAccountCatalogs) and CLIProxyAPI lists a model while
// one auth registered it (internal/registry/model_registry.go). ok is false when nothing is known: no account, or
// none ever answered.
func (s *Service) ServedNames(ctx context.Context, runtime string) (names []string, ok bool) {
	v := s.servedOf(ctx, runtime)

	return v.names, v.known
}

// servedOf is what the usable accounts of a runtime are served.
func (s *Service) servedOf(ctx context.Context, runtime string) servedView {
	accounts, err := s.List(ctx)
	if err != nil {
		return servedView{}
	}

	return s.servedAmong(ctx, runtime, accounts)
}

// servedAmong is what the usable accounts among accounts, of a runtime, are served: active, logged in here, in
// their order. An account whose reading failed adds what it was last read to serve, or nothing; it hides no other's
// (omp packages/catalog/src/provider-models/google.ts keeps a catalog it could not read again and skips an account
// that is refused).
func (s *Service) servedAmong(ctx context.Context, runtime string, accounts []Account) servedView {
	v := servedView{limits: map[string][2]int64{}, by: map[string][]string{}}

	for _, a := range accounts {
		if a.Runtime != runtime || a.Status != orchestration.AccountActive || a.State != StateActive {
			continue
		}

		r := s.reading(ctx, a.ID)
		if r.at.IsZero() {
			continue
		}

		v.known = true

		for _, m := range r.models {
			if _, seen := v.by[m.Name]; !seen {
				v.names = append(v.names, m.Name)
			}

			v.by[m.Name] = append(v.by[m.Name], a.ID)

			if _, given := v.limits[m.Name]; !given && (m.Window > 0 || m.Output > 0) {
				v.limits[m.Name] = [2]int64{m.Window, m.Output}
			}
		}
	}

	return v
}

// servers narrows a runtime's accounts to those whose models have model, when the runtime's list is authoritative
// and one of them has it (issue #814): CLIProxyAPI picks only an auth that registered the model
// (sdk/cliproxy/auth/conductor_selection.go authSupportsRouteModel) and omp enforces the accounts discovery saw serve
// it only when one of them is there (packages/ai/src/auth/select.ts enforceAccounts). Otherwise, or for no model,
// the accounts are as given.
func (s *Service) servers(ctx context.Context, runtime, model string, accounts []Account) []Account {
	if model == "" || !RosterRuntimes[runtime] {
		return accounts
	}

	by := s.servedAmong(ctx, runtime, accounts).by[model]
	if len(by) == 0 {
		return accounts
	}

	return slices.DeleteFunc(slices.Clone(accounts), func(a Account) bool { return !slices.Contains(by, a.ID) })
}

// reading is the last reading of the models an account is served, read again when it is older than servedKept and
// the last try is older than servedRetry. A reading that fails keeps the last one.
func (s *Service) reading(ctx context.Context, accountID string) servedReading {
	now := s.clock.Now()

	s.servedMu.Lock()
	r := s.served[accountID]
	stale := now.Sub(r.at) >= servedKept && now.Sub(r.tried) >= servedRetry

	if stale {
		r.tried = now
		s.served[accountID] = r
	}
	s.servedMu.Unlock()

	if !stale {
		return r
	}

	models, err := s.Models(ctx, accountID)
	if err != nil {
		return r
	}

	r = servedReading{models: models, at: now, tried: now}

	s.servedMu.Lock()
	s.served[accountID] = r
	s.servedMu.Unlock()

	return r
}
