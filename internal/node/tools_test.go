package node

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// releaseFile is a tool's release as its publisher ships it: a .tar.gz with
// the command beside a README, and the checksum published for it.
func releaseFile(t *testing.T, command string) (file []byte, checksum string) {
	t.Helper()

	var buf bytes.Buffer

	zipped := gzip.NewWriter(&buf)
	members := tar.NewWriter(zipped)

	for name, body := range map[string]string{"./README.md": "read me", "./gws": command} {
		require.NoError(t, members.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}))

		_, err := members.Write([]byte(body))
		require.NoError(t, err)
	}

	require.NoError(t, members.Close())
	require.NoError(t, zipped.Close())

	sum := sha256.Sum256(buf.Bytes())

	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// Scenario: the control plane tells the node to have gws. The node fetches
// the release file, checks it against the pinned checksum and unpacks the
// command under its home; asked again it fetches nothing. A release file that
// is not the pinned one installs nothing and leaves nothing behind.
func TestTheNodeInstallsAPinnedTool(t *testing.T) {
	t.Parallel()

	file, checksum := releaseFile(t, "#!/bin/sh\necho gws 0.22.5\n")

	var fetched atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)

		if r.URL.Path != "/gws.tar.gz" {
			http.NotFound(w, r)

			return
		}

		_, _ = w.Write(file)
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	installer := ToolInstaller{Home: home, allowHTTP: true}
	tool := nodeproto.Tool{
		Name: "gws", Version: "0.22.5", URL: srv.URL + "/gws.tar.gz", SHA256: checksum, Binary: "gws",
	}

	require.NoError(t, installer.Install(t.Context(), tool))

	command, err := os.ReadFile(ToolPath(home, "gws", "0.22.5"))
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\necho gws 0.22.5\n", string(command))

	require.NoError(t, installer.Install(t.Context(), tool))
	assert.Equal(t, int32(1), fetched.Load(), "the version is there: nothing is fetched again")

	left, err := os.ReadDir(filepath.Dir(ToolPath(home, "gws", "0.22.5")))
	require.NoError(t, err)
	assert.Len(t, left, 1, "only the command stays: the download is removed")

	// A newer pin whose release file is not the one that was pinned.
	tampered := tool
	tampered.Version, tampered.SHA256 = "0.23.0", hex.EncodeToString(make([]byte, 32))

	err = installer.Install(t.Context(), tampered)
	require.ErrorContains(t, err, "checksum")

	_, err = os.Stat(ToolPath(home, "gws", "0.23.0"))
	require.ErrorIs(t, err, os.ErrNotExist)

	left, err = os.ReadDir(filepath.Dir(ToolPath(home, "gws", "0.23.0")))
	require.NoError(t, err)
	assert.Empty(t, left, "a download that failed its checksum is deleted")

	// The version that was installed is untouched.
	_, err = os.Stat(ToolPath(home, "gws", "0.22.5"))
	require.NoError(t, err)
}

// Scenario: what the node refuses to install, before or after fetching.
func TestTheNodeRefusesAToolItCannotTrust(t *testing.T) {
	t.Parallel()

	file, checksum := releaseFile(t, "gws")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gone.tar.gz" {
			http.NotFound(w, r)

			return
		}

		_, _ = w.Write(file)
	}))
	t.Cleanup(srv.Close)

	good := nodeproto.Tool{Name: "gws", Version: "1.0.0", URL: srv.URL + "/gws.tar.gz", SHA256: checksum, Binary: "gws"}

	change := func(f func(*nodeproto.Tool)) nodeproto.Tool {
		tool := good
		f(&tool)

		return tool
	}

	for name, tc := range map[string]struct {
		tool nodeproto.Tool
		want string
	}{
		"no checksum":                        {change(func(t *nodeproto.Tool) { t.SHA256 = "" }), "checksum"},
		"a name that is a path":              {change(func(t *nodeproto.Tool) { t.Name = "../gws" }), "name"},
		"a version that is a path":           {change(func(t *nodeproto.Tool) { t.Version = "../1" }), "version"},
		"a command outside the release file": {change(func(t *nodeproto.Tool) { t.Binary = "../../bin/sh" }), "inside"},
		"a release file that is not there":   {change(func(t *nodeproto.Tool) { t.URL = srv.URL + "/gone.tar.gz" }), "404"},
		"a release file without the command": {change(func(t *nodeproto.Tool) { t.Binary = "bin/gws" }), "has no bin/gws"},
	} {
		home := t.TempDir()

		err := ToolInstaller{Home: home, allowHTTP: true}.Install(t.Context(), tc.tool)
		require.ErrorContains(t, err, tc.want, name)

		_, err = os.Stat(ToolPath(home, "gws", "1.0.0"))
		require.ErrorIs(t, err, os.ErrNotExist, name)
	}

	// Outside tests a release file comes over HTTPS only.
	err := ToolInstaller{Home: t.TempDir()}.Install(t.Context(), good)
	require.ErrorContains(t, err, "HTTPS")
}
