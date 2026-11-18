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

// gatewayAPI stands for the agent API's tools routes: it lists a Bot's
// tools (four of the one list's and a phone's) and answers a memory's change,
// a search, an image and a tap, and records what it was asked and with which
// credential.
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
	case "GET " + agenttool.PathDeviceTools:
		_, _ = io.WriteString(w, `{"data":[`+
			`{"name":"device_tap","description":"Tap the phone.","inputSchema":{"type":"object"}}]}`)
	case "POST " + agenttool.PathDeviceCall:
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"Tapped."}],"isError":false}`)
	case "POST " + agenttool.PathToolsCall:
		var call agenttool.BrowserCall

		_ = json.NewDecoder(r.Body).Decode(&call)

		if call.Tool == agenttool.ToolGenerateImage {
			made := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfox"))
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"Made by codex with gpt-image-2."},`+
				`{"type":"image","data":"`+made+`","mimeType":"image/png"}],"isError":false}`)

			return
		}

		if call.Tool == agenttool.ToolAudioSpeak {
			spoken := base64.StdEncoding.EncodeToString([]byte("ID3 hello"))
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"Spoken by soniox with tts-rt-v2."},`+
				`{"type":"audio","data":"`+spoken+`","mimeType":"audio/mpeg"}],"isError":false}`)

			return
		}

		if call.Tool == agenttool.ToolRoutines {
			// The arguments as the server sent them, for the test to read.
			text, err := json.Marshal(string(call.Arguments))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)

				return
			}

			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":`+string(text)+`}],"isError":false}`)

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

// startMCP starts `hypomnema mcp` as pi does, logged in to the control plane
// at url, in a folder of its own, which it returns: the Bot's.
func startMCP(t *testing.T, url string) (*mcpSession, string) {
	t.Helper()

	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "client.json"),
		[]byte(`{"url":"`+url+`","token":"`+boxLogin+`"}`), 0o600))

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

	return m, work
}

// Scenario: pi sets a script Routine with the routines tool (issue #536,
// step 5), naming a file it wrote in the Bot's folder. The tools server reads
// that file there and sends its text with the call, as show_artifact's are
// sent; a text the agent put in the arguments itself is never passed on. A
// path out of the folder, or a file that is not there, is the agent's error
// and nothing is sent.
func TestABotsPiSetsARoutineWithAScriptOfItsFolder(t *testing.T) {
	api := &gatewayAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	m, work := startMCP(t, srv.URL)

	script := "#!/bin/sh\necho copied\n"
	require.NoError(t, os.MkdirAll(filepath.Join(work, "routines"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(work, "routines", "copy.sh"), []byte(script), 0o600))

	sent := m.text(1, agenttool.ToolRoutines, map[string]any{
		"action": "set", "name": "sales-copy", "every_minutes": 60, "script": "routines/copy.sh",
		"script_text": "rm -rf /",
	})

	var call agenttool.RoutineCall
	require.NoError(t, json.Unmarshal([]byte(sent), &call))
	require.NotNil(t, call.ScriptText)
	assert.Equal(t, script, *call.ScriptText, "the file's text, not the agent's")
	assert.Equal(t, "routines/copy.sh", *call.Script)

	outside := filepath.Join(t.TempDir(), "x.sh")
	require.NoError(t, os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o600))

	for _, name := range []string{"../x.sh", outside, "routines/missing.sh"} {
		assert.Contains(t, m.text(2, agenttool.ToolRoutines, map[string]any{
			"action": "set", "name": "sales-copy", "script": name,
		}), "cannot be read", name)
	}

	api.mu.Lock()
	defer api.mu.Unlock()

	calls := 0

	for _, asked := range api.asked {
		if strings.HasPrefix(asked, "POST "+agenttool.PathToolsCall) {
			calls++
		}
	}

	assert.Equal(t, 1, calls, "a script that cannot be read is not sent")
}

// Scenario: pi starts `hypomnema mcp` in a Bot's box (issue #492). It lists
// the tools the control plane gives the pi's own token (the memory's, issue
// #536, and the gateway's) and the phone's (issue #536, step 4), and passes a
// memory's change and a search on as they are, and a phone's tap to the
// phone's route, as a worker's server does. An image is saved under images/
// in the Bot's folder, where pi works, and the answer gives the MEDIA line
// that sends it into the chat; one saved outside the folder says it cannot
// be sent. Speech is saved under audio/ the same way.
func TestABotsPiUsesTheGatewaysToolsThroughMCP(t *testing.T) {
	api := &gatewayAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	m, work := startMCP(t, srv.URL)

	assert.Equal(t, "hypomnema", m.ask(1, "initialize", map[string]any{"protocolVersion": "2025-06-18"})["serverInfo"].(map[string]any)["name"]) //nolint:forcetypeassert,lll // test

	raw, err := json.Marshal(m.ask(2, "tools/list", map[string]any{})["tools"])
	require.NoError(t, err)

	var tools []struct{ Name string }
	require.NoError(t, json.Unmarshal(raw, &tools))
	assert.Equal(t, []struct{ Name string }{
		{"memory"}, {"web_search"}, {"web_fetch"}, {"generate_image"}, {"device_tap"},
	}, tools)

	assert.Equal(t, "Tapped.", m.text(8, "device_tap", map[string]any{"target": "#3"}))
	// Filming the phone goes on the phone's route (issue #817), whose fake answers as it answers a tap.
	assert.Equal(t, "Tapped.", m.text(18, "video_start", map[string]any{"title": "Sign in"}))

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

	// Speech is saved under audio/ in the Bot's folder, with the MEDIA line that sends it (issue #723).
	answer = m.text(6, "audio_speak", map[string]any{"text": "hello"})
	lines = strings.Split(answer, "\n")
	require.Len(t, lines, 3, answer)
	assert.Equal(t, "Spoken by soniox with tts-rt-v2.", lines[0])

	media, ok = strings.CutPrefix(lines[2], "To send it into the chat, put this line on a line of its own in "+
		"your reply, outside a code block: MEDIA:")
	require.True(t, ok, lines[2])
	assert.True(t, strings.HasPrefix(media, "audio/speech-") && strings.HasSuffix(media, ".mp3"), media)

	saved, err = os.ReadFile(filepath.Join(work, filepath.FromSlash(media))) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, "ID3 hello", string(saved))

	outside := filepath.Join(t.TempDir(), "fox.png")
	answer = m.text(5, "generate_image", map[string]any{"prompt": "a fox", "path": outside})
	assert.Contains(t, answer, "Saved to "+outside)
	assert.Contains(t, answer, "cannot be sent into the chat")
	assert.NotContains(t, answer, "MEDIA:")

	api.mu.Lock()
	defer api.mu.Unlock()

	assert.Contains(t, api.asked, "POST "+agenttool.PathDeviceCall+" Bearer "+boxLogin)

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
