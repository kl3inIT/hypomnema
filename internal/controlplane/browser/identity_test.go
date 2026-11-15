package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// headlessUserAgent is as Chrome for Testing 157 reports itself headless on
// Linux; the hints are what it reports in a secure context.
const headlessUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) " +
	"HeadlessChrome/157.0.0.0 Safari/537.36"

const headlessHints = `{"architecture":"x86","bitness":"64","brands":[{"brand":"Chromium","version":"157"},` +
	`{"brand":"Not_A Brand","version":"24"}],"formFactors":["Desktop"],"fullVersionList":[{"brand":"Chromium",` +
	`"version":"157.0.8103.78"},{"brand":"Not_A Brand","version":"24.0.0.0"}],"mobile":false,"model":"",` +
	`"platform":"Linux","platformVersion":"6.8.0","uaFullVersion":"157.0.8103.78","wow64":false}`

// keptIdentity is the identity headed makes of them, as the engine keeps it.
const keptIdentity = `{"userAgent":"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) ` +
	`Chrome/157.0.0.0 Safari/537.36","userAgentMetadata":{"brands":[{"brand":"Chromium","version":"157"},` +
	`{"brand":"Not_A Brand","version":"24"}],"fullVersionList":[{"brand":"Chromium","version":"157.0.8103.78"},` +
	`{"brand":"Not_A Brand","version":"24.0.0.0"}],"fullVersion":"157.0.8103.78","platform":"Linux",` +
	`"platformVersion":"6.8.0","architecture":"x86","model":"","mobile":false,"bitness":"64","wow64":false,` +
	`"formFactors":["Desktop"]}}`

func probedOf(t *testing.T, userAgent, hints string) probed {
	t.Helper()

	var p probed

	require.NoError(t, json.Unmarshal([]byte(`{"userAgent":`+quote(userAgent)+`,"high":`+hints+`}`), &p))

	return p
}

func quote(s string) string {
	raw, _ := json.Marshal(s) //nolint:errchkjson // A string.

	return string(raw)
}

// What a headless Chrome is told to say is what the same binary says headed:
// only its HeadlessChrome token changes, and its client hints are its own.
func TestTheIdentityIsTheBinarysOwnHeaded(t *testing.T) {
	t.Parallel()

	id, err := headed(probedOf(t, headlessUserAgent, headlessHints))
	require.NoError(t, err)

	raw, err := json.Marshal(id)
	require.NoError(t, err)
	assert.JSONEq(t, keptIdentity, string(raw))
	assert.True(t, id.valid())

	var kept chromeIdentity

	require.NoError(t, json.Unmarshal([]byte(keptIdentity), &kept))
	assert.True(t, kept.valid(), "a kept identity is trusted when it is one headed makes")

	kept.UserAgent = headlessUserAgent
	assert.False(t, kept.valid())
}

// The headless shell reports a full version and a HeadlessChrome brand: it
// cannot be made to look like Chrome. A page outside a secure context has no
// client hints.
func TestTheIdentityRefusesWhatIsNotChrome(t *testing.T) {
	t.Parallel()

	_, err := headed(probedOf(t, strings.Replace(headlessUserAgent, "157.0.0.0", "157.0.8103.78", 1), headlessHints))
	require.ErrorContains(t, err, "not Chrome's")

	shell := strings.Replace(headlessHints, `"brands":[{"brand":"Chromium"`, `"brands":[{"brand":"HeadlessChrome"`, 1)
	_, err = headed(probedOf(t, headlessUserAgent, shell))
	require.ErrorContains(t, err, "headless brand")

	_, err = headed(probedOf(t, headlessUserAgent, `{}`))
	require.ErrorContains(t, err, "brands")

	_, err = headed(probedOf(t, headlessUserAgent, strings.Replace(headlessHints, `"platformVersion":"6.8.0",`, "", 1)))
	require.ErrorContains(t, err, "platform version")
}

func TestLanguageAndTimeZone(t *testing.T) {
	t.Parallel()

	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }

	assert.Equal(t, "vi-VN", Language("vi-VN", env(nil)))
	assert.Equal(t, "en-US", Language("", env(map[string]string{"LANG": "C.UTF-8"})))
	assert.Equal(t, "pt-BR", Language("", env(map[string]string{"LANG": "pt_BR.UTF-8"})))
	assert.Equal(t, "de", Language("not a language", env(map[string]string{"LC_ALL": "de", "LANG": "fr_FR"})))
	assert.Empty(t, Language("", env(nil)))

	assert.Equal(t, "Asia/Ho_Chi_Minh", TimeZone("Asia/Ho_Chi_Minh", env(map[string]string{"TZ": "UTC"})))
	assert.Equal(t, "UTC", TimeZone("", env(map[string]string{"TZ": "UTC"})))
	assert.Empty(t, TimeZone("Asia/Ho Chi Minh", env(nil)))

	assert.Equal(t, "vi-VN,vi,en-US,en", languageList("vi-VN"))
	assert.Equal(t, "en-US,en", languageList(""))
}

// fakeChrome stands for Chrome's DevTools: it answers each command with what
// answer gives, after the events answer gives to send first, and keeps each
// command as "<session> <method> <params>".
type fakeChrome struct {
	answer func(method, session string) (result string, events []string)
	// push is events Chrome sends by itself.
	push chan string

	mu  sync.Mutex
	got []string
}

func (f *fakeChrome) serve(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow() //nolint:errcheck // A test's connection.

		ctx := req.Context()

		var writing sync.Mutex

		write := func(s string) {
			writing.Lock()
			defer writing.Unlock()

			_ = conn.Write(ctx, websocket.MessageText, []byte(s))
		}

		go func() {
			for {
				select {
				case ev := <-f.push:
					write(ev)
				case <-ctx.Done():
					return
				}
			}
		}()

		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}

			var m cdpMessage
			if json.Unmarshal(raw, &m) != nil {
				continue
			}

			f.mu.Lock()
			f.got = append(f.got, strings.TrimSpace(m.SessionID+" "+m.Method+" "+string(m.Params)))
			f.mu.Unlock()

			// Each answered on its own, as Chrome answers a paused worker's override only once it runs.
			go func() {
				result, events := f.answer(m.Method, m.SessionID)
				for _, ev := range events {
					write(ev)
				}

				write(`{"id":` + jsonInt(m.ID) + `,"result":` + result + `}`)
			}()
		}
	}))
	t.Cleanup(srv.Close)

	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/devtools/browser/x"
}

func jsonInt(n int64) string {
	raw, _ := json.Marshal(n) //nolint:errchkjson // A number.

	return string(raw)
}

func (f *fakeChrome) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.got...)
}

// methods are the commands sent on a session, by name.
func (f *fakeChrome) methods(session string) []string {
	var out []string

	for _, line := range f.sent() {
		if s, rest, ok := strings.Cut(line, " "); ok && s == session {
			method, _, _ := strings.Cut(rest, " ")
			out = append(out, method)
		}
	}

	return out
}

// The identity is read from a page of the browser's own on loopback, once it
// has loaded, and is the binary's headed.
func TestTheIdentityIsReadFromAPageOnceItLoaded(t *testing.T) {
	t.Parallel()

	var asked sync.Mutex

	evaluated := 0
	chrome := &fakeChrome{answer: func(method, _ string) (string, []string) {
		switch method {
		case "Target.createTarget":
			return `{"targetId":"T1"}`, nil
		case "Target.attachToTarget":
			return `{"sessionId":"S1"}`, nil
		case "Runtime.evaluate":
			asked.Lock()
			defer asked.Unlock()

			if evaluated++; evaluated == 1 {
				return `{"result":{"type":"object","subtype":"null","value":null}}`, nil
			}

			return `{"result":{"type":"object","value":{"userAgent":` + quote(headlessUserAgent) +
				`,"high":` + headlessHints + `}}}`, nil
		}

		return `{}`, nil
	}}

	conn, err := dialCDP(t.Context(), chrome.serve(t), nil)
	require.NoError(t, err)
	t.Cleanup(conn.close)

	got, err := probe(t.Context(), conn, "http://127.0.0.1:4321/")
	require.NoError(t, err)

	id, err := headed(got)
	require.NoError(t, err)
	assert.Contains(t, id.UserAgent, " Chrome/157.0.0.0 ")

	sent := chrome.sent()
	require.GreaterOrEqual(t, len(sent), 4)
	assert.Contains(t, sent[0], `Target.createTarget {"url":"http://127.0.0.1:4321/"}`)
	assert.Contains(t, sent[1], `"flatten":true`)
	assert.Equal(t, []string{"Runtime.evaluate", "Runtime.evaluate"}, chrome.methods("S1"),
		"asked again until the page loaded")
}

// The sidecar gives every page, frame and worker the identity with its
// client hints before it runs, and lets it run; it never enables the
// runtime, which sites detect. A tab already open holds the browser's start
// until it has the identity, for so long only.
func TestTheSidecarGivesEveryTargetTheIdentityBeforeItRuns(t *testing.T) {
	t.Parallel()

	var id chromeIdentity

	require.NoError(t, json.Unmarshal([]byte(keptIdentity), &id))

	landed := make(chan struct{})
	attach := func(session, kind string) string {
		return `{"method":"Target.attachedToTarget","params":{"sessionId":"` + session + `","targetInfo":{"type":"` +
			kind + `"},"waitingForDebugger":true}}`
	}

	chrome := &fakeChrome{push: make(chan string, 8), answer: func(method, session string) (string, []string) {
		switch {
		case method == "Target.setAutoAttach" && session == "":
			// Chrome attaches the tabs already open before it answers.
			return `{}`, []string{attach("tab-1", "page")}
		case method == "Emulation.setUserAgentOverride" && session == "tab-1":
			<-landed
		}

		return `{}`, nil
	}}

	address := chrome.serve(t)
	started := make(chan *sidecar, 1)

	go func() {
		s, err := startSidecar(context.WithoutCancel(t.Context()), address, id, "vi-VN,vi,en-US,en")
		assert.NoError(t, err)
		started <- s
	}()

	select {
	case <-started:
		t.Fatal("the browser was handed over before its open tab had the identity")
	case <-time.After(300 * time.Millisecond):
	}

	close(landed)

	var s *sidecar

	select {
	case s = <-started:
	case <-time.After(identityLands):
		t.Fatal("the sidecar did not start")
	}

	require.NotNil(t, s)
	t.Cleanup(s.close)

	sent := chrome.sent()
	require.NotEmpty(t, sent)
	assert.Equal(t, `Target.setAutoAttach {"autoAttach":true,"filter":[{"type":"page"},{"type":"service_worker"},`+
		`{"type":"shared_worker"},{"exclude":true}],"flatten":true,"waitForDebuggerOnStart":true}`, sent[0])

	var override string

	for _, line := range sent {
		if strings.HasPrefix(line, "tab-1 Emulation.setUserAgentOverride ") {
			override = strings.TrimPrefix(line, "tab-1 Emulation.setUserAgentOverride ")
		}
	}

	var want map[string]any

	require.NoError(t, json.Unmarshal([]byte(keptIdentity), &want))
	want["acceptLanguage"] = "vi-VN,vi,en-US,en"

	wanted, err := json.Marshal(want)
	require.NoError(t, err)
	assert.JSONEq(t, string(wanted), override, "the override carries the client hints and the languages")

	// What starts afterwards: a page, a service worker, a dedicated worker of a page.
	chrome.push <- attach("page-2", "page")
	chrome.push <- attach("sw-1", "service_worker")
	chrome.push <- attach("w-1", "worker")

	require.Eventually(t, func() bool {
		return len(chrome.methods("page-2")) == 3 && len(chrome.methods("sw-1")) == 3 && len(chrome.methods("w-1")) == 2
	}, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, []string{"Emulation.setUserAgentOverride", "Target.setAutoAttach", "Runtime.runIfWaitingForDebugger"},
		chrome.methods("tab-1"))
	assert.Equal(t, []string{"Emulation.setUserAgentOverride", "Target.setAutoAttach", "Runtime.runIfWaitingForDebugger"},
		chrome.methods("page-2"))
	assert.Equal(t, []string{"Emulation.setUserAgentOverride", "Network.setUserAgentOverride",
		"Runtime.runIfWaitingForDebugger"}, chrome.methods("sw-1"))
	assert.Equal(t, []string{"Network.setUserAgentOverride", "Runtime.runIfWaitingForDebugger"}, chrome.methods("w-1"))

	for _, line := range chrome.sent() {
		assert.NotContains(t, line, "Runtime.enable")
	}
}
