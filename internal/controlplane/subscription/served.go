package subscription

import (
	"context"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// servedKept is how long a reading of the models an account is served is given again, and servedRetry how long
// after a failed reading the vendor is asked again. omp reads an account's roster once per discovery and keeps it
// (packages/catalog/src/discovery/antigravity.ts); a model's window changes rarely. servedWait bounds how long a
// caller waits for an account's first reading that another caller has started.
const (
	servedKept  = time.Hour
	servedRetry = 5 * time.Minute
	servedWait  = 30 * time.Second
)

// perAccount are the runtimes whose vendor lists each account's own models (issue #814): what they are served is the
// union of every account's list, and a request for a model goes only to the accounts whose list has it, when one
// has it. omp unions the per-account catalogs of Antigravity and Codex and records which account serves each model
// (packages/catalog/src/provider-models/google.ts and special.ts unionCodexModels, account-access.ts), and
// CLIProxyAPI registers each Antigravity and Codex auth's own models and routes a model only to an auth that has it
// (sdk/cliproxy/service_models.go, sdk/cliproxy/auth/conductor_selection.go authSupportsRouteModel). Claude is not
// one: neither reads an Anthropic list per account (CLIProxyAPI gives every Claude auth the same list), so a Claude
// runtime is served what its first account that answers is.
var perAccount = map[string]bool{"antigravity": true, "codex": true}

// servedReading is the last reading of the models one account is served, in the vendor's order, with each one's
// window and largest output.
type servedReading struct {
	models []ServedModel
	at     time.Time
	tried  time.Time
	// reading is closed when the read in flight ends; nil when none is.
	reading chan struct{}
}

// servedView is what a runtime's usable accounts are served, read from each account's own reading (issue #814).
type servedView struct {
	// names are the models, in the accounts' order, each once (issue #623).
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

// ServedNames are the models the vendor serves a runtime's accounts, by name: for a runtime whose vendor lists each
// account's own (perAccount), those of every usable account, each read and kept as ServedLimits reads them; for
// another, those of its first usable account that answers (issue #623). ok is false when nothing is known: no
// account, or none ever answered.
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

	v := servedView{limits: map[string][2]int64{}, by: map[string][]string{}}

	// An account whose reading failed adds what it was last read to serve, or nothing; it hides no other's (omp
	// packages/catalog/src/provider-models/google.ts keeps a catalog it could not read again and skips an account
	// that is refused).
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

		if !perAccount[runtime] {
			break
		}
	}

	return v
}

// reading is the last reading of the models an account is served, read again when it is older than servedKept and
// the last try is older than servedRetry. A reading that fails keeps the last one. While the account's first reading
// is in flight, another caller waits for it, at most servedWait, so that a model is neither left out nor sent to an
// account whose list does not have it because its list is not known yet.
func (s *Service) reading(ctx context.Context, accountID string) servedReading {
	now := s.clock.Now()

	s.servedMu.Lock()
	r := s.served[accountID]

	if r.at.IsZero() && r.reading != nil {
		done := r.reading
		s.servedMu.Unlock()

		wait := time.NewTimer(servedWait)
		defer wait.Stop()

		select {
		case <-done:
		case <-wait.C:
		case <-ctx.Done():
		}

		s.servedMu.Lock()
		defer s.servedMu.Unlock()

		return s.served[accountID]
	}

	stale := now.Sub(r.at) >= servedKept && now.Sub(r.tried) >= servedRetry
	if !stale {
		s.servedMu.Unlock()

		return r
	}

	done := make(chan struct{})
	r.tried, r.reading = now, done
	s.served[accountID] = r
	s.servedMu.Unlock()

	models, err := s.Models(ctx, accountID)

	s.servedMu.Lock()
	defer s.servedMu.Unlock()

	r = s.served[accountID]
	if err == nil {
		r.models, r.at = models, now
	}

	r.reading = nil
	s.served[accountID] = r
	close(done)

	return r
}
