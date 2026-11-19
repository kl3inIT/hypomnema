package diskguard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestCleanupKeepsAnOpenRunWithABindMountOfAClosedRunsSubdirectory(t *testing.T) {
	t.Parallel()
	if os.Getenv("HYP_TEST_PROTECTED_BIND") != "1" {
		unshare, err := exec.LookPath("unshare")
		if err != nil {
			t.Skip("unshare is not installed")
		}
		//nolint:gosec // unshare is resolved from the test's PATH
		probe := exec.CommandContext(t.Context(), unshare, "--user", "--map-root-user", "--mount", "true")
		if output, err := probe.CombinedOutput(); err != nil {
			t.Skipf("isolated mount namespace is unavailable: %v %s", err, output)
		}
		exe, err := os.Executable()
		require.NoError(t, err)
		args := []string{"--user", "--map-root-user", "--mount", exe, "-test.run=^" + t.Name() + "$", "-test.v"}
		cmd := exec.CommandContext(t.Context(), unshare, args...) //nolint:gosec // isolated test subprocess
		cmd.Env = append(os.Environ(), "HYP_TEST_PROTECTED_BIND=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return
	}
	root := t.TempDir()
	closed, open := filepath.Join(root, "closed"), filepath.Join(root, "open")
	child, alias := filepath.Join(closed, "child"), filepath.Join(open, "imported")
	for _, dir := range []string{child, alias} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	resume := filepath.Join(child, "resume.txt")
	require.NoError(t, os.WriteFile(resume, []byte("follow-up data"), 0o600))
	err := unix.Mount(child, alias, "", unix.MS_BIND, "")
	if errors.Is(err, unix.EPERM) {
		t.Skip("bind mounts are unavailable in the isolated namespace")
	}
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, unix.Unmount(alias, 0)) })
	assert.False(t, SameVolume(child, alias), "a bind mount has its own mount ID")
	for _, protected := range []string{open, alias} {
		ctx := WithProtection(t.Context(), protected)
		require.ErrorContains(t, RemoveTree(ctx, closed, func() bool { return true }), "protected filesystem identity")
		assert.FileExists(t, filepath.Join(alias, "resume.txt"))
	}
}
