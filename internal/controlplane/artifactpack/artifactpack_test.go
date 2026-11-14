package artifactpack_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/artifactpack"
)

var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

// Scenario: a Bot shows one file; what it is shown as comes from its bytes and its name, as Onyx picks a preview
// variant: an .html page, a PDF by its signature whatever its name, a PNG, an SVG, and anything else to download.
// A file named .html that is not text is a download, not a page.
func TestAFilesTypeComesFromItsBytesAndName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data []byte
		want artifactpack.Type
		mime string
	}{
		{"dash.html", []byte("<!doctype html><h1>Sales</h1>"), artifactpack.TypeHTML, "text/html; charset=utf-8"},
		{"report", []byte("%PDF-1.7\n..."), artifactpack.TypePDF, "application/pdf"},
		{"chart.png", pngBytes, artifactpack.TypeImage, "image/png"},
		{"flow.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), artifactpack.TypeImage, "image/svg+xml"},
		{"Báo cáo.xlsx", []byte("PK\x03\x04 a sheet"), artifactpack.TypeFile, "application/octet-stream"},
		{"broken.html", []byte{0xff, 0xfe, 0x00}, artifactpack.TypeFile, "application/octet-stream"},
	}

	for _, c := range cases {
		p, err := artifactpack.Check([]artifactpack.File{{Path: c.name, Data: c.data}})
		require.NoError(t, err, c.name)
		assert.Equal(t, c.want, p.Type, c.name)
		assert.Equal(t, c.name, p.Entry, c.name)
		assert.Equal(t, c.mime, artifactpack.MediaType(p, p.Files[0]), c.name)
	}
}

// Scenario: a folder is a page with its data beside index.html. Without index.html at its top, or with a file a
// page does not load, it is refused with what to do; its files are served by their names' types.
func TestAFolderIsAPageWithItsData(t *testing.T) {
	t.Parallel()

	p, err := artifactpack.Check([]artifactpack.File{
		{Path: "data/sales.json", Data: []byte(`[1,2]`)},
		{Path: "index.html", Data: []byte("<script src=app.js></script>")},
		{Path: "app.js", Data: []byte("fetch('data/sales.json')")},
	})
	require.NoError(t, err)
	assert.Equal(t, artifactpack.TypeHTML, p.Type)
	assert.Equal(t, "index.html", p.Entry)
	assert.Equal(t, []string{"app.js", "data/sales.json", "index.html"}, paths(p.Files), "sorted by path")
	assert.Equal(t, "application/json; charset=utf-8", artifactpack.MediaType(p, p.Files[1]))

	_, err = artifactpack.Check([]artifactpack.File{{Path: "a.html"}, {Path: "b.css"}})
	require.ErrorContains(t, err, "needs an index.html at its top")

	_, err = artifactpack.Check([]artifactpack.File{{Path: "index.html"}, {Path: "run.exe"}})
	require.ErrorContains(t, err, "cannot carry run.exe")

	_, err = artifactpack.Check([]artifactpack.File{{Path: "index.html"}, {Path: "../etc/passwd.txt"}})
	require.ErrorIs(t, err, artifactpack.ErrInvalid)
}

// Scenario: the limits are OpenMausBot's: a page is at most 256 KB, all the files 5 MB, a folder 200 files. Over
// any, the Bot is told the limit; at the limit it passes. The same files make the same digest whatever their order.
func TestAnArtifactIsBounded(t *testing.T) {
	t.Parallel()

	page := func(n int) []byte { return bytes.Repeat([]byte("a"), n) }

	_, err := artifactpack.Check([]artifactpack.File{{Path: "p.html", Data: page(artifactpack.MaxHTML)}})
	require.NoError(t, err)

	_, err = artifactpack.Check([]artifactpack.File{{Path: "p.html", Data: page(artifactpack.MaxHTML + 1)}})
	require.ErrorIs(t, err, artifactpack.ErrTooLarge)
	require.ErrorContains(t, err, "256 KB")

	_, err = artifactpack.Check([]artifactpack.File{
		{Path: "index.html", Data: []byte("<p>")}, {Path: "big.json", Data: page(artifactpack.MaxTotal)},
	})
	require.ErrorIs(t, err, artifactpack.ErrTooLarge)
	require.ErrorContains(t, err, "5 MB")

	many := []artifactpack.File{{Path: "index.html"}}
	for i := range artifactpack.MaxFiles {
		many = append(many, artifactpack.File{Path: "d/" + strings.Repeat("x", i%50+1) + string(rune('a'+i/50)) + ".json"})
	}

	_, err = artifactpack.Check(many)
	require.ErrorIs(t, err, artifactpack.ErrTooLarge)

	one, err := artifactpack.Check([]artifactpack.File{{Path: "index.html", Data: []byte("1")}, {Path: "a.css"}})
	require.NoError(t, err)
	two, err := artifactpack.Check([]artifactpack.File{{Path: "a.css"}, {Path: "index.html", Data: []byte("1")}})
	require.NoError(t, err)
	assert.Equal(t, one.Digest, two.Digest)

	require.ErrorIs(t, artifactpack.CheckTitle(strings.Repeat("é", artifactpack.MaxTitle+1)), artifactpack.ErrInvalid)
	require.ErrorIs(t, artifactpack.CheckTitle("two\nlines"), artifactpack.ErrInvalid)
	require.NoError(t, artifactpack.CheckTitle(strings.Repeat("é", artifactpack.MaxTitle)))
}

// Scenario: the files travel as a tar the control plane reads back whole; an archive that holds a link is refused
// before its files are looked at.
func TestTheArchiveCarriesRegularFilesOnly(t *testing.T) {
	t.Parallel()

	files := []artifactpack.File{{Path: "index.html", Data: []byte("<p>hi")}, {Path: "d/x.csv", Data: []byte("a,b")}}
	body, err := artifactpack.Tar(files)
	require.NoError(t, err)

	p, err := artifactpack.FromTar(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"d/x.csv", "index.html"}, paths(p.Files))
	assert.Equal(t, []byte("a,b"), p.Files[0].Data)

	_, err = artifactpack.FromTar([]byte("not a tar at all, not even close to 512 bytes"))
	require.ErrorIs(t, err, artifactpack.ErrInvalid)
}

// Scenario: the Bot names a folder in its directory. Hidden files, package folders, files a page does not load
// and a link out of the folder are left out and named; something outside the directory is refused, and so is a
// folder without index.html.
func TestReadTakesWhatAPageCarries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outside := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, p), []byte(s), 0o600))
	}

	write("dash/index.html", "<p>")
	write("dash/data/sales.csv", "a,b")
	write("dash/.env", "SECRET=1")
	write("dash/node_modules/x/index.js", "x")
	write("dash/notes.docx", "doc")
	write("report.pdf", "%PDF-1.4")
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.json"), []byte("{}"), 0o600))

	if runtime.GOOS != "windows" {
		require.NoError(t, os.Symlink(filepath.Join(outside, "secret.json"), filepath.Join(dir, "dash", "out.json")))
	}

	files, skipped, err := artifactpack.Read(dir, "dash")
	require.NoError(t, err)
	assert.Equal(t, []string{"data/sales.csv", "index.html"}, paths(files))

	note := artifactpack.SkippedNote(skipped)
	assert.Contains(t, note, ".env (hidden)")
	assert.Contains(t, note, "node_modules/ (packages)")
	assert.Contains(t, note, "notes.docx (not a kind of file a page loads)")

	if runtime.GOOS != "windows" {
		assert.Contains(t, note, "out.json (a link out of the folder)")
	}

	files, _, err = artifactpack.Read(dir, "report.pdf")
	require.NoError(t, err)
	assert.Equal(t, []string{"report.pdf"}, paths(files))

	_, _, err = artifactpack.Read(dir, filepath.Join(outside, "secret.json"))
	require.ErrorContains(t, err, "only a file or folder in your directory")

	_, _, err = artifactpack.Read(dir, "nothing-here")
	require.ErrorContains(t, err, "nothing is at nothing-here")

	write("plain/a.css", "")
	_, _, err = artifactpack.Read(dir, "plain")
	require.ErrorContains(t, err, "no index.html at its top")
}

func paths(files []artifactpack.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}

	return out
}
