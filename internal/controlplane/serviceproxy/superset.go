package serviceproxy

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// supersetMethods are the methods of Superset's own description of its API:
// each one's resource (the part of the API after /api/v1/), HTTP method and
// path, 210 methods on 157 paths. Taken from superset.spec.json as
// Winding2020/superset-mcp ships it (commit ff8e0eb, MIT per its
// package.json; aptro/superset-mcp, commit b3dd425, MIT, ships the same
// bytes). The description itself is Apache Superset's (Apache License 2.0,
// served by a Superset at /api/v1/_openapi); only these three fields of each
// method are kept. The notice is in third_party/superset/NOTICE.
//
//go:embed superset_methods.json
var supersetMethods []byte

// SupersetID is the Integration whose catalog this is. Its Connections each
// have their own address, so its routes name no host: a request is matched
// by its method and path, and sent to the address of the Connection it was
// made for.
const SupersetID = "superset"

// supersetServices names the parts of Superset as the owner reads them, in
// the order they are listed, each with the resources of the API that are it.
var supersetServices = []struct {
	key, name string
	resources []string
}{
	{"dashboards", "Dashboards", []string{"dashboard", "embedded_dashboard"}},
	{"charts", "Charts", []string{"chart", "explore"}},
	{"datasets", "Datasets", []string{"dataset", "datasource"}},
	{"databases", "Databases", []string{"database"}},
	{"sqllab", "SQL Lab", []string{"sqllab"}},
	{"queries", "Queries", []string{"query", "saved_query"}},
	{"reports", "Alerts and reports", []string{"report"}},
	{"annotations", "Annotations", []string{"annotation_layer"}},
	{"tags", "Tags", []string{"tag"}},
	{"css", "CSS templates", []string{"css_template"}},
	{"rls", "Row level security", []string{"rowlevelsecurity"}},
	{"assets", "Import and export", []string{"assets"}},
	{"logs", "Logs", []string{"log"}},
	{"system", "The account and the server", []string{
		"me", "user", "menu", "available_domains", "async_event", "advanced_data_type", "cachekey", "openapi",
	}},
}

// supersetReadPosts are the methods that are not a GET and still only read:
// they are sent a body because what they ask for does not fit an address.
// Each is what Superset's description says it is.
var supersetReadPosts = []string{
	// "Return payload data response for the given query": a chart's rows.
	"POST /api/v1/chart/data",
	// "Format SQL code": the text comes back laid out, nothing runs.
	"POST /api/v1/sqllab/format_sql/",
	// "Compute and cache a screenshot": a picture of what is there.
	"POST /api/v1/dashboard/{pk}/cache_dashboard_screenshot/",
}

// supersetSQL are the methods that run or cost SQL the caller wrote.
var supersetSQL = []string{
	"POST /api/v1/sqllab/execute/",
	"POST /api/v1/sqllab/estimate/",
	"POST /api/v1/database/{pk}/validate_sql/",
}

// The three things a resource's methods are, and what runs SQL.
const (
	supersetRead   = "read"
	supersetWrite  = "write"
	supersetDelete = "delete"
)

var supersetCatalog = sync.OnceValue(func() Catalog {
	var methods []struct {
		Resource string `json:"resource"`
		Method   string `json:"method"`
		Path     string `json:"path"`
	}

	// The file is this package's own; one that does not read is a broken build, and a catalog without its
	// methods then asks about everything.
	_ = json.Unmarshal(supersetMethods, &methods)

	service := map[string]string{}
	for _, s := range supersetServices {
		for _, r := range s.resources {
			service[r] = s.key
		}
	}

	byAction := map[string][]Route{}
	sql := []Route{}

	for _, m := range methods {
		key, known := service[m.Resource]
		if !known {
			// Signing in and Superset's users and roles have actions of their own, below.
			continue
		}

		named := m.Method + " " + m.Path

		if slices.Contains(supersetSQL, named) {
			sql = append(sql, Route{Method: m.Method, Path: m.Path})

			continue
		}

		what := supersetWrite

		switch {
		case m.Method == http.MethodGet || slices.Contains(supersetReadPosts, named):
			what = supersetRead
		case m.Method == http.MethodDelete:
			what = supersetDelete
		}

		byAction[key+"."+what] = append(byAction[key+"."+what], Route{Method: m.Method, Path: m.Path})
	}

	// A Superset newer than the description has methods it does not name. A request to a resource's part of
	// the API that the description does not name is that resource's read when it is a GET, its delete when it
	// deletes and its change otherwise, as Lark's catalog has it; a named method wins, since it names more of
	// the path. Only an action the description has a method of gets them: a part of Superset that has nothing
	// to delete today has no delete to set an answer for, and a request to delete there asks as any request
	// that is no action.
	rest := map[string][]string{
		supersetRead:   {http.MethodGet},
		supersetWrite:  {http.MethodPost, http.MethodPut, http.MethodPatch},
		supersetDelete: {http.MethodDelete},
	}

	for _, s := range supersetServices {
		for what, methods := range rest {
			key := s.key + "." + what
			if len(byAction[key]) == 0 {
				continue
			}

			for _, resource := range s.resources {
				if resource == "openapi" {
					continue
				}

				byAction[key] = append(byAction[key],
					routes("", methods, "/api/v1/"+resource, "/api/v1/"+resource+"/{rest...}")...)
			}
		}
	}

	c := Catalog{Integration: SupersetID}

	for _, s := range supersetServices {
		for _, a := range []Action{
			{
				ID: "superset." + s.key + "." + supersetRead, Name: "Read", Kind: Reads,
				Text: "Every GET, and what Superset reads with a POST: a chart's data, laying SQL out, a screenshot",
			},
			{
				ID: "superset." + s.key + "." + supersetWrite, Name: "Create or change", Kind: NeedsCare,
				Text: "Creating, changing, importing, uploading; the account's roles at Superset still decide",
			},
			{ID: "superset." + s.key + "." + supersetDelete, Name: "Delete", Kind: NeedsCare},
		} {
			a.Service, a.Routes = s.name, byAction[s.key+a.ID[strings.LastIndex(a.ID, "."):]]
			if len(a.Routes) > 0 {
				c.Actions = append(c.Actions, a)
			}
		}

		if s.key == "sqllab" {
			c.Actions = append(c.Actions, Action{
				ID: "superset.sql.run", Service: s.name, Name: "Run SQL", Kind: NeedsCare,
				Text:   "Run SQL the agent wrote on a database, estimate its cost or have a database check it",
				Routes: sql,
			})
		}
	}

	const security = "/api/v1/security"

	c.Actions = append(c.Actions,
		Action{
			ID: "superset.security.read", Service: "Users, roles and sign-in", Name: "Read", Kind: Reads,
			Text: "List users, roles and permissions; these are people's names and accounts",
			Routes: []Route{
				{Method: http.MethodGet, Path: security},
				{Method: http.MethodGet, Path: security + "/{rest...}"},
			},
		},
		Action{
			ID: "superset.security.admin", Service: "Users, roles and sign-in", Name: "Administer", Kind: NeedsCare,
			Text: "Create, change or delete users, roles and permissions; sign in or make a guest token",
			Routes: routes("", []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete},
				security, security+"/{rest...}"),
		},
		// A read of a part of Superset the description does not have. Anything else it does not have asks, as
		// any request that is no action.
		Action{
			ID: "superset.other.read", Service: "Everything else", Name: "Read", Kind: Reads,
			Text:   "A GET to a part of Superset's API that its description does not have; any other request there asks",
			Routes: []Route{{Method: http.MethodGet, Path: "/api/v1/{rest...}"}},
		},
	)

	return c
})
