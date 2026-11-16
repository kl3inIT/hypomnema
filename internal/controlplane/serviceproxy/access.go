package serviceproxy

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// What a Connection's account can really do, action by action (issue #567).
// The owner's answer for an action says whether agents may; this says whether
// the account can, from what it was found to hold at the service
// (integration.Holdings): granted, missing or unknown, with a short reason,
// as lark-cli's `auth check` answers granted and missing scopes
// (cmd/auth/check.go) and Onyx shows a check's outcome with its message
// (CredentialChecksCard.tsx). An action no rule here can answer is unknown,
// and an unknown action runs as before: lark-cli skips its scope check when
// it cannot tell (cmd/service/service.go, checkServiceScopes).

// Assess says what an account can do of each action its Integration lists.
func Assess(h integration.Holdings, now time.Time) orchestration.ConnectionAccess {
	out := orchestration.ConnectionAccess{CheckedAt: now.UTC(), Actions: map[string]orchestration.ActionAccess{}}

	catalog, ok := CatalogOf(h.Integration)
	if !ok {
		return out
	}

	for _, a := range catalog.Listed() {
		var got orchestration.ActionAccess

		switch {
		case h.Unknown != "":
			got = unknown(h.Unknown)
		case h.Integration == integration.GoogleID:
			got = googleAccess(a, h.Grants)
		case h.Integration == integration.LarkID:
			got = larkAccess(a, h)
		case h.Integration == SupersetID:
			got = supersetAccess(a, h.Roles)
		default:
			got = unknown("")
		}

		out.Actions[a.ID] = got
	}

	return out
}

// Cannot is one action an account cannot do, and why.
type Cannot struct {
	Action Action
	Reason string
}

// CannotOf lists the actions an account cannot do, in the order the owner
// reads them: what the agents and the owner are told before they try.
func CannotOf(integration string, access orchestration.ConnectionAccess) []Cannot {
	catalog, ok := CatalogOf(integration)
	if !ok || access.Missing() == 0 {
		return nil
	}

	var out []Cannot

	for _, a := range catalog.Listed() {
		if got := access.Of(a.ID); got.Access == orchestration.AccessMissing {
			out = append(out, Cannot{Action: a, Reason: got.Reason})
		}
	}

	return out
}

func unknown(reason string) orchestration.ActionAccess {
	return orchestration.ActionAccess{Access: orchestration.AccessUnknown, Reason: reason}
}

func granted(reason string) orchestration.ActionAccess {
	return orchestration.ActionAccess{Access: orchestration.AccessGranted, Reason: reason}
}

func missing(reason string) orchestration.ActionAccess {
	return orchestration.ActionAccess{Access: orchestration.AccessMissing, Reason: reason}
}

// googleGrants are the services of the Google Integration each part of the
// catalog needs: an action is the part its id starts with. Docs and Sheets
// are one part with two services.
var googleGrants = map[string][]string{
	"gmail": {"gmail"}, "calendar": {"calendar"}, "drive": {"drive"}, "docs": {"docs", "sheets"},
}

// googleAccess reads a Google action from the grants its sign-in gave
// (Hermes keeps granted_scopes the same way, google_chat/oauth.py): a part
// not granted is missing, and so is a change when it was granted read only.
func googleAccess(a Action, grants []integration.Grant) orchestration.ActionAccess {
	part, _, _ := strings.Cut(a.ID, ".")

	services, ok := googleGrants[part]
	if !ok {
		return unknown("")
	}

	write := a.Kind != Reads
	readOnly := []string{}

	for _, service := range services {
		i := slices.IndexFunc(grants, func(g integration.Grant) bool { return g.Service == service })

		switch {
		case i < 0:
		case !write || !grants[i].ReadOnly:
			return granted("granted " + grants[i].String())
		default:
			readOnly = append(readOnly, service)
		}
	}

	if len(readOnly) > 0 {
		return missing(strings.Join(readOnly, " and ") + " granted read only")
	}

	return missing(strings.Join(services, " or ") + " not granted at sign-in")
}

// larkAccess reads a Lark action from the app's enabled scopes, or a
// person's sign-in's, against the scopes of its methods: granted when any one
// of its methods passes, as any one method is enough to do some of what the
// action is; missing when none of the methods that name scopes passes; and
// unknown when lark-cli names no scope for any of them.
func larkAccess(a Action, h integration.Holdings) orchestration.ActionAccess {
	held := map[string]bool{}
	for _, s := range h.Scopes {
		held[s] = true
	}

	named, example := false, ""

	for _, m := range larkMethodList() {
		if m.action() != a.ID {
			continue
		}

		alternatives := m.App
		if h.User {
			alternatives = m.User
		}

		for _, alternative := range alternatives {
			need := strings.Fields(alternative)
			if len(need) == 0 {
				continue
			}

			named = true

			if !slices.ContainsFunc(need, func(s string) bool { return !held[s] }) {
				return granted("has " + alternative)
			}

			if example == "" {
				example = alternative
			}
		}
	}

	if !named {
		return unknown("lark-cli names no scope for these calls")
	}

	who := "the app has"
	if h.User {
		who = "the sign-in was granted"
	}

	return missing(fmt.Sprintf("%s none of the scopes these calls need, such as %s", who, example))
}

// supersetViews are the view menus a part of Superset's permissions are on:
// the class_permission_name of the API classes of its resources
// (superset/dashboards/api.py "Dashboard", queries/api.py "Query",
// queries/saved_queries/api.py "SavedQuery", row_level_security/api.py "Row
// Level Security", importexport/api.py the class's own name, and so on), and
// for users and roles Flask-AppBuilder's security API, which Superset's own
// lists name "User" and "Role" (security/manager.py ADMIN_ONLY_VIEW_MENUS).
// Mapping a part to them is inference, read from those values; a part with
// none (the account and the server, everything else) is unknown.
var supersetViews = map[string][]string{
	"dashboards": {"Dashboard"}, "charts": {"Chart"}, "datasets": {"Dataset"}, "databases": {"Database"},
	"sqllab": {"SQLLab"}, "sql": {"SQLLab"}, "queries": {"Query", "SavedQuery"}, "reports": {"ReportSchedule"},
	"annotations": {"Annotation"}, "tags": {"Tag"}, "css": {"CssTemplate"}, "rls": {"Row Level Security"},
	"assets": {"ImportExportRestApi"}, "logs": {"Log"}, "security": {"User", "Role"},
}

// supersetPermission is the permission an action needs on its views: a read
// is can_read and a change or a delete can_write, as Superset names its
// model APIs' methods (superset/constants.py,
// MODEL_API_RW_METHOD_PERMISSION_MAP); export and import are their methods'
// own names, and running SQL is SQL Lab's execute_sql_query.
func supersetPermission(a Action) string {
	switch a.ID {
	case "superset.assets.read":
		return "can_export"
	case "superset.assets.write":
		return "can_import_"
	case "superset.sql.run":
		return "can_execute_sql_query"
	}

	if a.Kind == Reads {
		return "can_read"
	}

	return "can_write"
}

// supersetAccess reads a Superset action from the account's roles: granted
// when a role has the permission on one of the action's views, missing when
// none has.
func supersetAccess(a Action, roles map[string][][2]string) orchestration.ActionAccess {
	part := strings.Split(a.ID, ".")[1]

	views, ok := supersetViews[part]
	if !ok {
		return unknown("Superset's roles do not name this part")
	}

	permission := supersetPermission(a)
	names := slices.Sorted(maps.Keys(roles))

	for _, role := range names {
		for _, p := range roles[role] {
			if p[0] == permission && slices.Contains(views, p[1]) {
				return granted(fmt.Sprintf("role %s has %s on %s", role, permission, p[1]))
			}
		}
	}

	switch len(names) {
	case 0:
		return missing("the account has no role")
	case 1:
		return missing(fmt.Sprintf("role %s has no %s on %s", names[0], permission, strings.Join(views, " or ")))
	}

	return missing(fmt.Sprintf("roles %s have no %s on %s", strings.Join(names, ", "), permission,
		strings.Join(views, " or ")))
}
