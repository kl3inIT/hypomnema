package serviceproxy

import (
	"cmp"
	"net/http"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Kind groups a service's actions as the owner reads them.
type Kind string

// The kinds of an action. A read looks, a change adds something that is easy
// to undo and reaches nobody else, and what needs care reaches other people
// or cannot be taken back.
const (
	Reads     Kind = "reads"
	Changes   Kind = "changes"
	NeedsCare Kind = "needs_care"
)

// Route recognizes a request as an action by its method, host and path, as
// Onyx's RestRoute does (backend/onyx/external_apps/providers/actions.py). A
// "{name}" segment of the path matches exactly one segment; a last segment
// "{name...}" matches one or more. Host is empty for an Integration whose
// Connections each have their own address (Superset): its requests are
// matched by method and path, and never come through a tunnel.
type Route struct {
	Method string
	Host   string
	Path   string
}

// Action is one thing an agent can do with a service: a name and a sentence
// for the owner, a kind, and the requests that are it. An Integration is a
// list of them, data and not a client (Onyx's EndpointSpec).
type Action struct {
	// ID is the action's name in commands and in the store: gmail.send.
	ID string
	// Service is the part of the Integration the action belongs to: Gmail.
	Service string
	Name    string
	Text    string
	Kind    Kind
	// Plumbing marks what a tool needs to run at all and no account is
	// involved in: it is allowed, carries no credential and is not listed.
	Plumbing bool
	// Never marks what is refused whatever the owner set: an AWS operation
	// that makes credentials (issue #331).
	Never  bool
	Routes []Route
	// Operations are the GraphQL operations that are the action, for an Integration whose API is GraphQL too
	// (GitHub's, issue #685).
	Operations []Operation
	// With are the other actions one request names beside this one: a GraphQL request with several root fields
	// (issue #739). Of them all, the one the owner's answers make the strictest governs the request (Onyx's
	// AllMatchedActions, sorted by policy severity).
	With []Action
}

// Default is the answer an action has until the owner gives another. Reading
// and what is easy to undo run on their own; the rest asks (Onyx: reading and
// drafts ALWAYS, an action that names no default ASK).
func (a Action) Default() string {
	if a.Kind == NeedsCare {
		return orchestration.PolicyAsk
	}

	return orchestration.PolicyAllow
}

// unlisted is the action of a request to an Integration's host that matches
// none of its actions. It always asks, whatever the owner set (Onyx: "a
// request to a built-in App that matches no action asks").
func unlisted(integration string) Action {
	return Action{
		ID: integration + ".unlisted", Service: "Other", Name: "Something this list does not name",
		Text: "A request to the service that matches no action above", Kind: NeedsCare,
	}
}

// Catalog is an Integration's actions, the most specific first: the first
// one a request matches is its action.
type Catalog struct {
	Integration string
	Actions     []Action
	// An Integration whose actions are too many to list (AWS, issue #331)
	// names them from the request instead: Reaches says which hosts are its,
	// Name names a request, and Known an action the owner sets an answer for.
	// Signed says each request is signed again with the Connection's
	// credentials, not given a bearer token.
	Reaches func(host string) bool
	Name    func(req *http.Request, body []byte, host string) Action
	Known   func(id string) (Action, bool)
	Signed  bool
	// SlashInSegment are the hosts whose paths carry a URL as one segment, its slashes encoded (%2F): Search
	// Console's site and sitemap (issue #617). Elsewhere an encoded slash is refused, as one the service could read
	// as two segments; on these hosts the service reads it as one, and every action that is not a read asks.
	SlashInSegment []string
	// Rewrite gives the body a request is sent on with, once its action is decided; nil sends it as it came. GitLab's
	// auto-merge is sent with each of its flags set, so that no version of GitLab reads it as a merge now (issue #740).
	Rewrite func(req *http.Request, body []byte, action Action) []byte
}

// Has reports whether host is one the Integration's actions are on.
func (c Catalog) Has(host string) bool {
	if c.Reaches != nil {
		return c.Reaches(host)
	}

	for _, a := range c.Actions {
		for _, r := range a.Routes {
			if r.Host == host {
				return true
			}
		}
	}

	return false
}

// Match returns the action a request is, and the unlisted action when it is
// none of the catalog's. Of the routes a request matches, the one that names
// the most of its path is the action: "messages/search" before
// "messages/{id}", and both before "{rest...}". Between equals the first in
// the catalog wins.
func (c Catalog) Match(method, host, path string) Action {
	best, named := unlisted(c.Integration), -1

	for _, a := range c.Actions {
		for _, r := range a.Routes {
			if r.Method != method || r.Host != host || !pathMatches(r.Path, path) {
				continue
			}

			if n := literals(r.Path); n > named {
				best, named = a, n
			}
		}
	}

	return best
}

// Of is the action a request is: named by the request for a catalog that
// names its actions so, matched against the routes otherwise.
func (c Catalog) Of(req *http.Request, body []byte, host string) Action {
	if c.Name != nil {
		return c.Name(req, body, host)
	}

	// The escaped path, so that a segment that is itself a URL, as Search Console's site and sitemap are
	// (https%3A%2F%2Fexample.com%2F), stays one segment (issue #609).
	return c.Match(req.Method, host, req.URL.EscapedPath())
}

// literals counts the segments of a route's path that are not placeholders.
func literals(template string) int {
	n := 0

	for _, segment := range strings.Split(strings.Trim(template, "/"), "/") {
		if !strings.HasPrefix(segment, "{") {
			n++
		}
	}

	return n
}

// Listed are the actions the owner sets an answer for, in the order he reads
// them: service by service, and within a service from what only looks to
// what needs care. Matching has its own order, the most specific first.
func (c Catalog) Listed() []Action {
	out := make([]Action, 0, len(c.Actions))
	service := map[string]int{}

	for _, a := range c.Actions {
		if a.Plumbing {
			continue
		}

		if _, seen := service[a.Service]; !seen {
			service[a.Service] = len(service)
		}

		out = append(out, a)
	}

	rank := map[Kind]int{Reads: 0, Changes: 1, NeedsCare: 2}

	slices.SortStableFunc(out, func(a, b Action) int {
		if by := cmp.Compare(service[a.Service], service[b.Service]); by != 0 {
			return by
		}

		return cmp.Compare(rank[a.Kind], rank[b.Kind])
	})

	return out
}

// Action returns a listed action by its id, or one the catalog names.
func (c Catalog) Action(id string) (Action, bool) {
	if c.Known != nil {
		return c.Known(id)
	}

	for _, a := range c.Listed() {
		if a.ID == id {
			return a, true
		}
	}

	return Action{}, false
}

// pathMatches compares a request path with a route's template, segment by
// segment. A single trailing slash is ignored.
func pathMatches(template, path string) bool {
	want := strings.Split(strings.Trim(template, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")

	for i, segment := range want {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "...}") {
			return i == len(want)-1 && len(got) > i && got[i] != ""
		}

		if i >= len(got) {
			return false
		}

		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			if got[i] == "" {
				return false
			}

			continue
		}

		if segment != got[i] {
			return false
		}
	}

	return len(got) == len(want)
}

// CatalogOf returns the catalog of an Integration that goes through the
// proxy, and false for one that does not.
func CatalogOf(integration string) (Catalog, bool) {
	switch integration {
	case "google":
		return google(), true
	case "lark":
		return larkCatalog(), true
	case SupersetID:
		return supersetCatalog(), true
	case AWSID:
		return awsCatalog, true
	case WhatsAppID:
		return whatsappCatalog(), true
	case ZaloID:
		return zaloCatalog(), true
	case TelegramID:
		return telegramCatalog(), true
	case GitHubID:
		return githubCatalog(), true
	}

	return Catalog{}, false
}

// routes builds the routes of several methods and paths on one host.
func routes(host string, methods []string, paths ...string) []Route {
	out := make([]Route, 0, len(methods)*len(paths))

	for _, m := range methods {
		for _, p := range paths {
			out = append(out, Route{Method: m, Host: host, Path: p})
		}
	}

	return out
}
