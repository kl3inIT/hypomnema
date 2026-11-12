//go:build linux

package coordinator

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The launcher's box (issue #260), run for real: the launcher is the file
// that is installed, with the four lines that say where things are pointed
// at a directory of the test's own, and pi is a script that reports what it
// sees. Where HYPOMNEMA_BOX_TEST=required (CI, after setup-box.sh), a machine
// that cannot make a box fails the test instead of skipping it.

const (
	boxRequired = "HYPOMNEMA_BOX_TEST"
	launcher    = "../../../deploy/coordinator/hypomnema-pi"
	bwrapPath   = "/usr/bin/bwrap"
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

// boxWorld is a coordinator's home laid out as the setup script and earlier
// launchers left it, in a directory of the test's own.
type boxWorld struct {
	root, userHome, home, script string
	// outside is a file in the machine's /tmp, which the box must not see.
	outside string
}

func newBoxWorld(t *testing.T, bwrap string) *boxWorld {
	t.Helper()

	root := t.TempDir()
	w := &boxWorld{root: root, userHome: filepath.Join(root, "home", "coordinator")}
	w.home = filepath.Join(w.userHome, ".hypomnema-coordinator")

	files := map[string]string{
		"agent/AGENTS.md":   "rules for every bot",
		"agent/models.json": "the models another chat's pi was given",
		"bots/other/notes":  "another bot's notes",
		"sessions/2026-11-01T00-00-00-000Z_cht_mine.jsonl":   `{"type":"session","id":"cht_mine"}`,
		"sessions/2026-11-01T00-00-00-000Z_cht_theirs.jsonl": `{"type":"session","id":"cht_theirs"}`,
		"work/README":       "the first bot's directory",
		"tools/bin/gws":     "#!/bin/sh\n",
		"pi/node_modules/x": "pi's code",
	}
	for name, content := range files {
		write(t, filepath.Join(w.home, name), content, 0o600)
	}

	write(t, filepath.Join(w.userHome, ".hypomnema", "client.json"),
		"{\n  \"url\": \"http://127.0.0.1:18990\",\n  \"token\": \"hyp_coordinator_v1_shared\"\n}\n", 0o600)
	write(t, filepath.Join(w.userHome, ".agents", "skills", "hypomnema-coordinator", "SKILL.md"), "skill", 0o600)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "node", "bin"), 0o700))

	// The control plane's CLI, as the launcher calls it before pi starts.
	write(t, filepath.Join(root, "bin", "hypomnema"), `#!/bin/sh
case "$1" in
  bots) echo "persona of $3" ;;
  skills) mkdir -p "$6" ;;
  pi-models) echo '{"providers":{"model-gateway":{}}}' ;;
  *) exit 9 ;;
esac
`, 0o700)

	outside, err := os.CreateTemp("/tmp", "hypomnema-box-outside-")
	require.NoError(t, err)
	require.NoError(t, outside.Close())
	t.Cleanup(func() { _ = os.Remove(outside.Name()) })
	w.outside = outside.Name()

	// pi reports what it sees, a line each.
	write(t, filepath.Join(w.home, "pi", "node_modules", ".bin", "pi"), `#!/bin/sh
H='`+w.home+`'
IFS= read -r first || true
echo "stdin=$first"
echo "args=$*"
prompt=""
while [ $# -gt 0 ]; do
  if [ "$1" = --append-system-prompt ]; then prompt=$2; fi
  shift
done
echo "prompt=$(cat "$prompt")"
echo "cwd=$(pwd)"
echo "pid=$$"
echo "client=$(tr -d '\n ' < "$HOME/.hypomnema/client.json")"
echo "agents=$(cat "$PI_CODING_AGENT_DIR/AGENTS.md")"
echo "models=$(cat "$PI_CODING_AGENT_DIR/models.json")"
if ls "$H/bots/other" > /dev/null 2>&1; then echo other=seen; else echo other=hidden; fi
echo "sessions=$(ls "$H/sessions" | tr '\n' ' ')"
echo "mine=$(ls "$H/sessions/cht_mine" | tr '\n' ' ')"
if echo hi > "$PWD/written"; then echo bot=writable; fi
if touch "$H/sessions/cht_mine/new.jsonl"; then echo session=writable; fi
if (echo x > "$PI_CODING_AGENT_DIR/AGENTS.md") 2> /dev/null; then echo shared=writable; else echo shared=read-only; fi
if echo '{}' > "$PI_CODING_AGENT_DIR/auth.json"; then echo agent=writable; fi
if [ -e '`+w.outside+`' ]; then echo tmp=shared; else echo tmp=private; fi
echo "skills=$(ls "$HOME/.agents/skills")"
echo "env=$(env | cut -d= -f1 | sort | tr '\n' ' ')"
`, 0o700)

	script, err := os.ReadFile(launcher)
	require.NoError(t, err)

	text := string(script)
	for from, to := range map[string]string{
		"user_home=/home/coordinator":        "user_home=" + w.userHome,
		"hypomnema=/usr/local/bin/hypomnema": "hypomnema=" + filepath.Join(root, "bin", "hypomnema"),
		"node=/opt/hypomnema-node":           "node=" + filepath.Join(root, "node"),
		"bwrap=/usr/bin/bwrap":               "bwrap=" + bwrap,
	} {
		require.Equal(t, 1, strings.Count(text, "\n"+from+"\n"), "the launcher says %s once", from)
		text = strings.Replace(text, "\n"+from+"\n", "\n"+to+"\n", 1)
	}

	w.script = filepath.Join(root, "hypomnema-pi")
	write(t, w.script, text, 0o700)

	return w
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), mode)) //nolint:gosec // A path of the test's own.
}

// start runs the launcher for chat cht_mine with stdin and returns what it
// printed and how it ended.
func (w *boxWorld) start(t *testing.T, stdin string) (map[string]string, string, error) {
	t.Helper()

	//nolint:gosec // The test's own copy of the launcher.
	cmd := exec.CommandContext(t.Context(), "sh", w.script, "cht_mine", "--bot", "coordinator", "--model", "gpt-sol")
	cmd.Env = append(os.Environ(), "HYPOMNEMA_SECRET_OF_THE_CALLER=x")
	cmd.Stdin = strings.NewReader(stdin)

	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()

	seen := map[string]string{}
	for line := range strings.Lines(out.String()) {
		key, value, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
		seen[key] = value
	}

	return seen, errOut.String(), err
}

// Scenario: the control plane starts pi for a chat with its own token on the
// first line of stdin. pi runs in a box: it is alone in its PID namespace,
// works in its Bot's directory, sees only its chat's sessions, reads its
// token in its client configuration and not the coordinator's, and sees no
// other Bot's directory and nothing of the machine's /tmp. It writes its
// Bot's directory and its sessions; the shared instructions it only reads.
func TestPiRunsInItsBoxWithItsOwnToken(t *testing.T) {
	t.Parallel()
	needBox(t)

	w := newBoxWorld(t, bwrapPath)
	seen, stderr, err := w.start(t, "hyp_bot_v1_mine\n{\"type\":\"get_state\"}\n")
	require.NoError(t, err, stderr)

	work := filepath.Join(w.home, "work")
	sessions := filepath.Join(w.home, "sessions", "cht_mine")

	assert.JSONEq(t, `{"type":"get_state"}`, seen["stdin"], "pi reads its commands; the token line is the launcher's")
	assert.Equal(t, "--mode rpc --session-dir "+sessions+" --session-id cht_mine --model gpt-sol "+
		"--append-system-prompt "+filepath.Join(w.home, "run", "cht_mine", "prompt.md")+
		" --skill "+filepath.Join(work, ".hypomnema", "skills"), seen["args"])
	assert.Equal(t, "persona of coordinator", seen["prompt"])
	assert.Equal(t, work, seen["cwd"], "the first Bot works where pi always worked, the path its sessions record")

	pid, err := strconv.Atoi(seen["pid"])
	require.NoError(t, err)
	assert.LessOrEqual(t, pid, 3, "pi has a PID namespace of its own")

	assert.JSONEq(t, `{"url":"http://127.0.0.1:18990","token":"hyp_bot_v1_mine"}`, seen["client"])
	assert.Equal(t, "rules for every bot", seen["agents"])
	assert.JSONEq(t, `{"providers":{"model-gateway":{}}}`, seen["models"], "this chat's models.json, not another's")
	assert.Equal(t, "hidden", seen["other"], "another Bot's directory")
	assert.Equal(t, "cht_mine ", seen["sessions"], "another chat's sessions")
	assert.Equal(t, "2026-11-01T00-00-00-000Z_cht_mine.jsonl ", seen["mine"], "the chat's session was moved in")
	assert.Equal(t, "writable", seen["bot"])
	assert.Equal(t, "writable", seen["session"])
	assert.Equal(t, "read-only", seen["shared"])
	assert.Equal(t, "writable", seen["agent"], "pi locks files in its agent directory")
	assert.Equal(t, "private", seen["tmp"])
	assert.Equal(t, "hypomnema-coordinator", seen["skills"])
	assert.Contains(t, seen["env"], "HYPOMNEMA_CHAT_ID ")
	assert.NotContains(t, seen["env"], "HYPOMNEMA_SECRET_OF_THE_CALLER", "nothing of the caller's environment")

	written, err := os.ReadFile(filepath.Join(work, "written")) //nolint:gosec // A path of the test's own.
	require.NoError(t, err)
	assert.Equal(t, "hi\n", string(written), "what pi writes in its Bot's directory stays")
	assert.FileExists(t, filepath.Join(sessions, "new.jsonl"))
	assert.FileExists(t, filepath.Join(w.home, "sessions", "2026-11-01T00-00-00-000Z_cht_theirs.jsonl"))
	assert.NoFileExists(t, filepath.Join(w.home, "agent", "auth.json"), "pi's agent directory is its own")

	shared, err := os.ReadFile(filepath.Join(w.userHome, ".hypomnema", "client.json"))
	require.NoError(t, err)
	assert.Contains(t, string(shared), "hyp_coordinator_v1_shared", "the coordinator's login is left as it was")
}

// Scenario: pi does not start without its token, nor without a box.
func TestPiDoesNotStartWithoutItsTokenOrItsBox(t *testing.T) {
	t.Parallel()

	w := newBoxWorld(t, bwrapPath)

	seen, stderr, err := w.start(t, "{\"type\":\"get_state\"}\n")
	require.Error(t, err)
	assert.Contains(t, stderr, "is not pi's token")
	assert.Empty(t, seen["args"], "pi did not run")

	unboxed := newBoxWorld(t, filepath.Join(t.TempDir(), "no-bwrap"))

	seen, stderr, err = unboxed.start(t, "hyp_bot_v1_mine\n")
	require.Error(t, err)
	assert.Contains(t, stderr, "bubblewrap is not installed")
	assert.Empty(t, seen["args"], "pi did not run outside a box")
}
