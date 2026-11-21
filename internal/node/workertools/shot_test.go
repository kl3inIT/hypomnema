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

	for id, asked := range []string{outside, "../out.jpg", "up/out.jpg"} {
		assert.Contains(t, call(3+id, map[string]any{"save_to": asked}), "the folder you work in", asked)
		assert.NoFileExists(t, outside)
	}

	assert.Contains(t, call(6, map[string]any{"save_to": "home.gif"}), ".png, .jpg or .jpeg")
	assert.Len(t, cp.deviceCalls, 2, "a refused path does not reach the phone")
}
