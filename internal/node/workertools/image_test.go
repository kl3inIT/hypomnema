package workertools

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// png is the start of a PNG file, enough to be taken for one.
const png = "\x89PNG\r\n\x1a\n...."

// Scenario: a worker edits an image it has on disk (issue #492). The file
// it names is read on its machine and sent whole with its type; the image
// made comes back saved where it asked, with the image's own extension, and
// the answer says what made it and where it is, not the image. Without a
// path the image is a new file in the temporary folder.
func TestAWorkerEditsAnImageOnItsMachine(t *testing.T) {
	t.Parallel()

	made := base64.StdEncoding.EncodeToString([]byte(png + "made"))
	cp := &controlPlane{result: `{"content":[{"type":"text","text":"Made by codex with gpt-image-2."},` +
		`{"type":"image","data":"` + made + `","mimeType":"image/png"}],"isError":false}`}
	c := browsing(t, cp)

	work := t.TempDir()
	photo := filepath.Join(work, "photo.png")
	require.NoError(t, os.WriteFile(photo, []byte(png+"photo"), 0o600))

	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "generate_image", "arguments": map[string]any{
			"prompt": "make the sky blue", "images": []string{photo}, "path": filepath.Join(work, "out", "sky.jpg"),
			"size": "1024x1024",
		},
	}})

	want := filepath.Join(work, "out", "sky.png")
	assert.Equal(t, "Made by codex with gpt-image-2.\nSaved to "+want, text(t, c.read()))

	saved, err := os.ReadFile(want) //nolint:gosec // the test's own file
	require.NoError(t, err)
	assert.Equal(t, png+"made", string(saved))

	require.Len(t, cp.calls, 1)
	assert.Equal(t, "generate_image", cp.calls[0].Tool)

	var sent agenttool.ImageCall
	require.NoError(t, json.Unmarshal(cp.calls[0].Arguments, &sent))
	assert.Equal(t, agenttool.ImageCall{Prompt: "make the sky blue", Size: "1024x1024", Images: []agenttool.ImageInput{
		{MIME: "image/png", Data: []byte(png + "photo")},
	}}, sent)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "generate_image", "arguments": map[string]any{"prompt": "a fox"},
	}})

	answer := text(t, c.read())
	temp, ok := strings.CutPrefix(answer, "Made by codex with gpt-image-2.\nSaved to ")
	require.True(t, ok, answer)
	t.Cleanup(func() { _ = os.Remove(temp) })
	assert.Equal(t, os.TempDir(), filepath.Dir(temp))
	assert.Equal(t, ".png", filepath.Ext(temp))
}

// A file that cannot be sent says why, in words the worker can act on, and
// reaches no provider; an answer that is the tool's error is the worker's
// as the control plane gave it.
func TestAnImageThatCannotBeSentSaysWhy(t *testing.T) {
	t.Parallel()

	const refused = `{"content":[{"type":"text","text":"no image provider made the image: codex gpt-image-2: ` +
		`rate limited"}],"isError":true}`

	cp := &controlPlane{result: refused}
	c := browsing(t, cp)

	work := t.TempDir()
	notes := filepath.Join(work, "notes.txt")
	require.NoError(t, os.WriteFile(notes, []byte("not a picture"), 0o600))

	for _, tc := range []struct{ image, says string }{
		{filepath.Join(work, "missing.png"), "file not found: "},
		{notes, "is not a PNG, JPEG, WebP or GIF image"},
		{work, "is not a file"},
	} {
		c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
			"name": "generate_image", "arguments": map[string]any{"prompt": "x", "images": []string{tc.image}},
		}})
		assert.Contains(t, text(t, c.read()), tc.says)
	}

	assert.Empty(t, cp.calls)

	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "generate_image", "arguments": map[string]any{"prompt": "a fox"},
	}})

	raw, err := json.Marshal(c.read()["result"])
	require.NoError(t, err)
	assert.JSONEq(t, refused, string(raw))
}
