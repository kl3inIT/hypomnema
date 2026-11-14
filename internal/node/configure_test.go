package node

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: a node is installed again over itself, to upgrade it or to give
// it a new token. What its machine's owner set stays: the worktree root that
// holds its work, the terminal and the self-update that were turned off, the
// check interval, and the Android device its first boot named. Only where it
// connects and with what change. A first install gets the defaults.
func TestInstallingOverANodeKeepsItsOwnSettings(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "node")
	root := filepath.Join(t.TempDir(), "work")

	require.NoError(t, Configure(home, "https://old.example", "hyn_old", root, "android"))

	cfg, err := LoadConfig(home)
	require.NoError(t, err)

	cfg.DisableTerminal, cfg.DisableAutoUpdate, cfg.UpdateEveryMinutes = true, true, 90
	require.NoError(t, saveConfig(home, cfg))

	require.NoError(t, Configure(home, "https://new.example/", "hyn_new", "", ""))

	got, err := LoadConfig(home)
	require.NoError(t, err)
	assert.Equal(t, Config{
		URL: "https://new.example", Token: "hyn_new", WorktreeRoot: root,
		DisableTerminal: true, DisableAutoUpdate: true, UpdateEveryMinutes: 90, Device: "android",
	}, got)

	fresh := filepath.Join(t.TempDir(), "fresh")
	require.NoError(t, Configure(fresh, "https://new.example", "hyn_new", "", ""))

	first, err := LoadConfig(fresh)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(fresh, "worktrees"), first.WorktreeRoot)
	assert.False(t, first.DisableTerminal)
	assert.Empty(t, first.Device)
}

// Scenario: an install names a device no node knows how to hold. Nothing is
// written: the node would otherwise say at hello that it holds it.
func TestInstallRefusesADeviceItCannotHold(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "node")

	require.ErrorContains(t, Configure(home, "https://cp.example", "hyn_1", "", "ios"), `the device "ios"`)

	_, err := LoadConfig(home)
	require.Error(t, err)
}
