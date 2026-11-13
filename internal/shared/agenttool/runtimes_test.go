package agenttool

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Every node of a system the agent CLIs are built for is told to have omp and
// opencode, each pinned so the node accepts it: fetched over HTTPS, with a
// SHA-256, the command at a path inside its release file.
func TestEveryNodeIsToldToHaveTheAgentCLIs(t *testing.T) {
	t.Parallel()

	for _, system := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}} {
		tools := RuntimeTools(system[0], system[1])

		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			names = append(names, tool.Name)
			require.NoError(t, ToolInstaller{}.valid(tool), "%s on %s", tool.Name, system)
		}

		assert.Equal(t, []string{"omp", "opencode"}, names, system)
	}

	assert.Empty(t, RuntimeTools("windows", "amd64"), "a node that runs no ACP worker is told to have none")
}

// Scenario: a release file that is no archive, as omp's binaries are, is the
// command itself.
func TestTheNodeInstallsAToolThatIsOneBinary(t *testing.T) {
	t.Parallel()

	binary := []byte("#!/bin/sh\necho omp 18.18.6\n")
	sum := sha256.Sum256(binary)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(binary) }))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	tool := nodeproto.Tool{
		Name: "omp", Version: "18.18.6", URL: srv.URL + "/omp-linux-x64", SHA256: hex.EncodeToString(sum[:]),
		Binary: "omp-linux-x64",
	}

	require.NoError(t, ToolInstaller{Home: home, allowHTTP: true}.Install(t.Context(), tool))

	got, err := os.ReadFile(ToolPath(home, "omp", "18.18.6"))
	require.NoError(t, err)
	assert.Equal(t, binary, got)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(ToolPath(home, "omp", "18.18.6"))
		require.NoError(t, err)
		assert.NotZero(t, info.Mode().Perm()&0o100, "its owner can run it")
	}
}
