//go:build linux

package routines_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The launcher of a Routine's script, run for real in its box (issue #211,
// after issue #260's test of pi's box, coordinator/box_linux_test.go): the
// launcher is the file that is installed, with the four lines that say where
// things are pointed at a directory of the test's own, and the script
// reports what it sees. Where HYPOMNEMA_BOX_TEST=required (CI, after
// setup-box.sh), a machine that cannot make a box fails the test instead of
// skipping it.

const (
	boxRequired     = "HYPOMNEMA_BOX_TEST"
	routineLauncher = "../../../deploy/coordinator/hypomnema-routine"
	bwrapPath       = "/usr/bin/bwrap"
	boxRoutine      = "rtn_sync"
)

// needBox skips, or fails where the box is required, when bubblewrap cannot
// make a box on this machine.
func needBox(t *testing.T) {
	t.Helper()

	why := ""

	if _, err := os.Stat(bwrapPath); err != nil {
		why = "bubblewrap is not installed at " + bwrapPath
	} else {
		out, err := exec.CommandContext(t.Context(), bwrapPath, "--unshare-user", "--unshare-pid",
			"--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib",
			"--symlink", "usr/lib64", "/lib64", "--proc", "/proc", "--dev", "/dev", "/usr/bin/true").CombinedOutput()
		if err != nil {
			why = "bubblewrap cannot make a box here (" + strings.TrimSpace(string(out)) + ")"
		}
	}

	if why == "" {
		return
	}

	if os.Getenv(boxRequired) == "required" {
		t.Fatalf("%s; deploy/coordinator/setup-box.sh sets it up", why)
	}

	t.Skipf("%s; deploy/coordinator/setup-box.sh sets it up (%s=required makes this a failure)", why, boxRequired)
}

// boxWorld is a coordinator's home laid out as the setup script and the
// launchers leave it, in a directory of the test's own.
type boxWorld struct {
	root, userHome, home, launcher string
	// outside is a file in the machine's /tmp, which the box must not see.
	outside string
}

func newBoxWorld(t *testing.T, bwrap, script string) *boxWorld {
	t.Helper()

	root := t.TempDir()
	w := &boxWorld{root: root, userHome: filepath.Join(root, "home", "coordinator")}
	w.home = filepath.Join(w.userHome, ".hypomnema-coordinator")

	for name, content := range map[string]string{
		"bots/other/notes":                   "another bot's notes",
		"sessions/cht_mine/2026-11-01.jsonl": "a chat's session",
		"run/cht_mine/prompt.md":             "a chat's prompt",
		"work/README":                        "the first bot's directory",
	} {
		boxWrite(t, filepath.Join(w.home, name), content, 0o600)
	}

	boxWrite(t, filepath.Join(w.home, "tools", "bin", "gws"), "#!/bin/sh\n", 0o700)

	boxWrite(t, filepath.Join(w.userHome, ".hypomnema", "client.json"),
		"{\n  \"url\": \"http://127.0.0.1:18990\",\n  \"token\": \"hyp_coordinator_v1_shared\"\n}\n", 0o600)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "node", "bin"), 0o700))

	// The script the control plane keeps, which its CLI writes into the run's folder with the run's key, as
	// `hypomnema routines script` does, and the limit the test's caller names.
	boxWrite(t, filepath.Join(root, "kept", "sync.sh"), script, 0o600)
	boxWrite(t, filepath.Join(root, "bin", "hypomnema"), `#!/bin/sh
[ "$1 $2 $4 $6" = "routines script --bot --into" ] || exit 9
dir=$7
mkdir -p "$dir"
cp '`+filepath.Join(root, "kept", "sync.sh")+`' "$dir/sync.sh"
chmod 700 "$dir/sync.sh"
echo "key-of-$3-for-$5" > "$dir/.run-$3"
echo sync.sh
echo "${ROUTINE_LIMIT:-60}"
`, 0o700)

	outside, err := os.CreateTemp("/tmp", "hypomnema-box-outside-")
	require.NoError(t, err)
	require.NoError(t, outside.Close())
	t.Cleanup(func() { _ = os.Remove(outside.Name()) })
	w.outside = outside.Name()

	text, err := os.ReadFile(routineLauncher)
	require.NoError(t, err)

	launcher := string(text)
	for from, to := range map[string]string{
		"user_home=/home/coordinator":        "user_home=" + w.userHome,
		"hypomnema=/usr/local/bin/hypomnema": "hypomnema=" + filepath.Join(root, "bin", "hypomnema"),
		"node=/opt/hypomnema-node":           "node=" + filepath.Join(root, "node"),
		"bwrap=/usr/bin/bwrap":               "bwrap=" + bwrap,
	} {
		require.Equal(t, 1, strings.Count(launcher, "\n"+from+"\n"), "the launcher says %s once", from)
		launcher = strings.Replace(launcher, "\n"+from+"\n", "\n"+to+"\n", 1)
	}

	w.launcher = filepath.Join(root, "hypomnema-routine")
	boxWrite(t, w.launcher, launcher, 0o700)

	return w
}

func boxWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), mode)) //nolint:gosec // A path of the test's own.
}

// command is the launcher for routine rtn_sync of the first Bot, with the
// caller's environment and a variable the script must not see.
func (w *boxWorld) command(t *testing.T, env ...string) *exec.Cmd {
	t.Helper()

	//nolint:gosec // The test's own copy of the launcher.
	cmd := exec.CommandContext(t.Context(), "sh", w.launcher, "coordinator", boxRoutine)
	cmd.Env = append(append(os.Environ(), "HYPOMNEMA_SECRET_OF_THE_CALLER=x"), env...)

	return cmd
}

// held starts cmd with stdin written to its input, which is then held open
// as the control plane holds it while a run goes on (its end stops the run),
// and returns how cmd ended.
func held(t *testing.T, cmd *exec.Cmd, stdin string) error {
	t.Helper()

	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	// A launcher that refuses ends before it reads all of it.
	_, _ = io.WriteString(in, stdin)

	return cmd.Wait()
}

// run runs the launcher to its end with stdin, and returns what the script
// printed, a line each, and how it ended.
func (w *boxWorld) run(t *testing.T, stdin string) (map[string]string, string, error) {
	t.Helper()

	cmd := w.command(t)

	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := held(t, cmd, stdin)

	seen := map[string]string{}
	for line := range strings.Lines(out.String()) {
		key, value, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
		seen[key] = value
	}

	return seen, errOut.String(), err
}

// reporter is a script that reports what it sees in its box, a line each.
func reporter(home, outside string) string {
	return `#!/bin/sh
H='` + home + `'
echo "cwd=$(pwd)"
echo "pid=$$"
echo "client=$(tr -d '\n ' < "$HOME/.hypomnema/client.json")"
echo "key=$(cat "$HYPOMNEMA_ROUTINE_RUN_FILE")"
IFS= read -r line || line=none
echo "stdin=$line"
if ls "$H/bots/other" > /dev/null 2>&1; then echo other=seen; else echo other=hidden; fi
if ls "$H/sessions" > /dev/null 2>&1; then echo sessions=seen; else echo sessions=hidden; fi
if ls "$H/run/cht_mine" > /dev/null 2>&1; then echo chats=seen; else echo chats=hidden; fi
if echo hi > "$PWD/written"; then echo bot=writable; fi
if (echo x > "$0") 2> /dev/null; then echo script=writable; else echo script=read-only; fi
if [ -e '` + outside + `' ]; then echo tmp=shared; else echo tmp=private; fi
if command -v gws > /dev/null; then echo tools=seen; fi
echo "env=$(env | cut -d= -f1 | sort | tr '\n' ' ')"
exit 3
`
}

// Scenario: the control plane starts a script run with the run's own token
// on the first line of stdin. The script runs in a box: alone in its PID
// namespace, in its Bot's directory, which it writes, with the run's token
// in its client configuration and not the coordinator's, the run's key
// beside its file, which it cannot change, the Bot's tools, and nothing of
// the rest of stdin, other Bots, the chats or the machine's /tmp. Its exit
// code is the launcher's, and the run's folder is gone afterwards.
func TestARoutinesScriptRunsInItsBoxWithItsOwnToken(t *testing.T) {
	t.Parallel()
	needBox(t)

	w := newBoxWorld(t, bwrapPath, "")
	// The script names the world's own paths, known once the world is made.
	boxWrite(t, filepath.Join(w.root, "kept", "sync.sh"), reporter(w.home, w.outside), 0o600)

	seen, stderr, err := w.run(t, "hyp_bot_v1_run\nmore of the control plane's pipe\n")

	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, stderr)
	assert.Equal(t, 3, exit.ExitCode(), "the script's own exit code")

	work := filepath.Join(w.home, "work")
	assert.Equal(t, work, seen["cwd"], "the first Bot works where pi always worked")

	pid, err := strconv.Atoi(seen["pid"])
	require.NoError(t, err)
	assert.LessOrEqual(t, pid, 3, "the script has a PID namespace of its own")

	assert.JSONEq(t, `{"url":"http://127.0.0.1:18990","token":"hyp_bot_v1_run"}`, seen["client"])
	assert.Equal(t, "key-of-"+boxRoutine+"-for-coordinator", seen["key"])
	assert.Equal(t, "none", seen["stdin"], "the control plane's pipe is the launcher's")
	assert.Equal(t, "hidden", seen["other"], "another Bot's directory")
	assert.Equal(t, "hidden", seen["sessions"], "the chats' sessions")
	assert.Equal(t, "hidden", seen["chats"], "what is made for a chat's pi")
	assert.Equal(t, "writable", seen["bot"])
	assert.Equal(t, "read-only", seen["script"], "the file that runs is the control plane's text")
	assert.Equal(t, "private", seen["tmp"])
	assert.Equal(t, "seen", seen["tools"])
	assert.Contains(t, seen["env"], "HYPOMNEMA_ROUTINE ")
	assert.Contains(t, seen["env"], "HYPOMNEMA_ROUTINE_RUN_FILE ")
	assert.NotContains(t, seen["env"], "HYPOMNEMA_SECRET_OF_THE_CALLER", "nothing of the caller's environment")

	written, err := os.ReadFile(filepath.Join(work, "written")) //nolint:gosec // A path of the test's own.
	require.NoError(t, err)
	assert.Equal(t, "hi\n", string(written), "what the script writes in its Bot's directory stays")
	assert.NoDirExists(t, filepath.Join(w.home, "run", boxRoutine), "the script and the run's key are gone")

	shared, err := os.ReadFile(filepath.Join(w.userHome, ".hypomnema", "client.json"))
	require.NoError(t, err)
	assert.Contains(t, string(shared), "hyp_coordinator_v1_shared", "the coordinator's login is left as it was")
}

// Scenario: a script that goes on is stopped at its limit, and when the
// control plane closes the launcher's input (the Routine was removed); the
// box goes with it, so nothing the script started outlives the run.
func TestARoutinesScriptIsStoppedWithItsBox(t *testing.T) {
	t.Parallel()
	needBox(t)

	long := "#!/bin/sh\necho started > \"$PWD/started\"\nsleep 3\necho late > \"$PWD/late\"\n"
	work := func(w *boxWorld) string { return filepath.Join(w.home, "work") }

	limited := newBoxWorld(t, bwrapPath, long)
	cmd := limited.command(t, "ROUTINE_LIMIT=1")

	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := held(t, cmd, "hyp_bot_v1_run\n")

	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, out.String())
	assert.Equal(t, 124, exit.ExitCode(), "timeout's code: the runner reads it as the limit")

	stopped := newBoxWorld(t, bwrapPath, long)
	cmd = stopped.command(t)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	_, err = io.WriteString(stdin, "hyp_bot_v1_run\n")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(work(stopped), "started"))

		return err == nil
	}, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, stdin.Close())

	ended := make(chan error, 1)

	go func() { ended <- cmd.Wait() }()

	select {
	case err = <-ended:
		require.Error(t, err, "a stopped run is no success")
	case <-time.After(10 * time.Second):
		t.Fatal("the launcher did not stop when its input closed")
	}

	time.Sleep(4 * time.Second)
	assert.NoFileExists(t, filepath.Join(work(limited), "late"), "the box ended at the limit")
	assert.NoFileExists(t, filepath.Join(work(stopped), "late"), "the box ended with its run")
}

// Scenario: no script runs without the run's token, nor without a box.
func TestARoutinesScriptDoesNotRunWithoutItsTokenOrItsBox(t *testing.T) {
	t.Parallel()

	ran := "#!/bin/sh\necho ran=yes\n"

	w := newBoxWorld(t, bwrapPath, ran)

	seen, stderr, err := w.run(t, "hyp_coordinator_v1_shared\n")
	require.Error(t, err)
	assert.Contains(t, stderr, "is not the run's token")
	assert.Empty(t, seen["ran"], "the script did not run")

	unboxed := newBoxWorld(t, filepath.Join(t.TempDir(), "no-bwrap"), ran)

	seen, stderr, err = unboxed.run(t, "hyp_bot_v1_run\n")
	require.Error(t, err)
	assert.Contains(t, stderr, "bubblewrap is not installed")
	assert.Empty(t, seen["ran"], "the script did not run outside a box")
}
