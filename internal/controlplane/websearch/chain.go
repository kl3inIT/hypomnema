package websearch

import (
	"context"
	"slices"
	"sync"
	"time"
)

// What the owner sets on Settings → Gateway → Web search (issues #354,
// #393), and the chain as that page shows it: every way a search may be
// asked, in the order a search that names no provider tries them, with where
// each one's search comes from, the owner's switch, whether it can be asked,
// its keys and their rests, and how much of it is used. OmniRoute keeps a
// switch for each search provider (providers/components/ProviderCard.tsx,
// isActive); search-rotation's dashboard reorders its engines by dragging.

// Settings are what the owner set.
type Settings struct {
	// Off are the providers switched off: they are neither tried nor may be named.
	Off []string
	// Order is the owner's order of the keyed providers; a keyed provider it
	// leaves out comes after, in the default order. Empty: the default order.
	Order []string
}

// order is the keyed providers in the owner's order.
func (set Settings) order() []string {
	var out []string

	for _, p := range append(slices.Clone(set.Order), KeyedProviders()...) {
		if slices.Contains(KeyedProviders(), p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}

	return out
}

// Where a provider's search comes from.
const (
	// SourceKey: the owner's API keys, from the vault.
	SourceKey = "key"
	// SourcePublic: a public endpoint asked with no key.
	SourcePublic = "public"
	// SourceSubscription: a Codex, Claude or Antigravity account logged in on the control plane.
	SourceSubscription = "subscription"
)

// Link is one way of asking in the chain.
type Link struct {
	Provider string
	Source   string
	// On is the owner's switch for the provider; its ways with and without keys share one.
	On bool
	// Usable says it can be asked: a keyed provider has a key in the vault,
	// the control plane has subscriptions. Whether a subscription has an
	// account logged in is the caller's to add.
	Usable bool
	// RestingUntil is when it is asked again, and Rest why it rests
	// (RestRateLimited…); zero when it does not rest. A keyed provider rests
	// when each of its keys does, until the first is free.
	RestingUntil time.Time
	Rest         string
	// Keys are a keyed provider's keys, in the order a search asks them now.
	Keys []KeyState
	// Usage is what was used without a key, or through the subscription.
	Usage Count
}

// KeyState is one of a provider's keys as the page shows it; never its value.
type KeyState struct {
	Name         string
	RestingUntil time.Time
	Rest         string
	// Spent says its balance was read as spent: it is skipped until it comes back.
	Spent bool
	// Balance is its balance as its provider tells it; nil when it has none or it could not be read.
	Balance  *Balance
	LastUsed time.Time
	Usage    Count
}

// Chain is every way a search may be asked, in the order they are tried:
// the keyed providers in the owner's order, each listed whether it has keys
// or not; the keyless ones in the ring's order (a search starts the ring
// one further each time); the subscriptions.
func (s *Service) Chain(ctx context.Context) ([]Link, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}

	keys, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}

	counts := map[[2]string]Count{}

	if s.cfg.Usage != nil {
		all, err := s.cfg.Usage.SearchCounts(ctx, s.now())
		if err != nil {
			return nil, err
		}

		for _, c := range all {
			counts[[2]string{c.Provider, c.Key}] = c
		}
	}

	off := func(p string) bool { return slices.Contains(set.Off, p) }

	var out []Link

	for _, p := range set.order() {
		l := Link{Provider: p, Source: SourceKey, On: !off(p), Usable: len(keys[p]) > 0, Keys: []KeyState{}}

		for _, k := range s.leastRecentlyUsed(keys[p]) {
			l.Keys = append(l.Keys, s.keyState(k, counts[[2]string{p, k.Name}]))
		}

		s.readBalances(ctx, l.Keys, keys[p])
		l.RestingUntil, l.Rest = providerRest(l.Keys)
		out = append(out, l)
	}

	for _, p := range keylessRing() {
		r := keylessRoute(p)
		l := Link{Provider: p, Source: SourcePublic, On: !off(p), Usable: true, Usage: counts[[2]string{p, ""}]}

		if rs, ok := s.restOf(r.id); ok {
			l.RestingUntil, l.Rest = rs.until, rs.why
		}

		out = append(out, l)
	}

	for _, p := range SubscriptionProviders() {
		r := subscriptionRoute(p)
		l := Link{
			Provider: p, Source: SourceSubscription, On: !off(p), Usable: s.subs != nil,
			Usage: counts[[2]string{p, ""}],
		}

		if rs, ok := s.restOf(r.id); ok {
			l.RestingUntil, l.Rest = rs.until, rs.why
		}

		out = append(out, l)
	}

	return out, nil
}

func (s *Service) keyState(k Key, c Count) KeyState {
	id := keyRoute(k).id
	ks := KeyState{Name: k.Name, LastUsed: s.lastUse(id), Usage: c}

	if rs, ok := s.restOf(id); ok {
		ks.RestingUntil, ks.Rest = rs.until, rs.why
	}

	return ks
}

// readBalances reads the balances of a provider's keys at once.
func (s *Service) readBalances(ctx context.Context, states []KeyState, keys []Key) {
	var wg sync.WaitGroup

	for i := range states {
		for _, k := range keys {
			if k.Name != states[i].Name || !hasBalance(k.Provider) {
				continue
			}

			wg.Go(func() {
				if b, ok := s.balance(ctx, k); ok {
					states[i].Balance = &b
					states[i].Spent = b.Remaining <= 0
				}
			})
		}
	}

	wg.Wait()
}

// providerRest is a keyed provider's rest: when every key rests or is
// spent and one rests, until the first is free.
func providerRest(keys []KeyState) (time.Time, string) {
	var (
		until time.Time
		why   string
	)

	for _, k := range keys {
		switch {
		case k.RestingUntil.IsZero() && !k.Spent:
			return time.Time{}, ""
		case !k.RestingUntil.IsZero() && (until.IsZero() || k.RestingUntil.Before(until)):
			until, why = k.RestingUntil, k.Rest
		}
	}

	return until, why
}
