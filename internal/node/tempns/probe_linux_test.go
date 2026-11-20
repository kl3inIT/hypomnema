package tempns

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeReportsWhyBubblewrapIsUnavailable(t *testing.T) {
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	path := t.TempDir()
	t.Setenv("PATH", path)
	assert.Contains(t, Probe(t.Context(), "").Reason, "not installed")
	require.NoError(t, os.Symlink(shell, filepath.Join(path, "sh")))
	bwrap := filepath.Join(path, "bwrap")
	require.NoError(t, os.WriteFile(bwrap, []byte("#!/bin/sh\nprintf 'namespace not permitted\\n' >&2\nexit 1\n"), 0o700))
	isolation := Probe(t.Context(), "")
	assert.False(t, isolation.On())
	assert.Contains(t, isolation.Reason, "namespace not permitted")
}

func TestBundledBubblewrapFollowsTheCodexLauncherSymlink(t *testing.T) {
	t.Parallel()
	triple, pkg := codexTriple()
	if triple == "" {
		t.Skip("Codex does not build for this architecture")
	}
	root := t.TempDir()
	exe := filepath.Join(root, "node_modules", "@openai", "codex", "bin", "codex.js")
	bwrap := filepath.Join(root, "node_modules", "@openai", pkg, "vendor", triple, "codex-resources", "bwrap")
	for _, path := range []string{exe, bwrap} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, nil, 0o700))
	}
	launcher := filepath.Join(root, "codex")
	require.NoError(t, os.Symlink(exe, launcher))
	assert.Equal(t, []string{bwrap}, bundled(launcher))
}
