package websearch

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// What the owner sets on Settings → Gateway → Web search (issue #354), and
// the chain as that page shows it: every way a search may be asked, in the
// order a search that names no provider tries them, with where each one's
// search comes from, the owner's switch, whether it can be asked, and its
// rest after a 429. OmniRoute keeps a switch for each search provider
// (providers/components/ProviderCard.tsx, isActive) and SearXNG's address
// beside it (providerSpecificData.baseUrl); OpenClaw has SearXNG's address
// as the provider's setting (extensions/searxng, "SearXNG Base URL").

// Settings are what the owner set.
type Settings struct {
	// Off are the providers switched off: they are neither tried nor may be named.
	Off []string
	// SearXNGURL is the owner's SearXNG; empty: SearXNG is not asked.
	SearXNGURL string
}

// Where a provider's search comes from.
const (
	// SourceKey: Exa with the owner's API key, from the vault.
	SourceKey = "key"
	// SourceSelfHosted: the owner's SearXNG.
	SourceSelfHosted = "self_hosted"
	// SourcePublic: a public endpoint asked with no key.
	SourcePublic = "public"
	// SourceSubscription: a Codex, Claude or Antigravity account logged in on the control plane.
	SourceSubscription = "subscription"
)

// Link is one way of asking in the chain.
type Link struct {
	Provider string
	Source   string
	// On is the owner's switch for the provider; Exa's two ways share one.
	On bool
	// Usable says it can be asked: Exa's key is in the vault, SearXNG's
	// address is set, the control plane has subscriptions. Whether a
	// subscription has an account logged in is the caller's to add.
	Usable bool
	// Address is SearXNG's, when it is set.
	Address string
	// RestingUntil is when it is asked again after a 429; zero when it is not resting.
	RestingUntil time.Time
}

// Chain is every way a search may be asked, in the order they are tried.
func (s *Service) Chain(ctx context.Context) ([]Link, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return nil, err
	}

	key, err := s.exaKey(ctx)
	if err != nil {
		return nil, err
	}

	routes := chain(key, set.SearXNGURL)
	out := make([]Link, 0, len(routes))

	for _, r := range routes {
		l := Link{
			Provider: r.provider, Source: r.source, On: !slices.Contains(set.Off, r.provider), Usable: s.usable(r),
			Address: r.address,
		}
		if until, ok := s.restingUntil(r.id); ok {
			l.RestingUntil = until
		}

		out = append(out, l)
	}

	return out, nil
}

// SearXNGAddress checks the address of a SearXNG and returns it without its
// trailing slash: an http(s) address with a host, as OmniRoute's base URL
// field takes it. It may be on the local network: SearXNG is the owner's.
func SearXNGAddress(address string) (string, error) {
	address = strings.TrimRight(strings.TrimSpace(address), "/")

	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("%w: SearXNG's address is an http(s) address, such as http://localhost:8888", ErrInvalid)
	}

	return address, nil
}
