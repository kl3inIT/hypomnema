package worktree

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOwnedGitLockRecoveryPreservesReplacement(t *testing.T) {
	mirror := t.TempDir()
	root, err := os.OpenRoot(mirror)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	const name = "config.lock"
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	recoverLocks := ownedGitLocks(os.Getpid(), mirror)
	require.NotNil(t, recoverLocks)
	require.NoError(t, root.Remove(name))
	require.NoError(t, root.WriteFile(name, []byte("new owner"), 0o600))
	recoverLocks()
	data, err := root.ReadFile(name)
	require.NoError(t, err)
	require.Equal(t, "new owner", string(data))
}
