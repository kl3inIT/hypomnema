package coordinator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// fakeUnits plays the launcher (deploy/coordinator/hypomnema-background) over systemd units held in memory: what
// `list` prints for each, the output each wrote, and the calls it was given.
type fakeUnits struct {
	mu     sync.Mutex
	units  map[unitKey]string
	output map[unitKey]string
	calls  []string
	// fail makes a call of that action fail.
	fail string
}

func newFakeUnits() *fakeUnits {
	return &fakeUnits{units: map[unitKey]string{}, output: map[unitKey]string{}}
}

// set gives the unit of chat's command id the properties systemctl show would print.
func (f *fakeUnits) set(chat, id, props string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.units[unitKey{chat, id}] = props
}

func (f *fakeUnits) run(_ context.Context, args []string, stdin string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == f.fail {
		return nil, fmt.Errorf("%w: the background launcher's %s failed: exit status 1", orchestration.ErrUnavailable,
			args[0])
	}

	switch args[0] {
	case "start":
		k := unitKey{args[1], args[3]}
		f.units[k] = "ActiveState=active\nSubState=running\nResult=success\nExecMainCode=0\nExecMainStatus=0"
		f.output[k] = ""

		return []byte("/run/" + k.chat + "/background/" + k.id + ".log\n"), nil
	case "list":
		var blocks []string

		for k, props := range f.units {
			blocks = append(blocks, "Id=hypomnema-bg-"+k.chat+"-"+k.id+".service\n"+props)
		}

		return []byte(strings.Join(blocks, "\n\n") + "\n"), nil
	case "read":
		k := unitKey{args[1], args[2]}
		out := f.output[k]

		return fmt.Appendf(nil, "/run/%s/background/%s.log\n%d\n%s", k.chat, k.id, len(out), out), nil
	case "stop", "cleanup":
		delete(f.units, unitKey{args[1], args[2]})
	}

	return nil, nil
}

func (f *fakeUnits) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string{}, f.calls...)
}

// backgroundWorld is a supervisor, not running, whose launcher is fake, on a store of its own.
func backgroundWorld(t *testing.T) (*orchestration.Service, *Supervisor, *fakeUnits) {
	t.Helper()

	svc, _ := newStore(t)
	sup := New(svc, Config{})
	units := newFakeUnits()
	sup.bg.run = units.run

	return svc, sup, units
}

// observe runs one observation now, whenever the last one was.
func observe(t *testing.T, sup *Supervisor) {
	t.Helper()

	sup.bg.last = time.Time{}
	sup.observeBackground(t.Context())
}

func wakes(t *testing.T, svc *orchestration.Service, chatID string) []string {
	t.Helper()

	msgs, err := svc.Undelivered(t.Context(), chatID)
	require.NoError(t, err)

	var out []string

	for _, m := range msgs {
		if m.Kind == orchestration.ChatWake {
			out = append(out, m.Text)
		}
	}

	return out
}

// Scenario (issue #1084): a command started before a restart of the control plane runs on in its unit; the
// supervisor of the next start finds it by its unit's name and wakes the chat once, when it ends, with how it
// ended, how long it ran by its unit's clock and its output, then lets go of the unit and of its file.
func TestABackgroundCommandOutlivesARestartAndItsEndWakesTheChat(t *testing.T) {
	t.Parallel()

	svc, before, units := backgroundWorld(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	started, err := before.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "pi-hash",
		agenttool.BackgroundRequest{Kind: agenttool.BackgroundStart, Command: "make all", Cwd: "/bots/main"})
	require.NoError(t, err)
	assert.Regexp(t, `^bg_[0-9a-f]{12}$`, started.ID)
	assert.Equal(t, "/run/"+chat.ID+"/background/"+started.ID+".log", started.OutputFile)
	assert.Equal(t, DefaultBackgroundTimeout, started.TimeoutSeconds)
	assert.Equal(t, []string{"start " + chat.ID + " " + orchestration.DefaultBotID + " " + started.ID +
		" 1800 /bots/main"}, units.called())

	// The control plane restarts: a new supervisor on the same store, the unit still running.
	after := New(svc, Config{})
	after.bg.run = units.run

	observe(t, after)
	assert.Empty(t, wakes(t, svc, chat.ID), "a command that runs wakes nothing")

	units.set(chat.ID, started.ID, "ActiveState=failed\nSubState=failed\nResult=exit-code\nExecMainCode=1\n"+
		"ExecMainStatus=2\nExecMainStartTimestampMonotonic=1000000\nExecMainExitTimestampMonotonic=253000000")
	units.output[unitKey{chat.ID, started.ID}] = "fetched 29530 places\nerror: disk full\n"

	observe(t, after)
	observe(t, after)
	assert.Equal(t, []string{"Background command " + started.ID + " exited with code 2 after 4m12s:\nfetched 29530 " +
		"places\nerror: disk full"}, wakes(t, svc, chat.ID), "its end wakes the chat once")
	assert.Contains(t, units.called(), "cleanup "+chat.ID+" "+started.ID+" drop",
		"a file whose output the wake has whole is not kept")

	rows, err := svc.BackgroundCommands(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// Scenario (issue #1084): a command whose output is longer than the wake carries keeps its file, which the wake
// names; one a signal ended at its deadline says so.
func TestALongBackgroundOutputKeepsItsFile(t *testing.T) {
	t.Parallel()

	svc, sup, units := backgroundWorld(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	started, err := sup.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "pi",
		agenttool.BackgroundRequest{Command: "fetch", Cwd: "/bots/main", TimeoutSeconds: 60})
	require.NoError(t, err)

	k := unitKey{chat.ID, started.ID}
	units.set(chat.ID, started.ID, "ActiveState=failed\nSubState=failed\nResult=timeout\nExecMainCode=2\n"+
		"ExecMainStatus=15")
	units.output[k] = strings.Repeat("é", orchestration.BackgroundTail+10)

	observe(t, sup)

	got := wakes(t, svc, chat.ID)
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "was killed (SIGTERM) when its timeout ran out")
	assert.Contains(t, got[0], "the end of its 4020 bytes of output:\n"+strings.Repeat("é", orchestration.BackgroundTail))
	assert.Contains(t, got[0], "\nFull output: /run/"+chat.ID+"/background/"+started.ID+".log")
	assert.Contains(t, units.called(), "cleanup "+chat.ID+" "+started.ID+" keep")
}

// Scenario (issue #1084): a command whose unit is gone, as after the machine restarted, is reported lost with its
// output's file; a unit no row names, as its chat was deleted, is let go of.
func TestABackgroundCommandWhoseUnitIsGoneIsLostAndAStrayUnitLetGo(t *testing.T) {
	t.Parallel()

	svc, sup, units := backgroundWorld(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	require.NoError(t, svc.StartBackground(ctx, chat.ID, "an old pi", "bg_aaaaaa"))
	units.output[unitKey{chat.ID, "bg_aaaaaa"}] = "half done\n"
	units.set("cht_deleted", "bg_bbbbbb", "ActiveState=active\nSubState=running")

	observe(t, sup)

	got := wakes(t, svc, chat.ID)
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "Background command bg_aaaaaa (started ")
	assert.Contains(t, got[0], "ended with no result: its unit is no longer there")
	assert.Contains(t, got[0], "Its output: /run/"+chat.ID+"/background/bg_aaaaaa.log")
	assert.Contains(t, units.called(), "cleanup "+chat.ID+" bg_aaaaaa keep")
	assert.Contains(t, units.called(), "cleanup cht_deleted bg_bbbbbb keep")
}

// Scenario (issues #1058, #1084): a control plane without a launcher reports lost, once after its start, the commands
// an earlier release ran in pi's box, which the restart ended.
func TestWithoutALauncherTheCommandsLeftAreReportedLostOnce(t *testing.T) {
	t.Parallel()

	svc, _ := newStore(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)
	require.NoError(t, svc.StartBackground(ctx, chat.ID, "an old pi", "bg_dddddd"))

	sup := New(svc, Config{})
	observe(t, sup)
	observe(t, sup)

	got := wakes(t, svc, chat.ID)
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "Background command bg_dddddd (started ")
	assert.Contains(t, got[0], "ended with no result: the control plane restarted, which stopped it")
}

// Scenario (issue #1084): with nothing running the units are listed once, after the start, and not again.
func TestNoBackgroundCommandListsTheUnitsOnceAfterTheStart(t *testing.T) {
	t.Parallel()

	_, sup, units := backgroundWorld(t)

	observe(t, sup)
	observe(t, sup)
	observe(t, sup)

	assert.Equal(t, []string{"list"}, units.called())
}

// Scenario (issue #1084): a command that cannot start leaves no row and says why; a request out of bounds is refused
// before anything runs; a control plane without a launcher refuses, naming its setting.
func TestABackgroundCommandThatCannotStartLeavesNoRow(t *testing.T) {
	t.Parallel()

	svc, sup, units := backgroundWorld(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	units.fail = "start"
	_, err = sup.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "pi",
		agenttool.BackgroundRequest{Command: "make", Cwd: "/bots/main"})
	require.ErrorIs(t, err, orchestration.ErrUnavailable)

	rows, err := svc.BackgroundCommands(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)

	for name, in := range map[string]agenttool.BackgroundRequest{
		"no command":     {Command: " ", Cwd: "/w"},
		"a relative cwd": {Command: "make", Cwd: "w"},
		"a long timeout": {Command: "make", Cwd: "/w", TimeoutSeconds: maxBackgroundTimeout + 1},
	} {
		_, err := sup.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "pi", in)
		require.ErrorIs(t, err, orchestration.ErrInvalid, name)
	}

	assert.Len(t, units.called(), 1)

	none := New(svc, Config{})
	_, err = none.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "pi",
		agenttool.BackgroundRequest{Command: "make", Cwd: "/w"})
	require.ErrorIs(t, err, orchestration.ErrUnavailable)
	require.ErrorContains(t, err, "HYPOMNEMA_BACKGROUND_COMMAND is not set")
}

// Scenario (issue #1084): any pi of the chat stops one of its commands; the chat is woken saying it was stopped,
// with what it wrote. A command that is not running is not stopped.
func TestAChatStopsItsBackgroundCommand(t *testing.T) {
	t.Parallel()

	svc, sup, units := backgroundWorld(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	started, err := sup.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "first pi",
		agenttool.BackgroundRequest{Command: "watch.sh", Cwd: "/bots/main", TimeoutSeconds: 86400})
	require.NoError(t, err)

	units.output[unitKey{chat.ID, started.ID}] = "watching\n"

	stopped, err := sup.StopBackground(ctx, chat.ID, started.ID)
	require.NoError(t, err)
	assert.True(t, stopped)
	assert.Contains(t, units.called(), "stop "+chat.ID+" "+started.ID)

	got := wakes(t, svc, chat.ID)
	require.Len(t, got, 1)
	assert.Regexp(t, `^Background command bg_[0-9a-f]{12} was stopped, as this chat asked, after \d+s:\nwatching$`,
		got[0])

	stopped, err = sup.StopBackground(ctx, chat.ID, started.ID)
	require.NoError(t, err)
	assert.False(t, stopped, "a command that ended is not stopped again")

	_, err = sup.StopBackground(ctx, chat.ID, "../etc")
	require.ErrorIs(t, err, orchestration.ErrInvalid)
}

// Scenario (issue #1084): a pi is stopped when idle even while its chat's command runs, as the command no longer
// lives in its box; the command goes on.
func TestAnIdlePiIsStoppedWhileItsBackgroundCommandRuns(t *testing.T) {
	t.Parallel()

	tokens := &fakeTokens{}
	svc, sup := newSupervisorConfig(t, Config{Tokens: tokens, IdleAfter: 50 * time.Millisecond})
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, err = svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "hello")
	require.NoError(t, err)
	waitFor(t, svc, chat.ID, orchestration.ChatCoordinator, "echo: hello")
	require.NoError(t, svc.StartBackground(ctx, chat.ID, "pi", "bg_cccccc"))

	require.Eventually(t, func() bool {
		_, err := svc.AddChatMessage(ctx, chat.ID, orchestration.ChatSystem, "a tool line")

		return err == nil && !sup.Idle(chat.ID)
	}, 10*time.Second, 100*time.Millisecond, "an idle pi is stopped")

	rows, err := svc.BackgroundCommands(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "its command is still the chat's")
}

func TestUnitsAndOutputAreRead(t *testing.T) {
	t.Parallel()

	units := parseUnits([]byte("Id=hypomnema-bg-cht_a1-bg_abcdef.service\nActiveState=active\nSubState=exited\n" +
		"Result=success\nExecMainCode=1\nExecMainStatus=0\n\nId=hypomnema-bg-nothing.service\nActiveState=active\n\n" +
		"Id=other.service\n"))
	require.Len(t, units, 1)

	u := units[unitKey{"cht_a1", "bg_abcdef"}]
	assert.False(t, u.running(), "a unit that exited 0 stays active, exited")
	assert.Equal(t, 0, *u.end("bg_abcdef").ExitCode)

	o := parseOutput([]byte("/run/f.log\n12\n\xa9 caf\xc3\xa9 ok\n"))
	assert.Equal(t, output{file: "/run/f.log", tail: " café ok\n", size: 12, cut: true}, o,
		"a read that began inside a character drops its rest")

	assert.Equal(t, output{file: "/run/g.log"}, parseOutput([]byte("/run/g.log\n0\n")))
	assert.Equal(t, "SIGKILL", signalName(9))
	assert.Equal(t, "SIG40", signalName(40))
	assert.ErrorIs(t, errNoBackground, orchestration.ErrUnavailable)
}
