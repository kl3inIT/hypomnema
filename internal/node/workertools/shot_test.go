package workertools

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
)

// Scenario (issue #1059): a worker keeps the phone's screen as a file of its
// worktree with save_to. The call reaches the phone without save_to, the
// picture is written where it was asked, in the format its name says, and
// the answer is the path, not the picture. A path out of the worktree, or a
// name that is not a picture's, is refused before the phone is called.
func TestAWorkerSavesThePhonesScreenInItsWorktree(t *testing.T) {
	t.Parallel()

	var picture bytes.Buffer
	require.NoError(t, jpeg.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 4, 6)), nil))

	cp := &controlPlane{phone: true, result: `{"content":[{"type":"image","data":"` +
		base64.StdEncoding.EncodeToString(picture.Bytes()) + `","mimeType":"image/jpeg"},` +
		`{"type":"text","text":"Tap a point with device_tap."}],"isError":false}`}
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)

	worktree := t.TempDir()
	dir := t.TempDir()
	require.NoError(t, bridge.SetAgent(dir, bridge.Agent{URL: srv.URL, Key: dispatchKey, Worktree: worktree}))

	c := newClientIn(t, dir)
	call := func(id int, arguments map[string]any) string {
		c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{
			"name": "device_screen", "arguments": arguments,
		}})

		return text(t, c.read())
	}

	assert.Equal(t, "Screenshot saved to: "+filepath.Join(worktree, "shots", "home.jpg"),
		call(1, map[string]any{"boxes": true, "save_to": "shots/home.jpg"}))
	require.Len(t, cp.deviceCalls, 1)
	assert.JSONEq(t, `{"boxes":true}`, string(cp.deviceCalls[0].Arguments), "save_to stays on the worker's side")

	saved, err := os.ReadFile(filepath.Join(worktree, "shots", "home.jpg")) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, picture.Bytes(), saved, "a JPEG asked as .jpg is written as it came")

	assert.Equal(t, "Screenshot saved to: "+filepath.Join(worktree, "home.png"),
		call(2, map[string]any{"save_to": filepath.Join(worktree, "home.png")}))

	saved, err = os.ReadFile(filepath.Join(worktree, "home.png")) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, "image/png", http.DetectContentType(saved), "a .png is a PNG")

	outside := filepath.Join(filepath.Dir(worktree), "out.jpg")
	require.NoError(t, os.Symlink(filepath.Dir(worktree), filepath.Join(worktree, "up")))

	// A link to a file out of the worktree that is not there yet, as mobile-mcp follows one (src/utils.ts:95-142).
	require.NoError(t, os.Symlink(outside, filepath.Join(worktree, "gone.jpg")))

	for id, asked := range []string{outside, "../out.jpg", "up/out.jpg", "up/new/out.jpg", "gone.jpg"} {
		assert.Contains(t, call(3+id, map[string]any{"save_to": asked}), "the folder you work in", asked)
		assert.NoFileExists(t, outside)
	}

	assert.Contains(t, call(9, map[string]any{"save_to": "home.gif"}), ".png, .jpg or .jpeg")
	assert.Len(t, cp.deviceCalls, 2, "a refused path does not reach the phone")

	// A file of the worktree that is a hard link of one outside is replaced, not written through, as mobile-mcp
	// writes a file beside it and renames it over (src/utils.ts:165-185).
	shared := filepath.Join(filepath.Dir(worktree), "shared.jpg")
	require.NoError(t, os.WriteFile(shared, []byte("kept"), 0o600))
	require.NoError(t, os.Link(shared, filepath.Join(worktree, "shared.jpg")))
	assert.Equal(t, "Screenshot saved to: "+filepath.Join(worktree, "shared.jpg"),
		call(10, map[string]any{"save_to": "shared.jpg"}))

	kept, err := os.ReadFile(shared) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, "kept", string(kept))

	saved, err = os.ReadFile(filepath.Join(worktree, "shared.jpg")) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, picture.Bytes(), saved)

	left, err := os.ReadDir(worktree)
	require.NoError(t, err)

	for _, entry := range left {
		assert.NotContains(t, entry.Name(), ".partial", "no half-written file is left")
	}
}

// Scenario (issue #1059, review round 1): a worker keeps a full page of the
// browser as a file. save_to goes on to the control plane, which sends the
// picture agent-browser saved whole (internal/controlplane/browser), and the
// answer, larger than one with an image for the agent to see, is written in
// the worktree.
func TestAWorkerSavesALargeScreenshotOfTheBrowser(t *testing.T) {
	t.Parallel()

	page := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 20<<20)...)
	cp := &controlPlane{result: `{"content":[{"type":"image","data":"` + base64.StdEncoding.EncodeToString(page) +
		`","mimeType":"image/png"}],"isError":false}`}
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)

	worktree := t.TempDir()
	dir := t.TempDir()
	require.NoError(t, bridge.SetAgent(dir, bridge.Agent{URL: srv.URL, Key: dispatchKey, Worktree: worktree}))

	c := newClientIn(t, dir)
	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "agent_browser_screenshot", "arguments": map[string]any{"fullPage": true, "save_to": "page.png"},
	}})
	assert.Equal(t, "Screenshot saved to: "+filepath.Join(worktree, "page.png"), text(t, c.read()))

	require.Len(t, cp.calls, 1)
	assert.JSONEq(t, `{"fullPage":true,"save_to":"page.png"}`, string(cp.calls[0].Arguments),
		"the control plane is told to send the whole picture")

	saved, err := os.ReadFile(filepath.Join(worktree, "page.png")) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, page, saved)
}

// Scenario (issue #1059, review round 2): paths in the worktree that are
// saved to as they were before the write went beside the name: a name as long
// as a file's name may be, and a link whose target is relative to a folder
// reached through another link.
func TestAWorkerSavesToLongNamesAndLinksThroughAnAlias(t *testing.T) {
	t.Parallel()

	var picture bytes.Buffer
	require.NoError(t, jpeg.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 4, 6)), nil))

	cp := &controlPlane{phone: true, result: `{"content":[{"type":"image","data":"` +
		base64.StdEncoding.EncodeToString(picture.Bytes()) + `","mimeType":"image/jpeg"}],"isError":false}`}
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)

	worktree := t.TempDir()
	dir := t.TempDir()
	require.NoError(t, bridge.SetAgent(dir, bridge.Agent{URL: srv.URL, Key: dispatchKey, Worktree: worktree}))

	c := newClientIn(t, dir)
	call := func(id int, saveTo string) string {
		c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{
			"name": "device_screen", "arguments": map[string]any{"save_to": saveTo},
		}})

		return text(t, c.read())
	}

	long := strings.Repeat("s", 244) + ".jpg"
	assert.Equal(t, "Screenshot saved to: "+filepath.Join(worktree, long), call(1, long))
	assert.FileExists(t, filepath.Join(worktree, long))

	// a is x/y; x/y/link.jpg is ../../inside.jpg, which from x/y is the worktree's own inside.jpg.
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, "x", "y"), 0o750))
	require.NoError(t, os.Symlink(filepath.Join("x", "y"), filepath.Join(worktree, "a")))
	require.NoError(t, os.Symlink(filepath.Join("..", "..", "inside.jpg"), filepath.Join(worktree, "x", "y", "link.jpg")))
	assert.Equal(t, "Screenshot saved to: "+filepath.Join(worktree, "a", "link.jpg"), call(2, "a/link.jpg"))
	assert.Len(t, cp.deviceCalls, 2)
}
