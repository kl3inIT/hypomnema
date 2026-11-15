package clientcli

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// boxLogin is the token in the box's client.json: the chat's pi's own.
const boxLogin = "hyp_bot_v1_the_pis_own"

// gatewayAPI stands for the agent API's tools routes: it lists a Bot's four
// tools and answers a memory's change, a search and an image, and records
// what it was asked and with which credential.
type gatewayAPI struct {
	mu    sync.Mutex
	asked []string
}

func (g *gatewayAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.asked = append(g.asked, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
	g.mu.Unlock()

	switch r.Method + " " + r.URL.Path {
	case "GET " + agenttool.PathTools:
		_, _ = io.WriteString(w, `{"data":[`+
			`{"name":"memory","description":"Keep a fact.","inputSchema":{"type":"object"}},`+
			`{"name":"web_search","description":"Search the web.","inputSchema":{"type":"object"}},`+
			`{"name":"web_fetch","description":"Read a page.","inputSchema":{"type":"object"}},`+
			`{"name":"generate_image","description":"Make one image.","inputSchema":{"type":"object"}}]}`)
	case "POST " + agenttool.PathToolsCall:
		var call agenttool.BrowserCall

		_ = json.NewDecoder(r.Body).Decode(&call)

		if call.Tool == agenttool.ToolGenerateImage {
			made := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfox"))
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"Made by codex with gpt-image-2."},`+
				`{"type":"image","data":"`+made+`","mimeType":"image/png"}],"isError":false}`)

			return
		}

		if call.Tool == agenttool.ToolMemory {
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"Saved. memory: 20 of 2,200 characters (0%)"}],`+
				`"isError":false}`)

			return
		}

		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"<<<EXTERNAL_UNTRUSTED_CONTENT>>> go.dev"}],`+
			`"isError":false}`)
	default:
		http.NotFound(w, r)
	}
}

// mcpSession is `hypomnema mcp` as pi starts it, spoken to over its standard
// input and output.
type mcpSession struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
}

func (m *mcpSession) ask(id int, method string, params any) map[string]any {
	m.t.Helper()

	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	require.NoError(m.t, err)

	_, err = m.in.Write(append(raw, '\n'))
	require.NoError(m.t, err)
	require.True(m.t, m.out.Scan(), "the server answers")

	var msg struct {
		Result map[string]any `json:"result"`
	}

	require.NoError(m.t, json.Unmarshal(m.out.Bytes(), &msg))

	return msg.Result
}

func (m *mcpSession) text(id int, tool string, arguments map[string]any) string {
	m.t.Helper()

	result := m.ask(id, "tools/call", map[string]any{"name": tool, "arguments": arguments})

	raw, err := json.Marshal(result["content"])
	require.NoError(m.t, err)

	var content []struct{ Text string }
	require.NoError(m.t, json.Unmarshal(raw, &content))
	require.Len(m.t, content, 1)

	return content[0].Text
}

// Scenario: pi starts `hypomnema mcp` in a Bot's box (issue #492). It lists
// the tools the control plane gives the pi's own token (the memory's, issue
// #536, and the gateway's), and passes a memory's change and a search on as
// they are. An image is saved under images/
// in the Bot's folder, where pi works, and the answer gives the MEDIA line
// that sends it into the chat; one saved outside the folder says it cannot
// be sent.
func TestABotsPiUsesTheGatewaysToolsThroughMCP(t *testing.T) {
	api := &gatewayAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "client.json"),
		[]byte(`{"url":"`+srv.URL+`","token":"`+boxLogin+`"}`), 0o600))

	work := t.TempDir()
	t.Chdir(work)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)

	go func() {
		done <- Command(t.Context(), home, []string{"mcp"}, inR, outW)

		_ = outW.Close()
	}()

	t.Cleanup(func() {
		_ = inW.Close()

		require.NoError(t, <-done)
	})

	m := &mcpSession{t: t, in: inW, out: bufio.NewScanner(outR)}
	m.out.Buffer(make([]byte, 64<<10), 1<<20)

	assert.Equal(t, "hypomnema", m.ask(1, "initialize", map[string]any{"protocolVersion": "2025-06-18"})["serverInfo"].(map[string]any)["name"]) //nolint:forcetypeassert,lll // test

	raw, err := json.Marshal(m.ask(2, "tools/list", map[string]any{})["tools"])
	require.NoError(t, err)

	var tools []struct{ Name string }
	require.NoError(t, json.Unmarshal(raw, &tools))
	assert.Equal(t, []struct{ Name string }{{"memory"}, {"web_search"}, {"web_fetch"}, {"generate_image"}}, tools)

	assert.Equal(t, "Saved. memory: 20 of 2,200 characters (0%)",
		m.text(9, "memory", map[string]any{"target": "bot", "action": "add", "text": "Lan is the designer."}))

	assert.Contains(t, m.text(3, "web_search", map[string]any{"query": "go"}), "EXTERNAL_UNTRUSTED_CONTENT")

	answer := m.text(4, "generate_image", map[string]any{"prompt": "a fox"})
	lines := strings.Split(answer, "\n")
	require.Len(t, lines, 3, answer)
	assert.Equal(t, "Made by codex with gpt-image-2.", lines[0])

	media, ok := strings.CutPrefix(lines[2], "To send it into the chat, put this line on a line of its own in "+
		"your reply, outside a code block: MEDIA:")
	require.True(t, ok, lines[2])
	assert.True(t, strings.HasPrefix(media, "images/image-") && strings.HasSuffix(media, ".png"), media)

	saved, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(media))) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, "\x89PNG\r\n\x1a\nfox", string(saved))
	assert.Equal(t, "Saved to "+filepath.Join(work, filepath.FromSlash(media)), lines[1])

	outside := filepath.Join(t.TempDir(), "fox.png")
	answer = m.text(5, "generate_image", map[string]any{"prompt": "a fox", "path": outside})
	assert.Contains(t, answer, "Saved to "+outside)
	assert.Contains(t, answer, "cannot be sent into the chat")
	assert.NotContains(t, answer, "MEDIA:")

	api.mu.Lock()
	defer api.mu.Unlock()

	for _, asked := range api.asked {
		assert.True(t, strings.HasSuffix(asked, "Bearer "+boxLogin), "each call is the pi's: %s", asked)
		assert.NotContains(t, asked, "/browser/", "a Bot's server asks for none of a worker's tools")
	}
}

// Scenario: the launcher writes pi's mcp.json (issue #492): one server, this
// binary's `mcp` at the path it names, every tool declared to the model, and
// a wait long enough for an image. No login is needed.
func TestPiMCPNamesTheToolsServer(t *testing.T) {
	t.Parallel()

	out, err := newWorld(t).cmd(t, "pi-mcp", "/usr/local/bin/hypomnema")
	require.NoError(t, err)

	var file struct {
		MCPServers map[string]struct {
			Command  string   `json:"command"`
			Args     []string `json:"args"`
			Timeout  int      `json:"timeout"`
			Exposure string   `json:"exposure"`
		} `json:"mcpServers"`
	}

	require.NoError(t, json.Unmarshal([]byte(out), &file))
	require.Len(t, file.MCPServers, 1)

	server := file.MCPServers["hypomnema"]
	assert.Equal(t, "/usr/local/bin/hypomnema", server.Command)
	assert.Equal(t, []string{"mcp"}, server.Args)
	assert.Equal(t, "direct", server.Exposure)
	assert.Greater(t, server.Timeout, 11*60, "pi waits longer than an image may take")
}
