package device

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// fakeSystem stands in for the machine: it records what the device runs and
// starts, and answers as adb, Xvfb and the emulator do for the commands the
// device gives them.
type fakeSystem struct {
	mu      sync.Mutex
	pid     int
	procs   map[int]*fakeProc
	runs    []string
	starts  []Command
	signals []string
	// busy are displays another X server holds.
	busy map[int]bool
	// notBooted keeps Android from saying it booted.
	notBooted bool
	// deaf are the programs that do not end when asked, by name: "emulator" or "Xvfb".
	deaf map[string]bool
	// crash makes the emulator end as it starts, after writing its reason.
	crash string
}

type fakeProc struct {
	cmd  Command
	done chan struct{}
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{pid: 1000, procs: map[int]*fakeProc{}, busy: map[int]bool{}, deaf: map[string]bool{}}
}

func program(c Command) string { return filepath.Base(c.Path) }

func (s *fakeSystem) Run(_ context.Context, c Command) (Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	line := program(c) + " " + strings.Join(c.Args, " ")
	s.runs = append(s.runs, line)

	switch {
	case slices.Contains(c.Args, "getprop"):
		if p := s.running("emulator"); p != nil && !s.notBooted {
			return Output{Stdout: "1\n"}, nil
		}

		return Output{Code: 1, Stderr: "error: device 'emulator-5554' not found"}, nil
	case slices.Contains(c.Args, "emu"):
		if p := s.running("emulator"); p != nil && !s.deaf["emulator"] {
			s.end(p)
		}
	}

	return Output{}, nil
}

func (s *fakeSystem) Start(c Command) (Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pid++
	p := &fakeProc{cmd: c, done: make(chan struct{})}
	s.procs[s.pid] = p
	s.starts = append(s.starts, c)

	if program(c) == "emulator" && s.crash != "" {
		_ = os.WriteFile(c.Log, []byte("INFO | starting\n"+s.crash+"\n"), 0o600)
		s.end(p)
	}

	return fakeProcess{pid: s.pid, done: p.done}, nil
}

type fakeProcess struct {
	pid  int
	done chan struct{}
}

func (p fakeProcess) Pid() int              { return p.pid }
func (p fakeProcess) Done() <-chan struct{} { return p.done }

// running is a live process of the program named; the caller holds mu.
func (s *fakeSystem) running(name string) *fakeProc {
	for _, p := range s.procs {
		if program(p.cmd) == name && !ended(p) {
			return p
		}
	}

	return nil
}

func ended(p *fakeProc) bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (s *fakeSystem) end(p *fakeProc) {
	if !ended(p) {
		close(p.done)
	}
}

func (s *fakeSystem) Signal(pgid int, sig Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.procs[pgid]
	if p == nil {
		return nil
	}

	name := map[Signal]string{Terminate: "term", Kill: "kill"}[sig]
	s.signals = append(s.signals, name+" "+program(p.cmd))

	if sig == Kill || !s.deaf[program(p.cmd)] {
		s.end(p)
	}

	return nil
}

func (s *fakeSystem) Alive(pgid int, needle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.procs[pgid]

	return p != nil && !ended(p) &&
		strings.Contains(filepath.ToSlash(p.cmd.Path)+" "+strings.Join(p.cmd.Args, " "), needle)
}

func (s *fakeSystem) DisplayFree(n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return !s.busy[n]
}

func (s *fakeSystem) DisplayReady(n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.running("Xvfb")

	return p != nil && slices.Contains(p.cmd.Args, ":"+strconv.Itoa(n))
}

func (s *fakeSystem) ranCommands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.runs)
}

func (s *fakeSystem) started() []Command {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.starts)
}

func (s *fakeSystem) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.signals)
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// rig is a Manager on a fake machine whose components are served by a test
// server.
type rig struct {
	m     *Manager
	sys   *fakeSystem
	clock *clock
	dir   string
	comps []component
	kvm   bool
}

func newRig(t *testing.T) *rig {
	t.Helper()

	r := &rig{sys: newFakeSystem(), clock: &clock{now: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)},
		dir: t.TempDir(), comps: testComponents(t, newFiles(t)), kvm: true}
	r.m = r.manager()

	return r
}

// manager is a Manager of the rig's directory and machine, as a node that
// starts again makes one.
func (r *rig) manager() *Manager {
	return newManager(Options{
		Dir: r.dir, System: r.sys, KVM: func() bool { return r.kvm }, Clock: r.clock,
		Poll: time.Millisecond, BootWithin: 200 * time.Millisecond, EndWithin: 50 * time.Millisecond,
	}, r.comps)
}

func (r *rig) start(t *testing.T) {
	t.Helper()

	require.NoError(t, r.m.Start(t.Context()))
	require.Equal(t, nodeproto.DeviceRunning, r.m.Status().State)
}

// A phone that was never started is absent; the first start installs what
// is missing, writes the AVD and boots it: Xvfb on the first free display,
// adb's server before the emulator, the emulator with the proven settings,
// and the animations off once Android says it booted.
func TestStartInstallsAndBootsThePhone(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.sys.busy[99] = true

	assert.Equal(t, nodeproto.DeviceAbsent, r.m.Status().State)

	r.start(t)

	st := r.m.Status()
	assert.Empty(t, st.Reason)
	assert.Equal(t, []nodeproto.DeviceComponent{
		{Name: namePlatformTools, Version: "1.0"}, {Name: nameAndroidCLI, Version: "2.0"},
	}, st.Installed)
	assert.FileExists(t, filepath.Join(r.dir, "avd", "phone.avd", "config.ini"))

	starts := r.sys.started()
	require.Len(t, starts, 2)
	assert.Equal(t, "Xvfb", starts[0].Path)
	assert.Equal(t, []string{":100", "-screen", "0", "1280x1024x24", "+extension", "GLX"}, starts[0].Args,
		"display 99 is held by another X server")

	emulator := starts[1]
	assert.Equal(t, filepath.Join(r.dir, "sdk", "emulator", "emulator"), emulator.Path)
	assert.Equal(t, []string{
		"-avd", "phone", "-no-window", "-no-audio", "-no-boot-anim", "-gpu", "host",
		"-memory", "4096", "-cores", "6", "-port", "5554",
	}, emulator.Args, "Quick Boot: no -wipe-data, -no-snapshot-save or -read-only")
	assert.Subset(t, emulator.Env, []string{
		"DISPLAY=:100", "ANDROID_SDK_ROOT=" + filepath.Join(r.dir, "sdk"), "ANDROID_AVD_HOME=" + filepath.Join(r.dir, "avd"),
	})

	for _, e := range emulator.Env {
		assert.NotContains(t, e, "ANDROID_USER_HOME=", "adb's keys stay in the node user's own ~/.android")
	}

	runs := r.sys.ranCommands()
	require.NotEmpty(t, runs)
	assert.Equal(t, "adb start-server", runs[0], "adb's server starts before the emulator")
	assert.Equal(t, []string{
		"adb -s emulator-5554 shell settings put global window_animation_scale 0",
		"adb -s emulator-5554 shell settings put global transition_animation_scale 0",
		"adb -s emulator-5554 shell settings put global animator_duration_scale 0",
	}, runs[len(runs)-3:])

	select {
	case <-r.m.Changed():
	default:
		t.Fatal("a change of state is told")
	}

	// A start of a phone that runs starts nothing more.
	require.NoError(t, r.m.Start(t.Context()))
	assert.Len(t, r.sys.started(), 2)
}

// A start that fails says why, and ends what it had started.
func TestAFailedStartSaysWhy(t *testing.T) {
	t.Parallel()

	t.Run("no kvm", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		r.kvm = false

		require.Error(t, r.m.Start(t.Context()))

		st := r.m.Status()
		assert.Equal(t, nodeproto.DeviceFailed, st.State)
		assert.Contains(t, st.Reason, "/dev/kvm")
		assert.Empty(t, r.sys.started(), "nothing is started on a machine without KVM")
	})

	t.Run("the emulator ends while booting", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		r.sys.crash = "FATAL | Cannot find AVD system path"

		require.Error(t, r.m.Start(t.Context()))

		st := r.m.Status()
		assert.Equal(t, nodeproto.DeviceFailed, st.State)
		assert.Contains(t, st.Reason, "Cannot find AVD system path")
		assert.Contains(t, r.sys.sent(), "term Xvfb", "the display is ended with the start")
		assert.NoFileExists(t, filepath.Join(r.dir, "run", "run.json"))
	})

	t.Run("Android does not boot in time", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		r.sys.notBooted = true

		require.Error(t, r.m.Start(t.Context()))
		assert.Contains(t, r.m.Status().Reason, "did not boot within")
		assert.Contains(t, r.sys.ranCommands(), "adb -s emulator-5554 emu kill")

		// The next start tries again.
		r.sys.notBooted = false
		r.start(t)
	})
}

// A stop asks the emulator to end, so it saves its Quick Boot state, and
// ends Xvfb after it; a program that does not end in time is killed.
func TestStopEndsTheEmulatorThenTheDisplay(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.start(t)

	r.m.Stop(t.Context())

	assert.Equal(t, nodeproto.DeviceStopped, r.m.Status().State)
	assert.Contains(t, r.sys.ranCommands(), "adb -s emulator-5554 emu kill")
	assert.Equal(t, []string{"term Xvfb"}, r.sys.sent(), "the emulator ended when asked and was not signaled")
	assert.NoFileExists(t, filepath.Join(r.dir, "run", "run.json"))

	r.sys.deaf["emulator"], r.sys.deaf["Xvfb"] = true, true
	r.start(t)

	r.m.Stop(t.Context())

	assert.Equal(t, nodeproto.DeviceStopped, r.m.Status().State)
	assert.Equal(t, []string{"term Xvfb", "kill emulator", "term Xvfb", "kill Xvfb"}, r.sys.sent())
}

// The phone is stopped once nothing has used it for IdleAfter; a use puts
// that off.
func TestAnIdlePhoneIsStopped(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.start(t)

	r.clock.advance(IdleAfter - time.Minute)
	r.m.Sweep(t.Context())
	assert.Equal(t, nodeproto.DeviceRunning, r.m.Status().State)

	r.m.Touch()
	r.clock.advance(IdleAfter - time.Minute)
	r.m.Sweep(t.Context())
	assert.Equal(t, nodeproto.DeviceRunning, r.m.Status().State, "the touch put the stop off")

	r.clock.advance(time.Minute)
	r.m.Sweep(t.Context())
	assert.Equal(t, nodeproto.DeviceStopped, r.m.Status().State)
	assert.Contains(t, r.sys.ranCommands(), "adb -s emulator-5554 emu kill")
}

// An emulator that ended by itself is a failed phone, and its display is
// ended with it.
func TestAnEmulatorThatEndedIsFailed(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.start(t)

	r.sys.mu.Lock()
	r.sys.end(r.sys.running("emulator"))
	r.sys.mu.Unlock()

	r.m.Sweep(t.Context())

	st := r.m.Status()
	assert.Equal(t, nodeproto.DeviceFailed, st.State)
	assert.Contains(t, st.Reason, "ended by itself")
	assert.Contains(t, r.sys.sent(), "term Xvfb")
}

// A node that starts again takes back the phone the one before it left
// running, and can stop it; what run.json names that is not a booted phone
// is ended.
func TestANodeTakesBackThePhoneItLeft(t *testing.T) {
	t.Parallel()

	t.Run("running", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		r.start(t)

		again := r.manager()
		assert.Equal(t, nodeproto.DeviceStopped, again.Status().State)

		again.adopt(t.Context())
		assert.Equal(t, nodeproto.DeviceRunning, again.Status().State)
		assert.Len(t, r.sys.started(), 2, "nothing is started again")

		again.Stop(t.Context())
		assert.Equal(t, nodeproto.DeviceStopped, again.Status().State)
		assert.Contains(t, r.sys.ranCommands(), "adb -s emulator-5554 emu kill")
		assert.Contains(t, r.sys.sent(), "term Xvfb")
	})

	t.Run("not booted", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		r.start(t)

		r.sys.notBooted = true

		again := r.manager()
		again.adopt(t.Context())

		assert.Equal(t, nodeproto.DeviceStopped, again.Status().State)
		assert.Contains(t, r.sys.sent(), "term Xvfb", "a leftover display is ended")
		assert.NoFileExists(t, filepath.Join(r.dir, "run", "run.json"))
	})
}

// A stop asked while the phone boots calls the start off and ends what it
// started.
func TestAStopCallsAStartOff(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.sys.notBooted = true
	r.m.opts.BootWithin = time.Minute

	done := make(chan error, 1)

	go func() { done <- r.m.Start(context.WithoutCancel(t.Context())) }()

	var last nodeproto.DeviceState

	require.Eventually(t, func() bool {
		last = r.m.Status()

		return last.State == nodeproto.DeviceStarting
	}, 5*time.Second, time.Millisecond, "the phone never booted: %+v", &last)

	r.m.Stop(t.Context())

	require.ErrorIs(t, <-done, context.Canceled)
	assert.Equal(t, nodeproto.DeviceStopped, r.m.Status().State)
	assert.Contains(t, r.sys.sent(), "term Xvfb")
}
