package serviceproxy

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
)

// Google through the proxy, for the requests the Google Workspace CLI (gws) makes (issue #154). The catalog is
// generated (issue #741, step 3 of #713): every method of the Discovery documents of the APIs gws names
// (crates/google-workspace/src/services.rs: Gmail, Calendar, Drive, Docs, Sheets, Slides, Tasks, People, Chat,
// Classroom, Forms, Keep, Meet, Admin reports, Workspace events, Model Armor, Apps Script) and of Search Console is an
// action, so that nothing gws sends by a method's name is unlisted. google_methods.json keeps, of each method, its
// API, id, HTTP method, host, flatPath and the paths its media is uploaded to, read where gws reads them
// (discovery.rs, executor.rs build_url); catalogsgen writes it from the documents catalog_sources.json pins, and the
// notice is in third_party/google/NOTICE.
//
// Each API is a service the owner reads (google_workspace_mcp's per-service levels), with a read, a write and a
// delete: a GET reads, a DELETE deletes, anything else writes (github-mcp-server's ReadOnlyHint, Onyx's defaults),
// except the few POSTs that only read, listed one by one with what their document says they do (googleReadPosts,
// as Superset's supersetReadPosts). A method's scopes do not decide: drive.files.copy and
// slides.presentations.batchUpdate take a read-only scope and still change things. Reads run; the rest asks. The
// actions the catalog named by hand before (read off one probe of gws 0.22.5 on 2026-11-11, and Onyx's Gmail,
// Calendar and Drive lists, providers/gmail.py, google_calendar.py, google_drive.py) keep their ids, so that the
// owner's answers stay: the methods they named are mapped to them by googleNamed, Sheets stays with Docs, and their
// GET routes stay for what the documents do not have.

//go:embed google_methods.json
var googleMethodsFile []byte

// googleDeclaration is google_methods.json: the gws version it was generated for (agenttool.GoogleWorkspaceCLI's),
// and every method.
type googleDeclaration struct {
	GWS     string         `json:"gws"`
	Methods []googleMethod `json:"methods"`
}

// googleMethod is one method of a Discovery document.
type googleMethod struct {
	API     string   `json:"api"`
	ID      string   `json:"id"`
	Method  string   `json:"method"`
	Host    string   `json:"host"`
	Path    string   `json:"path"`
	Uploads []string `json:"uploads"`
}

// googleService is a part of Google as the owner reads it: the APIs, by their Discovery name, that are it.
type googleService struct {
	key, name, things string
	apis              []string
}

// googleServices are in the order the owner reads them: the five the catalog named before first, as they were.
var googleServices = []googleService{
	{"gmail", "Gmail", "mail and its settings", []string{"gmail"}},
	{"calendar", "Calendar", "events and calendars", []string{"calendar"}},
	{"drive", "Drive", "files", []string{"drive"}},
	{"docs", "Docs and Sheets", "documents and sheets", []string{"docs", "sheets"}},
	{"searchconsole", "Search Console", "Search Console", []string{"searchconsole"}},
	{"slides", "Slides", "presentations", []string{"slides"}},
	{"tasks", "Tasks", "task lists and tasks", []string{"tasks"}},
	{"people", "Contacts", "contacts and profiles", []string{"people"}},
	{"chat", "Chat", "Chat spaces and messages", []string{"chat"}},
	{"classroom", "Classroom", "classes, rosters and coursework", []string{"classroom"}},
	{"forms", "Forms", "forms and their responses", []string{"forms"}},
	{"keep", "Keep", "notes", []string{"keep"}},
	{"meet", "Meet", "meeting spaces and conference records", []string{"meet"}},
	{"reports", "Admin reports", "audit logs and usage reports", []string{"admin"}},
	{"events", "Workspace events", "event subscriptions", []string{"workspaceevents"}},
	{"modelarmor", "Model Armor", "Model Armor's templates and checks", []string{"modelarmor"}},
	{"script", "Apps Script", "Apps Script projects and their runs", []string{"script"}},
}

// googleReadPosts are the methods that are not a GET and still only read, each with what its Discovery document
// says it does.
var googleReadPosts = []string{
	// "Returns free/busy information for a set of calendars."
	"calendar.freebusy.query",
	// "Query your data with filters and parameters that you define. Returns zero or more rows …"
	"webmasters.searchanalytics.query",
	// "Index inspection."
	"searchconsole.urlInspection.index.inspect",
	// "Runs Mobile-Friendly Test for a given URL." A read of the catalog before it was generated (issue #609).
	"searchconsole.urlTestingTools.mobileFriendlyTest.run",
	// "Searches for messages in Google Chat that the calling user has access to."
	"chat.spaces.messages.search",
	// "Downloads the content of a file."
	"drive.files.download",
}

// googleNamed maps chosen methods, by their Discovery id, to the actions the catalog named before it was generated.
// Every other method is its service's read, write or delete.
var googleNamed = map[string]string{
	"gmail.users.messages.send": "gmail.send",
	"gmail.users.drafts.send":   "gmail.send",

	"gmail.users.messages.trash":       "gmail.trash",
	"gmail.users.threads.trash":        "gmail.trash",
	"gmail.users.messages.batchDelete": "gmail.trash",
	"gmail.users.messages.delete":      "gmail.trash",
	"gmail.users.threads.delete":       "gmail.trash",
	"gmail.users.drafts.delete":        "gmail.trash",

	"gmail.users.messages.modify":      "gmail.label",
	"gmail.users.threads.modify":       "gmail.label",
	"gmail.users.messages.batchModify": "gmail.label",
	"gmail.users.messages.untrash":     "gmail.label",
	"gmail.users.threads.untrash":      "gmail.label",
	"gmail.users.labels.create":        "gmail.label",
	"gmail.users.labels.update":        "gmail.label",
	"gmail.users.labels.patch":         "gmail.label",
	"gmail.users.labels.delete":        "gmail.label",

	"gmail.users.drafts.create": "gmail.draft",
	"gmail.users.drafts.update": "gmail.draft",

	"drive.permissions.create": "drive.share",
	"drive.permissions.update": "drive.share",
	"drive.permissions.delete": "drive.share",
	"drive.files.update":       "drive.edit",
	"drive.files.copy":         "drive.edit",
	"drive.files.create":       "drive.upload",

	"webmasters.sitemaps.submit": "searchconsole.sitemap",
	"webmasters.sitemaps.delete": "searchconsole.sitemap",
	"webmasters.sites.add":       "searchconsole.site",
	"webmasters.sites.delete":    "searchconsole.site",
}

// googleCurated are the actions the catalog named by hand before it was generated, with the names and sentences
// the owner has read: each takes the place of the generated action of its id, and the others are listed in their
// service between its read and its write.
var googleCurated = []Action{
	{
		ID: "gmail.read", Service: "Gmail", Name: "Read mail", Kind: Reads,
		Text: "List, search and read messages, threads, drafts, labels and attachments",
	},
	{
		ID: "gmail.send", Service: "Gmail", Name: "Send mail", Kind: NeedsCare,
		Text: "A new message, a reply, a forward or a draft",
	},
	{
		ID: "gmail.trash", Service: "Gmail", Name: "Trash or delete mail", Kind: NeedsCare,
		Text: "Move a message or a thread to the trash, or delete it or a draft",
	},
	{
		ID: "gmail.label", Service: "Gmail", Name: "Label and archive", Kind: NeedsCare,
		Text: "Mark read, add or remove labels, take out of the trash",
	},
	{
		ID: "gmail.draft", Service: "Gmail", Name: "Write drafts", Kind: Changes,
		Text: "Create and edit drafts; nothing is sent",
	},
	{
		ID: "calendar.read", Service: "Calendar", Name: "Read events", Kind: Reads,
		Text: "The agenda, free and busy times, one event",
	},
	{
		ID: "calendar.write", Service: "Calendar", Name: "Create or change an event", Kind: NeedsCare,
		Text: "Guests, if any, get an email from Google",
	},
	{ID: "calendar.delete", Service: "Calendar", Name: "Delete an event or a calendar", Kind: NeedsCare},
	{ID: "drive.read", Service: "Drive", Name: "Read files", Kind: Reads, Text: "Search, list and download"},
	{
		ID: "drive.share", Service: "Drive", Name: "Share a file", Kind: NeedsCare,
		Text: "Give someone access, change it or take it away",
	},
	{
		ID: "drive.edit", Service: "Drive", Name: "Change a file", Kind: NeedsCare,
		Text: "Replace a file's content, rename, move or copy it",
	},
	{
		ID: "drive.upload", Service: "Drive", Name: "Add a new file", Kind: Changes,
		Text: "Upload a file or make a folder; nothing existing changes",
	},
	{
		ID: "drive.delete", Service: "Drive", Name: "Delete a file", Kind: NeedsCare,
		Text: "Delete a file or empty the trash",
	},
	{ID: "docs.read", Service: "Docs and Sheets", Name: "Read documents and sheets", Kind: Reads},
	{
		ID: "docs.write", Service: "Docs and Sheets", Name: "Create or change a document or a sheet",
		Kind: NeedsCare,
	},
	{
		ID: "searchconsole.read", Service: "Search Console", Name: "Read Search Console", Kind: Reads,
		Text: "Sites, sitemaps, search performance, and why a page is or is not indexed",
	},
	{
		ID: "searchconsole.sitemap", Service: "Search Console", Name: "Submit or remove a sitemap", Kind: NeedsCare,
		Text: "Google crawls what a submitted sitemap lists",
	},
	{ID: "searchconsole.site", Service: "Search Console", Name: "Add or remove a site", Kind: NeedsCare},
}

// googleModelArmor is a regional host of Model Armor: its Discovery document names us-central1's, and gws reaches
// the region a template is in (crates/google-workspace-cli/src/helpers/modelarmor.rs: "Model Armor requires
// region-specific endpoints: modelarmor.{region}.rep.googleapis.com"). Each region's requests are the same methods.
var googleModelArmor = regexp.MustCompile(`^modelarmor\.[a-z0-9-]+\.rep\.googleapis\.com$`)

// googleWWW is the host Calendar and Drive are on, and where gws reads Discovery documents first.
const googleWWW = "www.googleapis.com"

// googleSearchConsole is Search Console's host: a site and a sitemap are each a URL in one segment (issue #617).
const googleSearchConsole = "searchconsole.googleapis.com"

// googleRoute is a declared method's address, its path split into segments.
type googleRoute struct {
	method, host string
	segments     []string
	action       string
}

// googleTable is the declaration read once: its routes, the hosts they are on and the catalog's actions.
type googleTable struct {
	routes  []googleRoute
	hosts   []string
	armor   string
	catalog Catalog
}

var googleTableOf = sync.OnceValue(func() googleTable {
	// The file is this package's own; one that does not read is a broken build, which the catalog's tests catch, and
	// a catalog without its methods then asks about everything.
	d, _ := readGoogleDeclaration(googleMethodsFile)

	return googleTableFrom(d)
})

func readGoogleDeclaration(raw []byte) (googleDeclaration, error) {
	var d googleDeclaration

	err := json.Unmarshal(raw, &d)

	return d, err
}

// googleServiceOf is the service of a Discovery document; one no service names has none.
func googleServiceOf(api string) (googleService, bool) {
	for _, s := range googleServices {
		if slices.Contains(s.apis, api) {
			return s, true
		}
	}

	return googleService{}, false
}

// googleMethodAction is the id of a method's action, and false for a method of an API no service names.
func googleMethodAction(m googleMethod) (string, bool) {
	if id, ok := googleNamed[m.ID]; ok {
		return id, true
	}

	s, ok := googleServiceOf(m.API)
	if !ok {
		return "", false
	}

	what := githubWrite

	switch {
	case m.Method == http.MethodGet || slices.Contains(googleReadPosts, m.ID):
		what = githubRead
	case m.Method == http.MethodDelete:
		what = githubDelete
	}

	return s.key + "." + what, true
}

func googleTableFrom(d googleDeclaration) googleTable {
	var t googleTable

	present := map[string]bool{}

	for _, m := range d.Methods {
		id, ok := googleMethodAction(m)
		if !ok {
			continue
		}

		present[id] = true

		for _, p := range append([]string{m.Path}, m.Uploads...) {
			t.routes = append(t.routes, googleRoute{method: m.Method, host: m.Host, segments: splitPath(p), action: id})
		}

		if !slices.Contains(t.hosts, m.Host) {
			t.hosts = append(t.hosts, m.Host)
		}

		if m.API == "modelarmor" {
			t.armor = m.Host
		}
	}

	hand := googleHandReads()

	// What a tool needs to run at all: gws reads a Discovery document where discovery.rs fetches it, on
	// www.googleapis.com or on the API's own host.
	discovery := Action{
		ID: "google.discovery", Plumbing: true, Kind: Reads,
		Routes: routes(googleWWW, []string{http.MethodGet}, "/discovery/v1/apis/{api}/{version}/rest"),
	}
	for _, h := range t.hosts {
		discovery.Routes = append(discovery.Routes, Route{Method: http.MethodGet, Host: h, Path: "/$discovery/rest"})
	}

	c := Catalog{
		Integration: integration.GoogleID, SlashInSegment: []string{googleSearchConsole}, Actions: []Action{discovery},
	}

	for _, s := range googleServices {
		generated := []Action{{
			ID: s.key + "." + githubRead, Service: s.name, Name: "Read " + s.things, Kind: Reads,
			Text: "Every GET there, and what its API reads with a POST",
		}}

		for _, a := range googleCurated {
			if a.Service == s.name && a.ID != s.key+"."+githubRead && a.ID != s.key+"."+githubWrite &&
				a.ID != s.key+"."+githubDelete {
				generated = append(generated, a)
			}
		}

		generated = append(generated,
			Action{
				ID: s.key + "." + githubWrite, Service: s.name, Name: "Create or change " + s.things, Kind: NeedsCare,
				Text: "Any request there that is not a GET and that no action above names",
			},
			Action{
				ID: s.key + "." + githubDelete, Service: s.name, Name: "Delete " + s.things, Kind: NeedsCare,
				Text: "A DELETE request there that no action above names",
			},
		)

		for _, a := range generated {
			if i := slices.IndexFunc(googleCurated, func(c Action) bool { return c.ID == a.ID }); i >= 0 {
				a = googleCurated[i]
			}

			a.Routes = hand[a.ID]
			if present[a.ID] || len(a.Routes) > 0 {
				c.Actions = append(c.Actions, a)
			}
		}
	}

	c.Reaches = func(host string) bool { return slices.Contains(t.hosts, host) || googleModelArmor.MatchString(host) }
	c.Name = func(req *http.Request, _ []byte, host string) Action {
		return t.actionOf(req.Method, host, req.URL.EscapedPath())
	}
	t.catalog = c

	return t
}

// googleHandReads are the GET routes the catalog named by hand before it was generated. The documents name every
// one of them more exactly; they stay for a request they do not have (a method newer than the pinned document), so
// that what read before still reads, as GitHub's catalog keeps its hand-written reads (githubHandReads).
func googleHandReads() map[string][]Route {
	get := []string{http.MethodGet}

	return map[string][]Route{
		"gmail.read":    routes("gmail.googleapis.com", get, "/gmail/v1/users/{user}/{rest...}"),
		"calendar.read": routes(googleWWW, get, "/calendar/v3/{rest...}"),
		"drive.read":    routes(googleWWW, get, "/drive/v3/{rest...}"),
		"docs.read": append(routes("docs.googleapis.com", get, "/v1/documents/{rest...}"),
			routes("sheets.googleapis.com", get, "/v4/spreadsheets/{rest...}")...),
		"searchconsole.read": routes(googleSearchConsole, get, "/webmasters/v3/{rest...}"),
	}
}

// google is the catalog of the Google Integration.
func google() Catalog { return googleTableOf().catalog }

// splitPath splits a path into its segments; a single trailing slash is ignored, as pathMatches does.
func splitPath(p string) []string { return strings.Split(strings.Trim(p, "/"), "/") }

// actionOf is the action of a request to Google. Its path's segments are read as Google reads them, decoded; an
// encoded slash stays in its segment (Search Console's site). Of the declared methods it matches, the one that names
// the most of its path is it: "messages/send" before "messages/{id}", "{name}:verb" before "{name}". Between methods
// that name as much of it, each one's action is the request's, and the strictest of the owner's answers governs
// it (Action.With). A request no method matches is a read the catalog named by hand, or unlisted.
func (t googleTable) actionOf(method, host, path string) Action {
	if googleModelArmor.MatchString(host) {
		host = t.armor
	}

	c := t.catalog

	if a := c.Match(method, host, path); a.Plumbing {
		return a
	}

	segments := splitPath(path)
	for i, s := range segments {
		decoded, err := url.PathUnescape(s)
		if err != nil {
			return unlisted(integration.GoogleID)
		}

		segments[i] = decoded
	}

	best, ids := [2]int{-1, -1}, []string{}

	for _, r := range t.routes {
		if r.method != method || r.host != host {
			continue
		}

		score, ok := r.matches(segments)
		if !ok {
			continue
		}

		switch {
		case score[0] > best[0] || (score[0] == best[0] && score[1] > best[1]):
			best, ids = score, []string{r.action}
		case score == best && !slices.Contains(ids, r.action):
			ids = append(ids, r.action)
		}
	}

	if len(ids) == 0 {
		return c.Match(method, host, path)
	}

	named := make([]Action, 0, len(ids))
	for _, id := range ids {
		if a, ok := c.Action(id); ok {
			named = append(named, a)
		}
	}

	if len(named) == 0 {
		return unlisted(integration.GoogleID)
	}

	rank := map[Kind]int{Reads: 0, Changes: 1, NeedsCare: 2}

	slices.SortStableFunc(named, func(a, b Action) int { return rank[b.Kind] - rank[a.Kind] })

	governing := named[0]
	governing.With = named[1:]

	return governing
}

// matches is whether a path's segments are the route's, and how much of it the route names: its literal segments,
// then the literal text of the segments that hold a parameter ("{keyPairId}:disable"). A parameter matches a
// segment's text between the literal text around it, and never nothing.
func (r googleRoute) matches(segments []string) ([2]int, bool) {
	var score [2]int

	if len(segments) != len(r.segments) {
		return score, false
	}

	for i, want := range r.segments {
		got := segments[i]

		open := strings.Index(want, "{")
		if open < 0 {
			if want != got {
				return score, false
			}

			score[0]++

			continue
		}

		closing := strings.LastIndex(want, "}")
		prefix, suffix := want[:open], want[closing+1:]

		if len(got) <= len(prefix)+len(suffix) || !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, suffix) {
			return score, false
		}

		score[1] += len(prefix) + len(suffix)
	}

	return score, true
}

// GoogleActionIDs names the action of every method of a google_methods.json, keyed "METHOD host/path", each upload
// path too: catalogsgen prints which ones a regeneration adds, removes or moves. A path's parameters are read as a
// parameter's value.
func GoogleActionIDs(methods []byte) (map[string]string, error) {
	d, err := readGoogleDeclaration(methods)
	if err != nil {
		return nil, err
	}

	t := googleTableFrom(d)
	out := make(map[string]string, len(d.Methods))

	for _, m := range d.Methods {
		for _, p := range append([]string{m.Path}, m.Uploads...) {
			out[m.Method+" "+m.Host+p] = t.actionOf(m.Method, m.Host, googleSample(p)).ID
		}
	}

	return out, nil
}

// googleParameter is a parameter of a path, with what is written around it in its segment.
var googleParameter = regexp.MustCompile(`\{[^}/]*\}`)

// googleSample is a path a method's template matches: each parameter is "1".
func googleSample(template string) string {
	return googleParameter.ReplaceAllString(template, "1")
}
