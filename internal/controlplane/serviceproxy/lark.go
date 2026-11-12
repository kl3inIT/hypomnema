package serviceproxy

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// larkMethods are the methods of Lark's own command-line tool with the risk
// it gives each: read, write or high-risk-write. Taken from lark-cli 1.0.100
// (github.com/larksuite/cli, commit ac22a60, MIT, Copyright (c) 2026 Lark
// Technologies Pte. Ltd.), keeping only each method's service, HTTP method,
// path and risk. lark-cli declares what it calls in two places, and the list
// has both (issue #236): its service catalog
// (internal/registry/catalog/services/*.json) and its shortcut commands
// (shortcuts/<group>/*.go: each command's Risk and the calls of its dry-run
// description). Base, Docs, Whiteboard and Search exist only as shortcuts. A
// shortcut's call belongs to the service whose part of the API it is; a GET
// is a read whatever its command, and a call that commands of different
// risks name has the highest. The tool itself asks for --yes before a
// high-risk write (extension/command/definition.go).
//
//go:embed lark_methods.json
var larkMethods []byte

// larkHost is where Lark's API is, for the international brand the
// Integration declares.
const larkHost = "open.larksuite.com"

// The risks lark-cli gives a method.
const (
	larkRead  = "read"
	larkWrite = "write"
	larkRisky = "high-risk-write"
)

// larkServices names a service of lark-cli's catalog or shortcuts as the owner reads it,
// in the order they are listed.
var larkServices = []struct{ key, name string }{
	{"im", "Messages"}, {"mail", "Mail"}, {"calendar", "Calendar"}, {"task", "Tasks"}, {"drive", "Drive"},
	{"wiki", "Wiki"}, {"doc", "Docs"}, {"sheets", "Sheets"}, {"base", "Base"}, {"slides", "Slides"},
	{"whiteboard", "Whiteboard"}, {"mindnotes", "Mind notes"}, {"search", "Search"},
	{"minutes", "Minutes"}, {"vc", "Meetings"}, {"approval", "Approvals"}, {"okr", "OKR"},
	{"contact", "Contacts"}, {"attendance", "Attendance"},
}

var larkCatalog = sync.OnceValue(func() Catalog {
	var methods []struct {
		Service string `json:"service"`
		Method  string `json:"method"`
		Path    string `json:"path"`
		Risk    string `json:"risk"`
	}

	// The file is this package's own; one that does not read is a broken build, and a catalog without its
	// methods then asks about everything but a plain GET.
	_ = json.Unmarshal(larkMethods, &methods)

	routes := map[string][]Route{}
	// A service's part of the API: "/open-apis/im", from the paths of its methods.
	parts := map[string][]string{}

	for _, m := range methods {
		key := m.Service + "." + m.Risk
		routes[key] = append(routes[key], Route{Method: m.Method, Host: larkHost, Path: m.Path})

		if segments := strings.Split(strings.Trim(m.Path, "/"), "/"); len(segments) > 1 {
			part := "/" + segments[0] + "/" + segments[1]
			if !slices.Contains(parts[m.Service], part) {
				parts[m.Service] = append(parts[m.Service], part)
			}
		}
	}

	// lark-cli's list does not name every method its own commands call: sending a message is not in it. A
	// request to a service's part of the API that the list does not name is that service's read when it is a
	// GET, its high-risk change when it deletes, and its write otherwise; a named method wins, since it names
	// more of the path.
	for service, prefixes := range parts {
		for _, prefix := range prefixes {
			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch} {
				routes[service+"."+larkWrite] = append(routes[service+"."+larkWrite],
					Route{Method: method, Host: larkHost, Path: prefix + "/{rest...}"})
			}

			routes[service+"."+larkRisky] = append(routes[service+"."+larkRisky],
				Route{Method: http.MethodDelete, Host: larkHost, Path: prefix + "/{rest...}"})
			// And a GET it does not name is that service's read: listing chats is not in the list.
			routes[service+"."+larkRead] = append(routes[service+"."+larkRead],
				Route{Method: http.MethodGet, Host: larkHost, Path: prefix + "/{rest...}"})
		}
	}

	c := Catalog{Integration: "lark"}

	for _, s := range larkServices {
		for _, a := range []Action{
			{
				ID: "lark." + s.key + ".read", Name: "Read", Kind: Reads,
				Text: "What lark-cli marks as a read, and any GET it does not name",
			},
			{
				ID: "lark." + s.key + ".write", Name: "Create or change", Kind: NeedsCare,
				Text: "What lark-cli marks as a write, and what it does not name: sending, creating, editing",
			},
			{
				ID: "lark." + s.key + ".risky", Name: "Delete and other high-risk changes", Kind: NeedsCare,
				Text: "What lark-cli itself asks to confirm: deleting, transferring, changing access",
			},
		} {
			risk := map[string]string{"read": larkRead, "write": larkWrite, "risky": larkRisky}[a.ID[len("lark."+s.key+"."):]]

			a.Service, a.Routes = s.name, routes[s.key+"."+risk]
			if len(a.Routes) > 0 {
				c.Actions = append(c.Actions, a)
			}
		}
	}

	// A read of a part of Lark that lark-cli's catalog does not have, such as a document's text: every GET the
	// catalog does name is a read. Anything else it does not have asks, as any request that is no action.
	c.Actions = append(c.Actions, Action{
		ID: "lark.other.read", Service: "Everything else", Name: "Read", Kind: Reads,
		Text:   "A GET to a part of Lark that lark-cli's own list does not have; any other request there asks",
		Routes: []Route{{Method: http.MethodGet, Host: larkHost, Path: "/open-apis/{rest...}"}},
	})

	return c
})
