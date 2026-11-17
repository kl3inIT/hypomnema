package serviceproxy_test

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

const larkHost = "open.larksuite.com"

// larkParameter is a parameter of a path of lark_methods.json.
var larkParameter = regexp.MustCompile(`\{[^}/]*\}`)

// Scenario: the catalog has not fallen behind lark-cli (#742, decision 9a of #713). Every method of
// lark_methods.json is the action of its own service and risk, never lark.unlisted and never another's: a GET reads,
// and what TestLarkReadsThatAreNotGETsAreQueries names is the only other read. A path with a query (Base's
// advperm/enable?enable=…) is no path a request has, and is left out.
func TestEveryMethodOfLarksDeclarationIsItsAction(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf("lark")
	require.True(t, ok)

	raw, err := os.ReadFile("lark_methods.json")
	require.NoError(t, err)

	var methods []struct{ Service, Method, Path, Risk string }
	require.NoError(t, json.Unmarshal(raw, &methods))
	require.NotEmpty(t, methods)

	what := map[string]string{"read": "read", "write": "write", "high-risk-write": "risky"}

	for _, m := range methods {
		if strings.Contains(m.Path, "?") {
			continue
		}

		a := catalog.Match(m.Method, larkHost, larkParameter.ReplaceAllString(m.Path, "1"))

		if !assert.Equal(t, "lark."+m.Service+"."+what[m.Risk], a.ID, "%s %s", m.Method, m.Path) {
			continue
		}

		_, listed := catalog.Action(a.ID)
		assert.True(t, listed, "%s is listed", a.ID)

		if m.Method == http.MethodGet {
			assert.Equal(t, serviceproxy.Reads, a.Kind, "%s %s", m.Method, m.Path)
		}
	}
}

// Scenario: the catalog is generated for the lark-cli agenttool pins (#742, decision 9b of #713). Bumping lark-cli
// fails here until `make catalogs-generate SERVICE=lark` has been run for the new version.
func TestLarksCatalogIsForTheLarkCLIAgenttoolPins(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("catalog_sources.json")
	require.NoError(t, err)

	var sources struct {
		Lark struct {
			CLI struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"cli"`
		} `json:"lark"`
	}

	require.NoError(t, json.Unmarshal(raw, &sources))
	assert.Equal(t, agenttool.LarkCLI().Name, sources.Lark.CLI.Name)
	assert.Equal(t, agenttool.LarkCLI().Version, sources.Lark.CLI.Version, "catalog_sources.json")
}

// Scenario: pi searches Lark messages as the owner (issue #742, seen on zm on 2026-11-17). lark-cli's
// +messages-search reads the chats it found with POST chats/batch_query, in a helper (shortcuts/im/helpers.go
// queryChatBatch); that read is Messages' read now, where it asked four times as a write. What the commands' helpers
// send and the old list did not name is named by the command's risk; the requests the old list named keep their
// action.
func TestLarkCLIsHelpersRequestsAreTheirCommandsRisk(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf("lark")
	require.True(t, ok)

	for request, action := range map[[2]string]string{
		// The issue's request, and what +messages-search and the feed commands read beside it.
		{http.MethodPost, "/open-apis/im/v1/chats/batch_query"}:     "lark.im.read",
		{http.MethodPost, "/open-apis/im/v1/chat_p2p/batch_query"}:  "lark.im.read",
		{http.MethodPost, "/open-apis/contact/v3/users/search"}:     "lark.contact.read",
		{http.MethodPost, "/open-apis/calendar/v4/freebusy/batch"}:  "lark.calendar.read",
		{http.MethodPost, "/open-apis/docs_ai/v1/documents"}:        "lark.doc.write",
		{http.MethodPut, "/open-apis/im/v1/messages/om_1"}:          "lark.im.write",
		{http.MethodPost, "/open-apis/vc/v1/bots/end"}:              "lark.vc.risky",
		{http.MethodPost, "/open-apis/drive/v1/medias/upload_part"}: "lark.drive.risky",
		// A transfer, sharing with members and Base's advanced permissions are what lark-cli asks to confirm
		// (calendar +transfer, drive +member-add, base +advperm-disable, which sends the same request as
		// +advperm-enable with another query).
		{http.MethodPost, "/open-apis/calendar/v4/calendars/cal_1/events/ev_1/transfer"}: "lark.calendar.risky",
		{http.MethodPost, "/open-apis/drive/v1/permissions/doc_1/members/batch_create"}:  "lark.drive.risky",
		{http.MethodPut, "/open-apis/base/v3/bases/bas_1/advperm/enable"}:                "lark.base.risky",
		// A delete a write command makes on its way is still a delete.
		{http.MethodDelete, "/open-apis/docx/v1/documents/doc_1/blocks/blk_1/children/batch_delete"}: "lark.doc.risky",
		// The requests the old list named with a trailing slash (task/v2/tasks/, read off `"…/tasks/" + id`) are
		// the catalog's own tasks and tasklists.
		{http.MethodGet, "/open-apis/task/v2/tasks/"}:      "lark.task.read",
		{http.MethodPost, "/open-apis/task/v2/tasks/"}:     "lark.task.write",
		{http.MethodGet, "/open-apis/task/v2/tasklists/"}:  "lark.task.read",
		{http.MethodPost, "/open-apis/task/v2/tasklists/"}: "lark.task.write",
		{http.MethodPatch, "/open-apis/task/v2/tasks/t_1"}: "lark.task.write",
	} {
		assert.Equal(t, action, catalog.Match(request[0], larkHost, request[1]).ID, request[0]+" "+request[1])
	}
}

// larkSample is a request a route of the Lark catalog matches: each parameter "1", the rest of a part "z1/z2".
func larkSample(path string) string {
	return larkParameter.ReplaceAllString(strings.ReplaceAll(path, "{rest...}", "z1/z2"), "1")
}

// larkRank orders Lark's actions from the one that asks least: a read, a write, a high-risk change, and what no
// action names, which always asks.
func larkRank(id string) int {
	switch {
	case id == "lark.unlisted":
		return 3
	case strings.HasSuffix(id, ".risky"):
		return 2
	case strings.HasSuffix(id, ".write"):
		return 1
	}

	return 0
}

// Scenario: regenerating the catalog from lark-cli's source (#742) lets no request through that asked before
// (review of #794). Every route of the catalog before (testdata/lark_methods_before_742.json, main at 5547f61d) and
// of the generated one, each method's path and each service's rest of its part, is the same action as before, or
// one of its own service that asks more, or something unnamed. The only exceptions are the twelve queries lark-cli
// marks as reads, which asked as writes or as unnamed before.
func TestNoLarkRequestAsksLessThanBefore(t *testing.T) {
	t.Parallel()

	reads := map[string]bool{
		"POST /open-apis/im/v1/chats/batch_query":                       true,
		"POST /open-apis/im/v1/chat_p2p/batch_query":                    true,
		"POST /open-apis/im/v1/chat_user_setting/batch_get_mute_status": true,
		"POST /open-apis/im/v2/chats/search":                            true,
		"POST /open-apis/contact/v3/users/search":                       true,
		"POST /open-apis/bot/v4/bot/search":                             true,
		"POST /open-apis/calendar/v4/freebusy/batch":                    true,
		"POST /open-apis/calendar/v4/freebusy/room_find":                true,
		"POST /open-apis/calendar/v4/freebusy/suggestion":               true,
		"POST /open-apis/drive/v1/export_tasks":                         true,
		"POST /open-apis/board/v1/whiteboards/1/export":                 true,
		"POST /open-apis/vc/v1/bots/screenshot":                         true,
	}

	before, err := os.ReadFile("testdata/lark_methods_before_742.json")
	require.NoError(t, err)

	old, err := serviceproxy.LarkCatalogOf(before)
	require.NoError(t, err)

	now, ok := serviceproxy.CatalogOf("lark")
	require.True(t, ok)

	service := func(id string) string { return id[:strings.LastIndex(id, ".")] }
	exceptions := map[string]bool{}

	for _, c := range []serviceproxy.Catalog{old, now} {
		for _, a := range c.Actions {
			for _, r := range a.Routes {
				request := r.Method + " " + larkSample(r.Path)
				was, is := old.Match(r.Method, larkHost, larkSample(r.Path)).ID, now.Match(r.Method, larkHost,
					larkSample(r.Path)).ID

				switch {
				case was == is:
				case reads[request] && strings.HasSuffix(is, ".read"):
					exceptions[request] = true
				case is == "lark.unlisted",
					larkRank(is) > larkRank(was) && service(is) == service(was):
				default:
					t.Errorf("%s asked as %s and is %s", request, was, is)
				}
			}
		}
	}

	assert.Len(t, exceptions, len(reads), "each of the twelve reads is one")
}
