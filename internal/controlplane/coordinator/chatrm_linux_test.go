//go:build linux

package coordinator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chatRemover is the file that is installed as /usr/local/bin/hypomnema-chat-rm.
const chatRemover = "../../../deploy/coordinator/hypomnema-chat-rm"

// Scenario: a deleted chat's files on the coordinator's machine go (issue #484): its run/ folder and its sessions,
// and nothing of another chat's. A link pi left in its sessions is removed, not followed. An argument that is not a
// chat id of the shape the control plane makes is refused, so no other path is ever removed.
func TestTheChatRemoverRemovesOnlyThatChatsFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	userHome := filepath.Join(root, "home", "coordinator")
	home := filepath.Join(userHome, ".hypomnema-coordinator")

	const mine, theirs = "cht_aaaaaaaaaaaaaaaaaaaaaaaaaa", "cht_bbbbbbbbbbbbbbbbbbbbbbbbbb"

	outside := filepath.Join(root, "outside")
	for _, f := range []string{
		"run/" + mine + "/prompt.md", "sessions/" + mine + "/1_" + mine + ".jsonl",
		"run/" + theirs + "/prompt.md", "sessions/" + theirs + "/1_" + theirs + ".jsonl",
	} {
		write(t, filepath.Join(home, f), "kept by pi", 0o600)
	}

	write(t, filepath.Join(outside, "keep"), "outside the chat", 0o600)
	require.NoError(t, os.Symlink(outside, filepath.Join(home, "sessions", mine, "link")))

	script, err := os.ReadFile(chatRemover)
	require.NoError(t, err)

	text := string(script)
	from := "\nuser_home=/home/coordinator\n"
	require.Equal(t, 1, strings.Count(text, from), "the remover says where the coordinator's home is once")
	remover := filepath.Join(root, "hypomnema-chat-rm")
	write(t, remover, strings.Replace(text, from, "\nuser_home="+userHome+"\n", 1), 0o700)

	run := func(args ...string) error {
		return exec.CommandContext(t.Context(), remover, args...).Run() //nolint:gosec // The test's own copy.
	}

	for _, bad := range [][]string{
		{}, {"cht_../../outside"}, {"cht_" + strings.Repeat("a", 25)}, {"cht_AAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"run_aaaaaaaaaaaaaaaaaaaaaaaaaa"}, {mine, theirs}, {"cht_aaaaaaaaaaaaaaaaaaaaaaaaa/"},
	} {
		var exit *exec.ExitError
		require.ErrorAs(t, run(bad...), &exit, "%q", bad)
		assert.Equal(t, 2, exit.ExitCode(), "%q is refused", bad)
	}

	require.NoError(t, run(mine))
	require.NoError(t, run(mine), "a chat whose files are gone already is no failure")

	assert.NoDirExists(t, filepath.Join(home, "run", mine))
	assert.NoDirExists(t, filepath.Join(home, "sessions", mine))
	assert.FileExists(t, filepath.Join(home, "run", theirs, "prompt.md"))
	assert.FileExists(t, filepath.Join(home, "sessions", theirs, "1_"+theirs+".jsonl"))
	assert.FileExists(t, filepath.Join(outside, "keep"), "the link is removed, not followed")
}
