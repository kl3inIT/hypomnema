package listening

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// The Vietnamese forums of issue #1289, Voz, Tinhte and Otofun: no reference reads them (research note §2), so each is
// read as the research note's route says, a web search `site:<forum> <query>`, then the thread fetched where its
// robots.txt and content signals allow (robots.go), as last30days reads Reddit threads a web search found
// (reddit_discover.py, reddit_keyless._web_floor). A thread not read in full keeps the search's title, excerpt and
// link. voz.vn reserves its content to reference use, so it is never read in full (research note §2; its robots.txt
// read 2026-11-28).

// forum is one forum source.
type forum struct {
	source string
	// site is what the web search is asked for, `site:<site>`.
	site string
	// hosts are the hosts its threads are on.
	hosts []string
	// thread is a thread's path, its first group the thread without its page or post.
	thread *regexp.Regexp
}

// forums are the forum sources, in order; their thread paths read from each site on 2026-11-28 (XenForo's /t/ and
// /threads/, Tinhte's /thread/). Otofun moved to otofun.net.vn; its old host redirects there.
var forums = []forum{
	{source: SourceVoz, site: "voz.vn", hosts: []string{"voz.vn"}, thread: regexp.MustCompile(`^(/t/[^/]+\.\d+)(?:/|$)`)},
	{
		source: SourceTinhte, site: "tinhte.vn", hosts: []string{"tinhte.vn"},
		thread: regexp.MustCompile(`^(/thread/[^/]+\.\d+)(?:/|$)`),
	},
	{
		source: SourceOtofun, site: "otofun.net.vn", hosts: []string{"otofun.net.vn", "otofun.net"},
		thread: regexp.MustCompile(`^(/threads/[^/]+\.\d+)(?:/|$)`),
	},
}

// forumResults is how many results the web search is asked for a forum's threads, as for Reddit's (last30days
// reddit_discover.RESULTS_PER_QUERY).
const forumResults = redditThreads

// forumBackend reads a forum's threads; at moves them to another address (tests).
func forumBackend(f forum, at string, pages *http.Client, rb *robots) Backend {
	hosts := f.hosts
	scheme := "https"

	if at != "" {
		u, err := url.Parse(at)
		if err == nil {
			hosts, scheme = []string{strings.TrimPrefix(strings.ToLower(u.Host), "www.")}, u.Scheme
		}
	}

	return Backend{
		Name: BackendWebThread, Host: f.site, Probe: needsWeb,
		Search: func(ctx context.Context, r Request) (Result, error) {
			found, err := r.Web(ctx, "site:"+f.site+" "+r.Query, forumResults)
			if errors.Is(err, websearch.ErrNoResults) {
				return Result{Detail: "the web search found no thread"}, nil
			}

			if err != nil {
				return Result{}, webFailure(err)
			}

			var threads []Item

			seen := map[string]bool{}

			for _, res := range found.Results {
				u, err := url.Parse(res.URL)
				if err != nil || !containsHost(hosts, u.Host) {
					continue
				}

				m := f.thread.FindStringSubmatch(u.Path)
				if m == nil {
					continue
				}

				link := scheme + "://" + u.Host + m[1] + "/"
				if seen[link] {
					continue
				}

				seen[link] = true
				threads = append(threads, Item{
					NativeID: link, URL: link, Title: res.Title, Text: res.Snippet, Container: f.site,
					PublishedAt: parseDate(res.PublishedAt),
				})
			}

			detail := "web search " + answeredBy(found)
			if len(threads) == 0 {
				return Result{Detail: detail + "; no thread among its results"}, nil
			}

			threads = threads[:min(len(threads), r.Limit)]
			read, excerpts := 0, 0

			var failed *SourceError

			// Every hop of a thread's read is checked against its site's robots.txt and content signals, the
			// thread's first address and each redirect's (RFC 9309 §2.2: the rules are the URI's own).
			allowed := func(u *url.URL) error {
				rules, err := rb.of(ctx, r, u.Scheme+"://"+u.Host)
				if err != nil {
					return err
				}

				if !rules.readable(u.EscapedPath()) {
					return errDisallowed
				}

				return nil
			}

			for i := range threads {
				if failed != nil && stopsHost(failed.Code) {
					// The forum refused or asked to slow down: its other threads keep their excerpts.
					break
				}

				a, err := readPage(ctx, pages, r, threads[i].URL, allowed)

				switch {
				case errors.Is(err, errDisallowed):
					excerpts++
				case err != nil:
					// A thread not read keeps its excerpt, and the read is partial with its code, so coverage and
					// health show it (last30days doctor.py:128-161).
					f := AsFailure(err)
					failed = worse(failed, &SourceError{Code: f.Code, Detail: "thread " + threads[i].URL + ": " + f.Detail,
						Until: f.Until})
				default:
					threads[i] = readThread(threads[i], string(a.body))
					read++
				}
			}

			detail += fmt.Sprintf("; %d threads read, %d as the search's excerpt (robots.txt or content signals)",
				read, excerpts)

			return Result{Items: threads, Detail: detail, Failed: failed}, nil
		},
	}
}

func containsHost(hosts []string, host string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	for _, h := range hosts {
		if h == host {
			return true
		}
	}

	return false
}

var (
	metaTag    = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	metaAttr   = regexp.MustCompile(`(?is)\b(property|name|content)\s*=\s*"([^"]*)"`)
	dateTime   = regexp.MustCompile(`\bdatetime="([^"]+)"`)
	dataAuthor = regexp.MustCompile(`\bdata-author="([^"]+)"`)
)

// readThread fills a thread's item from its page: its title and first post's excerpt from the page's Open Graph tags
// or description, its date and author from its JSON-LD (Tinhte's DiscussionForumPosting) or else the first post's
// `<time datetime>` and `data-author` (XenForo's, Voz's and Otofun's), read from each site on 2026-11-28 (inferred: no
// reference reads these forums).
func readThread(it Item, page string) Item {
	meta := map[string]string{}

	for _, tag := range metaTag.FindAllString(page, -1) {
		key, content := "", ""

		for _, a := range metaAttr.FindAllStringSubmatch(tag, -1) {
			if strings.EqualFold(a[1], "content") {
				content = html.UnescapeString(a[2])
			} else {
				key = strings.ToLower(a[2])
			}
		}

		if _, ok := meta[key]; key != "" && !ok {
			meta[key] = content
		}
	}

	if t := strings.TrimSpace(meta["og:title"]); t != "" {
		it.Title = t
	}

	for _, k := range []string{"og:description", "description"} {
		if t := strings.TrimSpace(meta[k]); t != "" {
			it.Text = t

			break
		}
	}

	for _, m := range jsonLD.FindAllStringSubmatch(page, -1) {
		for _, obj := range ldObjects(m[1]) {
			if d := parseDate(str(obj["datePublished"])); d != nil && it.PublishedAt == nil {
				it.PublishedAt = d
			}

			if a, ok := obj["author"].(map[string]any); ok && it.Author == "" {
				it.Author = strings.TrimSpace(str(a["name"]))
			}
		}
	}

	if m := dateTime.FindStringSubmatch(page); m != nil && it.PublishedAt == nil {
		it.PublishedAt = parseDate(m[1])
	}

	if m := dataAuthor.FindStringSubmatch(page); m != nil && it.Author == "" {
		it.Author = html.UnescapeString(m[1])
	}

	return it
}
