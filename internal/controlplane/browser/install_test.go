package browser

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// served is a program whose release file for this machine is body, served by
// a test server that counts how often it was fetched.
func served(t *testing.T, body []byte, binary string) (program, *atomic.Int32) {
	t.Helper()

	var fetched atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetched.Add(1)

		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	sum := sha256.Sum256(body)

	return program{
		Name: "tool", Version: "1.2.3",
		Releases: map[string]release{runtime.GOOS + "/" + runtime.GOARCH: {
			URL: server.URL, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body)), Binary: binary,
		}},
	}, &fetched
}

func zipped(t *testing.T, members map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	w := zip.NewWriter(&buf)

	for name, content := range members {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o755)

		member, err := w.CreateHeader(header)
		require.NoError(t, err)

		_, err = member.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, w.Close())

	return buf.Bytes()
}

// A release file is the command itself when it names no path inside it, and
// a version that is there is not fetched again.
func TestInstallPutsTheCommandInPlaceOnce(t *testing.T) {
	t.Parallel()

	p, fetched := served(t, []byte("the command"), "")
	in := installer{dir: t.TempDir()}

	command, err := in.install(t.Context(), p)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(in.dir, "tools", "tool", "1.2.3", "tool"), command)

	content, err := os.ReadFile(command) //nolint:gosec // The test's own directory.
	require.NoError(t, err)
	assert.Equal(t, "the command", string(content))

	again, err := in.install(t.Context(), p)
	require.NoError(t, err)
	assert.Equal(t, command, again)
	assert.EqualValues(t, 1, fetched.Load(), "an installed version is not fetched again")
}

// A .zip is unpacked as the tree it holds, and the command is the member the
// pin names.
func TestInstallUnpacksAZip(t *testing.T) {
	t.Parallel()

	p, _ := served(t, zipped(t, map[string]string{
		"shell/chrome": "the browser", "shell/lib/data.pak": "its data",
	}), "shell/chrome")
	in := installer{dir: t.TempDir()}

	command, err := in.install(t.Context(), p)
	require.NoError(t, err)

	content, err := os.ReadFile(command) //nolint:gosec // The test's own directory.
	require.NoError(t, err)
	assert.Equal(t, "the browser", string(content))
	assert.FileExists(t, filepath.Join(filepath.Dir(command), "lib", "data.pak"))
}

// What does not match its pin is not installed: a file of another size, a
// file of another checksum, and a .zip with a member outside its own tree.
func TestInstallRefusesWhatDoesNotMatchItsPin(t *testing.T) {
	t.Parallel()

	tests := map[string]func(p *program, rel release){
		"another size": func(p *program, rel release) {
			rel.Bytes++
			p.Releases[runtime.GOOS+"/"+runtime.GOARCH] = rel
		},
		"another checksum": func(p *program, rel release) {
			rel.SHA256 = hex.EncodeToString(make([]byte, sha256.Size))
			p.Releases[runtime.GOOS+"/"+runtime.GOARCH] = rel
		},
	}

	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, _ := served(t, []byte("the command"), "")
			change(&p, p.Releases[runtime.GOOS+"/"+runtime.GOARCH])

			in := installer{dir: t.TempDir()}

			_, err := in.install(t.Context(), p)
			require.ErrorContains(t, err, "nothing was installed")
			assert.NoDirExists(t, filepath.Join(in.dir, "tools", "tool", "1.2.3"))
		})
	}

	t.Run("a member outside the release file's tree", func(t *testing.T) {
		t.Parallel()

		p, _ := served(t, zipped(t, map[string]string{"../../escaped": "x", "shell/chrome": "y"}), "shell/chrome")
		in := installer{dir: t.TempDir()}

		_, err := in.install(t.Context(), p)
		require.ErrorContains(t, err, "not a path inside the release file")
		assert.NoFileExists(t, filepath.Join(in.dir, "escaped"))
		assert.NoFileExists(t, filepath.Join(in.dir, "tools", "escaped"))
		assert.NoDirExists(t, filepath.Join(in.dir, "tools", "tool", "1.2.3"))
	})
}
