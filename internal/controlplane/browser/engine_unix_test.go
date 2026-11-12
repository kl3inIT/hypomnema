//go:build unix

package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// standIn is a program put where the pinned agent-browser is installed. It
// writes each run to `log` beside itself (the session, the Chrome it was told
// to attach to, whether its tab is pinned, the proxy and the Chrome arguments
// it was given, a variable of the control plane's that it must never see, and
// its arguments) and answers as agent-browser
// does for the commands the engine runs:
//
//   - `get cdp-url` gives the address in `up`, a file a launch writes and the
//     launching session's `close` removes;
//   - a launch fails as Chrome does on a machine that blocks its sandbox
//     while `sandbox-blocked` is there and `--no-sandbox` is not given;
//   - `mcp call` answers a tool's result with more beside it. As agent-browser
//     does, it keeps the session's tab in a file beside its sockets, which
//     outlives the session, and answers `tab_gone` once that tab was closed or
//     its Chrome went away. A call takes a second while `slow` is there.
//
// Chrome's DevTools port is the one in `devtools`, or one nothing listens on.
const standIn = `#!/bin/sh
dir=$(dirname "$0")
who="session=$AGENT_BROWSER_SESSION cdp=${AGENT_BROWSER_CDP:-} pin=${AGENT_BROWSER_PIN_TAB:-}"
chrome=$(printf %s "${AGENT_BROWSER_ARGS:-}" | tr '\n' ' ')
gate="proxy=${AGENT_BROWSER_PROXY:-} bypass=${AGENT_BROWSER_PROXY_BYPASS:-} chrome=$chrome"
echo "$who $gate secret=${HYPOMNEMA_VAULT_KEYS:-} args=$*" >> "$dir/log"
case "$1" in
mcp)
  cat > /dev/null
  if [ "$2" = call ]; then
    bound="$AGENT_BROWSER_SOCKET_DIR/$AGENT_BROWSER_SESSION.target"
    if [ -f "$bound" ] && ! grep -q "\"cdp\":\"$AGENT_BROWSER_CDP\"" "$bound"; then
      echo '{"content":[{"type":"text","text":"tab_gone: bound tab is gone"}],"isError":true}'
      exit 1
    fi
    printf '{"targetId":"T-%s","url":"about:blank","cdp":"%s"}' "$AGENT_BROWSER_SESSION" "$AGENT_BROWSER_CDP" > "$bound"
    if [ -f "$dir/slow" ]; then sleep 1; fi
    echo '{"content":[{"type":"text","text":"done"}],"isError":false,"structuredContent":{"profile":"/engine/profile"}}'
  else
    cat "$dir/tools"
  fi
  exit 0 ;;
get)
  if [ -f "$dir/up" ]; then cat "$dir/up"; exit 0; fi
  echo "Browser not launched"; exit 1 ;;
close)
  if [ "$AGENT_BROWSER_SESSION" = host ]; then rm -f "$dir/up"; fi
  exit 0 ;;
tab)
  echo closed > "$AGENT_BROWSER_SOCKET_DIR/$AGENT_BROWSER_SESSION.target"
  exit 0 ;;
esac
if [ -f "$dir/sandbox-blocked" ]; then
  case "$chrome" in
  *--no-sandbox*) ;;
  *) echo "FATAL: No usable sandbox! If you are running on Ubuntu 23.10+" >&2; exit 1 ;;
  esac
fi
echo "ws://127.0.0.1:$(cat "$dir/devtools" 2>/dev/null || echo 9)/devtools/browser/$(cat "$dir/chrome" 2>/dev/null)" > "$dir/up"
`

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) pass(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// rig is an engine whose agent-browser is the stand-in.
type rig struct {
	*Engine

	clock *clock
	bin   string
}

func newRig(t *testing.T, cfg Config) rig {
	t.Helper()

	system := runtime.GOOS + "/" + runtime.GOARCH

	shell, ok := headlessShell().Releases[system]
	if !ok {
		t.Skipf("no browser is pinned for %s", system)
	}

	cfg.Dir = t.TempDir()

	ab := agentBrowser()
	bin := filepath.Join(cfg.Dir, "tools", ab.Name, ab.Version)
	chrome := filepath.Join(cfg.Dir, "tools", headlessShell().Name, headlessShell().Version,
		filepath.FromSlash(shell.Binary))

	require.NoError(t, os.MkdirAll(bin, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Dir(chrome), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, ab.Name), []byte(standIn), 0o700)) //nolint:gosec // A command.
	require.NoError(t, os.WriteFile(chrome, nil, 0o700))                                  //nolint:gosec // A command.

	answer, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": listed()}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tools"), append(answer, '\n'), 0o600))

	c := &clock{now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	cfg.Clock = c

	return rig{Engine: New(cfg), clock: c, bin: bin}
}

// runs returns the stand-in's runs so far, and forgets them.
func (r rig) runs(t *testing.T) []string {
	t.Helper()

	log := filepath.Join(r.bin, "log")

	raw, err := os.ReadFile(log) //nolint:gosec // The test's own directory.
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)
	require.NoError(t, os.Remove(log))

	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func (r rig) touch(t *testing.T, name, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(r.bin, name), []byte(content), 0o600))
}

func containing(lines []string, parts ...string) []string {
	var out []string

	for _, line := range lines {
		all := true
		for _, part := range parts {
			all = all && strings.Contains(line, part)
		}

		if all {
			out = append(out, line)
		}
	}

	return out
}

// Two agents share one Chrome and each has a session of its own, pinned to
// its own tab; an agent's first call that is not `open` is given a blank tab
// first; and an agent is told what the tool said, not how the engine is set
// up.
func TestCallRunsEachAgentInItsOwnPinnedSession(t *testing.T) {
	t.Setenv("HYPOMNEMA_VAULT_KEYS", "the-root-key")

	r := newRig(t, Config{})

	result, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com","session":"host"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"content":[{"type":"text","text":"done"}],"isError":false}`, string(result))

	_, err = r.Call(t.Context(), "d-two", "agent_browser_get_url", nil)
	require.NoError(t, err)

	runs := r.runs(t)
	for _, run := range runs {
		assert.Contains(t, run, "secret= ", "agent-browser is run without the control plane's environment")
	}

	launches := containing(runs, "session=host", "--profile", "open about:blank")
	require.Len(t, launches, 1, "one Chrome is started for both agents")
	assert.NotContains(t, launches[0], "--no-sandbox")
	assert.Regexp(t, `proxy=socks5://127\.0\.0\.1:[1-9]\d* bypass=<-loopback> `, launches[0],
		"Chrome is launched through the gate, loopback included")
	assert.Contains(t, launches[0], "--force-webrtc-ip-handling-policy=disable_non_proxied_udp")

	const attached = "cdp=ws://127.0.0.1:9/devtools/browser/ pin=1"

	one := containing(runs, "session=d-one", "mcp call")
	require.Len(t, one, 1)
	assert.Contains(t, one[0], attached)
	assert.Contains(t, one[0], "args=mcp call agent_browser_open")

	two := containing(runs, "session=d-two", "mcp call")
	require.Len(t, two, 2)
	assert.Contains(t, two[0], "args=mcp call agent_browser_open", "a blank tab comes before the agent's first call")
	assert.Contains(t, two[1], "args=mcp call agent_browser_get_url")
	assert.Contains(t, two[1], attached)

	assert.ElementsMatch(t, []string{"d-one", "d-two"}, r.Status().Agents)
}

// A tab nobody used for TabIdle is closed, tab first and then the session;
// Chrome is closed once it has had no tab for ChromeIdle, and the next call
// starts it again.
func TestSweepClosesIdleTabsAndThenChrome(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.clock.pass(TabIdle - time.Minute)
	r.Sweep(t.Context())
	assert.Equal(t, []string{"d-one"}, r.Status().Agents, "a tab is kept until it has been idle for TabIdle")

	r.runs(t)
	r.clock.pass(time.Minute)
	r.Sweep(t.Context())

	closed := containing(r.runs(t), "session=d-one")
	require.Len(t, closed, 2)
	assert.Contains(t, closed[0], "args=tab close")
	assert.Contains(t, closed[1], "args=close")
	assert.Empty(t, r.Status().Agents)
	assert.False(t, r.Status().Running, "the tab's last use was over ChromeIdle ago, so Chrome goes with it")

	result, err := r.Call(t.Context(), "d-one", "agent_browser_get_url", nil)
	require.NoError(t, err)
	assert.Contains(t, string(result), `"isError":false`, "the agent is not held to the tab that was closed")
	assert.Len(t, containing(r.runs(t), "session=host", "open about:blank"), 1, "Chrome is started again")
}

// Chrome with no tab is kept until it has been idle for ChromeIdle.
func TestSweepKeepsChromeForAWhileAfterItsLastTab(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	r.Release(t.Context(), "d-one")
	r.clock.pass(ChromeIdle - time.Second)
	r.Sweep(t.Context())
	assert.True(t, r.Status().Running)

	r.clock.pass(time.Second)
	r.Sweep(t.Context())
	assert.False(t, r.Status().Running)
}

// A machine that blocks Chrome's sandbox is said to, with what to do; the
// sandbox is turned off only by the setting.
func TestChromeKeepsItsSandboxUnlessToldOtherwise(t *testing.T) {
	r := newRig(t, Config{})
	r.touch(t, "sandbox-blocked", "")

	_, err := r.Call(t.Context(), "d-one", "agent_browser_get_url", nil)
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorContains(t, err, "AppArmor")
	assert.Empty(t, containing(r.runs(t), "--no-sandbox"), "a failed launch is not tried again without the sandbox")

	off := newRig(t, Config{NoSandbox: true})
	off.touch(t, "sandbox-blocked", "")

	_, err = off.Call(t.Context(), "d-one", "agent_browser_get_url", nil)
	require.NoError(t, err)
	assert.Len(t, containing(off.runs(t), "session=host", "--no-sandbox", "open about:blank"), 1)
}

// When Chrome is gone, the tabs are gone with it: the engine starts it again
// and the agent's next call gets a new tab on the new Chrome.
func TestCallStartsChromeAgainAfterItWentAway(t *testing.T) {
	r := newRig(t, Config{})

	_, err := r.Call(t.Context(), "d-one", toolOpen, json.RawMessage(`{"url":"example.com"}`))
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(r.bin, "up")))
	r.touch(t, "chrome", "second")
	r.clock.pass(time.Minute)
	r.runs(t)

	result, err := r.Call(t.Context(), "d-one", "agent_browser_get_url", nil)
	require.NoError(t, err)
	assert.Contains(t, string(result), `"isError":false`, "the agent is not held to a tab of the Chrome that went away")

	runs := r.runs(t)
	assert.Len(t, containing(runs, "session=host", "open about:blank"), 1)

	calls := containing(runs, "session=d-one", "mcp call")
	require.Len(t, calls, 2, "the tab of the Chrome that went away is not used: a new one is opened first")

	for _, call := range calls {
		assert.Contains(t, call, "cdp=ws://127.0.0.1:9/devtools/browser/second")
	}
}
