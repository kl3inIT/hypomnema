package clientcli

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
)

// Scenario: in its directory, a Bot writes a dashboard folder and shows it with `artifact show --path`; it gets
// the id back and what the folder left out. It adds a region and shows it again with --id: revision 2. Showing
// the same again changes nothing. A PDF is shown as one file. The owner lists the chat's artifacts, reads one's
// revisions and downloads the folder as a zip and the PDF as itself (issue #402).
func TestABotShowsAnArtifactFromItsDirectory(t *testing.T) { //nolint:paralleltest // changes the directory
	w := newWorld(t)
	w.login(t, bearertoken.Owner)

	_, _, err := w.svc.AddToken(t.Context(), "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(t.Context(), "", "")
	require.NoError(t, err)

	secret, err := w.svc.AddBotToken(t.Context(), "pi-coordinator", chat.ID, chat.Bot)
	require.NoError(t, err)

	pi := &world{svc: w.svc, url: w.url, home: filepath.Join(t.TempDir(), "pi")}
	pi.loginWith(t, secret)

	dir := t.TempDir()
	t.Chdir(dir)

	write := func(p, s string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(s), 0o600))
	}

	write("dash/index.html", "<h1>Sales</h1><script src=app.js></script>")
	write("dash/app.js", "draw()")
	write("dash/.cache", "x")
	write("report.pdf", "%PDF-1.7 report")

	out, err := pi.cmd(t, "artifact", "show", "--title", "Sales, week 46", "--path", "dash")
	require.NoError(t, err)

	id := regexp.MustCompile(`^art_[a-z2-7]{26}`).FindString(out)
	require.NotEmpty(t, id, out)
	assert.Contains(t, out, id+" revision 1: Sales, week 46 (html, ")
	assert.Contains(t, out, "Left out .cache (hidden).")

	write("dash/index.html", "<h1>Sales</h1><h2>By region</h2><script src=app.js></script>")

	out, err = pi.cmd(t, "artifact", "show", "--id", id, "--path", "dash")
	require.NoError(t, err)
	assert.Contains(t, out, id+" revision 2: Sales, week 46")

	out, err = pi.cmd(t, "artifact", "show", "--id", id, "--path", "dash")
	require.NoError(t, err)
	assert.Contains(t, out, "unchanged; nothing shown")

	out, err = pi.cmd(t, "artifact", "show", "--title", "Report", "--path", "report.pdf")
	require.NoError(t, err)
	pdf := regexp.MustCompile(`^art_[a-z2-7]{26}`).FindString(out)
	assert.Contains(t, out, "(pdf, ")

	_, err = pi.cmd(t, "artifact", "show", "--path", "dash")
	require.ErrorIs(t, err, ErrUsage, "a new artifact needs a title")

	_, err = pi.cmd(t, "artifact", "show", "--title", "x", "--path", filepath.Join(t.TempDir(), "x.html"))
	require.Error(t, err)

	out, err = w.cmd(t, "artifact", "list", "--chat", chat.ID)
	require.NoError(t, err)
	assert.Regexp(t, pdf+`  r1  pdf  .*Report\n`+id+`  r2  html .*Sales, week 46\n`, out)

	out, err = w.cmd(t, "artifact", "info", id)
	require.NoError(t, err)
	assert.Contains(t, out, "  revision 2  ")
	assert.Contains(t, out, "  revision 1  ")
	assert.Contains(t, out, "    app.js  6 B  text/javascript; charset=utf-8\n")

	_, err = w.cmd(t, "artifact", "download", id, "--rev", "1", "--out", "dash.zip")
	require.NoError(t, err)

	zip, err := os.ReadFile("dash.zip")
	require.NoError(t, err)
	assert.Equal(t, "PK", string(zip[:2]))

	_, err = w.cmd(t, "artifact", "download", pdf, "--out", "got.pdf")
	require.NoError(t, err)

	got, err := os.ReadFile("got.pdf")
	require.NoError(t, err)
	assert.Equal(t, "%PDF-1.7 report", string(got))
}
