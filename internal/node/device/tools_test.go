package device

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// phoneTools answers the commands of the phone's tools as Android CLI
// 1.1.16919734 and adb do: a screen capture writes its PNG, adb's screencap
// writes a PNG to its output, a box resolves to
// "x y" (the documented output of `screen resolve`), and what a test sets
// fails as Android CLI fails, "Error:" on its output with exit 0.
type phoneTools struct {
	mu       sync.Mutex
	captures int
	// failWith is what an `android input` command prints, as a failure.
	failWith string
	// noMatches makes `android input` answer that no view matches, as it did
	// for the launcher's search field (issue #578).
	noMatches bool
	logcat    string
	// noLauncher makes monkey fail as it does for a package not installed.
	noLauncher bool
	// notPNG makes screencap write what is not a PNG.
	notPNG bool
}

// noMatchesAnswer is what Android CLI answered to `input text` on the phone
// when the focused field was not in the view tree (2026-11-15, issue #578).
const noMatchesAnswer = "{\n  \"results\": [\n    {\n      \"action\": \"text\",\n      \"status\": \"no_matches\",\n" +
	"      \"error\": \"No matching elements\"\n    }\n  ]\n}"

var boxArg = regexp.MustCompile(`^--string=#([0-9]+)$`)

// screenPNG is a PNG of a screen w pixels wide and h high, as a capture
// writes one: its size tells which capture it is.
func screenPNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.NRGBA{R: byte(x * 16 % 256), G: byte(y * 8 % 256), B: 200, A: 255})
		}
	}

	var out bytes.Buffer
	_ = png.Encode(&out, img)

	return out.Bytes()
}

// pictureSize decodes a picture an answer gives as a JPEG and says its size.
func pictureSize(t *testing.T, c nodeproto.DeviceContent) (int, int) {
	t.Helper()

	assert.Equal(t, "image", c.Type)
	assert.Equal(t, "image/jpeg", c.MimeType)

	raw, err := base64.StdEncoding.DecodeString(c.Data)
	require.NoError(t, err)

	config, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	require.NoError(t, err, "the picture is a JPEG")

	return config.Width, config.Height
}

func (p *phoneTools) answer(c Command) (Output, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	args := strings.Join(c.Args, " ")

	switch {
	case program(c) == "android" && strings.Contains(args, "screen capture"):
		p.captures++

		for _, a := range c.Args {
			if file, ok := strings.CutPrefix(a, "--output="); ok {
				_ = os.WriteFile(file, screenPNG(10+p.captures, 30), 0o600)
			}
		}

		return Output{}, true
	case program(c) == "adb" && strings.Contains(args, "exec-out screencap -p"):
		p.captures++

		picture := screenPNG(10+p.captures, 20)
		if p.notPNG {
			picture = []byte("screencap: error")
		}

		_ = os.WriteFile(c.Out, picture, 0o600)

		return Output{}, true
	case program(c) == "android" && strings.Contains(args, "screen resolve"):
		for _, a := range c.Args {
			if m := boxArg.FindStringSubmatch(a); m != nil {
				return Output{Stdout: m[1] + "00 640\n"}, true
			}
		}

		return Output{Code: 2}, true
	case program(c) == "android" && slices.Contains(c.Args, "input"):
		if p.noMatches {
			return Output{Code: 1, Stdout: noMatchesAnswer}, true
		}

		if p.failWith != "" {
			return Output{Stdout: p.failWith}, true
		}

		return Output{}, true
	case program(c) == "android":
		return Output{Stdout: `[{"text":"Search"}]`}, true
	case program(c) == "adb" && slices.Contains(c.Args, "logcat"):
		return Output{Stdout: p.logcat}, true
	case program(c) == "adb" && slices.Contains(c.Args, "monkey") && p.noLauncher:
		return Output{Code: 252, Stdout: "  bash arg: -p\n** No activities found to run, monkey aborted.\n"}, true
	case program(c) == "adb" && (slices.Contains(c.Args, "input") || slices.Contains(c.Args, "monkey")):
		return Output{Stdout: "Events injected: 1"}, true
	}

	return Output{}, false
}

// toolRig is a rig whose phone has been installed and stopped, with the
// tools' commands answered.
func toolRig(t *testing.T) (*rig, *phoneTools) {
	t.Helper()

	r := newRig(t)
	tools := &phoneTools{}
	r.sys.tool = tools.answer

	r.start(t)
	r.m.Stop(t.Context())
	require.Equal(t, nodeproto.DeviceStopped, r.m.Status().State)

	return r, tools
}

func call(t *testing.T, r *rig, agent, tool, arguments string) nodeproto.DeviceAnswer {
	t.Helper()

	return r.m.Call(t.Context(), agent, tool, json.RawMessage(arguments))
}

func textOf(a nodeproto.DeviceAnswer) string {
	var parts []string

	for _, c := range a.Content {
		parts = append(parts, c.Text)
	}

	return strings.Join(parts, " ")
}

// ranSince is what the machine ran after the first n commands.
func ranSince(r *rig, n int) []string { return r.sys.ranCommands()[n:] }

// A call on a stopped phone boots it first, then answers: the screen taken
// by adb's screencap into a file whole, without Android CLI's search for
// boxes, given as a JPEG of the same pixels (issue #578).
func TestACallStartsAStoppedPhoneAndShowsItsScreen(t *testing.T) {
	t.Parallel()

	r, _ := toolRig(t)

	answer := call(t, r, "d-1", "device_screen", `{}`)
	require.False(t, answer.IsError, textOf(answer))
	assert.Equal(t, nodeproto.DeviceRunning, r.m.Status().State)

	require.Len(t, answer.Content, 2)

	w, h := pictureSize(t, answer.Content[0])
	assert.Equal(t, []int{11, 20}, []int{w, h}, "the first capture's pixels, not scaled")

	assert.Contains(t, r.sys.ranCommands(), "adb -s emulator-5554 exec-out screencap -p")

	for _, line := range r.sys.ranCommands() {
		assert.NotContains(t, line, "screen capture", "no boxes were asked for")
	}
}

// A screen with boxes is Android CLI's annotated capture, run against the
// phone's own SDK with no metrics sent, and kept as the agent's last screen:
// a PNG on disk, which `screen resolve` reads, and a JPEG to the agent.
func TestAScreenWithBoxesIsAndroidCLIsAnnotatedCapture(t *testing.T) {
	t.Parallel()

	r, _ := toolRig(t)

	answer := call(t, r, "d-1", "device_screen", `{"boxes":true}`)
	require.False(t, answer.IsError, textOf(answer))
	require.Len(t, answer.Content, 2)

	w, h := pictureSize(t, answer.Content[0])
	assert.Equal(t, []int{11, 30}, []int{w, h})
	assert.Contains(t, textOf(answer), "Numbered boxes")

	kept, err := os.ReadFile(r.m.screenFile("d-1"))
	require.NoError(t, err)
	_, err = png.DecodeConfig(bytes.NewReader(kept))
	require.NoError(t, err, "the screen kept is Android CLI's PNG")

	sdk := filepath.Join(r.dir, "sdk")
	capture := slices.IndexFunc(r.sys.ranCommands(), func(line string) bool {
		return strings.HasPrefix(line, "android --no-metrics --sdk="+sdk+" screen capture --annotate --device=emulator-5554")
	})
	assert.GreaterOrEqual(t, capture, 0, r.sys.ranCommands())
	assert.FileExists(t, r.m.screenFile("d-1"))
}

// Numbers are taken only from a screen with boxes: a plain screen gives none,
// and one taken after a screen with boxes leaves its numbers to be tapped.
func TestAPlainScreenGivesNoNumbers(t *testing.T) {
	t.Parallel()

	r, _ := toolRig(t)

	require.False(t, call(t, r, "d-1", "device_screen", `{}`).IsError)

	answer := call(t, r, "d-1", "device_tap", `{"target":"#1"}`)
	assert.True(t, answer.IsError)
	assert.Contains(t, textOf(answer), "call device_screen with boxes first")

	require.False(t, call(t, r, "d-1", "device_screen", `{"boxes":true}`).IsError)
	require.False(t, call(t, r, "d-1", "device_screen", `{}`).IsError)

	answer = call(t, r, "d-1", "device_tap", `{"target":"#1"}`)
	require.False(t, answer.IsError, textOf(answer))
	assert.Equal(t, "Tapped at 100,640.", textOf(answer))
}

// What adb's screencap writes that is not a PNG is not shown as a picture.
func TestAScreencapThatIsNotAPictureFails(t *testing.T) {
	t.Parallel()

	r, tools := toolRig(t)
	tools.notPNG = true

	answer := call(t, r, "d-1", "device_screen", `{}`)
	assert.True(t, answer.IsError)
	assert.Contains(t, textOf(answer), "not a picture")
}

// A phone not installed yet is installed without holding the call, which
// says to come back.
func TestACallOnAPhoneNotInstalledSaysToComeBack(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.sys.tool = (&phoneTools{}).answer

	answer := call(t, r, "d-1", "device_screen", `{}`)
	assert.True(t, answer.IsError)
	assert.Contains(t, textOf(answer), "being installed")

	require.Eventually(t, func() bool { return r.m.Status().State == nodeproto.DeviceRunning }, 5*time.Second,
		5*time.Millisecond, "the install and boot go on after the call")
}

// A box's number is taken from the agent's own last screen and tapped with
// adb at the center Android CLI resolves; an agent with no screen is told to
// take one.
func TestATapByNumberUsesTheAgentsOwnLastScreen(t *testing.T) {
	t.Parallel()

	r, _ := toolRig(t)

	require.False(t, call(t, r, "d-1", "device_screen", `{"boxes":true}`).IsError)

	before := len(r.sys.ranCommands())
	answer := call(t, r, "d-1", "device_tap", `{"target":"#3"}`)
	require.False(t, answer.IsError, textOf(answer))

	ran := ranSince(r, before)
	require.Len(t, ran, 2)
	assert.Contains(t, ran[0], "screen resolve --screenshot="+r.m.screenFile("d-1")+" --string=#3")
	assert.Equal(t, "adb -s emulator-5554 shell input tap 300 640", ran[1])

	answer = call(t, r, "d-2", "device_tap", `{"target":"#3"}`)
	assert.True(t, answer.IsError)
	assert.Contains(t, textOf(answer), "call device_screen with boxes first")
}

// After another agent acted on the phone, an agent's old box numbers are
// refused until it looks again; coordinates and its own actions are not.
func TestAnotherAgentsActionMakesTheScreenStale(t *testing.T) {
	t.Parallel()

	r, _ := toolRig(t)

	require.False(t, call(t, r, "d-1", "device_screen", `{"boxes":true}`).IsError)
	require.False(t, call(t, r, "d-1", "device_tap", `{"target":"#1"}`).IsError)
	require.False(t, call(t, r, "d-1", "device_tap", `{"target":"#2"}`).IsError,
		"an agent's own action leaves its screen its own")

	require.False(t, call(t, r, "d-2", "device_key", `{"key":"back"}`).IsError)

	answer := call(t, r, "d-1", "device_tap", `{"target":"#1"}`)
	assert.True(t, answer.IsError)
	assert.Contains(t, textOf(answer), "Another agent used the phone")

	assert.False(t, call(t, r, "d-1", "device_tap", `{"target":"10,20"}`).IsError)

	require.False(t, call(t, r, "d-1", "device_screen", `{"boxes":true}`).IsError)
	assert.False(t, call(t, r, "d-1", "device_tap", `{"target":"#1"}`).IsError)
}

// Text is one argument after "--", so a text that starts with "-" is typed,
// not read as an option; the field is the focused one by default.
func TestTypeGivesTheTextAsOneArgument(t *testing.T) {
	t.Parallel()

	r, _ := toolRig(t)
	require.False(t, call(t, r, "d-1", "device_screen", `{}`).IsError)

	before := len(r.sys.ranCommands())
	answer := call(t, r, "d-1", "device_type", `{"text":"-5 Phở Hà Nội","submit":true}`)
	require.False(t, answer.IsError, textOf(answer))

	ran := ranSince(r, before)
	require.Len(t, ran, 1)
	assert.True(t, strings.HasSuffix(ran[0], "input --device=emulator-5554 text --submit -- -5 Phở Hà Nội focused=true"),
		ran[0])
}

// A focused field the view tree does not show, as the launcher's search was
// while it asked "Always show keyboard here?", is no match for Android CLI:
// the text is typed with adb into what has input focus, spaces as %s, and
// Enter pressed with keyevent 66, as Google's android-cli skill types
// (issue #578).
func TestTypeIntoAFieldTheViewTreeDoesNotShowGoesThroughAdb(t *testing.T) {
	t.Parallel()

	r, tools := toolRig(t)
	require.False(t, call(t, r, "d-1", "device_screen", `{}`).IsError)

	tools.noMatches = true

	before := len(r.sys.ranCommands())
	answer := call(t, r, "d-1", "device_type", `{"text":"hello world","submit":true}`)
	require.False(t, answer.IsError, textOf(answer))
	assert.Contains(t, textOf(answer), "Typed with adb")

	ran := ranSince(r, before)
	require.Len(t, ran, 3, ran)
	assert.True(t, strings.HasSuffix(ran[0], "input --device=emulator-5554 text --submit -- hello world focused=true"),
		ran[0])
	assert.Equal(t, "adb -s emulator-5554 shell input text hello%sworld", ran[1])
	assert.Equal(t, "adb -s emulator-5554 shell input keyevent 66", ran[2])
}

// adb types only what it can without a keyboard of its own, and never into
// another field than the one named: the agent is told why instead.
func TestTypeThatAdbCannotStandInForFails(t *testing.T) {
	t.Parallel()

	for name, arguments := range map[string]string{
		"text adb cannot type": `{"text":"Phở Hà Nội"}`,
		"a field named":        `{"text":"hello","into":"id=search"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r, tools := toolRig(t)
			require.False(t, call(t, r, "d-1", "device_screen", `{}`).IsError)

			tools.noMatches = true

			before := len(r.sys.ranCommands())
			answer := call(t, r, "d-1", "device_type", arguments)
			assert.True(t, answer.IsError)

			ran := ranSince(r, before)
			assert.Len(t, ran, 1, "adb typed nothing: %v", ran)
		})
	}
}

// Android CLI says some failures on its output and exits with 0; the agent
// is told the call failed.
func TestAnErrorAndroidCLIPrintsIsAFailure(t *testing.T) {
	t.Parallel()

	r, tools := toolRig(t)
	tools.failWith = "Error: Device with serial or AVD name 'emulator-5554' not found."

	answer := call(t, r, "d-1", "device_key", `{"key":"home"}`)
	assert.True(t, answer.IsError)
	assert.Contains(t, textOf(answer), "not found")
}

// The log is read once, and only the last lines that hold the text are kept.
func TestLogcatKeepsTheLastMatchingLines(t *testing.T) {
	t.Parallel()

	r, tools := toolRig(t)
	tools.logcat = "a vn.app one\nb other\nc vn.app two\nd vn.app three\n"

	answer := call(t, r, "d-1", "device_logcat", `{"lines":2,"contains":"vn.app","level":"W"}`)
	require.False(t, answer.IsError, textOf(answer))
	assert.Equal(t, "c vn.app two\nd vn.app three", textOf(answer))

	assert.Contains(t, r.sys.ranCommands(),
		"adb -s emulator-5554 logcat -d -v threadtime -t 5000 *:W")
}

// A level alone also reads many lines: logcat's -t counts lines before the
// level filter, so the last lines read with -t 2 may hold no error at all.
func TestLogcatAtALevelReadsBeforeKeepingTheLast(t *testing.T) {
	t.Parallel()

	r, tools := toolRig(t)
	tools.logcat = "--------- beginning of main\na E one\nb E two\nc E three\n"

	answer := call(t, r, "d-1", "device_logcat", `{"lines":2,"level":"E"}`)
	require.False(t, answer.IsError, textOf(answer))
	assert.Equal(t, "b E two\nc E three", textOf(answer))

	assert.Contains(t, r.sys.ranCommands(),
		"adb -s emulator-5554 logcat -d -v threadtime -t 5000 *:E")
}

// An app that is not installed is said so, without monkey's arguments.
func TestLaunchingAnAppNotInstalledSaysSo(t *testing.T) {
	t.Parallel()

	r, tools := toolRig(t)
	tools.noLauncher = true

	answer := call(t, r, "d-1", "device_launch", `{"package":"vn.not.installed"}`)
	assert.True(t, answer.IsError)
	assert.Equal(t,
		"No app with the package vn.not.installed is installed on the phone, or it has no launcher.",
		textOf(answer))
}

// A call the phone cannot take is refused before the phone is started.
func TestABadCallDoesNotStartThePhone(t *testing.T) {
	t.Parallel()

	r, _ := toolRig(t)

	answer := call(t, r, "d-1", "device_key", `{"key":"power"}`)
	assert.True(t, answer.IsError)
	assert.Equal(t, nodeproto.DeviceStopped, r.m.Status().State)
}

// A screen of an earlier boot is not tapped by number: the phone stopped and
// started again since, and what it shows now is another screen.
func TestAScreenOfAnEarlierBootIsNotTappedByNumber(t *testing.T) {
	t.Parallel()

	r, _ := toolRig(t)

	require.False(t, call(t, r, "d-1", "device_screen", `{"boxes":true}`).IsError)
	r.m.Stop(t.Context())

	answer := call(t, r, "d-1", "device_tap", `{"target":"#1"}`)
	assert.True(t, answer.IsError)
	assert.Contains(t, textOf(answer), "started again")
	assert.Equal(t, nodeproto.DeviceRunning, r.m.Status().State, "the call started the phone")
}
