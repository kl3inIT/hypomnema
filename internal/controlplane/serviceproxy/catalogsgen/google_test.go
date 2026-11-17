package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chatDiscovery is a Discovery document as Chat's is written: a base URL with no service path, methods whose path
// is a resource's name ({+name}) beside a flatPath of one segment per parameter, a custom verb, and a media upload
// whose path names the parent the same way.
const chatDiscovery = `{
  "name": "chat", "version": "v1", "revision": "20261113",
  "rootUrl": "https://chat.googleapis.com/", "servicePath": "", "baseUrl": "https://chat.googleapis.com/",
  "resources": {
    "spaces": {
      "methods": {
        "get": {"id": "chat.spaces.get", "httpMethod": "GET", "path": "v1/{+name}", "flatPath": "v1/spaces/{spacesId}"},
        "completeImport": {"id": "chat.spaces.completeImport", "httpMethod": "POST",
          "path": "v1/{+name}:completeImport", "flatPath": "v1/spaces/{spacesId}:completeImport"}
      },
      "resources": {
        "messages": {"methods": {
          "search": {"id": "chat.spaces.messages.search", "httpMethod": "POST", "path": "v1/{+parent}/messages:search",
            "flatPath": "v1/spaces/{spacesId}/messages:search"}
        }}
      }
    },
    "media": {"methods": {
      "upload": {"id": "chat.media.upload", "httpMethod": "POST", "path": "v1/{+parent}/attachments:upload",
        "flatPath": "v1/spaces/{spacesId}/attachments:upload",
        "mediaUpload": {"protocols": {
          "simple": {"path": "/upload/v1/{+parent}/attachments:upload"},
          "resumable": {"path": "/resumable/upload/v1/{+parent}/attachments:upload"}
        }}}
    }}
  }
}`

// calendarDiscovery is written as Calendar's is: a service path, and paths of one segment per parameter with no
// flatPath.
const calendarDiscovery = `{
  "name": "calendar", "version": "v3", "revision": "20261113",
  "rootUrl": "https://www.googleapis.com/", "servicePath": "calendar/v3/",
  "resources": {"events": {"methods": {
    "insert": {"id": "calendar.events.insert", "httpMethod": "POST", "path": "calendars/{calendarId}/events"}
  }}}
}`

// Scenario: the generator reads each method's address as Google reads a request's (#741): the base URL and the
// flatPath, one segment per parameter, whichever of path or flatPath gws fills in; Calendar's path where it has no
// flatPath; an upload's path with its parent's segments. A document that is not the one pinned, or a path whose
// parameter may hold several segments with no flatPath, fails rather than being guessed at.
func TestTheGeneratorReadsEachMethodOfADiscoveryDocument(t *testing.T) {
	t.Parallel()

	chat, err := discoveryMethods(discoverySource{API: "chat", Version: "v1", source: source{Revision: "20261113"}},
		[]byte(chatDiscovery))
	require.NoError(t, err)

	assert.Equal(t, []googleMethod{
		{API: "chat", ID: "chat.media.upload", Method: "POST", Host: "chat.googleapis.com",
			Path: "/v1/spaces/{spacesId}/attachments:upload", Uploads: []string{
				"/resumable/upload/v1/spaces/{spacesId}/attachments:upload",
				"/upload/v1/spaces/{spacesId}/attachments:upload",
			}},
		{API: "chat", ID: "chat.spaces.completeImport", Method: "POST", Host: "chat.googleapis.com",
			Path: "/v1/spaces/{spacesId}:completeImport"},
		{API: "chat", ID: "chat.spaces.get", Method: "GET", Host: "chat.googleapis.com", Path: "/v1/spaces/{spacesId}"},
		{API: "chat", ID: "chat.spaces.messages.search", Method: "POST", Host: "chat.googleapis.com",
			Path: "/v1/spaces/{spacesId}/messages:search"},
	}, chat)

	calendar, err := discoveryMethods(discoverySource{API: "calendar", Version: "v3"}, []byte(calendarDiscovery))
	require.NoError(t, err)
	assert.Equal(t, []googleMethod{{API: "calendar", ID: "calendar.events.insert", Method: "POST",
		Host: "www.googleapis.com", Path: "/calendar/v3/calendars/{calendarId}/events"}}, calendar)

	_, err = discoveryMethods(discoverySource{API: "chat", Version: "v2"}, []byte(chatDiscovery))
	require.ErrorContains(t, err, "the document is chat v1")

	_, err = discoveryMethods(discoverySource{API: "chat", Version: "v1", source: source{Revision: "20261114"}},
		[]byte(chatDiscovery))
	require.ErrorContains(t, err, "revision is 20261113")

	_, err = discoveryMethods(discoverySource{API: "x", Version: "v1"}, []byte(`{"name": "x", "version": "v1",
		"rootUrl": "https://x.googleapis.com/", "methods": {"get": {"id": "x.get", "httpMethod": "GET",
		"path": "v1/{+name}"}}}`))
	require.ErrorContains(t, err, "several segments")
}

// Scenario: what gws names is what the catalog must have (decision 9c of #713). The generator reads the APIs of
// gws's services.rs, leaves out its own workflow, and names any that no pinned document is.
func TestTheGeneratorFailsOnAnAPIGwsNamesThatIsNotPinned(t *testing.T) {
	t.Parallel()

	named, err := servicesOf([]byte(`pub const SERVICES: &[ServiceEntry] = &[
    ServiceEntry { aliases: &["drive"], api_name: "drive", version: "v3", description: "Files" },
    ServiceEntry {
        aliases: &["admin-reports", "reports"],
        api_name: "admin",
        version: "reports_v1",
        description: "Audit logs and usage reports",
    },
    ServiceEntry { aliases: &["workflow", "wf"], api_name: "workflow", version: "v1", description: "Own" },
];`))
	require.NoError(t, err)
	assert.Equal(t, []string{"drive v3", "admin reports_v1"}, named)

	assert.Equal(t, []string{"admin reports_v1"}, unpinned(named, []discoverySource{{API: "drive", Version: "v3"}}))
}

// Scenario: Google serves one Discovery document with its keys in another order each time; the sum pinned is of
// its canonical form, the same whatever the order, and still changes with any value.
func TestADiscoveryDocumentIsPinnedWhateverTheOrderOfItsKeys(t *testing.T) {
	t.Parallel()

	a, err := canonicalJSON([]byte(`{"name": "x", "revision": "1", "n": 1.50}`))
	require.NoError(t, err)

	b, err := canonicalJSON([]byte(`{"revision":"1","n":1.50,"name":"x"}`))
	require.NoError(t, err)

	c, err := canonicalJSON([]byte(`{"revision":"2","n":1.50,"name":"x"}`))
	require.NoError(t, err)

	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)

	_, err = canonicalJSON([]byte(`{} {}`))
	require.Error(t, err)
}
