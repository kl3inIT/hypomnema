package device

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// files serves bodies by path and counts how often each was fetched.
type files struct {
	mu      sync.Mutex
	bodies  map[string][]byte
	fetched map[string]int
	server  *httptest.Server
}

func newFiles(t *testing.T) *files {
	t.Helper()

	f := &files{bodies: map[string][]byte{}, fetched: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body, ok := f.bodies[r.URL.Path]
		f.fetched[r.URL.Path]++
		f.mu.Unlock()

		if !ok {
			http.NotFound(w, r)

			return
		}

		_, _ = w.Write(body)
	}))
	t.Cleanup(f.server.Close)

	return f
}

func (f *files) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.fetched[path]
}

// pin serves body at path and returns a component pinned to it.
func (f *files) pin(path string, body []byte, c component) component {
	f.mu.Lock()
	f.bodies[path] = body
	f.mu.Unlock()

	sum := sha256.Sum256(body)
	c.URL, c.SHA256, c.Bytes = f.server.URL+path, hex.EncodeToString(sum[:]), int64(len(body))

	if c.MaxUnpacked == 0 {
		c.MaxUnpacked = 1 << 20
	}

	return c
}

// member is one entry of a test .zip; link makes it a symbolic link to content.
type member struct {
	name, content string
	link          bool
}

func zipped(t *testing.T, members ...member) []byte {
	t.Helper()

	var buf bytes.Buffer

	w := zip.NewWriter(&buf)

	for _, m := range members {
		header := &zip.FileHeader{Name: m.name, Method: zip.Deflate}
		header.SetMode(0o755)

		if m.link {
			header.SetMode(fs.ModeSymlink | 0o777)
		}

		entry, err := w.CreateHeader(header)
		require.NoError(t, err)

		_, err = entry.Write([]byte(m.content))
		require.NoError(t, err)
	}

	require.NoError(t, w.Close())

	return buf.Bytes()
}

// testComponents are a .zip that becomes a folder of the SDK and a command
// put in place as it is, as platform-tools and Android CLI are.
func testComponents(t *testing.T, f *files) []component {
	t.Helper()

	return []component{
		f.pin("/tools.zip", zipped(t,
			member{name: "platform-tools/adb", content: "adb"},
			member{name: "platform-tools/lib/data", content: "data"},
		), component{Name: namePlatformTools, Version: "1.0", Root: "platform-tools", Place: sdkDir + "/platform-tools"}),
		f.pin("/android", []byte("the cli"),
			component{Name: nameAndroidCLI, Version: "2.0", Place: androidCLIPath, Executable: true}),
	}
}

// Each component goes to its place in the SDK's layout, what was installed
// is recorded with its version, and a component at its pin is not fetched
// again.
func TestInstallPlacesEachComponentOnce(t *testing.T) {
	t.Parallel()

	f := newFiles(t)
	comps := testComponents(t, f)
	in := installer{dir: t.TempDir()}

	require.NoError(t, in.install(t.Context(), comps))

	adb, err := os.ReadFile(filepath.Join(in.dir, "sdk", "platform-tools", "adb"))
	require.NoError(t, err)
	assert.Equal(t, "adb", string(adb))
	assert.FileExists(t, filepath.Join(in.dir, "sdk", "platform-tools", "lib", "data"))

	cli, err := os.ReadFile(filepath.Join(in.dir, "bin", "android"))
	require.NoError(t, err)
	assert.Equal(t, "the cli", string(cli))

	have, err := in.installed()
	require.NoError(t, err)
	assert.Equal(t, "1.0", have[namePlatformTools].Version)
	assert.Equal(t, "2.0", have[nameAndroidCLI].Version)
	assert.True(t, in.complete(comps))
	assert.NoDirExists(t, filepath.Join(in.dir, downloadsDir), "nothing is left of the downloads")

	require.NoError(t, in.install(t.Context(), comps))
	assert.Equal(t, 1, f.count("/tools.zip"), "a component at its pin is not fetched again")
	assert.Equal(t, 1, f.count("/android"))
}

// What does not match its pin is not installed nor recorded: a file of
// another size, of another checksum, a .zip with a member or a link outside
// its tree, and a .zip without the folder the pin names.
func TestInstallRefusesWhatDoesNotMatchItsPin(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body   func(t *testing.T) []byte
		change func(c *component)
		want   string
	}{
		"another size": {
			body:   func(*testing.T) []byte { return []byte("the cli") },
			change: func(c *component) { c.Bytes++ },
			want:   "nothing was installed",
		},
		"another checksum": {
			body:   func(*testing.T) []byte { return []byte("the cli") },
			change: func(c *component) { c.SHA256 = hex.EncodeToString(make([]byte, sha256.Size)) },
			want:   "nothing was installed",
		},
		"a member outside the tree": {
			body: func(t *testing.T) []byte {
				return zipped(t, member{name: "../../escaped", content: "x"}, member{name: "emulator/emulator", content: "y"})
			},
			change: func(c *component) { c.Root = "emulator" },
			want:   "not a path inside the .zip",
		},
		"a link outside the tree": {
			body: func(t *testing.T) []byte {
				return zipped(t, member{name: "emulator/lib", content: "../../../etc", link: true})
			},
			change: func(c *component) { c.Root = "emulator" },
			want:   "points outside the .zip",
		},
		"no folder of the pin's": {
			body:   func(t *testing.T) []byte { return zipped(t, member{name: "other/emulator", content: "y"}) },
			change: func(c *component) { c.Root = "emulator" },
			want:   "has no emulator/ folder",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFiles(t)
			c := f.pin("/file", tt.body(t), component{Name: nameEmulator, Version: "1", Place: sdkDir + "/emulator"})
			tt.change(&c)

			in := installer{dir: t.TempDir()}

			err := in.install(t.Context(), []component{c})
			require.ErrorContains(t, err, tt.want)
			assert.NoDirExists(t, filepath.Join(in.dir, "sdk", "emulator"))
			assert.NoFileExists(t, filepath.Join(in.dir, "escaped"))
			assert.NoFileExists(t, filepath.Join(in.dir, installedFile))
		})
	}
}

// An install that stopped at a component takes up there: what was
// installed before it is kept and not fetched again.
func TestInstallTakesUpWhereItStopped(t *testing.T) {
	t.Parallel()

	f := newFiles(t)
	comps := testComponents(t, f)
	good := comps[1]
	comps[1].SHA256 = hex.EncodeToString(make([]byte, sha256.Size))

	in := installer{dir: t.TempDir()}

	require.ErrorContains(t, in.install(t.Context(), comps), "nothing was installed")
	assert.FileExists(t, filepath.Join(in.dir, "sdk", "platform-tools", "adb"))
	assert.False(t, in.complete(comps))

	comps[1] = good

	require.NoError(t, in.install(t.Context(), comps))
	assert.True(t, in.complete(comps))
	assert.Equal(t, 1, f.count("/tools.zip"), "the component installed before the failure is kept")
}

// A link inside the archive's own tree is kept as a link.
func TestInstallKeepsALinkInsideItsTree(t *testing.T) {
	t.Parallel()

	if !canLink(t) {
		t.Skip("this system makes no symbolic links for this user")
	}

	f := newFiles(t)
	c := f.pin("/emulator.zip", zipped(t,
		member{name: "emulator/lib64/libc.so.1", content: "lib"},
		member{name: "emulator/lib64/libc.so", content: "libc.so.1", link: true},
	), component{Name: nameEmulator, Version: "1", Root: "emulator", Place: sdkDir + "/emulator"})

	in := installer{dir: t.TempDir()}
	require.NoError(t, in.install(t.Context(), []component{c}))

	target, err := os.Readlink(filepath.Join(in.dir, "sdk", "emulator", "lib64", "libc.so"))
	require.NoError(t, err)
	assert.Equal(t, "libc.so.1", target)
}

func canLink(t *testing.T) bool {
	t.Helper()

	dir := t.TempDir()

	return os.Symlink("x", filepath.Join(dir, "link")) == nil
}
