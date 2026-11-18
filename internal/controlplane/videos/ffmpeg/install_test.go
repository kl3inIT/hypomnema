//go:build unix

package ffmpeg

import (
	"archive/tar"
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
	"github.com/ulikunitz/xz"
)

// fakeFFmpeg answers -filters and -encoders as ffmpeg does, listing what is given.
func fakeFFmpeg(filters, encoders string) string {
	return "#!/bin/sh\ncase \"$2\" in\n" +
		"-filters) printf 'Filters:\\n ---\\n" + filters + "' ;;\n" +
		"-encoders) printf 'Encoders:\\n ------\\n" + encoders + "' ;;\n" +
		"esac\n"
}

const (
	allFilters = " .. freezedetect      V->V       Detects frozen video input.\\n" +
		" .. subtitles         V->V       Render text subtitles.\\n" +
		" .. ass               V->V       Render ASS subtitles.\\n"
	allEncoders = " V....D libx264              libx264 H.264\\n A....D aac                  AAC\\n"
)

// tarXZ is a build's file: root/bin/ffmpeg, root/bin/ffprobe, root/LICENSE.txt and an ffplay that is not kept.
func tarXZ(t *testing.T, root, ffmpeg string) []byte {
	t.Helper()

	var buf bytes.Buffer

	unxz, err := xz.NewWriter(&buf)
	require.NoError(t, err)

	w := tar.NewWriter(unxz)

	for name, content := range map[string]string{
		"bin/ffmpeg": ffmpeg, "bin/ffprobe": "#!/bin/sh\necho ffprobe version n0\n", "bin/ffplay": "#!/bin/sh\n",
		"LICENSE.txt": "GPL",
	} {
		require.NoError(t, w.WriteHeader(&tar.Header{
			Name: root + "/" + name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}))

		_, err := w.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, w.Close())
	require.NoError(t, unxz.Close())

	return buf.Bytes()
}

// served is a build whose file for this machine is body, served by a test server that counts its fetches.
func served(t *testing.T, version, root string, body []byte) (build, *atomic.Int32) {
	t.Helper()

	var fetched atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetched.Add(1)

		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	sum := sha256.Sum256(body)

	return build{Version: version, Releases: map[string]release{runtime.GOOS + "/" + runtime.GOARCH: {
		URL: server.URL, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body)), Root: root,
	}}}, &fetched
}

func installer(dir string, b build) *Installer {
	in := New(Config{Dir: dir})
	in.build = b

	return in
}

// The build is unpacked, checked and put in place with the fonts; ffplay and
// the documents are not kept, and a build in place is not fetched again.
func TestEnsureInstallsTheBuildOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root := "ffmpeg-n1-linux64-gpl"
	b, fetched := served(t, "n1", root, tarXZ(t, root, fakeFFmpeg(allFilters, allEncoders)))

	tools, err := installer(dir, b).Ensure(t.Context())
	require.NoError(t, err)
	assert.Equal(t, Tools{
		FFmpeg: filepath.Join(dir, "n1", "ffmpeg"), FFprobe: filepath.Join(dir, "n1", "ffprobe"),
		FontsDir: filepath.Join(dir, "fonts"), Version: "n1",
	}, tools)

	entries, err := os.ReadDir(filepath.Join(dir, "n1"))
	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	assert.Equal(t, []string{"LICENSE.txt", "ffmpeg", "ffprobe"}, names)
	assert.FileExists(t, filepath.Join(dir, "fonts", "BeVietnamPro-SemiBold.ttf"))
	assert.FileExists(t, filepath.Join(dir, "fonts", "OFL.txt"))
	assert.NoDirExists(t, filepath.Join(dir, downloadsDir))

	current, err := os.ReadFile(filepath.Join(dir, currentFile)) //nolint:gosec // The test's folder.
	require.NoError(t, err)
	assert.Equal(t, "n1\n", string(current))

	// Another control plane start: what is in place is used as it is.
	again, err := installer(dir, b).Ensure(t.Context())
	require.NoError(t, err)
	assert.Equal(t, tools, again)
	assert.Equal(t, int32(1), fetched.Load())

	got, ok := Installed(dir)
	require.True(t, ok)
	assert.Equal(t, "n1", got.Version)
}

// A file that is not the pinned one is refused and nothing is installed.
func TestEnsureRefusesAFileThatIsNotPinned(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	b, _ := served(t, "n1", "root", tarXZ(t, "root", fakeFFmpeg(allFilters, allEncoders)))

	rel := b.Releases[runtime.GOOS+"/"+runtime.GOARCH]
	rel.SHA256 = "00" + rel.SHA256[2:]
	b.Releases[runtime.GOOS+"/"+runtime.GOARCH] = rel

	_, err := installer(dir, b).Ensure(t.Context())
	require.ErrorContains(t, err, "checksum")
	assert.NoDirExists(t, filepath.Join(dir, "n1"))
	assert.NoFileExists(t, filepath.Join(dir, currentFile))
}

// A new build that fails the check is removed, and the build before stays in
// use; one that passes replaces it and the one before is removed.
func TestANewBuildReplacesTheOldOnlyOnceItPasses(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	old, _ := served(t, "n1", "r1", tarXZ(t, "r1", fakeFFmpeg(allFilters, allEncoders)))

	_, err := installer(dir, old).Ensure(t.Context())
	require.NoError(t, err)

	// n2 has no libass.
	broken, _ := served(t, "n2", "r2", tarXZ(t, "r2", fakeFFmpeg(" .. freezedetect V->V x\\n", allEncoders)))

	tools, err := installer(dir, broken).Ensure(t.Context())
	require.NoError(t, err, "the build before is used")
	assert.Equal(t, "n1", tools.Version)
	assert.Equal(t, filepath.Join(dir, "n1", "ffmpeg"), tools.FFmpeg)
	assert.NoDirExists(t, filepath.Join(dir, "n2"))
	assert.NoDirExists(t, filepath.Join(dir, "n2.tmp"))

	good, _ := served(t, "n3", "r3", tarXZ(t, "r3", fakeFFmpeg(allFilters, allEncoders)))

	tools, err = installer(dir, good).Ensure(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "n3", tools.Version)
	assert.NoDirExists(t, filepath.Join(dir, "n1"))

	got, ok := Installed(dir)
	require.True(t, ok)
	assert.Equal(t, "n3", got.Version)
}

// The check names what a video needs that the build lacks.
func TestCheckNamesWhatIsMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	probe := filepath.Join(dir, "ffprobe")
	require.NoError(t, os.WriteFile(probe, []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // A command for the test.

	for _, tc := range []struct {
		filters, encoders, want string
	}{
		{allFilters, allEncoders, ""},
		{" .. subtitles V->V x\\n .. ass V->V x\\n", allEncoders, "ffmpeg has no filter freezedetect"},
		{allFilters, " A....D aac AAC\\n", "ffmpeg has no encoder libx264"},
	} {
		bin := filepath.Join(dir, "ffmpeg")
		require.NoError(t, os.WriteFile(bin, []byte(fakeFFmpeg(tc.filters, tc.encoders)), 0o700)) //nolint:gosec // A command.

		err := Check(t.Context(), Tools{FFmpeg: bin, FFprobe: probe})
		if tc.want == "" {
			require.NoError(t, err)
		} else {
			require.EqualError(t, err, tc.want)
		}
	}
}

// The pinned build has a file for each system the control plane ships for.
func TestThePinCoversTheControlPlanesSystems(t *testing.T) {
	t.Parallel()

	b := pinned()
	for _, system := range []string{"linux/amd64", "linux/arm64"} {
		rel, ok := b.Releases[system]
		require.True(t, ok, system)
		assert.Len(t, rel.SHA256, 64)
		assert.Positive(t, rel.Bytes)
		assert.Contains(t, rel.URL, rel.Root+".tar.xz")
	}
}
