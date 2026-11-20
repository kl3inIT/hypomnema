package modelgateway_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the control plane's release carries Codex's license and notice beside its binaries, since the gateway
// serves Codex's bundled catalog (issue #907, review of PR #922). The release script runs dry, building nothing, in a
// copy of what it reads, with DIST unset as the release workflow runs it, and writes both files into dist/, the
// folder the workflow publishes (`gh release create … dist/*`, .github/workflows/release.yml).
func TestTheReleaseCarriesCodexsLicense(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)

	//nolint:gosec // the repository's own file
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	require.NoError(t, err)
	require.Contains(t, string(workflow), " dist/* ", "the workflow publishes dist/")

	copied := t.TempDir()
	for _, file := range []string{
		"deploy/release/release-controlplane.sh", "third_party/codex/LICENSE", "third_party/codex/NOTICE",
	} {
		content, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // the repository's own file
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(copied, filepath.Dir(file)), 0o750))
		//nolint:gosec // a fixed name of the repository's under the test's own folder
		require.NoError(t, os.WriteFile(filepath.Join(copied, file), content, 0o600))
	}

	cmd := exec.CommandContext(t.Context(), "sh", "deploy/release/release-controlplane.sh", "v0.0.0", "owner/repo",
		"-s -w")
	cmd.Dir = copied

	// The environment as the workflow has it: no DIST, so the script's own default is what is checked.
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "DIST=") && !strings.HasPrefix(v, "RELEASE_DRY_RUN=") {
			cmd.Env = append(cmd.Env, v)
		}
	}

	cmd.Env = append(cmd.Env, "RELEASE_DRY_RUN=1")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	dist := filepath.Join(copied, "dist")

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
