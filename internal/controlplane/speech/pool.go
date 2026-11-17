package speech

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

// Several keys per provider, as web search keeps them (websearch/pool.go,
// issue #393): the owner's keys are vault secrets named after their
// provider, the first `speech-soniox`, the next `speech-soniox-2`, and so on.
// A call asks a provider's keys least recently used first, as mcp-nexus's
// Tavily pool picks a key (tavily/keyPool.ts), and each key rests on its own
// (rest.go).

// keyPrefix starts the name of every key's secret.
const keyPrefix = "speech-"

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

	if !slices.Contains(Providers(), rest) {
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
		s.cfg.Logger.ErrorContext(ctx, "reading the speech keys",
			slog.String("event", "speech.keys_failed"), slog.String("error", err.Error()))

		return nil, fmt.Errorf("reading the speech keys: %w", err)
	}

	out := map[string][]Key{}

	for _, k := range all {
		if k.Value != "" && slices.Contains(Providers(), k.Provider) {
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

// use notes that a key is being asked now.
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

// KeyState is one of a provider's keys as the Audio page shows it; never its value.
type KeyState struct {
	Name         string
	RestingUntil time.Time
	// Rest is why it rests (RestRateLimited…); empty when it does not.
	Rest     string
	LastUsed time.Time
}

// Link is a provider as the Audio page shows it.
type Link struct {
	Provider string
	// On is the owner's switch, shared by both kinds.
	On bool
	// Keys are its keys, in the order a call asks them now.
	Keys []KeyState
	// RestingUntil and Rest: every key rests, until the first is free.
	RestingUntil time.Time
	Rest         string
}

// Chain is every provider with its keys and what the owner set: the
// providers in the default order, and the settings that order each kind.
func (s *Service) Chain(ctx context.Context) ([]Link, Settings, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return nil, Settings{}, err
	}

	keys, err := s.keys(ctx)
	if err != nil {
		return nil, Settings{}, err
	}

	out := make([]Link, 0, len(Providers()))

	for _, p := range Providers() {
		l := Link{Provider: p, On: !slices.Contains(set.Off, p), Keys: []KeyState{}}

		for _, k := range s.leastRecentlyUsed(keys[p]) {
			id := keyRoute(k).id
			ks := KeyState{Name: k.Name, LastUsed: s.lastUse(id)}

			if r, ok := s.restOf(id); ok {
				ks.RestingUntil, ks.Rest = r.until, r.why
			}

			l.Keys = append(l.Keys, ks)
		}

		l.RestingUntil, l.Rest = providerRest(l.Keys)
		out = append(out, l)
	}

	return out, set, nil
}

// providerRest is a provider's rest: when every key rests, until the first is free.
func providerRest(keys []KeyState) (time.Time, string) {
	var (
		until time.Time
		why   string
	)

	for _, k := range keys {
		if k.RestingUntil.IsZero() {
			return time.Time{}, ""
		}

		if until.IsZero() || k.RestingUntil.Before(until) {
			until, why = k.RestingUntil, k.Rest
		}
	}

	return until, why
}
