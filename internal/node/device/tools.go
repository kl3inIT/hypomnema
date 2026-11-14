package device

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// An agent's call of a phone tool (issue #417, step 2): the control plane
// passes it on the node's socket, and the node runs it here against its own
// phone, started first when it is not running, and answers with the tool's
// result. Each command is Android CLI's or adb's, as Google's android-cli
// skill runs them (internal/shared/devicetool says which and where from).

const (
	// toolWithin bounds one command of a tool.
	toolWithin = 2 * time.Minute
	// maxTextBytes bounds the text a tool answers: a layout, a log.
	maxTextBytes = 256 << 10
	// logcatScan is how many lines are read when only some are kept.
	logcatScan = 5000
	screensDir = "screens"
)

// resolved is what `android screen resolve --string="#N"` prints: the box's
// center as "x y" (developer.android.com/tools/agents/android-cli/commands/screen_resolve).
var resolved = regexp.MustCompile(`^([0-9]+) ([0-9]+)$`)

// agents are what the node keeps of each agent's screens. A call runs only
// while it holds mu, so the phone has one holder at a time.
type agents struct {
	mu sync.Mutex
	// stale marks an agent whose last screen another agent acted on since:
	// its boxes are not tapped (the issue's "never runs on another's half-done
	// screen").
	stale map[string]bool
}

// Call runs one phone tool for an agent and returns its result. A phone that
// is not running is started first; one not installed yet is installed in the
// background, and the call says to come back. Every call puts the phone's
// idle stop off.
func (m *Manager) Call(ctx context.Context, agent, tool string, arguments json.RawMessage) nodeproto.DeviceAnswer {
	call, err := devicetool.Read(tool, arguments)
	if err != nil {
		return failed(err.Error())
	}

	m.agents.mu.Lock()
	defer m.agents.mu.Unlock()

	if answer, ready := m.ready(ctx); !ready {
		return answer
	}

	m.Touch()
	defer m.Touch()

	answer, acted := m.runTool(ctx, agent, call)

	// What an agent changed on the screen makes every other agent's screen out of date.
	if acted && !answer.IsError {
		for other := range m.agents.stale {
			if other != agent {
				m.agents.stale[other] = true
			}
		}
	}

	return answer
}

// ready has the phone running, and says why not when it cannot be.
func (m *Manager) ready(ctx context.Context) (nodeproto.DeviceAnswer, bool) {
	switch m.Status().State {
	case nodeproto.DeviceRunning:
		return nodeproto.DeviceAnswer{}, true
	case nodeproto.DeviceAbsent, nodeproto.DeviceInstalling:
		// Two gigabytes or so: longer than a call waits. The install goes on without the call.
		m.Ask(context.WithoutCancel(ctx), true)

		return failed("The phone is being installed, which takes a few minutes the first time. Try again in a few " +
			"minutes."), false
	}

	if err := m.Start(ctx); err != nil {
		return failed("The phone could not be started: " + err.Error()), false
	}

	return nodeproto.DeviceAnswer{}, true
}

// runTool runs a call on the running phone, and says whether it acted on it.
func (m *Manager) runTool(ctx context.Context, agent string, c devicetool.Call) (nodeproto.DeviceAnswer, bool) {
	ctx, cancel := context.WithTimeout(ctx, toolWithin)
	defer cancel()

	switch c.Tool {
	case devicetool.Screen:
		return m.screen(ctx, agent), false
	case devicetool.Layout:
		out, err := m.android(ctx, "layout", "--flat", "--device="+serial)

		return m.text(out, err), false
	case devicetool.Tap:
		return m.tap(ctx, agent, c), true
	case devicetool.Swipe:
		return m.swipe(ctx, agent, c), true
	case devicetool.Type:
		args := []string{"input", "--device=" + serial, "text"}
		if c.Submit {
			args = append(args, "--submit")
		}

		// After --, a text that starts with "-" is not read as an option (tried with 1.1.16919734).
		out, err := m.android(ctx, append(args, "--", c.Text, c.Into)...)

		return done(out, err, "Typed."), true
	case devicetool.Key:
		out, err := m.android(ctx, "input", "--device="+serial, "key", c.Key)

		return done(out, err, "Pressed "+c.Key+"."), true
	case devicetool.Launch:
		return m.launch(ctx, c.Package), true
	case devicetool.Logcat:
		return m.logcat(ctx, c), false
	default:
		return failed("no such phone tool: " + c.Tool), false
	}
}

// screen captures the screen with its numbered boxes into the agent's own
// file, which its taps resolve against.
func (m *Manager) screen(ctx context.Context, agent string) nodeproto.DeviceAnswer {
	if err := os.MkdirAll(m.path(runDir, screensDir), 0o700); err != nil {
		return failed("keeping the screen: " + err.Error())
	}

	file := m.screenFile(agent)
	_ = os.Remove(file)

	out, err := m.android(ctx, "screen", "capture", "--annotate", "--device="+serial, "--output="+file)
	if why := failure(out, err); why != "" {
		return failed("The screen could not be captured: " + why)
	}

	png, err := os.ReadFile(file) //nolint:gosec // The node's own file, named by a hash under its run directory.
	if err != nil {
		return failed("The screen could not be read: " + err.Error())
	}

	m.agents.stale[agent] = false

	return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{
		{Type: "image", Data: base64.StdEncoding.EncodeToString(png), MimeType: "image/png"},
		{Type: "text", Text: "Numbered boxes mark what was found on the screen. Tap one with device_tap and " +
			"its number, such as \"#3\"; take a new screen after each action."},
	}}
}

// screenFile is where an agent's last screen is kept, named by a hash of the
// agent's name.
func (m *Manager) screenFile(agent string) string {
	sum := sha256.Sum256([]byte(agent))

	return m.path(runDir, screensDir, hex.EncodeToString(sum[:8])+".png")
}

// point is a point's coordinates: a box's center from the agent's last
// screen, or the coordinates given.
func (m *Manager) point(ctx context.Context, agent string, p devicetool.Point) (string, string, string) {
	if p.Box == 0 {
		return strconv.Itoa(p.X), strconv.Itoa(p.Y), ""
	}

	file := m.screenFile(agent)

	stale, seen := m.agents.stale[agent]
	if _, err := os.Stat(file); err != nil || !seen {
		return "", "", "There is no screen of yours to take #" + strconv.Itoa(p.Box) + " from: call device_screen first."
	}

	if stale {
		return "", "", "Another agent used the phone since your last device_screen, so its numbers may point " +
			"elsewhere now. Call device_screen again."
	}

	out, err := m.android(ctx, "screen", "resolve", "--screenshot="+file, "--string=#"+strconv.Itoa(p.Box))
	if why := failure(out, err); why != "" {
		return "", "", "#" + strconv.Itoa(p.Box) + " could not be found on your last screen: " + why
	}

	match := resolved.FindStringSubmatch(strings.TrimSpace(out.Stdout))
	if match == nil {
		return "", "", "Your last screen has no box #" + strconv.Itoa(p.Box) + "."
	}

	return match[1], match[2], ""
}

func (m *Manager) tap(ctx context.Context, agent string, c devicetool.Call) nodeproto.DeviceAnswer {
	if c.Target == nil {
		out, err := m.android(ctx, "input", "--device="+serial, "tap", "--", c.Selector)

		return done(out, err, "Tapped.")
	}

	x, y, why := m.point(ctx, agent, *c.Target)
	if why != "" {
		return failed(why)
	}

	out, err := m.adb(ctx, toolWithin, "-s", serial, "shell", "input", "tap", x, y)

	return done(out, err, "Tapped at "+x+","+y+".")
}

func (m *Manager) swipe(ctx context.Context, agent string, c devicetool.Call) nodeproto.DeviceAnswer {
	x1, y1, why := m.point(ctx, agent, c.From)
	if why != "" {
		return failed(why)
	}

	x2, y2, why := m.point(ctx, agent, c.To)
	if why != "" {
		return failed(why)
	}

	out, err := m.adb(ctx, toolWithin, "-s", serial, "shell", "input", "swipe", x1, y1, x2, y2,
		strconv.Itoa(c.DurationMs))

	return done(out, err, "Swiped.")
}

// launch opens an app's launcher activity with monkey, as Orca and
// OpenMausBot do (android-app-control.ts, launchAppArgs).
func (m *Manager) launch(ctx context.Context, pkg string) nodeproto.DeviceAnswer {
	out, err := m.adb(ctx, toolWithin, "-s", serial, "shell", "monkey", "-p", pkg, "-c",
		"android.intent.category.LAUNCHER", "1")
	if why := failure(out, err); why != "" {
		return failed("The app could not be opened: " + why)
	}

	if strings.Contains(out.Stdout+out.Stderr, "No activities found") {
		return failed("No app with the package " + pkg + " is installed on the phone, or it has no launcher.")
	}

	return text("Opened " + pkg + ".")
}

// logcat reads the last lines of the phone's log, as Orca's logcat does
// (android-logcat.ts: -d, -v threadtime, -t), at a priority and above.
func (m *Manager) logcat(ctx context.Context, c devicetool.Call) nodeproto.DeviceAnswer {
	lines := c.Lines
	if c.Contains != "" {
		lines = logcatScan
	}

	args := []string{"-s", serial, "logcat", "-d", "-v", "threadtime", "-t", strconv.Itoa(lines)}
	if c.Level != "" {
		args = append(args, "*:"+c.Level)
	}

	out, err := m.adb(ctx, toolWithin, args...)
	if why := failure(out, err); why != "" {
		return failed("The log could not be read: " + why)
	}

	kept := strings.Split(strings.TrimRight(out.Stdout, "\n"), "\n")

	if c.Contains != "" {
		matching := kept[:0]

		for _, line := range kept {
			if strings.Contains(line, c.Contains) {
				matching = append(matching, line)
			}
		}

		kept = matching[max(0, len(matching)-c.Lines):]
	}

	if len(kept) == 0 || (len(kept) == 1 && kept[0] == "") {
		return text("No log line matches.")
	}

	return text(capText(strings.Join(kept, "\n")))
}

// android runs Android CLI against the phone's SDK and adb server, with no
// metrics sent.
func (m *Manager) android(ctx context.Context, args ...string) (Output, error) {
	sdk := m.path(sdkDir)

	return m.opts.System.Run(ctx, Command{
		Path: m.path(androidCLIPath),
		Args: append([]string{"--no-metrics", "--sdk=" + sdk}, args...),
		Env:  append(os.Environ(), "ANDROID_SDK_ROOT="+sdk, "ANDROID_HOME="+sdk),
	})
}

// done is a command's result as an action's: what it did, or why it failed.
func done(out Output, err error, did string) nodeproto.DeviceAnswer {
	if why := failure(out, err); why != "" {
		return failed(why)
	}

	return text(did)
}

// text is a command's output as a result.
func (m *Manager) text(out Output, err error) nodeproto.DeviceAnswer {
	if why := failure(out, err); why != "" {
		return failed(why)
	}

	return text(capText(out.Stdout))
}

// failure says why a command failed, or "". Android CLI says some failures
// on its output with "Error:" and exits with 0 (`input`, tried with
// 1.1.16919734), so that is read too.
func failure(out Output, err error) string {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "the phone did not answer within " + toolWithin.String()
		}

		return err.Error()
	}

	said := strings.TrimSpace(out.Stderr + "\n" + out.Stdout)
	if out.Code != 0 || strings.HasPrefix(strings.TrimSpace(out.Stdout), "Error:") ||
		strings.HasPrefix(strings.TrimSpace(out.Stderr), "Error:") {
		if said == "" {
			said = "exit " + strconv.Itoa(out.Code)
		}

		return capText(said)
	}

	return ""
}

func capText(s string) string {
	if len(s) <= maxTextBytes {
		return s
	}

	return s[:maxTextBytes] + fmt.Sprintf("\n[cut at %d KB]", maxTextBytes>>10)
}

func text(s string) nodeproto.DeviceAnswer {
	return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: s}}}
}

func failed(s string) nodeproto.DeviceAnswer {
	return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: s}}, IsError: true}
}
