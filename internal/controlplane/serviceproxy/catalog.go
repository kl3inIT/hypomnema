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
	Routes   []Route
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
}

// Has reports whether host is one the Integration's actions are on.
func (c Catalog) Has(host string) bool {
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

// Action returns a listed action by its id.
func (c Catalog) Action(id string) (Action, bool) {
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

// google is the catalog of the Google Integration, for the requests the
// Google Workspace CLI makes. The paths were read from what gws 0.22.5 sent
// through a probe on 2026-11-11 (issue #154): a send and a draft go to the
// /upload path as multipart, and Calendar and Drive share one host, so a
// service is told by its path. Onyx's Gmail, Calendar and Drive catalogs
// name the same actions (providers/gmail.py, google_calendar.py,
// google_drive.py).
func google() Catalog {
	const (
		gmail  = "gmail.googleapis.com"
		www    = "www.googleapis.com"
		docs   = "docs.googleapis.com"
		sheets = "sheets.googleapis.com"

		user   = "/gmail/v1/users/{user}"
		upload = "/upload/gmail/v1/users/{user}"
	)

	var (
		get    = []string{http.MethodGet}
		post   = []string{http.MethodPost}
		del    = []string{http.MethodDelete}
		write  = []string{http.MethodPost, http.MethodPut, http.MethodPatch}
		change = []string{http.MethodPut, http.MethodPatch}
	)

	discovery := Action{
		ID: "google.discovery", Plumbing: true, Kind: Reads,
		Routes: append(append(append(
			routes(www, get, "/discovery/v1/apis/{api}/{version}/rest"),
			routes(gmail, get, "/$discovery/rest")...),
			routes(docs, get, "/$discovery/rest")...),
			routes(sheets, get, "/$discovery/rest")...),
	}

	return Catalog{Integration: "google", Actions: []Action{
		discovery,
		{
			ID: "gmail.send", Service: "Gmail", Name: "Send mail", Kind: NeedsCare,
			Text: "A new message, a reply, a forward or a draft",
			Routes: routes(gmail, post, user+"/messages/send", upload+"/messages/send", user+"/drafts/send",
				upload+"/drafts/send"),
		},
		{
			ID: "gmail.trash", Service: "Gmail", Name: "Trash or delete mail", Kind: NeedsCare,
			Text: "Move a message or a thread to the trash, or delete it or a draft",
			Routes: append(
				routes(gmail, post, user+"/messages/{id}/trash", user+"/threads/{id}/trash", user+"/messages/batchDelete"),
				routes(gmail, del, user+"/messages/{id}", user+"/threads/{id}", user+"/drafts/{id}")...),
		},
		{
			ID: "gmail.label", Service: "Gmail", Name: "Label and archive", Kind: NeedsCare,
			Text: "Mark read, add or remove labels, take out of the trash",
			Routes: append(
				routes(gmail, post, user+"/messages/{id}/modify", user+"/threads/{id}/modify", user+"/messages/batchModify",
					user+"/messages/{id}/untrash", user+"/threads/{id}/untrash", user+"/labels"),
				routes(gmail, append(change, http.MethodDelete), user+"/labels/{id}")...),
		},
		{
			ID: "gmail.draft", Service: "Gmail", Name: "Write drafts", Kind: Changes,
			Text: "Create and edit drafts; nothing is sent",
			Routes: append(
				routes(gmail, post, user+"/drafts", upload+"/drafts"),
				routes(gmail, change, user+"/drafts/{id}", upload+"/drafts/{id}")...),
		},
		{
			ID: "gmail.read", Service: "Gmail", Name: "Read mail", Kind: Reads,
			Text:   "List, search and read messages, threads, drafts, labels and attachments",
			Routes: routes(gmail, get, user+"/{rest...}"),
		},
		{
			ID: "calendar.delete", Service: "Calendar", Name: "Delete an event or a calendar", Kind: NeedsCare,
			Routes: routes(www, del, "/calendar/v3/{rest...}"),
		},
		{
			ID: "calendar.write", Service: "Calendar", Name: "Create or change an event", Kind: NeedsCare,
			Text:   "Guests, if any, get an email from Google",
			Routes: routes(www, write, "/calendar/v3/{rest...}"),
		},
		{
			ID: "calendar.read", Service: "Calendar", Name: "Read events", Kind: Reads,
			Text:   "The agenda, free and busy times, one event",
			Routes: routes(www, get, "/calendar/v3/{rest...}"),
		},
		{
			ID: "drive.share", Service: "Drive", Name: "Share a file", Kind: NeedsCare,
			Text: "Give someone access, change it or take it away",
			Routes: routes(www, []string{http.MethodPost, http.MethodPatch, http.MethodDelete},
				"/drive/v3/files/{id}/permissions", "/drive/v3/files/{id}/permissions/{permission}"),
		},
		{
			ID: "drive.delete", Service: "Drive", Name: "Delete a file", Kind: NeedsCare,
			Text:   "Delete a file or empty the trash",
			Routes: routes(www, del, "/drive/v3/{rest...}"),
		},
		{
			ID: "drive.edit", Service: "Drive", Name: "Change a file", Kind: NeedsCare,
			Text: "Replace a file's content, rename, move or copy it",
			Routes: append(
				routes(www, change, "/drive/v3/files/{id}", "/upload/drive/v3/files/{id}"),
				routes(www, post, "/drive/v3/files/{id}/copy")...),
		},
		{
			ID: "drive.upload", Service: "Drive", Name: "Add a new file", Kind: Changes,
			Text:   "Upload a file or make a folder; nothing existing changes",
			Routes: routes(www, post, "/drive/v3/files", "/upload/drive/v3/files"),
		},
		{
			ID: "drive.read", Service: "Drive", Name: "Read files", Kind: Reads,
			Text:   "Search, list and download",
			Routes: routes(www, get, "/drive/v3/{rest...}"),
		},
		{
			ID: "docs.write", Service: "Docs and Sheets", Name: "Create or change a document or a sheet",
			Kind: NeedsCare,
			Routes: append(
				routes(docs, write, "/v1/documents", "/v1/documents/{rest...}"),
				routes(sheets, write, "/v4/spreadsheets", "/v4/spreadsheets/{rest...}")...),
		},
		{
			ID: "docs.read", Service: "Docs and Sheets", Name: "Read documents and sheets", Kind: Reads,
			Routes: append(
				routes(docs, get, "/v1/documents/{rest...}"),
				routes(sheets, get, "/v4/spreadsheets/{rest...}")...),
		},
	}}
}
