package browsertake_test

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browsertake"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browsertake/browsertaketest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/takes"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// clock is a clock the test moves.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) add(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)

	return c.now
}

type rig struct {
	clock    *clock
	browser  *browsertaketest.Browser
	store    *takes.Store
	recorder *browsertake.Recorder
	free     uint64
	alive    bool
	mu       sync.Mutex
}

func newRig(t *testing.T, tools func(context.Context) (ffmpeg.Tools, error)) *rig {
	t.Helper()

	store, err := takes.Open(t.TempDir())
	require.NoError(t, err)

	r := &rig{
		clock:   &clock{now: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)},
		browser: browsertaketest.New(), store: store, free: 1 << 40, alive: true,
	}

	if tools == nil {
		tools = func(context.Context) (ffmpeg.Tools, error) { return ffmpeg.Tools{}, errors.New("no ffmpeg here") }
	}

	r.recorder = browsertake.New(browsertake.Config{
		Browser: r.browser, Takes: store, Tools: tools, Clock: r.clock,
		FreeBytes: func(string) (uint64, error) { r.mu.Lock(); defer r.mu.Unlock(); return r.free, nil },
		WorkAlive: func(context.Context, string) bool { r.mu.Lock(); defer r.mu.Unlock(); return r.alive },
	})

	return r
}

var worker = browsertake.Caller{Key: "d-1", Name: "worker", Work: "dispatch 1"}

func (r *rig) start(t *testing.T, viewport browser.Size) devicetool.Take {
	t.Helper()

	take, said, refused := r.recorder.Start(t.Context(), worker, "Sign in to the shop", "", viewport)
	require.False(t, refused, said)

	return take
}

// waitFor waits for a take to leave finalizing.
func (r *rig) settled(t *testing.T, id string) devicetool.Take {
	t.Helper()

	r.recorder.Wait()

	got, err := r.store.Get(id)
	require.NoError(t, err)

	return got
}

// realTools is the pinned ffmpeg in HYPOMNEMA_FFMPEG_DIR; the test skips without it, unless
// HYPOMNEMA_NEED_FFMPEG says it is needed (as the videos' tests).
func realTools(t *testing.T) ffmpeg.Tools {
	t.Helper()

	if dir := os.Getenv("HYPOMNEMA_FFMPEG_DIR"); dir != "" {
		if tools, ok := ffmpeg.Installed(dir); ok {
			return tools
		}
	}

	why := "the pinned ffmpeg is not installed: run `go run ./internal/controlplane/videos/ffmpeg/installffmpeg <dir>` " +
		"and set HYPOMNEMA_FFMPEG_DIR=<dir>"
	if os.Getenv("HYPOMNEMA_NEED_FFMPEG") != "" {
		t.Fatal(why)
	}

	t.Skip(why)

	return ffmpeg.Tools{}
}

func probe(t *testing.T, tools ffmpeg.Tools, file string, entries string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), tools.FFprobe, "-v", "error", "-select_streams", "v:0", //nolint:gosec // The pinned ffprobe.
		"-count_frames", "-show_entries", entries, "-of", "default=noprint_wrappers=1", file).Output()
	require.NoError(t, err)

	return string(out)
}

// pixelAt is the colour at the middle of a video's frame that covers an instant.
func pixelAt(t *testing.T, tools ffmpeg.Tools, file string, at time.Duration) color.RGBA {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), tools.FFmpeg, "-hide_banner", "-loglevel", "error", //nolint:gosec // The pinned ffmpeg.
		"-ss", fmt.Sprintf("%.3f", at.Seconds()), "-i", file, "-frames:v", "1", "-vf", "crop=2:2:iw/2:ih/2",
		"-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(out), 3)

	return color.RGBA{R: out[0], G: out[1], B: out[2], A: 255}
}

func near(a, b color.RGBA) bool {
	d := func(x, y uint8) int { return max(int(x)-int(y), int(y)-int(x)) }

	return d(a.R, b.R) <= 24 && d(a.G, b.G) <= 24 && d(a.B, b.B) <= 24
}

var (
	red   = color.RGBA{R: 200, G: 40, B: 40, A: 255}
	green = color.RGBA{R: 40, G: 180, B: 60, A: 255}
	blue  = color.RGBA{R: 40, G: 60, B: 200, A: 255}
)

// A page filmed through a click and a typing: the take's segment is H.264 at 30 fps, twice the viewport, each
// frame on screen from when Chrome drew it until the next, the steps at the moments they were said with the
// centres of the elements' boxes, and the frames gone once it is kept.
func TestAPageFilmedThroughAClickAndATypingIsKeptAsATake(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	r := newRig(t, func(context.Context) (ffmpeg.Tools, error) { return tools, nil })
	viewport := browser.Size{Width: 320, Height: 240}
	w, h := viewport.Width*browser.FilmScale, viewport.Height*browser.FilmScale

	t0 := r.clock.Now()
	r.browser.First = []browser.Frame{{JPEG: browsertaketest.Picture(t, w, h, red), At: t0}}
	r.browser.Boxes["@e3"] = browser.Box{X: 100, Y: 50, Width: 40, Height: 20}
	r.browser.Boxes["#email"] = browser.Box{X: 20.4, Y: 150, Width: 200, Height: 31}

	take := r.start(t, viewport)
	assert.Equal(t, devicetool.SourceBrowser, take.Source)
	assert.Equal(t, &devicetool.TakeScreen{Width: 320, Height: 240}, take.Screen, "the taps' space is the viewport")

	r.clock.add(time.Second)
	said, failed := r.recorder.Say(t.Context(), worker, take.ID, "Chạm vào Đăng nhập", "Đăng nhập", "@e3")
	require.False(t, failed, said)
	assert.Equal(t, "Said at 0:01 of take "+take.ID+".", said)
	r.browser.Draw(browser.Frame{JPEG: browsertaketest.Picture(t, w, h, green), At: t0.Add(1200 * time.Millisecond)})

	r.clock.add(2 * time.Second)
	said, failed = r.recorder.Say(t.Context(), worker, take.ID, "Nhập email", "", "#email")
	require.False(t, failed, said)
	r.browser.Draw(browser.Frame{JPEG: browsertaketest.Picture(t, w, h, blue), At: t0.Add(3300 * time.Millisecond)})

	r.clock.add(2 * time.Second)
	said, failed = r.recorder.Stop(t.Context(), worker, take.ID)
	require.False(t, failed, said)
	assert.Contains(t, said, "finalizing")

	got := r.settled(t, take.ID)
	require.Equal(t, devicetool.TakeAvailable, got.State, got.Reason)
	assert.Equal(t, devicetool.EndedStop, got.EndedBy)
	assert.Equal(t, int64(5000), got.DurationMs)
	assert.Equal(t, []devicetool.TakeStep{
		{AtMs: 1000, Text: "Chạm vào Đăng nhập", Chapter: "Đăng nhập", Tap: &devicetool.TakeTap{X: 120, Y: 60}},
		{AtMs: 3000, Text: "Nhập email", Tap: &devicetool.TakeTap{X: 120, Y: 166}},
	}, got.Steps)

	require.Len(t, got.Segments, 1)
	seg := got.Segments[0]
	assert.Equal(t, 640, seg.Width)
	assert.Equal(t, 480, seg.Height)
	assert.Equal(t, int64(5000), seg.LengthMs)
	assert.Equal(t, int64(4966), seg.LastFrameMs)

	file, err := r.store.File(take.ID, seg.File)
	require.NoError(t, err)

	streams := probe(t, tools, file, "stream=codec_name,width,height,pix_fmt,r_frame_rate,nb_read_frames")
	for _, want := range []string{"codec_name=h264", "width=640", "height=480", "pix_fmt=yuv420p",
		"r_frame_rate=30/1", "nb_read_frames=150"} {
		assert.Contains(t, streams, want)
	}

	for at, want := range map[time.Duration]color.RGBA{
		500 * time.Millisecond: red, 1100 * time.Millisecond: red, 1300 * time.Millisecond: green,
		3200 * time.Millisecond: green, 3400 * time.Millisecond: blue, 4900 * time.Millisecond: blue,
	} {
		assert.True(t, near(pixelAt(t, tools, file, at), want), "at %s the frame drawn then: %v, not %v", at,
			pixelAt(t, tools, file, at), want)
	}

	_, err = os.Stat(filepath.Join(filepath.Dir(file), "frames"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the frames go once the segment is kept")
}

// A page that draws nothing after the take starts is a take as long as it was filmed, its one picture held: a
// still of the tab when the screencast sent no frame.
func TestAStillPageFilmedForFiveSecondsIsAFiveSecondTake(t *testing.T) {
	t.Parallel()

	tools := realTools(t)
	r := newRig(t, func(context.Context) (ffmpeg.Tools, error) { return tools, nil })
	r.browser.Still = browsertaketest.Picture(t, 640, 360, green)

	take := r.start(t, browser.Size{Width: 320, Height: 180})
	r.clock.add(5 * time.Second)

	_, failed := r.recorder.Stop(t.Context(), worker, take.ID)
	require.False(t, failed)

	got := r.settled(t, take.ID)
	require.Equal(t, devicetool.TakeAvailable, got.State, got.Reason)
	require.Len(t, got.Segments, 1)
	assert.Equal(t, int64(5000), got.Segments[0].LengthMs)

	file, err := r.store.File(take.ID, got.Segments[0].File)
	require.NoError(t, err)
	assert.Contains(t, probe(t, tools, file, "stream=nb_read_frames"), "nb_read_frames=150")
	assert.True(t, near(pixelAt(t, tools, file, 4900*time.Millisecond), green), "the still held to the end")
}

// A start retried with its request id answers the take it started; another start while one is filmed is refused,
// and a line on a take that is not the caller's is refused, as a phone take's.
func TestABrowserTakeIsTheCallersOwn(t *testing.T) {
	t.Parallel()

	r := newRig(t, nil)
	r.browser.Still = browsertaketest.Picture(t, 8, 8, red)

	first, said, refused := r.recorder.Start(t.Context(), worker, "Demo", "req-1", browser.Size{})
	require.False(t, refused, said)

	again, said, refused := r.recorder.Start(t.Context(), worker, "Demo", "req-1", browser.Size{})
	assert.False(t, refused, said)
	assert.Equal(t, first.ID, again.ID)

	_, said, refused = r.recorder.Start(t.Context(), worker, "Another", "", browser.Size{})
	assert.True(t, refused)
	assert.Contains(t, said, "You are filming take "+first.ID+" already")

	other := browsertake.Caller{Key: "d-2", Name: "other", Work: "dispatch 2"}
	said, failed := r.recorder.Say(t.Context(), other, first.ID, "Hi", "", "")
	assert.True(t, failed)
	assert.Contains(t, said, "is not being filmed")

	said, failed = r.recorder.Stop(t.Context(), other, first.ID)
	assert.True(t, failed)
	assert.Equal(t, "You have no browser take "+first.ID+".", said)

	said, failed = r.recorder.Say(t.Context(), worker, first.ID, "Click it", "", "#missing")
	assert.True(t, failed, "an element with no box says nothing")
	assert.Contains(t, said, "no such element")

	_, said, refused = r.recorder.Start(t.Context(), other, "Huge", "", browser.Size{Width: 5000, Height: 100})
	assert.True(t, refused)
	assert.Contains(t, said, "viewport")
}

// A take ends when the work that started it ends, when its tab goes away, or when the disk runs low; one an
// earlier control plane left unfinished fails at the next start.
func TestABrowserTakeEndsWithItsWorkItsTabOrTheDisk(t *testing.T) {
	t.Parallel()

	t.Run("work", func(t *testing.T) {
		t.Parallel()

		r := newRig(t, nil)
		r.browser.Still = browsertaketest.Picture(t, 8, 8, red)
		take := r.start(t, browser.Size{})

		r.mu.Lock()
		r.alive = false
		r.mu.Unlock()
		r.recorder.Check(t.Context())

		got := r.settled(t, take.ID)
		assert.Equal(t, devicetool.EndedWorkEnd, got.EndedBy)
	})

	t.Run("a Bot's next turn", func(t *testing.T) {
		t.Parallel()

		r := newRig(t, nil)
		r.browser.Still = browsertaketest.Picture(t, 8, 8, red)
		take := r.start(t, browser.Size{})

		next := worker
		next.Work = "dispatch 2"
		_, said, refused := r.recorder.Start(t.Context(), next, "Again", "", browser.Size{})
		require.False(t, refused, said)

		got := r.settled(t, take.ID)
		assert.Equal(t, devicetool.EndedWorkEnd, got.EndedBy)
	})

	t.Run("tab", func(t *testing.T) {
		t.Parallel()

		r := newRig(t, nil)
		r.browser.Still = browsertaketest.Picture(t, 8, 8, red)
		take := r.start(t, browser.Size{})

		r.browser.Last.Stop(t.Context())

		require.Eventually(t, func() bool {
			got, err := r.store.Get(take.ID)

			return err == nil && got.EndedBy == devicetool.EndedStreamError
		}, 5*time.Second, 10*time.Millisecond)
	})

	t.Run("disk", func(t *testing.T) {
		t.Parallel()

		r := newRig(t, nil)
		r.mu.Lock()
		r.free = browsertake.MinFreeBytes - 1
		r.mu.Unlock()

		_, said, refused := r.recorder.Start(t.Context(), worker, "Demo", "", browser.Size{})
		assert.True(t, refused)
		assert.Contains(t, said, "400 MB")

		r.mu.Lock()
		r.free = 1 << 40
		r.mu.Unlock()

		take := r.start(t, browser.Size{})

		r.mu.Lock()
		r.free = browsertake.MinFreeBytes - 1
		r.mu.Unlock()
		r.clock.add(2 * browsertake.DiskEvery)
		r.browser.Draw(browser.Frame{JPEG: browsertaketest.Picture(t, 8, 8, green), At: r.clock.Now()})

		require.Eventually(t, func() bool {
			got, err := r.store.Get(take.ID)

			return err == nil && got.EndedBy == devicetool.EndedDiskLow
		}, 5*time.Second, 10*time.Millisecond)
	})

	t.Run("restart", func(t *testing.T) {
		t.Parallel()

		r := newRig(t, nil)
		left := devicetool.Take{
			ID: "tk_00000000000000aa", Source: devicetool.SourceBrowser, Agent: "d-1", State: devicetool.TakeRecording,
		}
		require.NoError(t, r.store.Create(left))

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		r.recorder.Run(ctx)

		got, err := r.store.Get(left.ID)
		require.NoError(t, err)
		assert.Equal(t, devicetool.TakeFailed, got.State)
		assert.Equal(t, devicetool.EndedRestart, got.EndedBy)
	})
}
