package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the node runs as a systemd user service, whose PATH lacks the
// user's ~/.local/bin, where the repository's setup put Go, Node and pnpm
// (issue #718). The worker gets that directory first, so the version the
// setup installed wins over an older one of the machine's; a home without
// it leaves the PATH as it is.
func TestTheWorkerFindsWhatTheSetupInstalledFirstOnItsPath(t *testing.T) {
	t.Parallel()

	sep := string(os.PathListSeparator)
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")

	assert.Equal(t, "/usr/bin"+sep+"/bin", WorkerPath(home, "/usr/bin"+sep+"/bin"), "no ~/.local/bin yet")
	assert.Equal(t, "/usr/bin", WorkerPath("", "/usr/bin"), "no home")

	require.NoError(t, os.MkdirAll(bin, 0o700))

	assert.Equal(t, bin+sep+"/usr/bin"+sep+"/bin", WorkerPath(home, "/usr/bin"+sep+"/bin"))
	assert.Equal(t, bin+sep+"/usr/bin", WorkerPath(home, bin+sep+"/usr/bin"), "already first")
	assert.Equal(t, bin, WorkerPath(home, ""))
}

// Scenario: the environment a runtime gives its worker carries that PATH.
func TestAWorkersEnvironmentCarriesItsPath(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	require.NoError(t, os.MkdirAll(bin, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin")

	env := HostEnv()

	assert.Contains(t, env, "PATH="+bin+string(os.PathListSeparator)+"/usr/bin")
	assert.Contains(t, env, "HOME="+home)
}
