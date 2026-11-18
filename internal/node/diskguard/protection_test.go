//go:build linux || darwin

package diskguard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProtectedDescendantMountIdentityPreventsDeletion(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	closed := filepath.Join(root, "closed")
	child := filepath.Join(closed, "subdirectory")
	open := filepath.Join(root, "open")
	mount := filepath.Join(open, "imported")
	require.NoError(t, os.MkdirAll(child, 0o700))
	require.NoError(t, os.Mkdir(open, 0o700))
	file := filepath.Join(child, "resume.txt")
	require.NoError(t, os.WriteFile(file, []byte("still open"), 0o600))
	require.NoError(t, os.Symlink(child, mount))
	paths := []string{open}
	require.NoError(t, Protect(WithProtection(t.Context(), paths...), closed, "", ""))
	require.ErrorContains(t, protectIdentities(t.Context(), closed, paths, []mountedPath{{path: mount, alias: true}}),
		"protected filesystem identity")
	assert.FileExists(t, file)
}
