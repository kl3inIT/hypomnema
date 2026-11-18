//go:build linux || darwin

package diskguard

import (
	"context"
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
	_, err = checkedIdentities(t.Context(), closed, paths, []mountedPath{{path: mount, alias: true}}, nil)
	require.ErrorContains(t, err, "protected filesystem identity")
	assert.FileExists(t, file)
}

func TestManagedRemovalKeepsRenamedNodeHome(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home := filepath.Join(root, "home")
	target := filepath.Join(root, "closed")
	branch := filepath.Join(target, "branch")
	moved := filepath.Join(branch, "moved")
	marker := filepath.Join(target, "before-branch")
	require.NoError(t, os.Mkdir(home, 0o700))
	require.NoError(t, os.MkdirAll(branch, 0o700))
	require.NoError(t, os.WriteFile(marker, nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, "state.db"), []byte("state"), 0o600))

	ctx := &removalContext{Context: t.Context(), afterEntry: func() bool {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			return false
		}
		require.NoError(t, os.Rename(home, moved))
		return true
	}}
	err = RemoveManagedTree(ctx, target, home, "", func() bool { return true })
	require.ErrorContains(t, err, "encountered a protected filesystem identity")
	assert.FileExists(t, filepath.Join(moved, "state.db"))
}

type removalContext struct {
	context.Context //nolint:containedctx // The test intercepts Err at deterministic points in the deletion walk.
	afterEntry      func() bool
}

func (ctx *removalContext) Err() error {
	if ctx.afterEntry != nil && ctx.afterEntry() {
		ctx.afterEntry = nil
	}
	return ctx.Context.Err()
}
