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
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	// IdleAfter is how long the phone runs after its last use (issue #417): a start, a Touch, or a view of its
	// screen open. Using the phone over adb does not count; another start puts the stop off.
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
	// GPU is the emulator's -gpu mode, one of GPUModes; empty: DefaultGPU.
	// The node's configuration sets it (device_gpu), so another renderer can
	// be measured on the machine itself, as serve-emu's --gpu and budtmo's
	// EMULATOR_GPU let one choose.
	GPU string
	// CPUs is the machine's CPU count; zero: runtime.NumCPU.
	CPUs int
}

// DefaultGPU is the renderer the trial machine proved: Mesa's llvmpipe
// through Xvfb. Under SwiftShader (swiftshader_indirect) the emulator died 4
// times out of 4 when the map app's "sign in with" link opened the toll app's
// authorize screen, and swangle_indirect crashed as the map app started;
// only host completed that sign-in (docs/research/2026-11-14-android-device-
// for-agents.md, section 2). Another mode is for measuring: it is kept only
// once it passes that sign-in.
const DefaultGPU = "host"

// GPUModes are the emulator's -gpu modes that are not deprecated
// (developer.android.com/studio/run/emulator-acceleration). Xvfb starts for
// each: budtmo keeps DISPLAY whatever its mode, and only host was tried.
var GPUModes = []string{"host", "auto", "software", "lavapipe", "swiftshader", "swangle"}

// minCores is the fewest cores the phone is given.
const minCores = 2

// cores is the phone's share of the machine: half its CPUs, at least
// minCores, so the rest is left to Xvfb, scrcpy's encoder, adb and the node.
// Google's container gives its phone 4 (emu/templates/avd/MediumPhone.avd/
// config.ini, hw.cpu.ncore), HQarroum 4 by default (scripts/start-emulator.sh);
// half the host is the owner's (2026-11-15).
func cores(cpus int) int { return max(minCores, cpus/2) }

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
	// asked counts the starts and stops asked; only the latest one asked is
	// carried out, whatever order they take op in.
	asked uint64
	// starts counts the starts asked that have not returned yet, a replaced one included until it returns.
	starts int
	// starting cancels the start under way; nil when none is.
	starting context.CancelFunc
	changed  chan struct{}

	// agents keeps the agents' screens apart and lets one call use the phone at a time (tools.go).
	agents agents
	// boots counts the boots this node saw, a phone taken back included; a screen of an earlier boot is not
	// tapped by number. Guarded by mu.
	boots uint64

	// live is the stream of the phone's screen the owner's views share (view.go), and screenSource what
	// starts it in a test; nil: scrcpy on the phone.
	live         screen
	screenSource func(ctx context.Context) (screenStream, error)
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

	if opts.GPU == "" {
		opts.GPU = DefaultGPU
	}

	if opts.CPUs <= 0 {
		opts.CPUs = runtime.NumCPU()
	}

	m := &Manager{
		opts: opts, comps: comps, in: installer{dir: opts.Dir, client: opts.Client},
		changed: make(chan struct{}, 1), agents: agents{stale: map[string]bool{}, boot: map[string]uint64{}},
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

	m.live.stateChanged(state, reason)

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

// Ask has the phone started (start) or stopped, on its own goroutine under
// ctx, and returns at once. Asks are ordered as they are made: the latest one
// wins over those before it that have not been carried out yet, and a stop
// calls off a boot under way. The daemon asks so, in the order the control
// plane's messages came.
func (m *Manager) Ask(ctx context.Context, start bool) {
	n := m.ask(start)

	go func() {
		if start {
			_ = m.start(ctx, n) // A failure is in the phone's state, with its reason.

			return
		}

		m.stop(ctx, n)
	}()
}

// ask records a start or a stop as the latest asked; a stop calls off the
// boot under way.
func (m *Manager) ask(start bool) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.asked++
	if start {
		m.starts++
	}

	if !start && m.starting != nil {
		m.starting()
	}

	return m.asked
}

// Busy says whether the phone is being installed or started, or a start was
// asked that has not ended yet: the node does not restart for its update
// then, which would cut the boot short (issue #545).
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.starts > 0 || m.state == nodeproto.DeviceInstalling || m.state == nodeproto.DeviceStarting
}

// Start has the phone running: installed first if it is not, then booted. A
// phone that runs is only touched. A start that a later stop or start
// replaced before it was carried out, or that a stop called off, returns
// context.Canceled.
func (m *Manager) Start(ctx context.Context) error { return m.start(ctx, m.ask(true)) }

// start carries out the start asked as n, unless a later ask replaced it.
func (m *Manager) start(ctx context.Context, n uint64) error {
	defer func() {
		m.mu.Lock()
		m.starts--
		m.mu.Unlock()
	}()

	m.op.Lock()
	defer m.op.Unlock()

	// The check and the cancel's record are one step under mu, so a stop asked
	// after it finds the cancel and one asked before it is seen here.
	m.mu.Lock()

	if m.asked != n {
		m.mu.Unlock()

		return context.Canceled
	}

	if m.state == nodeproto.DeviceRunning {
		m.lastUsed = m.opts.Clock.Now()
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

	if !slices.Contains(GPUModes, m.opts.GPU) {
		return fmt.Errorf("device_gpu %q in the node's configuration is not a renderer; it is one of %s",
			m.opts.GPU, strings.Join(GPUModes, ", "))
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

	if err := m.clearStaleLocks(ctx); err != nil {
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

	// adb's server first, with the node user's own ~/.android: it makes the key the Play image asks to
	// authorize. A trial with another HOME left adb "unauthorized" (issue #417).
	out, err := m.adb(ctx, m.opts.EndWithin, "start-server")
	if err != nil || out.Code != 0 {
		return fmt.Errorf("adb's server did not start: %s", why(err, out))
	}

	emulator, err := sys.Start(Command{
		Path: m.path(emulatorPath),
		Args: []string{
			"-avd", avdName, "-no-window", "-no-audio", "-no-boot-anim", "-gpu", m.opts.GPU,
			"-memory", "4096", "-cores", strconv.Itoa(cores(m.opts.CPUs)), "-port", strconv.Itoa(port),
		},
		Env: append(os.Environ(), "DISPLAY=:"+strconv.Itoa(display),
			"ANDROID_SDK_ROOT="+m.path(sdkDir), "ANDROID_AVD_HOME="+m.path(avdDir)),
		Log: m.path(runDir, emuLog),
	})
	if err != nil {
		return fmt.Errorf("starting the emulator: %w", err)
	}

	// A program the next node could not find is not left running: the start fails and ends it.
	if err := m.keep(func(r *record) { r.Emulator = emulator.Pid() }); err != nil {
		return err
	}

	if err := m.waitBoot(ctx, emulator); err != nil {
		return err
	}

	for _, scale := range animationScales {
		out, err := m.adb(ctx, m.opts.EndWithin, "-s", serial, "shell", "settings", "put", "global", scale, "0")
		if err != nil || out.Code != 0 {
			return fmt.Errorf("turning the animations off: %s", why(err, out))
		}
	}

	m.mu.Lock()
	m.lastUsed = m.opts.Clock.Now()
	m.boots++
	m.mu.Unlock()

	m.set(nodeproto.DeviceRunning, "")

	return nil
}

// clearStaleLocks removes the AVD's lock files that an emulator ended
// without its own clean-up left behind (avd.go, clearLocks), before the next
// one boots. They are left while any emulator of the node's SDK runs, the
// node's own or one it did not start: its locks are not stale. The caller
// holds op, so no start of this node's runs meanwhile.
func (m *Manager) clearStaleLocks(ctx context.Context) error {
	if m.opts.System.Running(emuNeedle) {
		m.opts.Logger.WarnContext(ctx, "an emulator of the phone runs already; its locks are left",
			slog.String("event", "device.locks_kept"))

		return nil
	}

	removed, err := clearLocks(m.opts.Dir)
	if len(removed) > 0 {
		m.opts.Logger.InfoContext(ctx, "the phone's stale locks are removed",
			slog.String("event", "device.locks_removed"), slog.String("locks", strings.Join(removed, " ")))
	}

	return err
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

	if err := m.keep(func(r *record) { r.Display, r.Xvfb = display, xvfb.Pid() }); err != nil {
		return 0, err
	}

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
// Each adb call is given the boot's deadline: an adb that hangs does not hold
// the boot past it.
func (m *Manager) waitBoot(ctx context.Context, emulator Process) error {
	boot, cancel := context.WithTimeout(ctx, m.opts.BootWithin)
	defer cancel()

	tick := time.NewTicker(m.opts.Poll)
	defer tick.Stop()

	for {
		if m.booted(boot, m.opts.BootWithin) {
			return nil
		}

		select {
		case <-boot.Done():
			if err := ctx.Err(); err != nil {
				return err
			}

			return fmt.Errorf("the phone did not boot within %s: %s", m.opts.BootWithin, m.tail(emuLog))
		case <-emulator.Done():
			return fmt.Errorf("the emulator ended while booting: %s", m.tail(emuLog))
		case <-tick.C:
		}
	}
}

// booted says whether the emulator answers adb and Android finished booting,
// asking adb for at most within.
func (m *Manager) booted(ctx context.Context, within time.Duration) bool {
	out, err := m.adb(ctx, within, "-s", serial, "shell", "getprop", "sys.boot_completed")

	return err == nil && out.Code == 0 && strings.TrimSpace(out.Stdout) == "1"
}

// adb runs the SDK's adb, ended after within.
func (m *Manager) adb(ctx context.Context, within time.Duration, args ...string) (Output, error) {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()

	return m.opts.System.Run(ctx, Command{Path: m.path(adbPath), Args: args, Env: os.Environ()})
}

// Stop ends the phone: a start under way is called off, then the emulator
// is asked to end and Xvfb after it. A start asked after it wins.
func (m *Manager) Stop(ctx context.Context) { m.stop(ctx, m.ask(false)) }

// stop carries out the stop asked as n, unless a later ask replaced it.
func (m *Manager) stop(ctx context.Context, n uint64) {
	m.op.Lock()
	defer m.op.Unlock()

	m.mu.Lock()
	replaced := m.asked != n
	m.mu.Unlock()

	if replaced {
		return
	}

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
		// An adb that hangs is given up after EndWithin, and the emulator then has its own EndWithin to end.
		_, _ = m.adb(ctx, m.opts.EndWithin, "-s", serial, "emu", "kill")

		m.end(ctx, run.Emulator, emuNeedle, false)
		m.opts.Logger.InfoContext(ctx, "the phone stopped", slog.String("event", "device.stopped"))
	}

	if run.Xvfb != 0 && sys.Alive(run.Xvfb, xvfbNeedle) {
		m.end(ctx, run.Xvfb, xvfbNeedle, true)
	}

	// A run.json left behind names programs that ended; the next node finds them gone and ends nothing.
	if err := m.keep(func(r *record) { *r = record{} }); err != nil {
		m.opts.Logger.WarnContext(ctx, "the phone's run.json was not removed",
			slog.String("event", "device.record_failed"), slog.String("error", err.Error()))
	}
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
	// An agent's call that is running is left to end: its phone is not stopped under it. The lock order is the
	// call's: agents.mu, then op.
	if !m.agents.mu.TryLock() {
		return
	}
	defer m.agents.mu.Unlock()

	if !m.op.TryLock() {
		return
	}

	// A view open is a use: the phone is not stopped under the owner's eyes.
	watched := m.live.watched()

	m.mu.Lock()
	if watched && m.state == nodeproto.DeviceRunning {
		m.lastUsed = m.opts.Clock.Now()
	}

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
		// Stopped under op rather than asked: a start asked meanwhile waits for op and boots the phone again.
		m.opts.Logger.InfoContext(ctx, "the idle phone is stopped",
			slog.String("event", "device.idle"), slog.Duration("idle", idle))
		m.halt(ctx)
		m.set(m.restingState(), "")
		m.op.Unlock()
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
	// run.json is read under op: a start cannot write a newer one between the read and its use.
	m.op.Lock()
	defer m.op.Unlock()

	raw, err := os.ReadFile(m.path(runDir, runFile))
	if err != nil {
		return
	}

	var run record
	if json.Unmarshal(raw, &run) != nil {
		return
	}

	m.mu.Lock()
	m.run = run
	m.mu.Unlock()

	sys := m.opts.System
	if sys != nil && run.Emulator != 0 && sys.Alive(run.Emulator, emuNeedle) && m.booted(ctx, m.opts.EndWithin) {
		m.mu.Lock()
		m.lastUsed = m.opts.Clock.Now()
		m.boots++
		m.mu.Unlock()

		m.set(nodeproto.DeviceRunning, "")
		m.opts.Logger.InfoContext(ctx, "the phone an earlier node started is taken back",
			slog.String("event", "device.adopted"))

		return
	}

	m.halt(ctx)
	m.set(m.restingState(), "")
}

// keep changes what the node knows of its programs and writes it to
// run.json; an empty record removes the file. The change is kept in memory
// even when the file could not be written, so that halt still ends what was
// started.
func (m *Manager) keep(change func(r *record)) error {
	m.mu.Lock()
	change(&m.run)
	run := m.run
	m.mu.Unlock()

	path := m.path(runDir, runFile)
	if run == (record{}) {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing the phone's %s: %w", runFile, err)
		}

		return nil
	}

	raw, err := json.Marshal(run)
	if err == nil {
		err = os.WriteFile(path+".tmp", raw, 0o600)
	}

	if err == nil {
		err = os.Rename(path+".tmp", path)
	}

	if err != nil {
		return fmt.Errorf("recording the phone's programs in %s: %w", runFile, err)
	}

	return nil
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
