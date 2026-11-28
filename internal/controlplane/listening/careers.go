package listening

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// A company's careers page (issue #1289): what it hires for hints at what it builds. As last30days' jobs lane reads it
// (jobs.py), cut to the tiers a Vietnamese company's own page has: the page found by a web search for its careers
// (_search_for_careers_url), its roles read from the schema.org JobPosting the page or its role pages carry
// (extract_jsonld_jobs, jsonld_role_text; most careers pages emit it for Google Jobs, jobs.py:16-18). The hosted
// boards' APIs (Greenhouse, Lever, Workday…), the job sitemaps and the guessed domains are left out: none of the
// owner's companies (VETC, Cốc Cốc) uses one, checked 2026-11-28, so they wait for one that does.

const (
	// careersResults is how many results the web search for the careers page is asked (inferred: last30days takes
	// the first matching result of its backend's page).
	careersResults = 10
)

var (
	// careersWords mark a careers page's address (last30days jobs._search_for_careers_url), with Vietnam's
	// "tuyển dụng" (recruitment) added (inferred: no reference reads a Vietnamese site).
	careersWords = []string{
		"career", "/jobs", "ashbyhq", "greenhouse", "lever.co", "workable", "smartrecruiters", "myworkdayjobs",
		"tuyen-dung", "tuyendung",
	}
	// roleLink marks a link to a role's page (last30days jobs._JOBS_LINK_WORDS, with "tuyen-dung" and "viec-lam",
	// job, inferred for Vietnamese sites).
	roleLink = regexp.MustCompile(`(?i)job|position|opening|vacanc|role|career|tuyen-dung|viec-lam`)
	href     = regexp.MustCompile(`(?i)<a\b[^>]*\bhref\s*=\s*["']([^"'#]+)["']`)
	jsonLD   = regexp.MustCompile(`(?is)<script[^>]*type\s*=\s*["']application/ld\+json["'][^>]*>(.*?)</script>`)
)

// careersBackend reads the company the query names: its careers page, found by the web search `<query> careers jobs`,
// then the roles on it.
func careersBackend(pages *http.Client) Backend {
	return Backend{
		Name: BackendCareers, Host: BackendCareers, Probe: needsWeb,
		Search: func(ctx context.Context, r Request) (Result, error) {
			found, err := r.Web(ctx, strings.TrimSpace(r.Query)+" careers jobs", careersResults)
			if errors.Is(err, websearch.ErrNoResults) {
				return Result{}, Fail(CodeNotFound, "the web search found no careers page for %q", r.Query)
			}

			if err != nil {
				return Result{}, webFailure(err)
			}

			page := ""

			for _, res := range found.Results {
				low := strings.ToLower(res.URL)
				if strings.HasPrefix(low, "http") && containsAny(low, careersWords) {
					page = res.URL

					break
				}
			}

			if page == "" {
				return Result{}, Fail(CodeNotFound, "the web search found no careers page for %q", r.Query)
			}

			a, err := readPage(ctx, pages, r, page)
			if err != nil {
				return Result{}, err
			}

			jobs := jobPostings(string(a.body), a.url)
			detail := "careers page " + a.url

			if len(jobs) == 0 {
				links := roleLinks(string(a.body), a.url)
				failed := 0

				for _, link := range links[:min(len(links), r.Limit)] {
					role, err := readPage(ctx, pages, r, link)
					if err != nil {
						failed++

						if f := AsFailure(err); stopsHost(f.Code) {
							detail += "; its site refused: " + f.Error()

							break
						}

						continue
					}

					if posted := jobPostings(string(role.body), role.url); len(posted) > 0 {
						jobs = append(jobs, posted[0])
					}
				}

				detail += fmt.Sprintf("; %d role pages read", len(links[:min(len(links), r.Limit)])-failed)
			}

			if len(jobs) == 0 {
				return Result{}, Fail(CodeNotFound, "the careers page %s lists no role with JobPosting data", a.url)
			}

			return Result{Items: markSignals(jobs), Detail: detail}, nil
		},
	}
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}

	return false
}

// readPage reads a page of a host a web search named, in its turn at that host, through the page client, which dials
// public addresses only and whose redirects are followed one by one.
func readPage(ctx context.Context, client *http.Client, r Request, address string) (answer, error) {
	host := hostOf(address)

	return call{
		host: host, address: address, accept: "text/html,application/xhtml+xml", userAgent: pageUserAgent,
		follow: true,
	}.do(ctx, client, r)
}

// roleLinks are the links of a careers page to pages of roles on its own site, each once, in the page's order; a
// link with a query is a filter of the board, not a role (inferred).
func roleLinks(page, at string) []string {
	base, err := url.Parse(at)
	if err != nil {
		return nil
	}

	var out []string

	seen := map[string]bool{}

	for _, m := range href.FindAllStringSubmatch(page, -1) {
		link, err := base.Parse(strings.TrimSpace(m[1]))
		if err != nil || link.Hostname() != base.Hostname() || link.RawQuery != "" ||
			strings.TrimRight(link.Path, "/") == strings.TrimRight(base.Path, "/") || !roleLink.MatchString(link.Path) {
			continue
		}

		if s := link.String(); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	return out
}

// posting is a role read from a JobPosting, with its place, which the signals compare.
type posting struct {
	item     Item
	location string
}

// jobPostings are the schema.org JobPostings a page's JSON-LD holds, each title once (last30days
// extract_jsonld_jobs: title, url, description, datePosted, occupationalCategory, jobLocation).
func jobPostings(page, at string) []posting {
	var out []posting

	seen := map[string]bool{}

	for _, m := range jsonLD.FindAllStringSubmatch(page, -1) {
		for _, obj := range ldObjects(m[1]) {
			if !isType(obj["@type"], "JobPosting") {
				continue
			}

			title := strings.TrimSpace(str(obj["title"]))
			if title == "" {
				title = strings.TrimSpace(str(obj["name"]))
			}

			if title == "" || seen[title] {
				continue
			}

			seen[title] = true

			link := strings.TrimSpace(str(obj["url"]))
			if link == "" {
				link = at
			}

			org, _ := obj["hiringOrganization"].(map[string]any)
			location := jobLocation(obj["jobLocation"])
			text := stripHTML(str(obj["description"]))

			if location != "" {
				text = location + " — " + text
			}

			out = append(out, posting{location: location, item: Item{
				NativeID: NormalizeURL(link) + "#" + title, URL: link, Title: title, Text: text,
				Container: strings.TrimSpace(str(org["name"])), PublishedAt: parseDate(str(obj["datePosted"])),
			}})
		}
	}

	return out
}

// ldObjects are the objects of a JSON-LD script; none when it is not JSON.
func ldObjects(script string) []map[string]any {
	var doc any
	if json.Unmarshal([]byte(strings.TrimSpace(script)), &doc) != nil {
		return nil
	}

	return walkLD(doc)
}

// walkLD is every object in a JSON-LD document: the document, its lists and its @graph.
func walkLD(doc any) []map[string]any {
	switch v := doc.(type) {
	case map[string]any:
		out := []map[string]any{v}
		if g, ok := v["@graph"]; ok {
			out = append(out, walkLD(g)...)
		}

		return out
	case []any:
		var out []map[string]any
		for _, x := range v {
			out = append(out, walkLD(x)...)
		}

		return out
	default:
		return nil
	}
}

func isType(t any, want string) bool {
	switch v := t.(type) {
	case string:
		return v == want
	case []any:
		for _, x := range v {
			if str(x) == want {
				return true
			}
		}
	}

	return false
}

func str(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return fmt.Sprint(s)
	}
}

// jobLocation is a JobPosting's place: its city, region and country (last30days jobs._jsonld_location).
func jobLocation(v any) string {
	places := walkLD(v)
	for _, p := range places {
		addr, _ := p["address"].(map[string]any)
		if addr == nil {
			continue
		}

		var parts []string

		for _, k := range []string{"addressLocality", "addressRegion", "addressCountry"} {
			if s := strings.TrimSpace(str(addr[k])); s != "" && !strings.HasPrefix(s, "map[") {
				parts = append(parts, s)
			}
		}

		if len(parts) > 0 {
			return strings.Join(parts, ", ")
		}
	}

	return ""
}
