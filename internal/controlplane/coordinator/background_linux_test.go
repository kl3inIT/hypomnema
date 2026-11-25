//go:build linux

package coordinator

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The background launcher (deploy/coordinator/hypomnema-background, issue #1084), run for real against this user's
// systemd user manager: the launcher is the file that is installed, with the lines that say where things are pointed
// at a directory of the test's own and its units named with a prefix of this run's, so no other unit of the machine
// is listed or let go of. The box is bubblewrap's where it can make one, else a stand-in that only enters the
// directory; the box itself is hypomnema-routine's, which box_linux_test.go and the routines' test check. A machine
// with no user manager skips.

const backgroundLauncherFile = "../../../deploy/coordinator/hypomnema-background"

// bwrapStandIn runs the command after `--` in the directory --chdir names, skipping the box's other arguments.
const bwrapStandIn = `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    --chdir) cd "$2"; shift 2 ;;
    --) shift; break ;;
    --unshare-*) shift ;;
    --proc | --dev | --tmpfs) shift 2 ;;
    *) shift 3 ;;
  esac
done
exec "$@"
`

type launcherWorld struct {
	launcher, prefix, dir, home string
}

func newLauncherWorld(t *testing.T) *launcherWorld {
	t.Helper()

	runtime := "/run/user/" + strconv.Itoa(os.Getuid())
	if _, err := os.Stat(filepath.Join(runtime, "bus")); err != nil {
		t.Skipf("no systemd user manager for this user (%s/bus): the launcher's units need one", runtime)
	}

	if out, err := exec.CommandContext(t.Context(), "systemctl", "--user", "show", "--property=Version").
		CombinedOutput(); err != nil {
		t.Skipf("the systemd user manager does not answer: %v: %s", err, out)
	}

	root := t.TempDir()
	userHome := filepath.Join(root, "home", "coordinator")
	home := filepath.Join(userHome, ".hypomnema-coordinator")
	dir := filepath.Join(home, "bots", orchestration.DefaultBotID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "node"), 0o700))

	bwrap := bwrapPath
	if exec.CommandContext(t.Context(), bwrapPath, "--unshare-user", "--unshare-pid", "--ro-bind", "/usr", "/usr",
		"--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64",
		"--proc", "/proc", "--dev", "/dev", "/usr/bin/true").Run() != nil {
		bwrap = filepath.Join(root, "bwrap")
		require.NoError(t, os.WriteFile(bwrap, []byte(bwrapStandIn), 0o700)) //nolint:gosec // the test's stand-in
	}

	b := make([]byte, 4)
	_, _ = rand.Read(b)
	prefix := "hyptest-bg-" + hex.EncodeToString(b)

	raw, err := os.ReadFile(backgroundLauncherFile)
	require.NoError(t, err)

	text := string(raw)
	for from, to := range map[string]string{
		"user_home=/home/coordinator": "user_home=" + userHome,
		"node=/opt/hypomnema-node":    "node=" + filepath.Join(root, "node"),
		"bwrap=/usr/bin/bwrap":        "bwrap=" + bwrap,
		"prefix=hypomnema-bg":         "prefix=" + prefix,
	} {
		require.Contains(t, text, "\n"+from+"\n")
		text = strings.Replace(text, "\n"+from+"\n", "\n"+to+"\n", 1)
	}

	launcher := filepath.Join(root, "hypomnema-background")
	require.NoError(t, os.WriteFile(launcher, []byte(text), 0o700)) //nolint:gosec // the launcher under test

	t.Cleanup(func() {
		// The units of this run, whatever the test left.
		for _, action := range []string{"stop", "reset-failed"} {
			//nolint:noctx,gosec // cleanup after the test's context ended, of the test's own units
			_ = exec.Command("systemctl", "--user", action, prefix+"-*").Run()
		}
	})

	return &launcherWorld{launcher: launcher, prefix: prefix, dir: dir, home: home}
}

func (w *launcherWorld) active(t *testing.T, chatID, id string) string {
	t.Helper()

	out, _ := exec.CommandContext(t.Context(), "systemctl", "--user", "show", //nolint:gosec // the test's own unit
		"--property=ActiveState", "--value", w.prefix+"-"+chatID+"-"+id+".service").Output()

	return strings.TrimSpace(string(out))
}

// Scenario (issue #1084, the acceptance): a background command started through the launcher before a restart of the
// control plane still runs after it, in its unit; the supervisor of the next start finds it and wakes the chat when
// it ends, with its exit code and output, and lets go of its unit and file.
func TestALaunchedBackgroundCommandRunsOnAfterARestartAndWakesTheChat(t *testing.T) {
	t.Parallel()

	w := newLauncherWorld(t)
	svc, _ := newStore(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	before := New(svc, Config{Background: []string{w.launcher}})
	gate := filepath.Join(w.dir, "go-on")
	started, err := before.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "pi",
		agenttool.BackgroundRequest{Command: "echo scanning in $(pwd)\nwhile [ ! -e go-on ]; do sleep 0.1; done\n" +
			"echo done\nexit 3", Cwd: w.dir, TimeoutSeconds: 120})
	require.NoError(t, err)
	assert.Equal(t, "active", w.active(t, chat.ID, started.ID))

	// The control plane restarts: the supervisor that started it is gone, and a new one starts on the same store.
	after := New(svc, Config{Background: []string{w.launcher}})
	observe(t, after)
	assert.Empty(t, wakes(t, svc, chat.ID), "the command runs on")
	assert.Equal(t, "active", w.active(t, chat.ID, started.ID))

	require.NoError(t, os.WriteFile(gate, nil, 0o600))
	require.Eventually(t, func() bool {
		observe(t, after)

		return len(wakes(t, svc, chat.ID)) > 0
	}, 30*time.Second, 200*time.Millisecond, "its end wakes the chat")

	got := wakes(t, svc, chat.ID)
	require.Len(t, got, 1)
	assert.Regexp(t, `^Background command `+started.ID+` exited with code 3 after \d+s:\nscanning in `+
		w.dir+`\ndone$`, got[0])
	assert.Equal(t, "inactive", w.active(t, chat.ID, started.ID), "its unit was let go of")
	assert.NoFileExists(t, started.OutputFile)
}

// Scenario (issue #1084): a command its chat stops is ended with its whole unit, and the wake says so.
func TestALaunchedBackgroundCommandIsStopped(t *testing.T) {
	t.Parallel()

	w := newLauncherWorld(t)
	svc, _ := newStore(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	sup := New(svc, Config{Background: []string{w.launcher}})
	started, err := sup.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "pi",
		agenttool.BackgroundRequest{Command: "echo watching; sleep 300 & sleep 300", Cwd: w.dir})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(started.OutputFile)

		return err == nil && string(raw) == "watching\n"
	}, 10*time.Second, 50*time.Millisecond)

	stopped, err := sup.StopBackground(ctx, chat.ID, started.ID)
	require.NoError(t, err)
	assert.True(t, stopped)
	assert.NotEqual(t, "active", w.active(t, chat.ID, started.ID))

	got := wakes(t, svc, chat.ID)
	require.Len(t, got, 1)
	assert.Regexp(t, `^Background command `+started.ID+` was stopped, as this chat asked, after \d+s:\nwatching$`,
		got[0])
}

// Scenario (issue #1084): the launcher refuses a directory outside the Bot's and starts nothing.
func TestTheLauncherStartsOnlyInTheBotsDirectory(t *testing.T) {
	t.Parallel()

	w := newLauncherWorld(t)
	svc, _ := newStore(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	sup := New(svc, Config{Background: []string{w.launcher}})
	_, err = sup.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "pi",
		agenttool.BackgroundRequest{Command: "true", Cwd: "/etc"})
	require.ErrorIs(t, err, orchestration.ErrUnavailable)
	require.ErrorContains(t, err, "starts in the Bot's directory")

	rows, err := svc.BackgroundCommands(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// Scenario (issue #1084, review round 1): a chat deleted while its last command runs does not leave the command
// running: the next pass lets go of its unit.
func TestADeletedChatsLaunchedCommandIsStopped(t *testing.T) {
	t.Parallel()

	w := newLauncherWorld(t)
	svc, _ := newStore(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	sup := New(svc, Config{Background: []string{w.launcher}})
	started, err := sup.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "pi",
		agenttool.BackgroundRequest{Command: "sleep 300", Cwd: w.dir})
	require.NoError(t, err)

	observe(t, sup)
	assert.Equal(t, "active", w.active(t, chat.ID, started.ID))

	_, err = svc.DeleteChat(ctx, chat.ID)
	require.NoError(t, err)

	observe(t, sup)
	assert.Equal(t, "inactive", w.active(t, chat.ID, started.ID), "its unit was stopped and let go of")
}

// Scenario (issue #1084, review round 1): the file of a command that ended more than 3 hours ago, which its wake
// named, is pruned by the supervisor's pass even when its chat runs no other command; a newer one stays.
func TestAnEndedCommandsFileIsPrunedAfterThreeHours(t *testing.T) {
	t.Parallel()

	w := newLauncherWorld(t)
	svc, _ := newStore(t)

	chat, err := svc.CreateChat(t.Context(), "", "")
	require.NoError(t, err)

	folder := filepath.Join(w.home, "run", chat.ID, "background")
	require.NoError(t, os.MkdirAll(folder, 0o700))

	old, recent := filepath.Join(folder, "bg_aaaaaa.log"), filepath.Join(folder, "bg_bbbbbb.log")
	for _, f := range []string{old, recent} {
		require.NoError(t, os.WriteFile(f, []byte("output\n"), 0o600))
	}

	fourHoursAgo := time.Now().Add(-4 * time.Hour)
	require.NoError(t, os.Chtimes(old, fourHoursAgo, fourHoursAgo))

	observe(t, New(svc, Config{Background: []string{w.launcher}}))

	assert.NoFileExists(t, old)
	assert.FileExists(t, recent)
}

// Scenario (issue #1084, review round 1): the command runs in the shell pi names, given it with -c as pi's bash
// gives it.
func TestALaunchedCommandRunsInPisShell(t *testing.T) {
	t.Parallel()

	w := newLauncherWorld(t)
	svc, _ := newStore(t)
	ctx := t.Context()

	chat, err := svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	sup := New(svc, Config{Background: []string{w.launcher}})
	started, err := sup.StartBackground(ctx, chat.ID, orchestration.DefaultBotID, "pi",
		agenttool.BackgroundRequest{Command: `echo "shell: $0"`, Cwd: w.dir, Shell: "/bin/sh"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		observe(t, sup)

		return len(wakes(t, svc, chat.ID)) > 0
	}, 30*time.Second, 200*time.Millisecond)
	assert.Regexp(t, `^Background command `+started.ID+` exited with code 0 after \d+s:\nshell: /bin/sh$`,
		wakes(t, svc, chat.ID)[0])
}
