// Package device is the Android phone a node holds (issue #417): an emulator
// on this machine, installed by the node itself on first use, started when
// asked and stopped when nothing has used it for a while. Its disk outlives
// a stop, with the owner's sign-ins on it.
//
// The shape is the shared browser's (internal/controlplane/browser): pinned
// downloads checked by size and checksum, installed at the first use, started
// at the first use and closed after an idle time. What it runs is what the
// trial machine proved (docs/research/2026-11-14-android-device-for-agents.md):
// an Xvfb display, on which `-gpu host` renders with Mesa's llvmpipe, adb's
// server started before the emulator so that its key exists for the Play
// image's authorization, and the emulator with no window, booted with the
// default Quick Boot as Orca and serve-emu boot theirs.
package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	// IdleAfter is how long the phone runs after its last use (issue #417).
	IdleAfter = 15 * time.Minute
	// BootWithin is how long the emulator has to finish booting.
	BootWithin = 5 * time.Minute
	// EndWithin is how long a program has to end once asked; then it is killed.
	EndWithin = 30 * time.Second
	// displayWithin is how long Xvfb has to listen.
	displayWithin = 10 * time.Second
	sweepEvery    = time.Minute
	pollEvery     = 2 * time.Second

	// port is the emulator's console port, and serial its name to adb: one
	// emulator a node, always the first an adb server names.
	port   = 5554
	serial = "emulator-5554"
	// firstDisplay is the first X display tried, the trial's.
	firstDisplay = 99
	lastDisplay  = 199

	runDir     = "run"
	runFile    = "run.json"
	xvfbLog    = "xvfb.log"
	emuLog     = "emulator.log"
	tailBytes  = 2 << 10
	emuNeedle  = sdkDir + "/emulator/"
	xvfbNeedle = "Xvfb"
)

// The animation scales, set to 0 once the phone booted, as budtmo's
// docker-android and T3 Code's device setup do: a screen that is still
// moving is what an agent's screenshot must not catch.
var animationScales = []string{"window_animation_scale", "transition_animation_scale", "animator_duration_scale"}

// Options are how a Manager is set up; zero fields take the defaults.
type Options struct {
	// Dir holds everything of the phone: the SDK, the AVD and its disk.
	Dir string
	// System runs the programs; nil: this machine's own, none where the
	// emulator cannot run.
	System System
	// KVM says whether /dev/kvm can be used now; nil: it cannot.
	KVM    func() bool
	Clock  platform.Clock
	Logger *slog.Logger
	Client *http.Client
	// Poll is how often a boot or an end is looked at.
	Poll time.Duration
	// BootWithin and EndWithin replace the package's limits.
	BootWithin time.Duration
	EndWithin  time.Duration
}

// Manager keeps the phone of this node.
type Manager struct {
	opts  Options
	comps []component
	in    installer

	// op makes a start and a stop happen one at a time.
	op sync.Mutex

	// mu guards what follows.
	mu       sync.Mutex
	state    string
	reason   string
	have     []nodeproto.DeviceComponent
	lastUsed time.Time
	run      record
	// starting cancels the start under way; nil when none is.
	starting context.CancelFunc
	changed  chan struct{}
}

// record is what the node keeps of the programs it started, in run.json, so
// that the next node finds them (adopt).
type record struct {
	Display  int `json:"display,omitempty"`
	Xvfb     int `json:"xvfb,omitempty"`
	Emulator int `json:"emulator,omitempty"`
}

// New returns a Manager. Nothing is installed or started until it is asked.
func New(opts Options) *Manager { return newManager(opts, components()) }

func newManager(opts Options, comps []component) *Manager {
	if opts.System == nil {
		opts.System = newSystem()
	}

	if opts.Clock == nil {
		opts.Clock = platform.SystemClock{}
	}

	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}

	if opts.Poll <= 0 {
		opts.Poll = pollEvery
	}

	if opts.BootWithin <= 0 {
		opts.BootWithin = BootWithin
	}

	if opts.EndWithin <= 0 {
		opts.EndWithin = EndWithin
	}

	m := &Manager{
		opts: opts, comps: comps, in: installer{dir: opts.Dir, client: opts.Client},
		changed: make(chan struct{}, 1),
	}
	m.state = m.restingState()
	m.refresh()

	return m
}

// Changed is told each time the state changes.
func (m *Manager) Changed() <-chan struct{} { return m.changed }

// Status is the phone's state, with what is installed.
func (m *Manager) Status() nodeproto.DeviceState {
	m.mu.Lock()
	defer m.mu.Unlock()

	return nodeproto.DeviceState{State: m.state, Reason: m.reason, Installed: slices.Clone(m.have)}
}

// refresh reads again what is installed, for Status.
func (m *Manager) refresh() {
	have, err := m.in.installed()
	if err != nil {
		return
	}

	var out []nodeproto.DeviceComponent

	for _, c := range m.comps {
		if entry, ok := have[c.Name]; ok {
			out = append(out, nodeproto.DeviceComponent{Name: c.Name, Version: entry.Version})
		}
	}

	m.mu.Lock()
	m.have = out
	m.mu.Unlock()
}

// restingState is the state with nothing running: stopped once installed.
func (m *Manager) restingState() string {
	if m.in.complete(m.comps) {
		return nodeproto.DeviceStopped
	}

	return nodeproto.DeviceAbsent
}

func (m *Manager) set(state, reason string) {
	m.mu.Lock()
	changed := m.state != state || m.reason != reason
	m.state, m.reason = state, reason
	m.mu.Unlock()

	if !changed {
		return
	}

	select {
	case m.changed <- struct{}{}:
	default:
	}
}

// Touch marks a use of the running phone, which puts its idle stop off.
func (m *Manager) Touch() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == nodeproto.DeviceRunning {
		m.lastUsed = m.opts.Clock.Now()
	}
}

// Start has the phone running: installed first if it is not, then booted. A
// phone that runs is only touched; a start under way is not begun twice.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()

	if m.state == nodeproto.DeviceRunning {
		m.lastUsed = m.opts.Clock.Now()
		m.mu.Unlock()

		return nil
	}

	if m.starting != nil {
		m.mu.Unlock()

		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	m.starting = cancel
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.starting = nil
		m.mu.Unlock()
		cancel()
	}()

	m.op.Lock()
	defer m.op.Unlock()

	err := m.boot(ctx)
	if err == nil {
		m.opts.Logger.InfoContext(ctx, "the phone started", slog.String("event", "device.started"))

		return nil
	}

	// What was started is ended, whatever stopped the start: a stop asked meanwhile, or the start's own failure.
	m.halt(context.WithoutCancel(ctx))

	if ctx.Err() != nil {
		m.set(m.restingState(), "")

		return ctx.Err()
	}

	m.set(nodeproto.DeviceFailed, err.Error())
	m.opts.Logger.WarnContext(ctx, "the phone did not start",
		slog.String("event", "device.start_failed"), slog.String("error", err.Error()))

	return err
}

// boot installs what is missing and starts the display, adb's server and
// the emulator. The caller holds op.
func (m *Manager) boot(ctx context.Context) error {
	sys := m.opts.System
	if sys == nil {
		return ErrUnsupported
	}

	if m.opts.KVM == nil || !m.opts.KVM() {
		return errors.New("the node's user cannot use /dev/kvm, which the emulator needs: is it in the kvm group?")
	}

	if !m.in.complete(m.comps) {
		m.set(nodeproto.DeviceInstalling, "")

		err := m.in.install(ctx, m.comps)
		m.refresh()

		if err != nil {
			return fmt.Errorf("installing: %w", err)
		}
	}

	if err := writeAVD(m.opts.Dir); err != nil {
		return err
	}

	m.set(nodeproto.DeviceStarting, "")

	if err := os.MkdirAll(m.path(runDir), 0o700); err != nil {
		return err
	}

	display, err := m.startDisplay(ctx)
	if err != nil {
		return err
	}

	adb := m.path(adbPath)

	// adb's server first, with the node user's own ~/.android: it makes the key the Play image asks to
	// authorize. A trial with another HOME left adb "unauthorized" (issue #417).
	out, err := sys.Run(ctx, Command{Path: adb, Args: []string{"start-server"}, Env: os.Environ()})
	if err != nil || out.Code != 0 {
		return fmt.Errorf("adb's server did not start: %s", why(err, out))
	}

	emulator, err := sys.Start(Command{
		Path: m.path(emulatorPath),
		Args: []string{
			"-avd", avdName, "-no-window", "-no-audio", "-no-boot-anim", "-gpu", "host",
			"-memory", "4096", "-cores", "6", "-port", strconv.Itoa(port),
		},
		Env: append(os.Environ(), "DISPLAY=:"+strconv.Itoa(display),
			"ANDROID_SDK_ROOT="+m.path(sdkDir), "ANDROID_AVD_HOME="+m.path(avdDir)),
		Log: m.path(runDir, emuLog),
	})
	if err != nil {
		return fmt.Errorf("starting the emulator: %w", err)
	}

	m.keep(func(r *record) { r.Emulator = emulator.Pid() })

	if err := m.waitBoot(ctx, emulator); err != nil {
		return err
	}

	for _, scale := range animationScales {
		out, err := sys.Run(ctx, Command{
			Path: adb, Args: []string{"-s", serial, "shell", "settings", "put", "global", scale, "0"}, Env: os.Environ(),
		})
		if err != nil || out.Code != 0 {
			return fmt.Errorf("turning the animations off: %s", why(err, out))
		}
	}

	m.mu.Lock()
	m.lastUsed = m.opts.Clock.Now()
	m.mu.Unlock()

	m.set(nodeproto.DeviceRunning, "")

	return nil
}

// startDisplay starts Xvfb on the first free display and waits until it
// listens.
func (m *Manager) startDisplay(ctx context.Context) (int, error) {
	sys := m.opts.System

	display := 0

	for n := firstDisplay; n <= lastDisplay; n++ {
		if sys.DisplayFree(n) {
			display = n

			break
		}
	}

	if display == 0 {
		return 0, fmt.Errorf("no X display from :%d to :%d is free", firstDisplay, lastDisplay)
	}

	xvfb, err := sys.Start(Command{
		Path: "Xvfb", Args: []string{":" + strconv.Itoa(display), "-screen", "0", "1280x1024x24", "+extension", "GLX"},
		Env: os.Environ(), Log: m.path(runDir, xvfbLog),
	})
	if err != nil {
		return 0, fmt.Errorf("starting Xvfb (is the xvfb package installed?): %w", err)
	}

	m.keep(func(r *record) { r.Display, r.Xvfb = display, xvfb.Pid() })

	deadline := time.NewTimer(displayWithin)
	defer deadline.Stop()

	tick := time.NewTicker(m.opts.Poll)
	defer tick.Stop()

	for !sys.DisplayReady(display) {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-xvfb.Done():
			return 0, fmt.Errorf("the display's Xvfb ended: %s", m.tail(xvfbLog))
		case <-deadline.C:
			return 0, fmt.Errorf("the display's Xvfb did not listen on :%d within %s", display, displayWithin)
		case <-tick.C:
		}
	}

	return display, nil
}

// waitBoot waits until Android says it booted, as Orca waits
// (android-avd-boot.ts, waitForNewBootedSerial: sys.boot_completed).
func (m *Manager) waitBoot(ctx context.Context, emulator Process) error {
	deadline := time.NewTimer(m.opts.BootWithin)
	defer deadline.Stop()

	tick := time.NewTicker(m.opts.Poll)
	defer tick.Stop()

	for {
		if m.booted(ctx) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-emulator.Done():
			return fmt.Errorf("the emulator ended while booting: %s", m.tail(emuLog))
		case <-deadline.C:
			return fmt.Errorf("the phone did not boot within %s: %s", m.opts.BootWithin, m.tail(emuLog))
		case <-tick.C:
		}
	}
}

// booted says whether the emulator answers adb and Android finished booting.
func (m *Manager) booted(ctx context.Context) bool {
	out, err := m.opts.System.Run(ctx, Command{
		Path: m.path(adbPath), Args: []string{"-s", serial, "shell", "getprop", "sys.boot_completed"}, Env: os.Environ(),
	})

	return err == nil && out.Code == 0 && strings.TrimSpace(out.Stdout) == "1"
}

// Stop ends the phone: a start under way is called off, then the emulator
// is asked to end and Xvfb after it.
func (m *Manager) Stop(ctx context.Context) {
	m.mu.Lock()
	if m.starting != nil {
		m.starting()
	}
	m.mu.Unlock()

	m.op.Lock()
	defer m.op.Unlock()

	m.halt(ctx)
	m.set(m.restingState(), "")
}

// halt ends what run.json names: the emulator with `adb emu kill`, which
// lets it save its Quick Boot state, and Xvfb with SIGTERM; either is killed
// if it has not ended within EndWithin. The caller holds op.
func (m *Manager) halt(ctx context.Context) {
	m.mu.Lock()
	run := m.run
	m.mu.Unlock()

	sys := m.opts.System
	if sys == nil {
		return
	}

	if run.Emulator != 0 && sys.Alive(run.Emulator, emuNeedle) {
		_, _ = sys.Run(ctx, Command{Path: m.path(adbPath), Args: []string{"-s", serial, "emu", "kill"}, Env: os.Environ()})

		m.end(ctx, run.Emulator, emuNeedle, false)
		m.opts.Logger.InfoContext(ctx, "the phone stopped", slog.String("event", "device.stopped"))
	}

	if run.Xvfb != 0 && sys.Alive(run.Xvfb, xvfbNeedle) {
		m.end(ctx, run.Xvfb, xvfbNeedle, true)
	}

	m.keep(func(r *record) { *r = record{} })
}

// end waits for a process group to end, after asking it with SIGTERM when
// term is set, and kills it after EndWithin.
func (m *Manager) end(ctx context.Context, pgid int, needle string, term bool) {
	sys := m.opts.System

	if term {
		_ = sys.Signal(pgid, Terminate)
	}

	deadline := time.NewTimer(m.opts.EndWithin)
	defer deadline.Stop()

	tick := time.NewTicker(m.opts.Poll)
	defer tick.Stop()

	for sys.Alive(pgid, needle) {
		select {
		case <-ctx.Done():
			_ = sys.Signal(pgid, Kill)

			return
		case <-deadline.C:
			m.opts.Logger.WarnContext(ctx, "a program of the phone did not end and was killed",
				slog.String("event", "device.killed"), slog.String("program", strings.Trim(needle, "/")))

			_ = sys.Signal(pgid, Kill)

			return
		case <-tick.C:
		}
	}
}

// Sweep stops the phone once it has not been used for IdleAfter, and marks
// it failed when its emulator ended by itself. A start or a stop under way is
// left alone.
func (m *Manager) Sweep(ctx context.Context) {
	if !m.op.TryLock() {
		return
	}

	m.mu.Lock()
	running, idle, emulator := m.state == nodeproto.DeviceRunning, m.opts.Clock.Now().Sub(m.lastUsed), m.run.Emulator
	m.mu.Unlock()

	switch {
	case !running:
		m.op.Unlock()
	case !m.opts.System.Alive(emulator, emuNeedle):
		m.halt(ctx)
		m.set(nodeproto.DeviceFailed, "the emulator ended by itself: "+m.tail(emuLog))
		m.op.Unlock()
	case idle >= IdleAfter:
		m.op.Unlock()
		m.opts.Logger.InfoContext(ctx, "the idle phone is stopped",
			slog.String("event", "device.idle"), slog.Duration("idle", idle))
		m.Stop(ctx)
	default:
		m.op.Unlock()
	}
}

// Run takes back a phone an earlier node left running, then sweeps until ctx
// ends. A phone that runs when ctx ends is left running: the next node takes
// it back.
func (m *Manager) Run(ctx context.Context) {
	m.adopt(ctx)

	tick := time.NewTicker(sweepEvery)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.Sweep(ctx)
		}
	}
}

// adopt takes back the phone run.json names when it still runs and has
// booted, as Orca takes an AVD that runs already rather than booting it again
// (android-avd-boot.ts, findRunningAvdSerial): a node that restarts, for its
// own update too, leaves the phone and the owner's session on it as they
// were. The browser instead closes a Chrome left by an earlier control plane,
// since its tabs belong to agents the new one does not know; the phone holds
// no agent's session. What run.json names that is not a booted phone is
// ended. The idle time counts from now.
func (m *Manager) adopt(ctx context.Context) {
	raw, err := os.ReadFile(m.path(runDir, runFile))
	if err != nil {
		return
	}

	var run record
	if json.Unmarshal(raw, &run) != nil {
		return
	}

	m.op.Lock()
	defer m.op.Unlock()

	m.mu.Lock()
	m.run = run
	m.mu.Unlock()

	sys := m.opts.System
	if sys != nil && run.Emulator != 0 && sys.Alive(run.Emulator, emuNeedle) && m.booted(ctx) {
		m.mu.Lock()
		m.lastUsed = m.opts.Clock.Now()
		m.mu.Unlock()

		m.set(nodeproto.DeviceRunning, "")
		m.opts.Logger.InfoContext(ctx, "the phone an earlier node started is taken back",
			slog.String("event", "device.adopted"))

		return
	}

	m.halt(ctx)
	m.set(m.restingState(), "")
}

// keep changes what run.json says and writes it; an empty record removes it.
func (m *Manager) keep(change func(r *record)) {
	m.mu.Lock()
	change(&m.run)
	run := m.run
	m.mu.Unlock()

	path := m.path(runDir, runFile)
	if run == (record{}) {
		_ = os.Remove(path)

		return
	}

	raw, err := json.Marshal(run)
	if err != nil {
		return
	}

	if err := os.WriteFile(path+".tmp", raw, 0o600); err == nil {
		_ = os.Rename(path+".tmp", path)
	}
}

func (m *Manager) path(parts ...string) string {
	return filepath.Join(append([]string{m.opts.Dir}, parts...)...)
}

// tail is the end of a program's log, the line that says why it ended.
func (m *Manager) tail(name string) string {
	f, err := os.Open(m.path(runDir, name))
	if err != nil {
		return "no output"
	}
	defer f.Close() //nolint:errcheck // Read only.

	if info, err := f.Stat(); err == nil && info.Size() > tailBytes {
		_, _ = f.Seek(info.Size()-tailBytes, io.SeekStart)
	}

	raw, _ := io.ReadAll(f)

	var said []string

	for line := range strings.SplitSeq(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			said = append(said, line)
		}
	}

	if len(said) == 0 {
		return "no output"
	}

	// The emulator prints its reason last (Orca, emulatorExitDetail: its last three lines).
	return strings.Join(said[max(0, len(said)-3):], " | ")
}

// why is what a failed run said.
func why(err error, out Output) string {
	if err != nil {
		return err.Error()
	}

	text := strings.TrimSpace(out.Stderr)
	if text == "" {
		text = strings.TrimSpace(out.Stdout)
	}

	return fmt.Sprintf("exit %d: %s", out.Code, text)
}
