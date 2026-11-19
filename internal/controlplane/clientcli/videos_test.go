package clientcli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
)

// cliVideos is one video of two revisions, the first in review, kept in memory; it records what it was asked.
type cliVideos struct {
	v     videos.Video
	file  string
	asked []string
	keys  []string
}

func (c *cliVideos) List(context.Context) ([]videos.Video, error) { return []videos.Video{c.v}, nil }

func (c *cliVideos) Get(_ context.Context, id string) (videos.Video, error) {
	if id != c.v.ID {
		return videos.Video{}, videos.ErrNotFound
	}

	return c.v, nil
}

func (c *cliVideos) Make(_ context.Context, req videos.MakeRequest) (videos.Video, videos.Revision, error) {
	c.asked = append(c.asked, "make "+req.Take+" "+req.Aspect+" "+req.Voice)
	c.keys = append(c.keys, req.RequestID)

	return c.v, c.v.Latest(), nil
}

func (c *cliVideos) Approve(_ context.Context, id string, rev int) (videos.Video, error) {
	c.asked = append(c.asked, "approve")
	c.v.Revisions[rev-1].State = videos.Approved

	return c.v, nil
}

func (c *cliVideos) RequestChanges(_ context.Context, _ string, _ int, req videos.ChangeRequest,
) (videos.Video, error) {
	c.asked = append(c.asked, "changes "+req.Note)
	c.v.Revisions[0].Note = req.Note
	c.v.Revisions = append(c.v.Revisions, videos.Revision{Rev: 2, Parent: 1, State: videos.Queued, Lines: req.Lines,
		Aspect: "9:16", Captions: true, Warnings: []string{}})

	return c.v, nil
}

func (c *cliVideos) Delete(context.Context, string) error {
	c.asked = append(c.asked, "delete")

	return nil
}

func (c *cliVideos) File(context.Context, string, int) (string, error)      { return c.file, nil }
func (c *cliVideos) Thumbnail(context.Context, string, int) (string, error) { return c.file, nil }

// Scenario: the owner makes a take into a video, reads it, asks for changes with edited lines, approves,
// downloads and deletes it, with the CLI (issue #818).
func TestTheOwnerMakesAndApprovesVideosWithTheCLI(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "v.mp4")
	require.NoError(t, os.WriteFile(file, []byte("the video"), 0o600))

	fake := &cliVideos{file: file, v: videos.Video{
		ID: "vid_abc", Take: "tk_0123456789abcdef", Title: "Đăng nhập", MadeBy: "d-1", MadeByName: "worker",
		CreatedAt: time.Now(),
		Revisions: []videos.Revision{{Rev: 1, State: videos.Review, Lines: []string{"Mở", "Chạm"}, Aspect: "9:16",
			Captions: true, Provider: "soniox", UsedVoice: "Linh", LengthMs: 65_000, Warnings: []string{}}},
	}}

	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithVideos(fake) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "videos", "make", "tk_0123456789abcdef", "--aspect", "16:9", "--voice", "Linh")
	require.NoError(t, err)
	assert.Contains(t, out, "vid_abc  r1 review 1:05")
	assert.Contains(t, out, "1. Mở")
	assert.Equal(t, "make tk_0123456789abcdef 16:9 Linh", fake.asked[0])
	assert.NotEmpty(t, fake.keys[0], "a make takes an idempotency key")

	out, err = w.cmd(t, "videos", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "vid_abc  r1 review 1:05  tk_0123456789abcdef")

	lines := filepath.Join(t.TempDir(), "lines.txt")
	require.NoError(t, os.WriteFile(lines, []byte("Mở\nChạm nút\n"), 0o600))

	out, err = w.cmd(t, "videos", "request-changes", "vid_abc", "1", "--note", "Sửa câu 2", "--lines-file", lines)
	require.NoError(t, err)
	assert.Contains(t, out, "note: Sửa câu 2")
	assert.Contains(t, out, "r2 queued")
	assert.Contains(t, out, "2. Chạm nút")

	_, err = w.cmd(t, "videos", "request-changes", "vid_abc", "1")
	require.ErrorIs(t, err, ErrUsage)

	out, err = w.cmd(t, "videos", "approve", "vid_abc", "1")
	require.NoError(t, err)
	assert.Contains(t, out, "approved r1")

	mp4 := filepath.Join(t.TempDir(), "out.mp4")
	out, err = w.cmd(t, "videos", "download", "vid_abc", "1", "--out", mp4)
	require.NoError(t, err)
	assert.Contains(t, out, "saved vid_abc r1")

	raw, err := os.ReadFile(mp4) //nolint:gosec // The test's own file.
	require.NoError(t, err)
	assert.Equal(t, "the video", string(raw))

	out, err = w.cmd(t, "videos", "delete", "vid_abc")
	require.NoError(t, err)
	assert.Contains(t, out, "deleted video vid_abc")
}
