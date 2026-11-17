package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// imService is a service of lark-cli's catalog as im.json is written: a service path, nested resources, methods
// with a risk and their scopes, one of them with requiredScopes.
const imService = `{
  "name": "im", "servicePath": "/open-apis/im/v1",
  "resources": {
    "chats": {"methods": {
      "get": {"path": "chats/{chat_id}", "httpMethod": "GET", "risk": "read",
        "scopes": ["im:chat:read", "im:chat"]},
      "update": {"path": "chats/{chat_id}", "httpMethod": "PUT", "risk": "write",
        "scopes": ["im:chat"], "requiredScopes": ["im:chat:update", "im:chat:read"]}
    }},
    "messages": {"methods": {
      "delete": {"path": "messages/{message_id}", "httpMethod": "DELETE", "risk": "high-risk-write",
        "scopes": ["im:message"]}
    }}
  }
}`

// imHelpers is a shortcut package as lark-cli's shortcuts/im is written: the request a helper sends
// (queryChatBatch in helpers.go), a wrapper that takes the method and the path, and a path built at run time.
const imHelpers = `package im

import (
	"fmt"
	"net/http"

	"github.com/larksuite/cli/shortcuts/common"
)

func queryChatBatch(rt *common.RuntimeContext, batch []string) error {
	_, err := rt.DoAPIJSONTyped(http.MethodPost, "/open-apis/im/v1/chats/batch_query", nil, batch)
	return err
}

func callIM(rt *common.RuntimeContext, method, path string) error {
	_, err := rt.CallAPITyped(method, path, nil, nil)
	return err
}

func pinMessage(rt *common.RuntimeContext, id string) error {
	return callIM(rt, "POST", fmt.Sprintf("/open-apis/im/v1/pins/%s", id))
}

func builtAtRunTime(rt *common.RuntimeContext, parts []string) error {
	return callIM(rt, "POST", joinPath(parts))
}
`

// imCommands are shortcut commands that call the helpers: a read whose search reads chats, a write that pins and
// deletes, and one whose risk is set at run time.
const imCommands = `package im

import (
	"context"

	"github.com/larksuite/cli/shortcuts/common"
)

var MessagesSearch = common.Shortcut{
	Service: "im", Command: "+messages-search", Risk: "read",
	Scopes: []string{"search:message", "im:chat:read"},
	DryRun: func(ctx context.Context, rt *common.RuntimeContext) *common.DryRunAPI {
		return common.NewDryRunAPI().POST("/open-apis/im/v1/messages/search")
	},
	Execute: func(ctx context.Context, rt *common.RuntimeContext) error {
		return queryChatBatch(rt, nil)
	},
}

var Pin = common.Shortcut{
	Service: "im", Command: "+pin", Risk: "write",
	UserScopes: []string{"im:message.pins:write"},
	BotScopes:  []string{"im:message.pins:write_only"},
	DryRun:     dryRunPin,
	Execute: func(ctx context.Context, rt *common.RuntimeContext) error {
		if err := pinMessage(rt, rt.Str("message-id")); err != nil {
			return err
		}
		_ = queryChatBatch(rt, nil)
		return builtAtRunTime(rt, nil)
	},
}

func dryRunPin(_ context.Context, rt *common.RuntimeContext) *common.DryRunAPI {
	return common.NewDryRunAPI().
		POST("/open-apis/im/v1/pins/:message_id").
		DELETE("/open-apis/im/v1/pins/:message_id").
		GET("/open-apis/im/v1/chats/:chat_id")
}

func newCommand(risk string) common.Shortcut {
	return common.Shortcut{
		Service: "im", Command: "+unpin", Risk: risk,
		DryRun: func(ctx context.Context, rt *common.RuntimeContext) *common.DryRunAPI {
			return common.NewDryRunAPI().POST("/open-apis/im/v1/pins/:pin_id/unpin")
		},
	}
}
`

// docsCommand calls a part of the API only shortcuts call, and one the catalog leaves out.
const docsCommand = `package doc

import (
	"context"

	"github.com/larksuite/cli/shortcuts/common"
)

var Create = common.Shortcut{
	Service: "docs", Command: "+create", Risk: "write",
	DryRun: func(ctx context.Context, rt *common.RuntimeContext) *common.DryRunAPI {
		return common.NewDryRunAPI().
			POST("/open-apis/docs_ai/v1/documents").
			GET("/open-apis/spark/v1/apps").
			PUT("<presigned_upload_url> (from pre_release response)")
	},
}
`

func larkFixture() map[string][]byte {
	return map[string][]byte{
		larkRegistryDir + "im.json":    []byte(imService),
		"shortcuts/im/helpers.go":      []byte(imHelpers),
		"shortcuts/im/commands.go":     []byte(imCommands),
		"shortcuts/doc/docs_create.go": []byte(docsCommand),
	}
}

// Scenario: the extractor reads both places lark-cli declares what it calls (#236, #742). The catalog's methods
// keep their risk and scopes. A shortcut's call takes its command's risk, including a call one of its helpers makes:
// +messages-search reads chats with POST chats/batch_query, a read. A GET is a read and a DELETE high-risk whatever
// the command; a call two commands make has the higher risk and passes with the scopes of either; a call of a
// method the catalog has is the catalog's; a risk set at run time is the highest. A path built at run time, a
// presigned address and a part the catalog leaves out are left out, and each method keeps the names it was written
// with before.
func TestTheGeneratorReadsLarkCLIsCatalogAndItsShortcuts(t *testing.T) {
	t.Parallel()

	previous := []larkMethod{{Service: "im", Method: "POST", Path: "/open-apis/im/v1/pins/{pin}", Risk: "write"}}

	methods, report, err := larkMethods(larkFixture(), previous)
	require.NoError(t, err)

	both := []string{"im:message.pins:write", "search:message im:chat:read"}

	assert.Equal(t, []larkMethod{
		{Service: "im", Method: "GET", Path: "/open-apis/im/v1/chats/{chat_id}", Risk: "read",
			User: []string{"im:chat:read", "im:chat"}, App: []string{"im:chat:read", "im:chat"}},
		{Service: "im", Method: "PUT", Path: "/open-apis/im/v1/chats/{chat_id}", Risk: "write",
			User: []string{"im:chat:update im:chat:read"}, App: []string{"im:chat:update im:chat:read"}},
		{Service: "im", Method: "DELETE", Path: "/open-apis/im/v1/messages/{message_id}", Risk: "high-risk-write",
			User: []string{"im:message"}, App: []string{"im:message"}},
		{Service: "doc", Method: "POST", Path: "/open-apis/docs_ai/v1/documents", Risk: "write"},
		{Service: "im", Method: "POST", Path: "/open-apis/im/v1/chats/batch_query", Risk: "write",
			User: both, App: []string{"im:message.pins:write_only", "search:message im:chat:read"}},
		{Service: "im", Method: "POST", Path: "/open-apis/im/v1/messages/search", Risk: "read",
			User: []string{"search:message im:chat:read"}, App: []string{"search:message im:chat:read"}},
		{Service: "im", Method: "DELETE", Path: "/open-apis/im/v1/pins/{message_id}", Risk: "high-risk-write",
			User: []string{"im:message.pins:write"}, App: []string{"im:message.pins:write_only"}},
		{Service: "im", Method: "POST", Path: "/open-apis/im/v1/pins/{pin_id}/unpin", Risk: "high-risk-write"},
		{Service: "im", Method: "POST", Path: "/open-apis/im/v1/pins/{pin}", Risk: "write",
			User: []string{"im:message.pins:write"}, App: []string{"im:message.pins:write_only"}},
	}, methods)
	assert.Equal(t, larkReport{registry: 3, calls: 6, shortcuts: 4}, report)
}

// Scenario: +messages-search alone (issue #742, seen on zm on 2026-11-17): the chats it reads with POST
// chats/batch_query, in a helper its Execute calls, are a read of Messages, not a write that asks.
func TestAReadCommandsHelperReadsChatsAsARead(t *testing.T) {
	t.Parallel()

	files := larkFixture()
	search, _, _ := strings.Cut(imCommands, "var Pin")
	files["shortcuts/im/commands.go"] = []byte(search)

	methods, _, err := larkMethods(files, nil)
	require.NoError(t, err)

	assert.Contains(t, methods, larkMethod{
		Service: "im", Method: "POST", Path: "/open-apis/im/v1/chats/batch_query", Risk: "read",
		User: []string{"search:message im:chat:read"}, App: []string{"search:message im:chat:read"},
	})
}

// Scenario: a shortcut calls a part of Lark's API no service is named for. The generation fails and names it, so
// that whoever bumps lark-cli decides which service it is rather than the catalog guessing.
func TestAPartOfTheAPINoServiceIsNamedForFails(t *testing.T) {
	t.Parallel()

	files := larkFixture()
	files["shortcuts/doc/docs_create.go"] = []byte(`package doc

import "github.com/larksuite/cli/shortcuts/common"

var Hire = common.Shortcut{
	Service: "hire", Command: "+job-create", Risk: "write",
	DryRun: func(ctx context.Context, rt *common.RuntimeContext) *common.DryRunAPI {
		return common.NewDryRunAPI().POST("/open-apis/hire/v1/jobs")
	},
}
`)

	_, _, err := larkMethods(files, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "POST /open-apis/hire/v1/jobs (hire +job-create)")
}

// Scenario: lark_methods.json is written as it has been since #175, so that regenerating it for the same lark-cli
// changes no byte of what did not change: the file in the repository reads back to the same bytes.
func TestTheLarkDeclarationIsWrittenAsBefore(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../" + larkFile)
	require.NoError(t, err)

	var methods []larkMethod
	require.NoError(t, json.Unmarshal(raw, &methods))

	written, err := encodeLark(methods)
	require.NoError(t, err)
	assert.Equal(t, string(raw), string(written))
}
