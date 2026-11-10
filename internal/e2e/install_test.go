package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node"
)

// Scenario: the install on a new machine, without starting the service. The
// binary is put in the node's home, the configuration holds the token, and
// the hello it says shows the control plane what the machine is.
func TestInstallSetsUpANodeThatConnects(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	home := filepath.Join(t.TempDir(), "node")

	exe, err := os.Executable()
	require.NoError(t, err)

	var out bytes.Buffer

	require.NoError(t, node.Install(t.Context(), node.InstallOptions{
		Home: home, URL: w.url, Exe: exe, Token: w.token, NoStart: true,
	}, &out))
	assert.Contains(t, out.String(), "connected to "+w.url)

	installed, err := filepath.Glob(filepath.Join(home, "bin", "hypomnema-node*"))
	require.NoError(t, err)
	require.Len(t, installed, 1)

	want, err := os.Stat(exe)
	require.NoError(t, err)

	got, err := os.Stat(installed[0])
	require.NoError(t, err)
	assert.Equal(t, want.Size(), got.Size())

	cfg, err := node.LoadConfig(home)
	require.NoError(t, err)
	assert.Equal(t, w.token, cfg.Token)
	assert.Equal(t, filepath.Join(home, "worktrees"), cfg.WorktreeRoot)

	nodes, err := w.svc.ListNodes(t.Context())
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.True(t, nodes[0].Connected)
	assert.Equal(t, runtime.GOOS, nodes[0].System.OS)
	assert.Positive(t, nodes[0].System.MemoryBytes)
}

// Scenario: the owner pastes a token the control plane does not know. The
// install says it cannot connect instead of starting a service that fails.
func TestInstallReportsATokenThatIsRefused(t *testing.T) {
	t.Parallel()

	w := newWorld(t)

	exe, err := os.Executable()
	require.NoError(t, err)

	err = node.Install(t.Context(), node.InstallOptions{
		Home: filepath.Join(t.TempDir(), "node"), URL: w.url, Exe: exe, Token: "hyn_wrong",
	}, &bytes.Buffer{})
	require.ErrorContains(t, err, "cannot connect")
}
