package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// An agent filming the phone (issue #817, design v3). A take is read from
// the one stream of the phone's screen that the owner's views share, before
// the views' fan-out and through a queue of its own that never drops a
// packet, as scrcpy's recorder has its own queue (app/src/recorder.c:602-635);
// the files are written off the stream's goroutine, so a slow disk holds up no
// view. A take is a list of segments (rows 1 and 2): a new one starts at the
// take's start, at a resume after the owner's takeover, when a new
// configuration comes (the phone turned, the encoder was reset), and when the
// stream starts again, and each starts on a key frame, which the server is
// asked for as a view asks for one. Its timeline (row 4) is the node's
// monotonic clock: a segment's origin is when its first key frame arrived, its
// last frame lasts until it closes, and the take's time is the finished
// segments' lengths and the time since the current one's origin, without the
// time paused, as autodemo's session clock is (src/live.js:102-119,
// 154-161). What a take is lives in take.json in its own folder, private to
// the node's user, so that a node that starts again finds it and uploads it.

const (
	takesDir = "takes"
	takeFile = "take.json"
	// minFreeBytes is the free disk a take needs to start and each new segment, autodemo's 400 MB
	// (src/live.js:97-99).
	minFreeBytes = 400_000_000
	// noFrame is why a take that never got a frame failed (agent-demo-video src/capture.ts:348-349).
	noFrame = "no frame from the phone"
)

// Errors of the video tools.
var (
	// ErrNoTake reports a take the node does not hold.
	ErrNoTake = errors.New("no such take on the phone's node")
	// ErrTakeEnded reports a take that is no longer filmed.
	ErrTakeEnded = errors.New("the take is no longer being filmed")
	// ErrFilming reports a start while another take is filmed.
	ErrFilming = errors.New("another take is being filmed")
)

// filming is what the node films: the take running, if any, and every take it
// still holds.
type filming struct {
	mu sync.Mutex
	// cur is the take recording or paused; nil when none is.
	cur *take
	// takes are the takes the node holds by id: running, being finished, or waiting to be uploaded.
	takes map[string]*take
	// uploads is told when a take is ready to be uploaded.
	uploads chan struct{}
}

// take is one take the node holds. Its fields are guarded by filming.mu but
// for saving, which saveMu orders.
type take struct {
	meta devicetool.Take
	dir  string
	// ended is set once the take stops being filmed; paused while the owner holds the phone.
	ended  bool
	paused bool
	// config is the last configuration the stream sent; seg the segment filmed now, nil while paused or ended.
	config []byte
	seg    *liveSegment
	// finished is the length of the segments closed, the take's time at the current segment's start.
	finished time.Duration
	opened   int
	q        *takeQueue
	done     chan struct{}

	saveMu sync.Mutex
}

// liveSegment is the segment being filmed: waiting for its first key frame
// until started.
type liveSegment struct {
	number  int
	started bool
	origin  time.Time
}

// itemKind is what the writer is to do.
type itemKind int

const (
	itemOpen itemKind = iota
	itemFrame
	itemClose
	itemFinish
)

// takeItem is one thing for the writer, in the order it is to be done.
type takeItem struct {
	kind   itemKind
	number int
	config []byte
	p      packet
	length time.Duration
}

// takeQueue is a take's queue to its writer: unbounded, as scrcpy's
// recorder queue is, so that no packet is dropped and no view waits.
type takeQueue struct {
	mu    sync.Mutex
	items []takeItem
	ready chan struct{}
}

func newTakeQueue() *takeQueue { return &takeQueue{ready: make(chan struct{}, 1)} }

func (q *takeQueue) push(it takeItem) {
	q.mu.Lock()
	q.items = append(q.items, it)
	q.mu.Unlock()

	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// pop waits for the next item.
func (q *takeQueue) pop() takeItem {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			it := q.items[0]
			q.items[0] = takeItem{}
			q.items = q.items[1:]
			q.mu.Unlock()

			return it
		}
		q.mu.Unlock()

		<-q.ready
	}
}

// takeIDs are the ids a take is given: the control plane's.
var takeIDs = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// segmentName is the name of a take's nth segment.
func segmentName(n int) string { return fmt.Sprintf("seg-%03d.mp4", n) }

// now is the node's time, monotonic for the take's clock.
func (m *Manager) now() time.Time { return m.opts.Clock.Now() }

// timeAt is the take's time at now. The caller holds filming.mu.
func (t *take) timeAt(now time.Time) time.Duration {
	if t.seg != nil && t.seg.started && !t.ended && !t.paused {
		return t.finished + max(now.Sub(t.seg.origin), 0)
	}

	return t.finished
}

// freeEnough says whether the disk that holds dir has room for a take; a
// disk whose free space is not known is taken to have it, as autodemo does.
func (m *Manager) freeEnough(dir string) bool {
	free, err := m.opts.FreeBytes(dir)

	return err != nil || free >= minFreeBytes
}

// StartTake starts filming the phone as a take, for agent. The phone must be
// running. A take started with the same request id answers the same take.
func (m *Manager) StartTake(ctx context.Context, agent, id, title, requestID string) (devicetool.Take, error) {
	if !takeIDs.MatchString(id) {
		return devicetool.Take{}, errors.New("the take's id is not one the control plane gives")
	}

	f := &m.film

	f.mu.Lock()

	if t := f.takes[id]; t != nil {
		meta := t.meta
		f.mu.Unlock()

		if meta.RequestID != requestID || meta.Agent != agent {
			return devicetool.Take{}, fmt.Errorf("%w: %s", ErrFilming, id)
		}

		return snapshot(meta), nil
	}

	if f.cur != nil {
		running := f.cur.meta.ID
		f.mu.Unlock()

		return devicetool.Take{}, fmt.Errorf("%w: %s", ErrFilming, running)
	}

	root := m.path(takesDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		f.mu.Unlock()

		return devicetool.Take{}, fmt.Errorf("making the takes' folder: %w", err)
	}

	if !m.freeEnough(root) {
		f.mu.Unlock()

		return devicetool.Take{}, errors.New("less than 400 MB of disk is free on the phone's node: a take " +
			"needs a few hundred MB. Ask the owner to free some space")
	}

	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		f.mu.Unlock()

		return devicetool.Take{}, fmt.Errorf("making the take's folder: %w", err)
	}

	t := &take{
		meta: devicetool.Take{
			ID: id, Title: title, Agent: agent, RequestID: requestID, StartedAt: m.now().UTC(),
			State: devicetool.TakeRecording, Segments: []devicetool.TakeSegment{}, Steps: []devicetool.TakeStep{},
			Pauses: []devicetool.TakePause{},
		},
		dir: dir, q: newTakeQueue(), done: make(chan struct{}),
	}

	m.live.mu.Lock()
	t.config = m.live.config
	m.live.mu.Unlock()

	t.next()

	if f.takes == nil {
		f.takes = map[string]*take{}
	}

	f.takes[id], f.cur = t, t
	meta := t.meta
	f.mu.Unlock()

	go m.writeTake(t)

	m.save(t)
	m.streamOn(ctx)

	m.opts.Logger.InfoContext(ctx, "a take started", slog.String("event", "device.take_started"), slog.String("take", id))

	return snapshot(meta), nil
}

// next starts a segment that waits for its first key frame. The caller holds
// filming.mu.
func (t *take) next() {
	t.opened++
	t.seg = &liveSegment{number: t.opened}
}

// cut closes the current segment at now. The caller holds filming.mu.
func (t *take) cut(now time.Time) {
	if t.seg == nil {
		return
	}

	if t.seg.started {
		length := max(now.Sub(t.seg.origin), 0)
		t.finished += length
		t.q.push(takeItem{kind: itemClose, number: t.seg.number, length: length})
	}

	t.seg = nil
}

// feed gives a take a packet of the stream as it arrives, before the views
// get it, and says whether a key frame is to be asked for: a segment waits
// for one.
func (m *Manager) feed(p packet) bool {
	f := &m.film

	f.mu.Lock()
	ask, ended := m.feedLocked(p)
	f.mu.Unlock()

	if ended != nil {
		go func() {
			m.save(ended)
			m.streamOff()
		}()
	}

	return ask
}

// feedLocked is feed under filming.mu; it answers the take it ended, if any.
func (m *Manager) feedLocked(p packet) (bool, *take) {
	f := &m.film

	t := f.cur
	if t == nil || t.ended {
		return false, nil
	}

	if p.Config {
		// A new configuration (the phone turned, the encoder was reset) closes the segment: one file holds one.
		if t.seg != nil && t.seg.started && !bytes.Equal(p.Data, t.config) {
			t.cut(m.now())
			t.next()
		}

		t.config = p.Data

		return false, nil
	}

	if t.paused || t.seg == nil {
		return false, nil
	}

	if !t.seg.started {
		if !p.Key || t.config == nil {
			return true, nil
		}

		if !m.freeEnough(t.dir) {
			m.endLocked(t, devicetool.EndedDiskLow, "less than 400 MB of disk was free for a new segment")

			return false, t
		}

		t.seg.started, t.seg.origin = true, m.now()
		t.q.push(takeItem{kind: itemOpen, number: t.seg.number, config: t.config})
	}

	t.q.push(takeItem{kind: itemFrame, number: t.seg.number, p: p})

	return false, nil
}

// streamRestarted closes the take's segment when the stream starts again:
// the new stream starts with its own configuration and key frame.
func (m *Manager) streamRestarted() {
	f := &m.film

	f.mu.Lock()
	defer f.mu.Unlock()

	t := f.cur
	if t == nil || t.ended || t.paused || t.seg == nil || !t.seg.started {
		return
	}

	t.cut(m.now())
	t.next()
	t.config = nil
}

// SayTake stamps a step on the take's timeline as the node receives it;
// tap, called after the stamp, resolves where the agent is about to tap. A
// step said while a segment waits for its first frame is at that segment's
// start.
func (m *Manager) SayTake(id, agent, text, chapter string, tap func() (*devicetool.TakeTap, error),
) (devicetool.Take, error) {
	f := &m.film

	f.mu.Lock()

	t, err := f.running(id, agent)
	if err != nil {
		f.mu.Unlock()

		return devicetool.Take{}, err
	}

	at := t.timeAt(m.now())
	f.mu.Unlock()

	step := devicetool.TakeStep{AtMs: at.Milliseconds(), Text: text, Chapter: chapter}

	if tap != nil {
		point, err := tap()
		if err != nil {
			return devicetool.Take{}, err
		}

		step.Tap = point
	}

	f.mu.Lock()
	// Steps stay in time order though two says may resolve their taps in another order.
	i, _ := slices.BinarySearchFunc(t.meta.Steps, step.AtMs, func(s devicetool.TakeStep, at int64) int {
		if s.AtMs <= at {
			return -1
		}

		return 1
	})
	t.meta.Steps = slices.Insert(t.meta.Steps, i, step)
	meta := m.liveMeta(t)
	f.mu.Unlock()

	m.save(t)

	return snapshot(meta), nil
}

// running is the take being filmed for agent. The caller holds filming.mu.
func (f *filming) running(id, agent string) (*take, error) {
	t := f.takes[id]
	if t == nil || t.meta.Agent != agent {
		return nil, fmt.Errorf("%w: %s", ErrNoTake, id)
	}

	if t.ended {
		return nil, fmt.Errorf("%w: %s is %s", ErrTakeEnded, id, t.meta.State)
	}

	return t, nil
}

// launchedApp records the app an agent opened while its take runs: the
// take's app is the one it opened last.
func (m *Manager) launchedApp(agent, pkg, version string) {
	f := &m.film

	f.mu.Lock()
	defer f.mu.Unlock()

	if t := f.cur; t != nil && !t.ended && t.meta.Agent == agent {
		t.meta.AppPackage, t.meta.AppVersion = pkg, version
	}
}

// PauseTake pauses the take for the owner's takeover: the segment closes now
// and packets are dropped until the take resumes. It answers once the
// segment is closed in the take's queue, so nothing that comes after reaches
// the take.
func (m *Manager) PauseTake(id string) (devicetool.Take, error) {
	f := &m.film

	f.mu.Lock()

	t := f.takes[id]
	if t == nil {
		f.mu.Unlock()

		return devicetool.Take{}, fmt.Errorf("%w: %s", ErrNoTake, id)
	}

	if t.ended || t.paused {
		meta := t.meta
		f.mu.Unlock()

		return snapshot(meta), nil
	}

	t.cut(m.now())
	t.paused = true
	t.meta.State = devicetool.TakePaused
	t.meta.Pauses = append(t.meta.Pauses, devicetool.TakePause{AtMs: t.finished.Milliseconds()})
	meta := m.liveMeta(t)
	f.mu.Unlock()

	m.save(t)

	return snapshot(meta), nil
}

// ResumeTake resumes a paused take in a new segment, which starts on a key
// frame.
func (m *Manager) ResumeTake(id string) (devicetool.Take, error) {
	f := &m.film

	f.mu.Lock()

	t := f.takes[id]
	if t == nil {
		f.mu.Unlock()

		return devicetool.Take{}, fmt.Errorf("%w: %s", ErrNoTake, id)
	}

	if t.ended || !t.paused {
		meta := t.meta
		f.mu.Unlock()

		return snapshot(meta), nil
	}

	t.paused = false
	t.meta.State = devicetool.TakeRecording
	t.next()
	meta := m.liveMeta(t)
	f.mu.Unlock()

	m.save(t)
	m.askKey()

	return snapshot(meta), nil
}

// StopTake ends a take, for endedBy; a take that ended already answers its
// state. It answers at once: the writer finishes the take, and it is then
// uploaded.
func (m *Manager) StopTake(id, endedBy, reason string) (devicetool.Take, error) {
	f := &m.film

	f.mu.Lock()

	t := f.takes[id]
	if t == nil {
		f.mu.Unlock()

		return devicetool.Take{}, fmt.Errorf("%w: %s", ErrNoTake, id)
	}

	ended := m.endLocked(t, endedBy, reason)
	meta := t.meta
	f.mu.Unlock()

	if ended {
		m.save(t)
		m.streamOff()
	}

	return snapshot(meta), nil
}

// endLocked ends a take's filming, and says whether it did: one that ended
// already is left. The caller holds filming.mu, and saves the take and
// releases the stream after.
func (m *Manager) endLocked(t *take, endedBy, reason string) bool {
	if t.ended {
		return false
	}

	t.cut(m.now())
	t.ended, t.paused = true, false
	t.meta.State, t.meta.EndedBy, t.meta.Reason = devicetool.TakeFinalizing, endedBy, reason
	t.meta.DurationMs = t.finished.Milliseconds()
	t.q.push(takeItem{kind: itemFinish})

	if m.film.cur == t {
		m.film.cur = nil
	}

	m.opts.Logger.Info("a take ended", slog.String("event", "device.take_ended"),
		slog.String("take", t.meta.ID), slog.String("ended_by", endedBy))

	return true
}

// endRunning ends the take running, if any, for endedBy.
func (m *Manager) endRunning(endedBy, reason string) {
	f := &m.film

	f.mu.Lock()

	t := f.cur
	if t == nil || !m.endLocked(t, endedBy, reason) {
		f.mu.Unlock()

		return
	}
	f.mu.Unlock()

	m.save(t)
	m.streamOff()
}

// TakeStatus is a take's state as the node holds it.
func (m *Manager) TakeStatus(id string) (devicetool.Take, error) {
	f := &m.film

	f.mu.Lock()
	defer f.mu.Unlock()

	t := f.takes[id]
	if t == nil {
		return devicetool.Take{}, fmt.Errorf("%w: %s", ErrNoTake, id)
	}

	return snapshot(m.liveMeta(t)), nil
}

// writeTake writes a take's segments as its queue says, until the take is
// finished. A write that fails ends the take, keeping the segments written
// before; the rest of the queue is dropped.
func (m *Manager) writeTake(t *take) {
	var (
		w      *segmentWriter
		failed bool
	)

	fail := func(err error) {
		if w != nil {
			_ = w.f.Close()
			_ = os.Remove(w.f.Name())
			w = nil
		}

		failed = true
		endedBy := devicetool.EndedWriterError

		if errors.Is(err, syscall.ENOSPC) {
			endedBy = devicetool.EndedDiskLow
		}

		m.opts.Logger.Warn("a take could not be written", slog.String("event", "device.take_write_failed"),
			slog.String("take", t.meta.ID), slog.String("error", err.Error()))

		m.film.mu.Lock()
		ended := m.endLocked(t, endedBy, err.Error())
		m.film.mu.Unlock()

		if ended {
			m.save(t)
			m.streamOff()
		}
	}

	for {
		it := t.q.pop()

		switch it.kind {
		case itemOpen:
			if failed {
				continue
			}

			var err error

			w, err = createSegment(filepath.Join(t.dir, segmentName(it.number)), segmentName(it.number), it.config)
			if err != nil {
				fail(err)
			}
		case itemFrame:
			if w == nil {
				continue
			}

			if err := m.writeFrame(w, it.p); err != nil {
				fail(err)
			}
		case itemClose:
			if w == nil {
				continue
			}

			info, kept, err := w.close(it.length)
			w = nil

			switch {
			case err != nil:
				_ = os.Remove(filepath.Join(t.dir, info.File))
				fail(err)
			case kept:
				m.segmentDone(t, info)
			}
		case itemFinish:
			m.finish(t)
			close(t.done)

			return
		}
	}
}

// writeFrame writes a frame, through the test's hook when it has one.
func (m *Manager) writeFrame(w *segmentWriter, p packet) error {
	if m.beforeWrite != nil {
		if err := m.beforeWrite(); err != nil {
			return err
		}
	}

	return w.write(p)
}

// segmentDone puts a closed segment on the take's timeline, after the ones
// before it.
func (m *Manager) segmentDone(t *take, info segmentInfo) {
	f := &m.film

	f.mu.Lock()

	var at int64
	if n := len(t.meta.Segments); n > 0 {
		last := t.meta.Segments[n-1]
		at = last.AtMs + last.LengthMs
	}

	t.meta.Segments = append(t.meta.Segments, devicetool.TakeSegment{
		File: info.File, AtMs: at, LengthMs: info.Length.Milliseconds(), Width: info.Width, Height: info.Height,
		Bytes: info.Bytes, SHA256: info.SHA256,
	})
	f.mu.Unlock()

	m.save(t)
}

// finish ends a take once its writer wrote everything: it is to be uploaded,
// or failed when it got no frame at all.
func (m *Manager) finish(t *take) {
	f := &m.film

	f.mu.Lock()

	var length int64
	for _, s := range t.meta.Segments {
		length = max(length, s.AtMs+s.LengthMs)
	}

	t.meta.DurationMs = length

	t.meta.State = devicetool.TakeUploading
	if len(t.meta.Segments) == 0 {
		t.meta.State = devicetool.TakeFailed
		if t.meta.Reason == "" {
			t.meta.Reason = noFrame
		}
	}

	f.mu.Unlock()

	m.save(t)

	select {
	case f.uploads <- struct{}{}:
	default:
	}
}

// save writes a take's take.json, private to the node's user, whole or not
// at all. Saves are ordered, and each writes the take as it is then.
func (m *Manager) save(t *take) {
	t.saveMu.Lock()
	defer t.saveMu.Unlock()

	m.film.mu.Lock()
	meta := snapshot(t.meta)
	m.film.mu.Unlock()

	if err := writeTakeFile(t.dir, meta); err != nil {
		m.opts.Logger.Warn("a take's take.json could not be written", slog.String("event", "device.take_save_failed"),
			slog.String("take", meta.ID), slog.String("error", err.Error()))
	}
}

func writeTakeFile(dir string, meta devicetool.Take) error {
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(dir, takeFile)

	err = os.WriteFile(path+".tmp", raw, 0o600)
	if err == nil {
		err = os.Rename(path+".tmp", path)
	}

	return err
}

// liveMeta is a take with its time now while it is filmed. The caller holds filming.mu.
func (m *Manager) liveMeta(t *take) devicetool.Take {
	meta := t.meta
	if !t.ended {
		meta.DurationMs = t.timeAt(m.now()).Milliseconds()
	}

	return meta
}

// snapshot is a copy of a take that shares nothing with it.
func snapshot(meta devicetool.Take) devicetool.Take {
	meta.Segments = slices.Clone(meta.Segments)
	meta.Steps = slices.Clone(meta.Steps)
	meta.Pauses = slices.Clone(meta.Pauses)

	return meta
}

// recoverTakes reads the takes an earlier node left: one that was being
// filmed when it ended is ended "restart", keeping the segments that were
// closed and removing the file of the one being written, and is then to be
// uploaded, as a take that ended is.
func (m *Manager) recoverTakes() {
	root := m.path(takesDir)

	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}

	f := &m.film

	for _, e := range entries {
		if !e.IsDir() || !takeIDs.MatchString(e.Name()) {
			continue
		}

		dir := filepath.Join(root, e.Name())

		raw, err := os.ReadFile(filepath.Join(dir, takeFile)) //nolint:gosec // The node's own takes' folder.
		if err != nil {
			continue
		}

		var meta devicetool.Take
		if json.Unmarshal(raw, &meta) != nil || meta.ID != e.Name() {
			continue
		}

		if !meta.Final() {
			if meta.EndedBy == "" {
				meta.EndedBy = devicetool.EndedRestart
			}

			meta.State = devicetool.TakeUploading
			if len(meta.Segments) == 0 {
				meta.State, meta.Reason = devicetool.TakeFailed, noFrame
			}

			meta.DurationMs = 0
			for _, s := range meta.Segments {
				meta.DurationMs = max(meta.DurationMs, s.AtMs+s.LengthMs)
			}
		}

		// A segment's file that take.json does not name was being written: it is not kept.
		removeUnlisted(dir, meta)

		if err := writeTakeFile(dir, meta); err != nil {
			continue
		}

		done := make(chan struct{})
		close(done)

		if f.takes == nil {
			f.takes = map[string]*take{}
		}

		f.takes[meta.ID] = &take{meta: meta, dir: dir, ended: true, done: done}
	}
}

// removeUnlisted removes a take folder's files that its take.json does not name.
func removeUnlisted(dir string, meta devicetool.Take) {
	keep := map[string]bool{takeFile: true}
	for _, s := range meta.Segments {
		keep[s.File] = true
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if !keep[e.Name()] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// Uploads are the finished takes the node holds, by id.
func (m *Manager) Uploads() []nodeproto.TakeUpload {
	f := &m.film

	f.mu.Lock()
	defer f.mu.Unlock()

	var out []nodeproto.TakeUpload

	for _, t := range f.takes {
		if !t.meta.Final() {
			continue
		}

		select {
		case <-t.done:
		default:
			continue
		}

		files := make([]string, 0, len(t.meta.Segments)+1)
		for _, s := range t.meta.Segments {
			files = append(files, s.File)
		}

		out = append(out, nodeproto.TakeUpload{ID: t.meta.ID, Dir: t.dir, Files: append(files, takeFile)})
	}

	slices.SortFunc(out, func(a, b nodeproto.TakeUpload) int { return strings.Compare(a.ID, b.ID) })

	return out
}

// UploadsReady is told when a take is ready to be uploaded.
func (m *Manager) UploadsReady() <-chan struct{} { return m.film.uploads }

// DropTake removes a take the node holds: it was uploaded, or the owner
// deleted it. A take still being filmed is ended first.
func (m *Manager) DropTake(id string) {
	f := &m.film

	f.mu.Lock()

	t := f.takes[id]
	if t == nil {
		f.mu.Unlock()

		return
	}

	ended := m.endLocked(t, devicetool.EndedStop, "")
	f.mu.Unlock()

	if ended {
		m.streamOff()
	}

	<-t.done

	f.mu.Lock()
	delete(f.takes, id)
	f.mu.Unlock()

	if err := os.RemoveAll(t.dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		m.opts.Logger.Warn("a take's folder could not be removed", slog.String("event", "device.take_remove_failed"),
			slog.String("take", id), slog.String("error", err.Error()))
	}
}

// HeldTakes are the takes the node holds, as it says them at hello.
func (m *Manager) HeldTakes() []nodeproto.TakeHeld {
	f := &m.film

	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]nodeproto.TakeHeld, 0, len(f.takes))
	for _, t := range f.takes {
		out = append(out, nodeproto.TakeHeld{ID: t.meta.ID, State: t.meta.State})
	}

	slices.SortFunc(out, func(a, b nodeproto.TakeHeld) int { return strings.Compare(a.ID, b.ID) })

	return out
}

// filmingNow says whether a take is being filmed, which keeps the stream open
// and puts the phone's idle stop off.
func (m *Manager) filmingNow() bool {
	m.film.mu.Lock()
	defer m.film.mu.Unlock()

	return m.film.cur != nil
}
