package clientcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
)

// cliVideos is one video of two revisions, the first in review, kept in memory; it records what it was asked.
type cliVideos struct {
	v     videos.Video
	file  string
	asked []string
	keys  []string
	// looks are the looks sent, as JSON, and previews the samples asked for.
	looks    []string
	previews []videos.PreviewRequest
	tracks   []videos.Track
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
	c.looks = append(c.looks, lookJSON(req.Look))

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

func (c *cliVideos) ChangeLook(_ context.Context, id string, rev int, req videos.LookChange,
) (videos.Video, videos.Revision, error) {
	c.asked = append(c.asked, fmt.Sprintf("look %s %d", id, rev))
	c.keys = append(c.keys, req.RequestID)
	c.looks = append(c.looks, lookJSON(req.Look))

	return c.v, c.v.Latest(), nil
}

func (c *cliVideos) Retry(_ context.Context, id string, rev int, _, requestID string,
) (videos.Video, videos.Revision, error) {
	c.asked = append(c.asked, fmt.Sprintf("retry %s %d", id, rev))
	c.keys = append(c.keys, requestID)

	return c.v, c.v.Latest(), nil
}

func (c *cliVideos) Preview(_ context.Context, req videos.PreviewRequest) ([]byte, error) {
	c.previews = append(c.previews, req)
	c.looks = append(c.looks, lookJSON(req.Look))

	return []byte{0xFF, 0xD8, 0xFF}, nil
}

func (c *cliVideos) Looks(context.Context) (videos.Catalog, error) {
	return videos.Catalog{
		Presets:     []videos.PresetInfo{{ID: "clean", Name: "Clean"}, {ID: "legacy", Name: "Classic"}},
		Backgrounds: []string{"midnight"}, Chromes: []compose.Chrome{compose.ChromeFull, compose.ChromeAndroid},
		Default: videos.DefaultLook(),
	}, nil
}

func lookJSON(l *videos.LookInput) string {
	raw, _ := json.Marshal(l) //nolint:errchkjson // The test's own value.

	return string(raw)
}

func (c *cliVideos) Music(context.Context) (videos.Library, error) {
	return videos.Library{Tracks: c.tracks, UsedBytes: 1500, QuotaBytes: videos.MusicQuotaBytes}, nil
}

func (c *cliVideos) UploadMusic(_ context.Context, name string, size int64, body io.Reader) (videos.Track, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return videos.Track{}, err
	}

	c.asked = append(c.asked, fmt.Sprintf("upload %s %d %s", name, size, raw))
	t := videos.Track{ID: "mus_0123456789abcdefghijklmnop", Name: name, MediaType: "audio/mpeg", LengthMs: 75_000,
		Bytes: size, CreatedAt: time.Now()}
	c.tracks = append(c.tracks, t)

	return t, nil
}

func (c *cliVideos) MusicAudio(context.Context, string) (string, string, error) {
	return c.file, "audio/mpeg", nil
}

func (c *cliVideos) DeleteMusic(_ context.Context, id string) error {
	c.asked = append(c.asked, "delete music "+id)

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

// Scenario: the owner chooses a video's look with the CLI (issue #908, row 16): only the sections whose flags are
// given are sent, and in them only the fields flagged; a look is changed, a failed revision made again, the
// catalog listed and a sample saved.
func TestTheCLISendsOnlyTheFlaggedLook(t *testing.T) {
	t.Parallel()

	fake := &cliVideos{v: videos.Video{
		ID: "vid_abc", Take: "tk_0123456789abcdef", Title: "Đăng nhập", CreatedAt: time.Now(),
		Revisions: []videos.Revision{{Rev: 1, State: videos.Review, Lines: []string{"Mở"}, Aspect: "9:16",
			Captions: true, Warnings: []string{}, Look: videos.DefaultLook()}},
	}}

	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithVideos(fake) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "videos", "make", "tk_0123456789abcdef")
	require.NoError(t, err)
	assert.Contains(t, out, "captions clean, android on midnight")
	assert.Equal(t, "null", fake.looks[0], "no flag, no look")

	_, err = w.cmd(t, "videos", "make", "tk_0123456789abcdef", "--caption-preset", "mono")
	require.NoError(t, err)
	assert.JSONEq(t, `{"captions":{"preset":"mono"}}`, fake.looks[1], "the captions alone")

	_, err = w.cmd(t, "videos", "make", "tk_0123456789abcdef", "--chrome", "iphone", "--shadow=false",
		"--padding", "0", "--background-colors", "#112233,#445566")
	require.NoError(t, err)
	assert.JSONEq(t, `{"frame":{"chrome":"iphone","padding":0,"shadow":false},`+
		`"background":{"colors":["#112233","#445566"]}}`, fake.looks[2])

	_, err = w.cmd(t, "videos", "look", "vid_abc", "1", "--caption-position", "top", "--safe-top")
	require.NoError(t, err)
	assert.Equal(t, "look vid_abc 1", fake.asked[len(fake.asked)-1])
	assert.JSONEq(t, `{"captions":{"position":"top"},"frame":{"safe_top":true}}`, fake.looks[3])
	assert.NotEmpty(t, fake.keys[len(fake.keys)-1])

	_, err = w.cmd(t, "videos", "look", "vid_abc", "1")
	require.ErrorIs(t, err, ErrUsage, "a look change needs a part to change")

	_, err = w.cmd(t, "videos", "make", "tk_0123456789abcdef", "--chrome", "mac")
	require.Error(t, err, "the API refuses an unknown chrome")

	_, err = w.cmd(t, "videos", "retry", "vid_abc", "1")
	require.NoError(t, err)
	assert.Equal(t, "retry vid_abc 1", fake.asked[len(fake.asked)-1])

	out, err = w.cmd(t, "videos", "looks")
	require.NoError(t, err)
	assert.Contains(t, out, "clean (Clean), legacy (Classic)")
	assert.Contains(t, out, "default: captions clean, android on midnight")

	jpg := filepath.Join(t.TempDir(), "look.jpg")
	out, err = w.cmd(t, "videos", "preview", "tk_0123456789abcdef", "--rev", "1", "--aspect", "16:9",
		"--background", "ocean", "--out", jpg)
	require.NoError(t, err)
	assert.Contains(t, out, "saved the look sample")
	require.Len(t, fake.previews, 1)
	assert.Equal(t, videos.PreviewRequest{Take: "tk_0123456789abcdef", Aspect: "16:9", Rev: 1,
		Look: fake.previews[0].Look}, fake.previews[0])
	assert.JSONEq(t, `{"background":{"preset":"ocean"}}`, fake.looks[len(fake.looks)-1])

	raw, err := os.ReadFile(jpg) //nolint:gosec // The test's own file.
	require.NoError(t, err)
	assert.Equal(t, []byte{0xFF, 0xD8, 0xFF}, raw)
}

// Scenario: the owner gives a video its cards with the CLI (issue #1230, row 1): a card is sent with its title and
// the fields flagged, -no-cards sends the section with none, a card's field without its title is refused before
// anything is sent, the cards are shown in the look's line, and a sample draws one.
func TestTheCLISendsTheCards(t *testing.T) {
	t.Parallel()

	look := videos.DefaultLook()
	look.Cards = &compose.Cards{Accent: "#6c8cff", End: &compose.Card{Title: "Tải SecondBite", DurationMs: 3000}}
	fake := &cliVideos{v: videos.Video{
		ID: "vid_abc", Take: "tk_0123456789abcdef", Title: "Đăng nhập", CreatedAt: time.Now(),
		Revisions: []videos.Revision{{Rev: 1, State: videos.Review, Lines: []string{"Mở"}, Aspect: "9:16",
			Captions: true, Warnings: []string{}, Look: look}},
	}}

	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithVideos(fake) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "videos", "make", "tk_0123456789abcdef", "--title-card", "SecondBite",
		"--end-card", "Tải SecondBite", "--end-card-url", "secondbite.vn", "--end-card-ms", "4000",
		"--card-accent", "#ff3366")
	require.NoError(t, err)
	assert.JSONEq(t, `{"cards":{"accent":"#ff3366","title":{"title":"SecondBite"},`+
		`"end":{"title":"Tải SecondBite","cta_url":"secondbite.vn","duration_ms":4000}}}`, fake.looks[0])
	assert.Contains(t, out, `end card "Tải SecondBite" 3.0 s`)

	_, err = w.cmd(t, "videos", "look", "vid_abc", "1", "--no-cards")
	require.NoError(t, err)
	assert.JSONEq(t, `{"cards":{}}`, fake.looks[1])

	for _, args := range [][]string{{"--end-card-url", "secondbite.vn"}, {"--no-cards", "--title-card", "A"}} {
		_, err = w.cmd(t, append([]string{"videos", "make", "tk_0123456789abcdef"}, args...)...)
		require.ErrorIs(t, err, ErrUsage, "%v", args)
	}

	assert.Len(t, fake.looks, 2, "nothing sent for a refused flag")

	_, err = w.cmd(t, "videos", "preview", "tk_0123456789abcdef", "--rev", "1", "--card", "end",
		"--out", filepath.Join(t.TempDir(), "card.jpg"))
	require.NoError(t, err)
	require.Len(t, fake.previews, 1)
	assert.Equal(t, videos.CardEnd, fake.previews[0].Card)
}

// Scenario: the owner keeps the music library with the CLI and chooses a video's music (issue #910): a file
// uploaded as it is, with its name, then listed and deleted; -music sends the music section, with -music-gain when
// given, and -music-gain alone is refused before anything is sent.
func TestTheCLIKeepsTheMusicAndChoosesIt(t *testing.T) {
	t.Parallel()

	fake := &cliVideos{v: videos.Video{
		ID: "vid_abc", Take: "tk_0123456789abcdef", Title: "Đăng nhập", CreatedAt: time.Now(),
		Revisions: []videos.Revision{{Rev: 1, State: videos.Review, Lines: []string{"Mở"}, Aspect: "9:16",
			Captions: true, Warnings: []string{}, Look: videos.DefaultLook()}},
	}}

	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithVideos(fake) })
	w.login(t, bearertoken.Owner)

	file := filepath.Join(t.TempDir(), "Nhạc nền.mp3")
	require.NoError(t, os.WriteFile(file, []byte("ID3 a track"), 0o600))

	out, err := w.cmd(t, "videos", "music", "add", file)
	require.NoError(t, err)
	assert.Contains(t, out, "added mus_0123456789abcdefghijklmnop  1:15")
	assert.Equal(t, "upload Nhạc nền 11 ID3 a track", fake.asked[0], "the file as it is, named after it")

	_, err = w.cmd(t, "videos", "music", "add", file, "--name", "Lofi")
	require.NoError(t, err)
	assert.Equal(t, "upload Lofi 11 ID3 a track", fake.asked[1])

	out, err = w.cmd(t, "videos", "music", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "mus_0123456789abcdefghijklmnop  1:15")
	assert.Contains(t, out, "Lofi")

	out, err = w.cmd(t, "videos", "music", "rm", "mus_0123456789abcdefghijklmnop")
	require.NoError(t, err)
	assert.Contains(t, out, "deleted music track mus_0123456789abcdefghijklmnop")

	_, err = w.cmd(t, "videos", "make", "tk_0123456789abcdef", "--music-gain", "-6")
	require.ErrorIs(t, err, ErrUsage)
	require.ErrorContains(t, err, "-music-gain needs -music")
	assert.Empty(t, fake.looks, "nothing is sent")

	_, err = w.cmd(t, "videos", "make", "tk_0123456789abcdef", "--music", "mus_0123456789abcdefghijklmnop",
		"--music-gain", "-6")
	require.NoError(t, err)
	assert.JSONEq(t, `{"music":{"track":"mus_0123456789abcdefghijklmnop","gain_db":-6}}`, fake.looks[0])

	_, err = w.cmd(t, "videos", "look", "vid_abc", "1", "--music", "none")
	require.NoError(t, err)
	assert.JSONEq(t, `{"music":{"track":"none"}}`, fake.looks[1])
}
