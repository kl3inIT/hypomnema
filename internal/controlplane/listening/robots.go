package listening

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A site's robots.txt, read before a forum thread is (issue #1289's design: "fetch the thread, respecting
// robots.txt"). Its rules are read as last30days reads them (job_boards/robots.py: the longest matching rule wins,
// Allow on a tie, `*` and `$` wildcards), from the group that names this control plane, else the `*` group, as RFC
// 9309 §2.2.1 has a crawler pick its group (last30days reads `*` only). A Content-Signal line in that group is read too:
// voz.vn's says `use=reference`, reserving its content to links and excerpts (research note §2), and a site that says
// `ai-input=no` refuses an AI's reading.

const (
	// robotsAgent is the product token a robots.txt names this control plane by (pageUserAgent).
	robotsAgent = "hypomnema"
	// robotsCache is how long a site's robots.txt is kept: RFC 9309 §2.4, "SHOULD NOT use the cached version for more
	// than 24 hours".
	robotsCache = 24 * time.Hour
	// maxRobots bounds a robots.txt read (last30days robots.MAX_ROBOTS_CHARS, RFC 9309 §2.5's 500 KiB at least).
	maxRobots = 512 << 10
)

type robotRule struct {
	allow   bool
	pattern string
}

// robotRules are what a site's robots.txt asks of this control plane.
type robotRules struct {
	rules []robotRule
	// signals are the group's Content-Signal values, by name.
	signals map[string]string
	// none: the file could not be read for a reason RFC 9309 §2.3.1.4 reads as "disallow everything" (a 5xx, no
	// answer).
	none bool
	at   time.Time
}

// allows says the path may be read (last30days Rules.allows).
func (rr robotRules) allows(path string) bool {
	if rr.none {
		return false
	}

	best, bestAllow := -1, true

	for _, r := range rr.rules {
		if !robotMatch(r.pattern, path) {
			continue
		}

		if n := len(r.pattern); n > best || n == best && r.allow {
			best, bestAllow = n, r.allow
		}
	}

	return bestAllow
}

// readable says a thread may be read in full: robots.txt allows its path, and the site's content signals neither
// refuse an AI's input nor reserve its content to reference use.
func (rr robotRules) readable(path string) bool {
	return rr.allows(path) && rr.signals["ai-input"] != "no" && rr.signals["use"] != "reference"
}

// robotMatch is a rule's pattern against a path, with `*` any run and a final `$` the end (last30days _matches).
func robotMatch(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	body := strings.TrimSuffix(pattern, "$")
	parts := strings.Split(body, "*")

	if len(parts) == 1 {
		if anchored {
			return path == body
		}

		return strings.HasPrefix(path, body)
	}

	if !strings.HasPrefix(path, parts[0]) {
		return false
	}

	pos := len(parts[0])

	middle := parts[1:]
	if anchored {
		middle = parts[1 : len(parts)-1]
	}

	for _, part := range middle {
		if part == "" {
			continue
		}

		i := strings.Index(path[pos:], part)
		if i < 0 {
			return false
		}

		pos += i + len(part)
	}

	if !anchored {
		return true
	}

	last := parts[len(parts)-1]

	return last == "" || strings.HasSuffix(path, last) && len(path)-len(last) >= pos
}

// parseRobots reads the groups of a robots.txt and keeps the one that names robotsAgent, else `*`.
func parseRobots(text string) robotRules {
	type group struct {
		agents  []string
		rules   []robotRule
		signals map[string]string
	}

	var (
		groups []*group
		cur    *group
	)

	for line := range strings.SplitSeq(text, "\n") {
		line, _, _ = strings.Cut(line, "#")

		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		field, value = strings.ToLower(strings.TrimSpace(field)), strings.TrimSpace(value)

		switch field {
		case "user-agent":
			if cur == nil || len(cur.rules) > 0 || len(cur.signals) > 0 {
				cur = &group{signals: map[string]string{}}
				groups = append(groups, cur)
			}

			cur.agents = append(cur.agents, strings.ToLower(value))
		case "allow", "disallow":
			if cur != nil && value != "" {
				cur.rules = append(cur.rules, robotRule{allow: field == "allow", pattern: value})
			}
		case "content-signal":
			if cur == nil {
				continue
			}

			for pair := range strings.SplitSeq(value, ",") {
				if k, v, ok := strings.Cut(pair, "="); ok {
					cur.signals[strings.ToLower(strings.TrimSpace(k))] = strings.ToLower(strings.TrimSpace(v))
				}
			}
		}
	}

	pick := func(agent string) robotRules {
		out := robotRules{signals: map[string]string{}}

		for _, g := range groups {
			for _, a := range g.agents {
				if a == agent {
					out.rules = append(out.rules, g.rules...)
					for k, v := range g.signals {
						out.signals[k] = v
					}

					break
				}
			}
		}

		return out
	}

	if own := pick(robotsAgent); len(own.rules) > 0 || len(own.signals) > 0 {
		return own
	}

	return pick("*")
}

// fetch reads a robots.txt one redirect at a time, each hop in its own host's turn and checked against that host's
// rest, the same rest its pages have (readPage): Otofun's old host sends its robots.txt to the new one, whose 429
// then rests the new host whichever host a link names (review round 3 of PR #1321).
func (rb *robots) fetch(ctx context.Context, r Request, address string) (answer, error) {
	for hop := 0; hop <= maxRedirects; hop++ {
		host := hostOf(address)

		a, err := call{
			host: host, path: host + "/pages", address: address, accept: "text/plain", userAgent: pageUserAgent,
			redirect: true, siteRests: true,
			failure: func(s int, h http.Header, now time.Time) *SourceError {
				if s >= 400 && s < 500 && s != http.StatusTooManyRequests {
					// Unavailable: no rules (RFC 9309 §2.3.1.3), and nothing rests.
					return Fail(CodeNotFound, "robots.txt answered HTTP %d", s)
				}

				return HTTPFailure(s, h, now)
			},
		}.do(ctx, rb.client, r)
		if err != nil || a.location == "" {
			return a, err
		}

		address = a.location
	}

	// RFC 9309 §2.3.1.2: follow at least five redirects; past them the file is unreachable.
	return answer{}, Fail(CodeBroken, "robots.txt redirected more than %d times", maxRedirects)
}

// robots reads and keeps the robots.txt of the sites a forum's threads are on.
type robots struct {
	client *http.Client

	mu    sync.Mutex
	sites map[string]robotRules
}

func newRobots(client *http.Client) *robots {
	return &robots{client: client, sites: map[string]robotRules{}}
}

// of is a site's rules, read at most once a robotsCache: a 4xx is no robots.txt, everything allowed, and a 5xx or no
// answer disallows everything (RFC 9309 §2.3.1.3 and §2.3.1.4; last30days reads a missing or silent file as no rules).
//
// A 429 is the site asking to slow down, not a missing file: it rests the host's pages (the same rest as a page's,
// readPage), Retry-After kept, so neither the robots.txt nor a thread is asked until then, and it is the read's
// failure, not kept as rules (review round 2 of PR #1321; trend-opportunity-radar collection-pacing-contract.md:12,
// "stop immediately on … rate limit").
func (rb *robots) of(ctx context.Context, r Request, origin string) (robotRules, error) {
	now := r.now()

	rb.mu.Lock()
	cached, ok := rb.sites[origin]
	rb.mu.Unlock()

	if ok && now.Sub(cached.at) < robotsCache {
		return cached, nil
	}

	a, err := rb.fetch(ctx, r, origin+"/robots.txt")

	var rules robotRules

	switch f := failureOf(err); {
	case f == nil:
		text := string(a.body)
		rules = parseRobots(text[:min(len(text), maxRobots)])
	case f.Code == CodeNotFound:
		rules = robotRules{}
	case stopsHost(f.Code):
		return robotRules{none: true}, &SourceError{Code: f.Code, Detail: "robots.txt: " + f.Detail, Until: f.Until}
	default:
		rules = robotRules{none: true}
	}

	rules.at = now

	rb.mu.Lock()
	rb.sites[origin] = rules
	rb.mu.Unlock()

	return rules, nil
}
