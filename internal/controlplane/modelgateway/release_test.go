package modelgateway_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the control plane's release carries Codex's license and notice beside its binaries, since the gateway
// serves Codex's bundled catalog (issue #907, review of PR #922). The release script runs dry, building nothing, and
// writes both files where the release workflow publishes dist/ from.
func TestTheReleaseCarriesCodexsLicense(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)

	dist := t.TempDir()

	cmd := exec.CommandContext(t.Context(), "sh", "deploy/release/release-controlplane.sh", "v0.0.0", "owner/repo",
		"-s -w")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "RELEASE_DRY_RUN=1", "DIST="+dist)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	for file, from := range map[string]string{
		"third_party-codex-LICENSE": "third_party/codex/LICENSE",
		"third_party-codex-NOTICE":  "third_party/codex/NOTICE",
	} {
		got, err := os.ReadFile(filepath.Join(dist, file)) //nolint:gosec // the test's own folder
		require.NoError(t, err, file)

		want, err := os.ReadFile(filepath.Join(root, from)) //nolint:gosec // the repository's own file
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), file)
	}

	entries, err := os.ReadDir(dist)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "a dry run builds no binary")
}
