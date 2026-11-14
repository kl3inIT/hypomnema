package websearch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

// The search cache is OmniRoute's (open-sse/services/searchCache.ts): in
// memory, keyed by the normalized query and what else changes the answer,
// for 5 minutes (its providers' cacheTTLMs), at most 500 entries, expired
// ones dropped and then the oldest when full. Only answers are kept, never a
// failure.

type cached struct {
	answer  Answer
	expires time.Time
	added   time.Time
}

// normalizeQuery is OmniRoute's: NFKC, lower case, trimmed, runs of space as one.
func normalizeQuery(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(norm.NFKC.String(query))), " ")
}

func cacheKey(query, provider string, limit int, set Settings) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%q %q %d %q %q", normalizeQuery(query), provider, limit,
		slices.Sorted(slices.Values(set.Off)), set.order()))

	return hex.EncodeToString(sum[:])
}

func (s *Service) cached(key string) (Answer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.cache[key]
	if !ok || !c.expires.After(s.now()) {
		return Answer{}, false
	}

	a := c.answer
	a.Results = slices.Clone(a.Results)
	a.Errors = slices.Clone(a.Errors)

	return a, true
}

func (s *Service) keep(key string, a Answer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	for k, c := range s.cache {
		if !c.expires.After(now) {
			delete(s.cache, k)
		}
	}

	for len(s.cache) >= maxCacheEntries {
		oldest := ""

		for k, c := range s.cache {
			if oldest == "" || c.added.Before(s.cache[oldest].added) {
				oldest = k
			}
		}

		delete(s.cache, oldest)
	}

	s.cache[key] = cached{answer: a, expires: now.Add(cacheTTL), added: now}
}
