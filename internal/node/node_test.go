package node

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: installing a node. The token comes from a file and lands only in
// the private configuration; a wrong URL or a file without a token is refused.
func TestInitWritesAPrivateConfiguration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	tokenFile := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("hyn_abc123\n"), 0o600))

	require.NoError(t, Init(home, "https://hypomnema.example/", tokenFile, ""))

	cfg, err := LoadConfig(home)
	require.NoError(t, err)
	assert.Equal(t, Config{
		URL: "https://hypomnema.example", Token: "hyn_abc123", WorktreeRoot: filepath.Join(home, "worktrees"),
	}, cfg)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(configPath(home))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

		info, err = os.Stat(home)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	}

	require.ErrorContains(t, Init(home, "hypomnema.example", tokenFile, ""), "must be http(s)://host")

	notToken := filepath.Join(dir, "not-token")
	require.NoError(t, os.WriteFile(notToken, []byte("password"), 0o600))
	require.ErrorContains(t, Init(home, "https://hypomnema.example", notToken, ""), "does not hold a node token")

	require.ErrorContains(t, Init(home, "https://hypomnema.example", tokenFile, "relative/dir"), "must be absolute")

	_, err = LoadConfig(filepath.Join(dir, "elsewhere"))
	require.ErrorContains(t, err, "hypomnema-node init")
}
