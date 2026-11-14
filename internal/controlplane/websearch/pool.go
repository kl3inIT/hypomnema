package websearch

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Several keys per provider (issue #393, owner 2026-11-14). The owner's keys
// are vault secrets named after their provider: the first `web-search-exa`
// (the name Exa's one key had), the next `web-search-exa-2`, and so on. A
// search asks a provider's keys least recently used first, as mcp-nexus's
// Tavily pool picks a key (tavily/keyPool.ts: lastUsedAt ascending, set when
// a key is taken), and each key rests on its own (rest.go): the others go on
// while one rests, as 9router and OmniRoute rotate a provider's connections.
// A failure that is not the key's (a 5xx, a timeout, no results) goes to the
// next provider instead, since another key of the same provider would meet
// it too: the gateway's accounts move on only for what is the account's.

// keyPrefix starts the name of every key's secret.
const keyPrefix = "web-search-"

// KeyName is the vault secret of a provider's nth key, from 1.
func KeyName(provider string, n int) string {
	if n <= 1 {
		return keyPrefix + provider
	}

	return keyPrefix + provider + "-" + strconv.Itoa(n)
}

// ParseKeyName reads a key's secret name: its provider and its number.
func ParseKeyName(name string) (provider string, n int, ok bool) {
	rest, found := strings.CutPrefix(name, keyPrefix)
	if !found {
		return "", 0, false
	}

	n = 1

	if head, tail, cut := strings.Cut(rest, "-"); cut {
		number, err := strconv.Atoi(tail)
		if err != nil || number < 2 || strconv.Itoa(number) != tail {
			return "", 0, false
		}

		rest, n = head, number
	}

	if !slices.Contains(KeyedProviders(), rest) {
		return "", 0, false
	}

	return rest, n, true
}

// NextKeyName is the name a provider's new key takes: the lowest number no
// secret of the vault has.
func NextKeyName(provider string, taken []string) string {
	for n := 1; ; n++ {
		if !slices.Contains(taken, KeyName(provider, n)) {
			return KeyName(provider, n)
		}
	}
}

// keys reads the owner's keys, by provider, in the order of their numbers.
func (s *Service) keys(ctx context.Context) (map[string][]Key, error) {
	if s.cfg.Keys == nil {
		return map[string][]Key{}, nil
	}

	all, err := s.cfg.Keys(ctx)
	if err != nil {
		s.cfg.Logger.ErrorContext(ctx, "reading the web search keys",
			slog.String("event", "websearch.keys_failed"), slog.String("error", err.Error()))

		return nil, fmt.Errorf("reading the web search keys: %w", err)
	}

	out := map[string][]Key{}

	for _, k := range all {
		if k.Value != "" && slices.Contains(KeyedProviders(), k.Provider) {
			out[k.Provider] = append(out[k.Provider], k)
		}
	}

	for _, ks := range out {
		slices.SortFunc(ks, func(a, b Key) int { return cmp.Compare(keyNumber(a.Name), keyNumber(b.Name)) })
	}

	return out, nil
}

func keyNumber(name string) int {
	_, n, _ := ParseKeyName(name)

	return n
}

// leastRecentlyUsed orders a provider's keys: one never asked first, then the
// one asked longest ago; the numbers break ties.
func (s *Service) leastRecentlyUsed(keys []Key) []Key {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := slices.Clone(keys)
	slices.SortStableFunc(out, func(a, b Key) int {
		return s.lastUsed[keyRoute(a).id].Compare(s.lastUsed[keyRoute(b).id])
	})

	return out
}

// use notes that a route is being asked now.
func (s *Service) use(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastUsed[id] = s.now()
}

func (s *Service) lastUse(id string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.lastUsed[id]
}

// keyRoute asks a provider with one of its keys.
func keyRoute(k Key) route {
	r := route{id: "key:" + k.Name, provider: k.Provider, source: SourceKey, key: k, label: "key " + k.Name + ": "}

	switch k.Provider {
	case ProviderExa:
		r.ask = (*Service).askExa
	case ProviderFirecrawl:
		r.ask, r.timeout = (*Service).askFirecrawl, firecrawlTimeout
	case ProviderParallel:
		r.ask = (*Service).askParallelKey
	case ProviderTavily:
		r.ask = (*Service).askTavily
	case ProviderTinyFish:
		r.ask = (*Service).askTinyFish
	}

	return r
}

// keylessRoute asks a provider without a key.
func keylessRoute(provider string) route {
	r := route{id: "public:" + provider, provider: provider, source: SourcePublic, label: "no key: "}

	switch provider {
	case ProviderExa:
		r.ask, r.label = (*Service).askExaMCP, "public MCP: "
	case ProviderParallel:
		r.ask, r.label = (*Service).askParallel, "public MCP: "
	case ProviderFirecrawl:
		r.ask, r.timeout = (*Service).askFirecrawl, firecrawlTimeout
	case ProviderTavily:
		r.ask = (*Service).askTavily
	case ProviderYou:
		r.ask, r.label, r.daily, r.timeout = (*Service).askYou, "public MCP: ", true, youTimeout
	}

	return r
}
