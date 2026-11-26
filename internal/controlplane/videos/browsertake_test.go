package videos

import (
	"context"
	"image/color"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browsertake"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browsertake/browsertaketest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/takes"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// keptMaker composes with ffmpeg and keeps what it made, the rings among it.
type keptMaker struct {
	composeMaker

	mu   sync.Mutex
	made compose.Result
}

func (m *keptMaker) Compose(ctx context.Context, tools ffmpeg.Tools, in compose.Input) (compose.Result, error) {
	res, err := m.composeMaker.Compose(ctx, tools, in)

	m.mu.Lock()
	m.made = res
	m.mu.Unlock()

	return res, err
}

// stepClock is a clock the test moves.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *stepClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// A take of a page in the shared browser (issue #1189) is made into a video as a phone's is: voiced, composed,
// captioned, with a ring on the element each step clicks, at its place on the page.
func TestABrowserTakeIsMadeIntoAVideoAsAPhoneTakeIs(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	r := newRig(t)
	maker := &keptMaker{}
	r.svc.cfg.Maker = maker
	r.svc.cfg.Tools = func(context.Context) (ffmpeg.Tools, error) { return tools, nil }
	r.svc.cfg.Speech = toneSpeech{fakeSpeech: r.speech, t: t, tools: tools}

	store, err := takes.Open(t.TempDir())
	require.NoError(t, err)
	r.svc.cfg.Takes = store

	// A phone-sized page, 360×640: grey, then lighter after the click.
	clock := &stepClock{now: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)}
	page := browsertaketest.New()
	page.First = []browser.Frame{{JPEG: browsertaketest.Picture(t, 360, 640, color.RGBA{R: 64, G: 64, B: 64, A: 255}),
		At: clock.Now()}}
	page.Boxes["@e1"] = browser.Box{X: 140, Y: 300, Width: 80, Height: 40}
	page.Boxes["#email"] = browser.Box{X: 40, Y: 400, Width: 280, Height: 40}

	recorder := browsertake.New(browsertake.Config{
		Browser: page, Takes: store, Clock: clock,
		Tools: func(context.Context) (ffmpeg.Tools, error) { return tools, nil },
	})
	agent := browsertake.Caller{Key: "d-1", Name: "worker", Work: "dispatch 1"}

	take, said, refused := recorder.Start(t.Context(), agent, "Đăng nhập", "", browser.Size{Width: 360, Height: 640})
	require.False(t, refused, said)

	clock.add(1500 * time.Millisecond)
	_, failed := recorder.Say(t.Context(), agent, take.ID, "Chạm vào Đăng nhập", "", "@e1")
	require.False(t, failed)
	page.Draw(browser.Frame{JPEG: browsertaketest.Picture(t, 360, 640, color.RGBA{R: 96, G: 96, B: 96, A: 255}),
		At: clock.Now().Add(200 * time.Millisecond)})

	clock.add(2 * time.Second)
	_, failed = recorder.Say(t.Context(), agent, take.ID, "Nhập email", "", "#email")
	require.False(t, failed)

	clock.add(2500 * time.Millisecond)
	_, failed = recorder.Stop(t.Context(), agent, take.ID)
	require.False(t, failed)
	recorder.Wait()

	got, err := store.Get(take.ID)
	require.NoError(t, err)
	require.Equal(t, devicetool.TakeAvailable, got.State, got.Reason)

	v, _, err := r.svc.Make(t.Context(), MakeRequest{Take: take.ID, By: "d-1", ByName: "worker"})
	require.NoError(t, err)
	r.runAll(t)

	rev := r.revision(t, v.ID, 1)
	require.Equal(t, Review, rev.State, rev.FailedStage+": "+rev.FailedCause)

	mp4, err := r.svc.File(t.Context(), v.ID, 1)
	require.NoError(t, err)

	streams := run(t.Context(), t, tools.FFprobe, "-v", "error", "-show_entries", "stream=codec_name,width,height",
		"-of", "csv=p=0", mp4)
	assert.Contains(t, string(streams), "h264,720,1280")
	assert.Contains(t, string(streams), "aac", "the voice")

	// The rings: one a step, at the center of the element's box, the page's pixels scaled to the 720×1280 video
	// (the 360×640 page fills it at twice its size).
	maker.mu.Lock()
	made := maker.made
	maker.mu.Unlock()

	require.Len(t, made.Scenes, 2)

	for i, want := range [][2]int{{360, 640}, {360, 840}} {
		ring := made.Scenes[i].Ring
		require.NotNil(t, ring, "scene %d has its ring", i)
		assert.InDelta(t, want[0], ring.X, 2, "scene %d", i)
		assert.InDelta(t, want[1], ring.Y, 2, "scene %d", i)
	}

	// The ring is drawn: blue on the grey page just right of the first ring's center, while it grows.
	ring := made.Scenes[0].Ring
	frame := run(t.Context(), t, tools.FFmpeg, "-hide_banner", "-loglevel", "error",
		"-ss", strconv.FormatFloat(float64(ring.AtMs+150)/1000, 'f', 3, 64),
		"-i", mp4, "-frames:v", "1", "-vf", "format=rgb24", "-f", "rawvideo", "-")

	blue := 0

	for y := ring.Y - ring.Size/2; y < ring.Y+ring.Size/2; y++ {
		for x := ring.X - ring.Size/2; x < ring.X+ring.Size/2; x++ {
			if i := (y*720 + x) * 3; i >= 0 && i+2 < len(frame) && frame[i+2] > 180 && frame[i] < 140 {
				blue++
			}
		}
	}

	assert.Positive(t, blue, "the ring is drawn on the element")

	// The captions: the spoken word in yellow at the bottom, in the first scene.
	bottom := run(t.Context(), t, tools.FFmpeg, "-hide_banner", "-loglevel", "error", "-ss", "0.1", "-i", mp4,
		"-frames:v", "1", "-vf", "crop=720:260:0:1000", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	yellow := 0

	for i := 0; i+2 < len(bottom); i += 3 {
		if bottom[i] > 200 && bottom[i+1] > 170 && bottom[i+2] < 80 {
			yellow++
		}
	}

	assert.Greater(t, yellow, 100, "the captions")
}
