// Package devicetool is what both binaries share about the phone's tools
// (issue #417, step 2): the tools an agent is offered, with their input
// schemas, and the reading of a call's arguments. The control plane lists the
// tools and refuses a call it can tell is wrong; the node that holds the phone
// reads the call again and runs it (internal/node/device, tools.go).
//
// The tools are Android CLI's commands and adb's, as Google's own android-cli
// skill drives a device (android/android-cli-plugin, skills/android-cli/SKILL.md
// at e16e722, Apache-2.0): see with `android layout`, and with `android screen
// capture --annotate` when the numbered boxes are asked for, tap and swipe with `adb shell input` at the coordinates
// `android screen resolve` gives for a box's number. Typing is Android CLI's
// `input text`, which typed Vietnamese where `adb shell input text` types
// ASCII only (docs/research/2026-11-14-android-device-for-agents.md, section
// 3). A screen without boxes is adb's `exec-out screencap -p`, as OpenMausBot
// takes one (server/drivers/phone-proxy.ts). Launching an app and reading its log are Orca's (src/main/emulator/android,
// launchAppArgs and logcatArgs). The names and arguments are this repository's.
package devicetool

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The tools' names: each is Prefix and what it does.
const (
	// Prefix starts every phone tool's name, which tells a worker's tools
	// server where to pass a call (internal/node/workertools).
	Prefix = "device_"

	Screen = "device_screen"
	Layout = "device_layout"
	Tap    = "device_tap"
	Swipe  = "device_swipe"
	Type   = "device_type"
	Key    = "device_key"
	Launch = "device_launch"
	Logcat = "device_logcat"
)

// Errors of a call an agent made wrongly.
var (
	// ErrNoSuchTool reports a tool that is not offered.
	ErrNoSuchTool = errors.New("no such phone tool")
	// ErrBadArguments reports arguments a tool was refused with.
	ErrBadArguments = errors.New("the phone tool's arguments were refused")
)

// Tool is one tool as an agent is offered it, in MCP's shape.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// The keys device_key presses: Android CLI's names for them (`android input key`).
var keys = []string{"back", "home", "recents", "enter"}

// The priorities logcat filters by, lowest first.
var levels = []string{"V", "D", "I", "W", "E", "F"}

// Limits of the arguments.
const (
	// MaxText bounds what device_type types.
	MaxText = 2000
	// MaxSelector bounds a selector and a logcat filter.
	MaxSelector = 500
	// DefaultLines and MaxLines are how many log lines device_logcat reads.
	DefaultLines = 100
	MaxLines     = 1000

	minSwipeMs    = 50
	maxSwipeMs    = 5000
	defaultSwipe  = 300
	maxCoordinate = 100000
)

const point = `"#N", a number from your last device_screen with boxes, or "x,y" in the screen's pixels`

// Tools are the tools an agent is offered.
func Tools() []Tool {
	return []Tool{
		tool(Screen, "Take a picture of the phone's screen. Look at it before acting and again after each "+
			"action. With boxes, what can be tapped is marked by numbered boxes, which device_tap takes by "+
			"number; that takes a second or two longer.",
			`{"boxes":{"type":"boolean","description":"mark what can be tapped with numbered boxes"}}`, nil),
		tool(Layout, "Read the views of the app on the phone's screen as JSON: text, descriptions, ids and "+
			"centers. Apps drawn by Flutter or a game engine show little here; use device_screen for those.",
			`{}`, nil),
		tool(Tap, "Tap the phone's screen at a point: "+point+"; or the first view matching an Android CLI "+
			"selector such as text=Search or desc=Back,clickable=true.",
			`{"target":{"type":"string","description":"#N, x,y or a selector"}}`, []string{"target"}),
		tool(Swipe, "Swipe on the phone's screen from one point to another, to scroll or drag. Each point is "+
			point+".",
			`{"from":{"type":"string"},"to":{"type":"string"},`+
				`"duration_ms":{"type":"integer","minimum":50,"maximum":5000,"default":300}}`,
			[]string{"from", "to"}),
		tool(Type, "Type text into a field of the app on the phone, replacing what it holds; any language. "+
			"The field is the focused one unless an Android CLI selector names another: tap it first.",
			`{"text":{"type":"string"},"into":{"type":"string","description":"a selector; default focused=true"},`+
				`"submit":{"type":"boolean","description":"press Enter after typing"}}`,
			[]string{"text"}),
		tool(Key, "Press one of the phone's keys.",
			`{"key":{"type":"string","enum":["back","home","recents","enter"]}}`, []string{"key"}),
		tool(Launch, "Open an app installed on the phone, by its package name, such as com.android.settings.",
			`{"package":{"type":"string"}}`, []string{"package"}),
		tool(Logcat, "Read the phone's last log lines (adb logcat), optionally only those holding some text or "+
			"of a priority and above.",
			`{"lines":{"type":"integer","minimum":1,"maximum":1000,"default":100},`+
				`"contains":{"type":"string"},"level":{"type":"string","enum":["V","D","I","W","E","F"]}}`, nil),
	}
}

func tool(name, description, properties string, required []string) Tool {
	schema := `{"type":"object","properties":` + properties + `,"additionalProperties":false`
	if len(required) > 0 {
		raw, _ := json.Marshal(required) //nolint:errchkjson // Strings always encode.
		schema += `,"required":` + string(raw)
	}

	return Tool{Name: name, Description: description, InputSchema: json.RawMessage(schema + "}")}
}

// Point is where a tap or a swipe is: a box of the agent's last screen, or
// coordinates.
type Point struct {
	// Box is the number of a box; 0 for coordinates.
	Box  int
	X, Y int
}

// Call is a call's arguments, read and checked. Only the fields of its tool
// are set.
type Call struct {
	Tool string
	// Boxes asks device_screen for the numbered boxes.
	Boxes bool
	// Target is a tap's point; Selector the view it taps instead.
	Target   *Point
	Selector string
	// From, To and DurationMs are a swipe's.
	From, To   Point
	DurationMs int
	// Text, Into and Submit are what device_type types, where, and whether
	// Enter follows.
	Text   string
	Into   string
	Submit bool
	Key    string
	// Package is the app device_launch opens.
	Package string
	// Lines, Contains and Level are what device_logcat reads.
	Lines    int
	Contains string
	Level    string
}

var (
	boxPattern     = regexp.MustCompile(`^#([1-9][0-9]{0,3})$`)
	xyPattern      = regexp.MustCompile(`^([0-9]{1,6})\s*[, ]\s*([0-9]{1,6})$`)
	packagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
)

// arguments are every tool's arguments; a call sets those of its tool.
type arguments struct {
	Boxes      bool    `json:"boxes"`
	Target     *string `json:"target"`
	From       *string `json:"from"`
	To         *string `json:"to"`
	DurationMs *int    `json:"duration_ms"`
	Text       *string `json:"text"`
	Into       string  `json:"into"`
	Submit     bool    `json:"submit"`
	Key        string  `json:"key"`
	Package    string  `json:"package"`
	Lines      *int    `json:"lines"`
	Contains   string  `json:"contains"`
	Level      string  `json:"level"`
}

// takes are the arguments each tool's schema names.
var takes = map[string][]string{
	Screen: {"boxes"},
	Tap:    {"target"},
	Swipe:  {"from", "to", "duration_ms"},
	Type:   {"text", "into", "submit"},
	Key:    {"key"},
	Launch: {"package"},
	Logcat: {"lines", "contains", "level"},
}

func takesOf(tool string) string {
	if len(takes[tool]) == 0 {
		return "none"
	}

	return strings.Join(takes[tool], ", ")
}

// Read reads a call's arguments, refusing a tool that is not offered and
// arguments its tool does not take.
func Read(tool string, raw json.RawMessage) (Call, error) {
	if !slices.ContainsFunc(Tools(), func(t Tool) bool { return t.Name == tool }) {
		return Call{}, fmt.Errorf("%w: %s", ErrNoSuchTool, tool)
	}

	var a arguments
	if len(raw) > 0 && string(raw) != "null" {
		var given map[string]json.RawMessage
		if err := json.Unmarshal(raw, &given); err != nil {
			return Call{}, fmt.Errorf("%w: they are not a JSON object of the tool's arguments", ErrBadArguments)
		}

		// What the tool's schema does not name is refused, as its additionalProperties says.
		for name := range given {
			if !slices.Contains(takes[tool], name) {
				return Call{}, bad("%s takes no argument %q; it takes %s", tool, name, takesOf(tool))
			}
		}

		if err := json.Unmarshal(raw, &a); err != nil {
			return Call{}, fmt.Errorf("%w: they are not a JSON object of the tool's arguments", ErrBadArguments)
		}
	}

	c := Call{Tool: tool}

	var err error

	switch tool {
	case Screen:
		c.Boxes = a.Boxes
	case Tap:
		err = c.readTap(a.Target)
	case Swipe:
		err = c.readSwipe(a.From, a.To, a.DurationMs)
	case Type:
		err = c.readType(a.Text, a.Into, a.Submit)
	case Key:
		c.Key = a.Key
		if !slices.Contains(keys, a.Key) {
			err = bad("key is one of %s", strings.Join(keys, ", "))
		}
	case Launch:
		c.Package = a.Package
		if !packagePattern.MatchString(a.Package) || len(a.Package) > 255 {
			err = bad("package is an app's package name, such as com.android.settings")
		}
	case Logcat:
		err = c.readLogcat(a.Lines, a.Contains, a.Level)
	}

	if err != nil {
		return Call{}, err
	}

	return c, nil
}

func bad(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadArguments, fmt.Sprintf(format, args...))
}

func (c *Call) readTap(target *string) error {
	if target == nil || strings.TrimSpace(*target) == "" {
		return bad("tap needs a target: %s; or a selector", point)
	}

	if p, ok := readPoint(*target); ok {
		c.Target = &p

		return nil
	}

	selector := strings.TrimSpace(*target)
	if !isSelector(selector) {
		return bad("tap's target is %s; or a selector of key=value pairs such as text=Search", point)
	}

	c.Selector = selector

	return nil
}

func (c *Call) readSwipe(from, to *string, duration *int) error {
	ok := from != nil && to != nil
	if ok {
		c.From, ok = readPoint(*from)
	}

	if ok {
		c.To, ok = readPoint(*to)
	}

	if !ok {
		return bad("swipe needs from and to, each %s", point)
	}

	c.DurationMs = defaultSwipe
	if duration != nil {
		if *duration < minSwipeMs || *duration > maxSwipeMs {
			return bad("duration_ms is from %d to %d", minSwipeMs, maxSwipeMs)
		}

		c.DurationMs = *duration
	}

	return nil
}

func (c *Call) readType(text *string, into string, submit bool) error {
	if text == nil || *text == "" || len(*text) > MaxText || strings.ContainsRune(*text, 0) {
		return bad("type needs text, at most %d bytes", MaxText)
	}

	into = strings.TrimSpace(into)
	if into == "" {
		into = "focused=true"
	}

	if !isSelector(into) {
		return bad("into is a selector of key=value pairs, such as focused=true or id=search")
	}

	c.Text, c.Into, c.Submit = *text, into, submit

	return nil
}

func (c *Call) readLogcat(lines *int, contains, level string) error {
	c.Lines = DefaultLines
	if lines != nil {
		if *lines < 1 || *lines > MaxLines {
			return bad("lines is from 1 to %d", MaxLines)
		}

		c.Lines = *lines
	}

	if level != "" && !slices.Contains(levels, level) {
		return bad("level is one of %s", strings.Join(levels, ", "))
	}

	if len(contains) > MaxSelector {
		return bad("contains is at most %d bytes", MaxSelector)
	}

	c.Contains, c.Level = contains, level

	return nil
}

// isSelector says whether s reads as Android CLI's selector: key=value pairs
// (or key:value), separated by commas. A selector never starts with "-",
// which Android CLI would read as an option.
func isSelector(s string) bool {
	return s != "" && len(s) <= MaxSelector && !strings.HasPrefix(s, "-") && strings.ContainsAny(s, "=:") &&
		!strings.ContainsRune(s, 0)
}

// readPoint reads "#N" or "x,y".
func readPoint(s string) (Point, bool) {
	s = strings.TrimSpace(s)

	if m := boxPattern.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])

		return Point{Box: n}, true
	}

	m := xyPattern.FindStringSubmatch(s)
	if m == nil {
		return Point{}, false
	}

	x, _ := strconv.Atoi(m[1])
	y, _ := strconv.Atoi(m[2])

	if x > maxCoordinate || y > maxCoordinate {
		return Point{}, false
	}

	return Point{X: x, Y: y}, true
}
