// Package browsertake films an agent's tab of the shared browser as a take
// (issue #1189), beside the phone's takes (issue #817): the agent starts
// filming the page it opened, says each step right before it does it, with the
// element it is about to click or type into, and stops; the frames Chrome drew
// are encoded into one H.264 segment, and the take is kept, listed and made
// into a video as a phone's is (row 3). The tools are the phone's video_start,
// video_say and video_stop for a page, as agent-demo-video and autodemo drive
// one capture with the agent's own browser actions (row 4).
//
// The bounds are a phone take's (row 7): it needs autodemo's 400 MB of free
// disk to start and keeps it while it films, and it ends with the work that
// started it, as a phone take's lease does (internal/controlplane/phone,
// lease.go). It also ends when its tab or Chrome goes away.
package browsertake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/ownerhold"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	// minFreeBytes is the free disk a take needs to start and keeps while it films, autodemo's 400 MB
	// (src/live.js:97-99), as a phone take's (internal/node/device/take.go).
	minFreeBytes = 400_000_000
	// diskEvery is how often a take's frames check the free disk.
	diskEvery = time.Second
	// firstFrame is how long a start waits for a frame before it takes a still (autodemo's 400 ms,
	// src/perform.js:234-239).
	firstFrame = 400 * time.Millisecond
	// encodeTimeout bounds encoding a take's frames.
	encodeTimeout = time.Hour
	// MaxViewport bounds a take's viewport, the browser's own bound of a page (fill.go's maxPage).
	MaxViewport = 3840
	// minViewport is the smallest viewport a take is filmed at.
	minViewport = 100
	// noFrame is why a take that got no frame failed (agent-demo-video src/capture.ts:348-349).
	noFrame = "no frame from the browser"
)

// Caller is the agent that films: Key names its tab and its takes, Name it to
// people, and Work is what it works on now, whose end ends its take (the
// phone's Caller).
type Caller struct {
	Key  string
	Name string
	Work string
}

// Film is a screencast of an agent's tab (browser.Film).
type Film interface {
	Done() <-chan struct{}
	Still(ctx context.Context) ([]byte, error)
	Stop(ctx context.Context)
	Size() browser.Size
}

// Browser is the shared browser (browser.Engine, through Engine).
type Browser interface {
	Film(ctx context.Context, agent string, viewport browser.Size, frame func(browser.Frame)) (Film, error)
	Box(ctx context.Context, agent, selector string) (browser.Box, error)
}

// Store keeps the takes (takes.Store).
type Store interface {
	Create(t devicetool.Take) error
	Get(id string) (devicetool.Take, error)
	Update(t devicetool.Take) (devicetool.Take, error)
	ByRequest(agent, requestID string) (devicetool.Take, bool)
	List() []devicetool.Take
	Frames(id string) (string, error)
	Finish(id, file string, seg devicetool.TakeSegment) error
	Fail(id, endedBy, why string) (devicetool.Take, error)
	Delete(id string) error
}

// Config is what a Recorder needs.
type Config struct {
	Browser Browser
	Takes   Store
	// Tools are the videos' ffmpeg, which encodes the frames.
	Tools func(ctx context.Context) (ffmpeg.Tools, error)
	// WorkAlive says whether a Caller's Work goes on; nil: it always does.
	WorkAlive func(ctx context.Context, work string) bool
	// FreeBytes says how much disk is free; nil: the file system's own.
	FreeBytes func(path string) (uint64, error)
	Clock     platform.Clock
	Logger    *slog.Logger
}

// Recorder films the agents' tabs, one take an agent at a time.
type Recorder struct {
	cfg Config

	mu    sync.Mutex
	films map[string]*film
	// encoding counts the takes being encoded, which Wait waits for.
	encoding sync.WaitGroup
}

// film is one take being filmed.
type film struct {
	take      string
	title     string
	caller    Caller
	requestID string
	dir       string
	// screencast and size are set once it films, before live is set under the Recorder's mu.
	screencast Film
	size       browser.Size
	live       bool

	mu      sync.Mutex
	started time.Time
	frames  []frameFile
	steps   []devicetool.TakeStep
	checked time.Time
	ended   bool
	once    sync.Once
}

// New returns a Recorder.
func New(cfg Config) *Recorder {
	if cfg.Clock == nil {
		cfg.Clock = platform.SystemClock{}
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if cfg.FreeBytes == nil {
		cfg.FreeBytes = platform.FreeBytes
	}

	return &Recorder{cfg: cfg, films: map[string]*film{}}
}

// Start starts filming the caller's tab at viewport, its size as it is when
// zero. A start retried with its request id answers the same take. It answers
// what the agent is told, and whether that is a refusal.
func (r *Recorder) Start(ctx context.Context, c Caller, title, requestID string, viewport browser.Size,
) (devicetool.Take, string, bool) {
	if requestID != "" {
		if t, ok := r.cfg.Takes.ByRequest(c.Key, requestID); ok {
			return t, "This request id started take " + t.ID + " already: " + devicetool.Describe(t), false
		}
	}

	if viewport != (browser.Size{}) && !inBounds(viewport) {
		return devicetool.Take{}, fmt.Sprintf("width and height are a viewport of %d to %d CSS pixels, given both "+
			"or neither.", minViewport, MaxViewport), true
	}

	r.endStale(ctx, c)

	r.mu.Lock()
	if f := r.films[c.Key]; f != nil {
		r.mu.Unlock()

		return devicetool.Take{}, "You are filming take " + f.take + " already: stop it with " +
			devicetool.BrowserVideoStop + " first.", true
	}

	// The agent's place is held while the take starts, so that a second start waits for none.
	f := &film{take: newTakeID(), title: title, caller: c, requestID: requestID}
	r.films[c.Key] = f
	r.mu.Unlock()

	t, why := r.start(ctx, f, viewport)
	if why != "" {
		r.mu.Lock()
		delete(r.films, c.Key)
		r.mu.Unlock()

		return t, why, true
	}

	r.cfg.Logger.InfoContext(ctx, "an agent started filming its tab", slog.String("event", "browsertake.started"),
		slog.String("take", f.take), slog.String("agent", c.Name))

	return t, devicetool.Describe(t), false
}

// start records the take and starts its screencast; why it could not otherwise.
func (r *Recorder) start(ctx context.Context, f *film, viewport browser.Size) (devicetool.Take, string) {
	t := devicetool.Take{
		ID: f.take, Title: f.title, Source: devicetool.SourceBrowser, Agent: f.caller.Key, AgentName: f.caller.Name,
		RequestID: f.requestID, StartedAt: r.cfg.Clock.Now().UTC(), State: devicetool.TakeRecording,
	}

	if err := r.cfg.Takes.Create(t); err != nil {
		return devicetool.Take{}, "The take could not be recorded: " + err.Error() + "."
	}

	// A start refused before anything was filmed (no tab, the owner's hold, the disk) leaves no take in the Studio:
	// the phone's video_start records a take only once its lease is held (phone/lease.go).
	failed := func(why string) (devicetool.Take, string) {
		_ = r.cfg.Takes.Delete(f.take)

		return devicetool.Take{}, "The take could not start: " + why + "."
	}

	dir, err := r.cfg.Takes.Frames(f.take)
	if err != nil {
		return failed(err.Error())
	}

	if !r.freeEnough(dir) {
		return failed("the control plane has less than 400 MB of free disk")
	}

	f.dir = dir

	life := context.WithoutCancel(ctx)

	screencast, err := r.cfg.Browser.Film(ctx, f.caller.Key, viewport, func(fr browser.Frame) { r.frame(life, f, fr) })
	if err != nil {
		return failed(strings.TrimSuffix(err.Error(), "."))
	}

	f.screencast, f.size = screencast, screencast.Size()

	f.mu.Lock()
	f.started = r.cfg.Clock.Now()
	f.mu.Unlock()

	// A page that draws nothing new sends no frame: it starts with a still of it, as autodemo's does.
	if !r.waitFrame(ctx, f) {
		if still, err := screencast.Still(ctx); err == nil {
			r.frame(life, f, browser.Frame{JPEG: still, At: f.started})
		}
	}

	t.Screen = &devicetool.TakeScreen{Width: f.size.Width, Height: f.size.Height}
	if kept, err := r.cfg.Takes.Update(devicetool.Take{ID: f.take, Screen: t.Screen}); err == nil {
		t = kept
	}

	r.mu.Lock()
	f.live = true
	r.mu.Unlock()

	go r.watch(life, f)

	return t, ""
}

// waitFrame waits up to firstFrame for the take's first frame and says whether it came.
func (r *Recorder) waitFrame(ctx context.Context, f *film) bool {
	deadline := time.NewTimer(firstFrame)
	defer deadline.Stop()

	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()

	for {
		f.mu.Lock()
		got := len(f.frames) > 0
		f.mu.Unlock()

		if got {
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

// watch ends a take whose tab or Chrome went away.
func (r *Recorder) watch(ctx context.Context, f *film) {
	<-f.screencast.Done()

	r.end(ctx, f, devicetool.EndedStreamError, "the tab or the browser went away")
}

// frame writes one frame of a take, and ends the take when the disk runs low or a frame cannot be written.
func (r *Recorder) frame(ctx context.Context, f *film, fr browser.Frame) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.ended || f.dir == "" {
		return
	}

	at := int64(0)
	if !f.started.IsZero() {
		at = max(fr.At.Sub(f.started).Milliseconds(), 0)
	}

	if n := len(f.frames); n > 0 {
		at = max(at, f.frames[n-1].atMs)
	}

	now := r.cfg.Clock.Now()
	if now.Sub(f.checked) >= diskEvery {
		f.checked = now

		if !r.freeEnough(f.dir) {
			go r.end(ctx, f, devicetool.EndedDiskLow, "the control plane ran low on disk")

			return
		}
	}

	file := filepath.Join(f.dir, fmt.Sprintf("%06d.jpg", len(f.frames)))
	if err := os.WriteFile(file, fr.JPEG, 0o600); err != nil {
		go r.end(ctx, f, devicetool.EndedWriterError, "a frame could not be written: "+err.Error())

		return
	}

	f.frames = append(f.frames, frameFile{file: file, atMs: at})
}

// Say stamps a line on the caller's take, at this moment of it, with the
// center of the element it names as the step's tap (row 5): a selector or a
// ref of the agent's last snapshot, as agent_browser_click takes.
func (r *Recorder) Say(ctx context.Context, c Caller, id, text, chapter, element string) (string, bool) {
	f := r.own(c, id)
	if f == nil {
		return "Take " + id + " is not being filmed: start one with " + devicetool.BrowserVideoStart + ".", true
	}

	at := r.since(f)
	step := devicetool.TakeStep{AtMs: at, Text: text, Chapter: chapter}

	if element != "" {
		box, err := r.cfg.Browser.Box(ctx, c.Key, element)
		if err != nil {
			return "The element's box could not be read, so nothing was said: " + err.Error() + ".", true
		}

		step.Tap = center(box, f.size)
	}

	f.mu.Lock()
	f.steps = append(f.steps, step)
	steps := append([]devicetool.TakeStep{}, f.steps...)
	f.mu.Unlock()

	if _, err := r.cfg.Takes.Update(devicetool.Take{ID: id, Steps: steps, DurationMs: at}); err != nil {
		r.cfg.Logger.WarnContext(ctx, "a take's step could not be recorded",
			slog.String("event", "browsertake.record_failed"), slog.String("take", id), slog.Any("err", err))
	}

	return "Said at " + devicetool.Clock(at) + " of take " + id + ".", false
}

// center is the middle of a box, in the viewport's pixels and within it.
func center(b browser.Box, size browser.Size) *devicetool.TakeTap {
	x := min(max(int(math.Round(b.X+b.Width/2)), 0), max(size.Width-1, 0))
	y := min(max(int(math.Round(b.Y+b.Height/2)), 0), max(size.Height-1, 0))

	return &devicetool.TakeTap{X: x, Y: y}
}

// noSteps is what a stop adds on a take with no step, as the phone's video_stop does (issue #1028).
const noSteps = "It has no steps, so it cannot be made into a video; it is kept. To make a video, film it again " +
	"with " + devicetool.BrowserVideoSay + " right before each action."

// Stop stops the caller's take: it answers at once, and the frames are
// encoded in the background, which video_status and video_make follow. A take that ended
// already answers its state.
func (r *Recorder) Stop(ctx context.Context, c Caller, id string) (string, bool) {
	f := r.own(c, id)
	if f == nil {
		t, err := r.cfg.Takes.Get(id)
		if err != nil || t.Agent != c.Key || !t.Browser() {
			return "You have no browser take " + id + ".", true
		}

		return devicetool.Describe(t), false
	}

	t := r.end(ctx, f, devicetool.EndedStop, "")

	said := devicetool.Describe(t)
	if len(t.Steps) == 0 {
		said += " " + noSteps
	}

	return said, false
}

// End ends a take still filmed, as stopped, without encoding it: the owner deletes it.
func (r *Recorder) End(ctx context.Context, id string) {
	r.mu.Lock()

	var found *film

	for _, f := range r.films {
		if f.take == id && f.live {
			found = f
		}
	}
	r.mu.Unlock()

	if found != nil {
		r.finish(ctx, found, devicetool.EndedStop, "", false)
	}
}

// own is the caller's take being filmed, when it is id.
func (r *Recorder) own(c Caller, id string) *film {
	r.mu.Lock()
	defer r.mu.Unlock()

	if f := r.films[c.Key]; f != nil && f.take == id && f.live {
		return f
	}

	return nil
}

// since is the time on a take's timeline now.
func (r *Recorder) since(f *film) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return max(r.cfg.Clock.Now().Sub(f.started).Milliseconds(), 0)
}

// end ends a take for endedBy and encodes it in the background; it answers the take as it is then.
func (r *Recorder) end(ctx context.Context, f *film, endedBy, reason string) devicetool.Take {
	return r.finish(ctx, f, endedBy, reason, true)
}

func (r *Recorder) finish(ctx context.Context, f *film, endedBy, reason string, keep bool) devicetool.Take {
	var t devicetool.Take

	f.once.Do(func() {
		r.mu.Lock()
		if r.films[f.caller.Key] == f {
			delete(r.films, f.caller.Key)
		}
		r.mu.Unlock()

		stopMs := r.since(f)
		f.screencast.Stop(ctx)

		f.mu.Lock()
		f.ended = true
		frames := append([]frameFile{}, f.frames...)
		steps := append([]devicetool.TakeStep{}, f.steps...)
		f.mu.Unlock()

		var err error

		t, err = r.cfg.Takes.Update(devicetool.Take{
			ID: f.take, State: devicetool.TakeFinalizing, EndedBy: endedBy, Reason: reason, DurationMs: stopMs,
			Steps: steps,
		})
		if err != nil || !keep {
			return
		}

		r.cfg.Logger.InfoContext(ctx, "a browser take ended", slog.String("event", "browsertake.ended"),
			slog.String("take", f.take), slog.String("ended_by", endedBy))

		r.encoding.Add(1)

		go func() {
			defer r.encoding.Done()

			r.encode(context.WithoutCancel(ctx), f, frames, stopMs)
		}()
	})

	if t.ID == "" {
		t, _ = r.cfg.Takes.Get(f.take)
	}

	return t
}

// encode makes a take's frames into its segment and the take available, or failed with why.
func (r *Recorder) encode(ctx context.Context, f *film, frames []frameFile, stopMs int64) {
	ctx, cancel := context.WithTimeout(ctx, encodeTimeout)
	defer cancel()

	fail := func(why string) {
		r.cfg.Logger.WarnContext(ctx, "a browser take could not be encoded", slog.String("event", "browsertake.failed"),
			slog.String("take", f.take), slog.String("error", why))

		_, _ = r.cfg.Takes.Fail(f.take, devicetool.EndedWriterError, why)
	}

	if len(frames) == 0 {
		fail(noFrame)

		return
	}

	tools, err := r.cfg.Tools(ctx)
	if err != nil {
		fail("ffmpeg is not installed: " + err.Error())

		return
	}

	// The frames are the viewport's size; H.264 in yuv420p takes even sides, so an odd one gets a pixel of pad.
	file, seg, err := encode(ctx, tools, f.dir, frames, stopMs, even(f.size.Width), even(f.size.Height))
	if err != nil {
		fail(err.Error())

		return
	}

	if err := r.cfg.Takes.Finish(f.take, file, seg); err != nil {
		fail(err.Error())
	}
}

// endStale ends the caller's take when the work that started it has ended: a
// Bot's next turn does not use the take of its turn before.
func (r *Recorder) endStale(ctx context.Context, c Caller) {
	r.mu.Lock()
	f := r.films[c.Key]
	stale := f != nil && f.live && f.caller.Work != c.Work
	r.mu.Unlock()

	if stale {
		r.end(ctx, f, devicetool.EndedWorkEnd, "")
	}
}

// Run ends the takes whose work ended until ctx ends; at its start, a browser
// take an earlier control plane left unfinished is failed, its frames
// dropped. At its end the takes being filmed are stopped unencoded, and the
// next Run fails them.
func (r *Recorder) Run(ctx context.Context) {
	for _, t := range r.cfg.Takes.List() {
		if t.Browser() && !t.Final() {
			_, _ = r.cfg.Takes.Fail(t.ID, devicetool.EndedRestart, "the control plane restarted before it was made")
		}
	}

	tick := time.NewTicker(ownerhold.CheckEvery)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			for _, f := range r.live() {
				f.screencast.Stop(context.WithoutCancel(ctx))
			}

			return
		case <-tick.C:
			r.check(ctx)
		}
	}
}

// check ends the takes whose work ended.
func (r *Recorder) check(ctx context.Context) {
	if r.cfg.WorkAlive == nil {
		return
	}

	for _, f := range r.live() {
		if !r.cfg.WorkAlive(ctx, f.caller.Work) {
			r.end(ctx, f, devicetool.EndedWorkEnd, "")
		}
	}
}

// live are the takes being filmed.
func (r *Recorder) live() []*film {
	r.mu.Lock()
	defer r.mu.Unlock()

	all := make([]*film, 0, len(r.films))
	for _, f := range r.films {
		if f.live {
			all = append(all, f)
		}
	}

	return all
}

// Wait waits for the takes being encoded.
func (r *Recorder) Wait() { r.encoding.Wait() }

// freeEnough says whether the disk that holds dir has room for a take; a disk whose free space is not known is
// taken to have it, as autodemo does.
func (r *Recorder) freeEnough(dir string) bool {
	free, err := r.cfg.FreeBytes(dir)

	return err != nil || free >= minFreeBytes
}

// even is n, or n+1 when it is odd.
func even(n int) int { return n + n%2 }

func inBounds(s browser.Size) bool {
	return s.Width >= minViewport && s.Height >= minViewport && s.Width <= MaxViewport && s.Height <= MaxViewport
}

// newTakeID is a new take's id, as the phone's are.
func newTakeID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)

	return "tk_" + hex.EncodeToString(raw)
}
