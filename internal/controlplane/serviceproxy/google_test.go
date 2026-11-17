package serviceproxy_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// googleDeclaration is the catalog's embedded declaration, read from the package's own file.
type googleDeclaration struct {
	GWS     string `json:"gws"`
	Methods []struct {
		API, ID, Method, Host, Path string
		Uploads                     []string
	} `json:"methods"`
}

func readGoogleDeclaration(t *testing.T) googleDeclaration {
	t.Helper()

	raw, err := os.ReadFile("google_methods.json")
	require.NoError(t, err)

	var d googleDeclaration
	require.NoError(t, json.Unmarshal(raw, &d))
	require.NotEmpty(t, d.Methods)

	return d
}

// googleParameter is a parameter of a Discovery path.
var googleParameter = regexp.MustCompile(`\{[^}/]*\}`)

// googleOf is the action of a request to Google.
func googleOf(t *testing.T, catalog serviceproxy.Catalog, method, host, path string) serviceproxy.Action {
	t.Helper()

	return catalog.Of(httptest.NewRequestWithContext(t.Context(), method, "https://"+host+path, nil), nil, host)
}

// Scenario: the catalog has not fallen behind the Discovery documents of the APIs gws names (#741, decision 9a of
// #713). Every method of the embedded declaration, and every path its media is uploaded to, is one listed action,
// never google.unlisted and never two at once. A GET reads; anything else does not, but the POSTs that only read,
// each by what its document says.
func TestEveryMethodOfGooglesDeclarationIsANamedAction(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf("google")
	require.True(t, ok)

	readPosts := []string{
		"calendar.freebusy.query", "webmasters.searchanalytics.query", "searchconsole.urlInspection.index.inspect",
		"searchconsole.urlTestingTools.mobileFriendlyTest.run", "chat.spaces.messages.search", "drive.files.download",
	}

	d := readGoogleDeclaration(t)
	apis := map[string]bool{}

	for _, m := range d.Methods {
		apis[m.API] = true

		assert.True(t, catalog.Has(m.Host), "%s is on %s", m.ID, m.Host)

		for _, p := range append([]string{m.Path}, m.Uploads...) {
			a := googleOf(t, catalog, m.Method, m.Host, googleParameter.ReplaceAllString(p, "1"))
			_, listed := catalog.Action(a.ID)

			if !assert.True(t, listed, "%s %s is %s", m.ID, p, a.ID) {
				continue
			}

			assert.Empty(t, a.With, "%s %s is one action", m.ID, p)

			read := m.Method == http.MethodGet || slices.Contains(readPosts, m.ID)
			assert.Equal(t, read, a.Kind == serviceproxy.Reads, "%s %s is %s", m.ID, p, a.ID)
		}
	}

	// The 17 APIs gws names (services.rs, its own workflow left out) and Search Console.
	assert.Len(t, apis, 18)
}

// Scenario: the catalog is generated for the gws agenttool pins (#741, decision 9b of #713). Bumping gws fails here
// until `make catalogs-generate SERVICE=google` has been run for the new version.
func TestGooglesCatalogIsForTheGwsAgenttoolPins(t *testing.T) {
	t.Parallel()

	want := agenttool.GoogleWorkspaceCLI().Version

	assert.Equal(t, want, readGoogleDeclaration(t).GWS, "google_methods.json")

	raw, err := os.ReadFile("catalog_sources.json")
	require.NoError(t, err)

	var sources struct {
		Google struct {
			CLI struct {
				Version string `json:"version"`
			} `json:"cli"`
		} `json:"google"`
	}

	require.NoError(t, json.Unmarshal(raw, &sources))
	assert.Equal(t, want, sources.Google.CLI.Version, "catalog_sources.json")
}

// Scenario: the owner's answers stay when the catalog is generated (#741, decision 7 of #713). Each request the
// hand-written catalog named (read off gws 0.22.5, issue #154, and Search Console's, #609) names the same action:
// sending and its uploads, trashing, labels, drafts, Calendar's and Drive's actions, Docs and Sheets together, and
// Search Console's. What changed is said: a free/busy query, a Chat search and a Drive download are reads though
// they are POSTs; a GET the documents do not have still reads where it read before. Model Armor's regional hosts are
// its, as gws reaches them.
func TestGooglesRequestsKeepTheirActions(t *testing.T) {
	t.Parallel()

	catalog, ok := serviceproxy.CatalogOf("google")
	require.True(t, ok)

	const (
		gmail  = "gmail.googleapis.com"
		www    = "www.googleapis.com"
		docs   = "docs.googleapis.com"
		sheets = "sheets.googleapis.com"
		search = "searchconsole.googleapis.com"
		chat   = "chat.googleapis.com"
		site   = "/webmasters/v3/sites/https%3A%2F%2Fbeyondpilot.ai%2F"
	)

	for _, c := range []struct{ method, host, path, want string }{
		{http.MethodGet, www, "/discovery/v1/apis/calendar/v3/rest", "google.discovery"},
		{http.MethodGet, gmail, "/$discovery/rest", "google.discovery"},
		{http.MethodGet, "forms.googleapis.com", "/$discovery/rest", "google.discovery"},

		{http.MethodPost, gmail, "/gmail/v1/users/me/messages/send", "gmail.send"},
		{http.MethodPost, gmail, "/upload/gmail/v1/users/me/messages/send", "gmail.send"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/drafts/send", "gmail.send"},
		{http.MethodPost, gmail, "/upload/gmail/v1/users/me/drafts/send", "gmail.send"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/messages/1a2b/trash", "gmail.trash"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/threads/1a2b/trash", "gmail.trash"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/messages/batchDelete", "gmail.trash"},
		{http.MethodDelete, gmail, "/gmail/v1/users/me/messages/1a2b", "gmail.trash"},
		{http.MethodDelete, gmail, "/gmail/v1/users/me/threads/1a2b", "gmail.trash"},
		{http.MethodDelete, gmail, "/gmail/v1/users/me/drafts/r1", "gmail.trash"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/messages/1a2b/modify", "gmail.label"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/threads/1a2b/modify", "gmail.label"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/messages/batchModify", "gmail.label"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/messages/1a2b/untrash", "gmail.label"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/threads/1a2b/untrash", "gmail.label"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/labels", "gmail.label"},
		{http.MethodPut, gmail, "/gmail/v1/users/me/labels/Label_1", "gmail.label"},
		{http.MethodPatch, gmail, "/gmail/v1/users/me/labels/Label_1", "gmail.label"},
		{http.MethodDelete, gmail, "/gmail/v1/users/me/labels/Label_1", "gmail.label"},
		{http.MethodPost, gmail, "/gmail/v1/users/me/drafts", "gmail.draft"},
		{http.MethodPost, gmail, "/upload/gmail/v1/users/me/drafts", "gmail.draft"},
		{http.MethodPut, gmail, "/gmail/v1/users/me/drafts/r1", "gmail.draft"},
		{http.MethodPut, gmail, "/upload/gmail/v1/users/me/drafts/r1", "gmail.draft"},
		{http.MethodGet, gmail, "/gmail/v1/users/me/messages", "gmail.read"},
		{http.MethodGet, gmail, "/gmail/v1/users/me/messages/1a2b/attachments/a1", "gmail.read"},
		{http.MethodGet, gmail, "/gmail/v1/users/me/profile", "gmail.read"},
		{http.MethodGet, gmail, "/gmail/v1/users/me/somethingNewer", "gmail.read"},

		{http.MethodDelete, www, "/calendar/v3/calendars/primary/events/e1", "calendar.delete"},
		{http.MethodPost, www, "/calendar/v3/calendars/primary/events", "calendar.write"},
		{http.MethodPatch, www, "/calendar/v3/calendars/primary/events/e1", "calendar.write"},
		{http.MethodPost, www, "/calendar/v3/calendars/primary/events/quickAdd", "calendar.write"},
		{http.MethodGet, www, "/calendar/v3/calendars/primary/events", "calendar.read"},
		{http.MethodGet, www, "/calendar/v3/users/me/calendarList", "calendar.read"},
		// A POST that only reads (#741); calendar.write before.
		{http.MethodPost, www, "/calendar/v3/freeBusy", "calendar.read"},

		{http.MethodPost, www, "/drive/v3/files/f1/permissions", "drive.share"},
		{http.MethodPatch, www, "/drive/v3/files/f1/permissions/p1", "drive.share"},
		{http.MethodDelete, www, "/drive/v3/files/f1/permissions/p1", "drive.share"},
		{http.MethodDelete, www, "/drive/v3/files/f1", "drive.delete"},
		{http.MethodDelete, www, "/drive/v3/files/trash", "drive.delete"},
		{http.MethodPatch, www, "/drive/v3/files/f1", "drive.edit"},
		{http.MethodPatch, www, "/upload/drive/v3/files/f1", "drive.edit"},
		{http.MethodPost, www, "/drive/v3/files/f1/copy", "drive.edit"},
		{http.MethodPost, www, "/drive/v3/files", "drive.upload"},
		{http.MethodPost, www, "/upload/drive/v3/files", "drive.upload"},
		{http.MethodGet, www, "/drive/v3/files", "drive.read"},
		{http.MethodGet, www, "/drive/v3/files/f1/export", "drive.read"},
		// A POST that only reads (#741); unlisted before.
		{http.MethodPost, www, "/drive/v3/files/f1/download", "drive.read"},

		{http.MethodPost, docs, "/v1/documents", "docs.write"},
		{http.MethodPost, docs, "/v1/documents/d1:batchUpdate", "docs.write"},
		{http.MethodPost, sheets, "/v4/spreadsheets", "docs.write"},
		{http.MethodPost, sheets, "/v4/spreadsheets/s1/values/A1:append", "docs.write"},
		{http.MethodPut, sheets, "/v4/spreadsheets/s1/values/A1", "docs.write"},
		{http.MethodPost, sheets, "/v4/spreadsheets/s1:batchUpdate", "docs.write"},
		{http.MethodGet, docs, "/v1/documents/d1", "docs.read"},
		{http.MethodGet, sheets, "/v4/spreadsheets/s1/values/A1", "docs.read"},

		{http.MethodGet, search, "/webmasters/v3/sites", "searchconsole.read"},
		{http.MethodGet, search, site + "/sitemaps", "searchconsole.read"},
		{http.MethodPost, search, site + "/searchAnalytics/query", "searchconsole.read"},
		{http.MethodPost, search, "/v1/urlInspection/index:inspect", "searchconsole.read"},
		{http.MethodPost, search, "/v1/urlTestingTools/mobileFriendlyTest:run", "searchconsole.read"},
		{http.MethodPut, search, site + "/sitemaps/https%3A%2F%2Fbeyondpilot.ai%2Fsitemap.xml", "searchconsole.sitemap"},
		{http.MethodDelete, search, site + "/sitemaps/https%3A%2F%2Fbeyondpilot.ai%2Fsitemap.xml",
			"searchconsole.sitemap"},
		{http.MethodPut, search, site, "searchconsole.site"},
		{http.MethodDelete, search, site, "searchconsole.site"},

		// The APIs the catalog did not name before: each its own service.
		{http.MethodPost, chat, "/v1/spaces/AAA/messages:search", "chat.read"},
		{http.MethodPost, chat, "/v1/spaces/AAA/messages", "chat.write"},
		{http.MethodPost, chat, "/upload/v1/spaces/AAA/attachments:upload", "chat.write"},
		{http.MethodPost, chat, "/v1/spaces/AAA:completeImport", "chat.write"},
		{http.MethodGet, chat, "/v1/spaces/AAA", "chat.read"},
		{http.MethodDelete, chat, "/v1/spaces/AAA/messages/m1", "chat.delete"},
		{http.MethodPost, "slides.googleapis.com", "/v1/presentations/p1:batchUpdate", "slides.write"},
		{http.MethodGet, "tasks.googleapis.com", "/tasks/v1/users/@me/lists", "tasks.read"},
		{http.MethodGet, "admin.googleapis.com", "/admin/reports/v1/activity/users/all/applications/drive",
			"reports.read"},
		{http.MethodPost, "workspaceevents.googleapis.com", "/v1/subscriptions", "events.write"},
		{http.MethodGet, "modelarmor.europe-west1.rep.googleapis.com", "/v1/projects/p/locations/europe-west1/templates",
			"modelarmor.read"},

		// What no method has asks.
		{http.MethodPost, gmail, "/gmail/v1/users/me/settings/nothing", "google.unlisted"},
		{http.MethodPost, www, "/batch/drive/v3", "google.unlisted"},
		{http.MethodPost, chat, "/v1/spaces/AAA:nothing", "google.unlisted"},
	} {
		assert.Equal(t, c.want, googleOf(t, catalog, c.method, c.host, c.path).ID, "%s %s%s", c.method, c.host, c.path)
	}

	for _, host := range []string{"example.com", "googleapis.com", "modelarmor.evil.example.com"} {
		assert.False(t, catalog.Has(host), host)
	}
}
