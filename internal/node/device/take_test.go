package device

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// takeRig is a running phone whose screen the test streams, packet by
// packet: send returns once a take had the packet.
type takeRig struct {
	*viewRig
	stream *fakeStream
	fed    chan packet
}

func newTakeRig(t *testing.T) *takeRig {
	t.Helper()

	r := &takeRig{viewRig: newViewRig(t), stream: newFakeStream(), fed: make(chan packet, 64)}
	r.m.fed = func(p packet) { r.fed <- p }
	r.m.opts.FreeBytes = func(string) (uint64, error) { return 1 << 40, nil }
	r.start(t)
	r.streams <- r.stream

	return r
}

// send plays a packet on the stream at the rig's time, and waits until the
// take had it.
func (r *takeRig) send(t *testing.T, p packet) {
	t.Helper()

	r.stream.packets <- p

	select {
	case <-r.fed:
	case <-time.After(5 * time.Second):
		t.Fatal("the packet did not reach the take")
	}
}

// begin starts a take and waits for its stream.
func (r *takeRig) begin(t *testing.T, id string) devicetool.Take {
	t.Helper()

	take, err := r.m.StartTake(t.Context(), "d-1", id, "Sign in", "req-"+id)
	require.NoError(t, err)

	select {
	case <-r.opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the take did not open the stream")
	}

	return take
}

// finished waits until the take's writer finished it, and returns it.
func (r *takeRig) finished(t *testing.T, id string) devicetool.Take {
	t.Helper()

	r.m.film.mu.Lock()
	tk := r.m.film.takes[id]
	r.m.film.mu.Unlock()
	require.NotNil(t, tk)

	select {
	case <-tk.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the take was not finished")
	}

	take, err := r.m.TakeStatus(id)
	require.NoError(t, err)

	return take
}

// say says a line now, with no tap.
func (r *takeRig) say(t *testing.T, id, text string) devicetool.TakeStep {
	t.Helper()

	take, err := r.m.SayTake(id, "d-1", text, "", nil)
	require.NoError(t, err)

	for _, s := range take.Steps {
		if s.Text == text {
			return s
		}
	}

	t.Fatalf("no step %q", text)

	return devicetool.TakeStep{}
}

// probe is what ffprobe reads of a segment: its frames' times and the
// stream's duration and size. ffprobe is an independent reader of the files;
// a node without it skips, and CI has it (HYPOMNEMA_NEED_FFPROBE).
type probe struct {
	Frames []struct {
		PTS string `json:"pts_time"`
		Key int    `json:"key_frame"`
	} `json:"frames"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		Codec    string `json:"codec_name"`
		Duration string `json:"duration"`
	} `json:"streams"`
}

func ffprobe(t *testing.T, path string) probe {
	t.Helper()

	bin, err := exec.LookPath("ffprobe")
	if err != nil {
		if os.Getenv("HYPOMNEMA_NEED_FFPROBE") != "" {
			t.Fatal("ffprobe is needed and not on PATH")
		}

		t.Skip("ffprobe is not on PATH")
	}

	out, err := exec.CommandContext(t.Context(), bin, "-v", "error", "-show_frames", "-show_format",
		"-show_streams", "-show_entries", "frame=pts_time,key_frame:format=duration:"+
			"stream=width,height,codec_name,duration", "-of", "json", path).Output() //nolint:gosec // The test's own file.
	require.NoError(t, err, "ffprobe could not read %s", path)

	var p probe
	require.NoError(t, json.Unmarshal(out, &p))

	return p
}

func seconds(t *testing.T, s string) float64 {
	t.Helper()

	f, err := strconv.ParseFloat(s, 64)
	require.NoError(t, err)

	return f
}

// Scenario: an agent films a key frame and two frames 100 ms apart, then
// stops 300 ms after the last. The take is one segment that ffprobe decodes:
// three frames at 0, 100 and 200 ms, the last lasting to the stop, so the
// file and the take are 500 ms long.
func TestATakeIsASegmentADecoderReads(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.begin(t, "tk_one")

	video := &testVideo{width: 64, height: 48}
	r.send(t, configPacket(video))
	r.send(t, keyPacket(video, 10*time.Second))
	r.clock.advance(100 * time.Millisecond)
	r.send(t, deltaPacket(video, 10*time.Second+100*time.Millisecond))
	r.clock.advance(100 * time.Millisecond)
	r.send(t, deltaPacket(video, 10*time.Second+200*time.Millisecond))
	r.clock.advance(300 * time.Millisecond)

	_, err := r.m.StopTake("tk_one", devicetool.EndedStop, "")
	require.NoError(t, err)

	take := r.finished(t, "tk_one")
	assert.Equal(t, devicetool.TakeUploading, take.State)
	assert.Equal(t, devicetool.EndedStop, take.EndedBy)
	require.Len(t, take.Segments, 1)
	assert.Equal(t, int64(500), take.DurationMs)
	assert.Equal(t, devicetool.TakeSegment{
		File: "seg-001.mp4", AtMs: 0, LengthMs: 500, Width: 64, Height: 48,
		Bytes: take.Segments[0].Bytes, SHA256: take.Segments[0].SHA256,
	}, take.Segments[0])

	file := filepath.Join(r.dir, takesDir, "tk_one", "seg-001.mp4")
	info, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a take is private to the node's user")
	assert.Equal(t, info.Size(), take.Segments[0].Bytes)

	p := ffprobe(t, file)
	require.Len(t, p.Streams, 1)
	assert.Equal(t, "h264", p.Streams[0].Codec)
	assert.Equal(t, 64, p.Streams[0].Width)
	require.Len(t, p.Frames, 3, "every frame decodes")
	assert.Equal(t, 1, p.Frames[0].Key)

	for i, want := range []float64{0, 0.1, 0.2} {
		assert.InDelta(t, want, seconds(t, p.Frames[i].PTS), 0.001, "frame %d", i)
	}

	assert.InDelta(t, 0.5, seconds(t, p.Streams[0].Duration), 0.001, "the last frame lasts to the stop")
	assert.InDelta(t, 0.5, seconds(t, p.Format.Duration), 0.001)
}

// Scenario: the phone turns mid-take. Its new configuration closes the
// segment, the next starts on the key frame after it, and both decode at
// their own size, one after the other on the take's timeline.
func TestANewConfigurationStartsANewSegment(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.begin(t, "tk_turn")

	upright := &testVideo{width: 48, height: 64}
	r.send(t, configPacket(upright))
	r.send(t, keyPacket(upright, time.Second))
	r.clock.advance(time.Second)
	r.send(t, deltaPacket(upright, 2*time.Second))
	r.clock.advance(time.Second)

	turned := &testVideo{width: 64, height: 48}
	r.send(t, configPacket(turned))
	r.clock.advance(50 * time.Millisecond)
	// A delta of the new configuration before its key frame cannot start a segment.
	r.send(t, deltaPacket(turned, 3*time.Second))
	r.send(t, keyPacket(turned, 3*time.Second+50*time.Millisecond))
	r.clock.advance(time.Second)

	_, err := r.m.StopTake("tk_turn", devicetool.EndedStop, "")
	require.NoError(t, err)

	take := r.finished(t, "tk_turn")
	require.Len(t, take.Segments, 2)
	assert.Equal(t, int64(0), take.Segments[0].AtMs)
	assert.Equal(t, int64(2000), take.Segments[0].LengthMs)
	assert.Equal(t, [2]int{48, 64}, [2]int{take.Segments[0].Width, take.Segments[0].Height})
	assert.Equal(t, int64(2000), take.Segments[1].AtMs)
	assert.Equal(t, int64(1000), take.Segments[1].LengthMs)
	assert.Equal(t, [2]int{64, 48}, [2]int{take.Segments[1].Width, take.Segments[1].Height})
	assert.Equal(t, int64(3000), take.DurationMs, "the 50 ms waiting for the key frame are not filmed")

	for i, s := range take.Segments {
		p := ffprobe(t, filepath.Join(r.dir, takesDir, "tk_turn", s.File))
		require.Len(t, p.Streams, 1)
		assert.Equal(t, s.Width, p.Streams[0].Width, "segment %d", i)
		assert.NotEmpty(t, p.Frames, "segment %d decodes", i)
		assert.Equal(t, 1, p.Frames[0].Key, "segment %d starts on a key frame", i)
	}
}

// Scenario: a take starts while the stream already runs, between key frames.
// Its segment waits for a key frame, which the server is asked for; a line
// said meanwhile is stamped at the segment's start, and the time before the
// first frame is not on the timeline: a frame 2 s late puts no offset between
// the video and the steps.
func TestASegmentWaitsForAKeyFrame(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.begin(t, "tk_wait")

	video := &testVideo{width: 32, height: 32}
	r.send(t, configPacket(video))
	r.send(t, deltaPacket(video, time.Second))

	assert.Contains(t, r.stream.controls(), resetVideoMessage(), "the server is asked for a key frame")

	r.clock.advance(2 * time.Second)
	early := r.say(t, "tk_wait", "Open the shop")
	assert.Equal(t, int64(0), early.AtMs, "said before the first frame: at the segment's start")

	r.send(t, keyPacket(video, 3*time.Second))
	r.clock.advance(1500 * time.Millisecond)
	r.send(t, deltaPacket(video, 4500*time.Millisecond))

	later := r.say(t, "tk_wait", "Tap Sign in")
	assert.Equal(t, int64(1500), later.AtMs, "the frame said at 1.5 s is the one 1.5 s into the video")

	_, err := r.m.StopTake("tk_wait", devicetool.EndedStop, "")
	require.NoError(t, err)

	take := r.finished(t, "tk_wait")
	require.Len(t, take.Segments, 1)
	assert.Equal(t, int64(1500), take.DurationMs)
	assert.Equal(t, []string{"Open the shop", "Tap Sign in"}, []string{take.Steps[0].Text, take.Steps[1].Text})

	p := ffprobe(t, filepath.Join(r.dir, takesDir, "tk_wait", "seg-001.mp4"))
	require.Len(t, p.Frames, 2)
	assert.InDelta(t, 1.5, seconds(t, p.Frames[1].PTS), 0.001)
}

// Scenario: the screen does not change for 30 s, so the phone sends nothing.
// A line said then is at 30 s, and a take stopped after a long still tail is
// as long as it was filmed: the last frame lasts until the stop.
func TestAStillScreenCountsOnTheTimeline(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.begin(t, "tk_still")

	video := &testVideo{width: 32, height: 32}
	r.send(t, configPacket(video))
	r.send(t, keyPacket(video, 0))
	r.clock.advance(30 * time.Second)

	step := r.say(t, "tk_still", "Nothing moved")
	assert.Equal(t, int64(30_000), step.AtMs)

	r.clock.advance(90 * time.Second)

	_, err := r.m.StopTake("tk_still", devicetool.EndedStop, "")
	require.NoError(t, err)

	take := r.finished(t, "tk_still")
	assert.Equal(t, int64(120_000), take.DurationMs)

	p := ffprobe(t, filepath.Join(r.dir, takesDir, "tk_still", "seg-001.mp4"))
	require.Len(t, p.Frames, 1)
	assert.InDelta(t, 120, seconds(t, p.Format.Duration), 0.001)
}

// Scenario: the phone sends no frame at all before the stop. The take
// failed, saying so, with no segment.
func TestATakeWithNoFrameFails(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.begin(t, "tk_none")
	r.clock.advance(5 * time.Second)

	_, err := r.m.StopTake("tk_none", devicetool.EndedStop, "")
	require.NoError(t, err)

	take := r.finished(t, "tk_none")
	assert.Equal(t, devicetool.TakeFailed, take.State)
	assert.Equal(t, "no frame from the phone", take.Reason)
	assert.Empty(t, take.Segments)

	entries, err := os.ReadDir(filepath.Join(r.dir, takesDir, "tk_none"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, takeFile, entries[0].Name())
}

// Scenario: the take's disk is slow, then fails. The owner's view goes on
// getting every frame while the take's writer waits; the failure ends the
// take as writer_error, keeping the segment written before it.
func TestASlowOrFailingWriterHoldsUpNoView(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)

	var (
		mu      sync.Mutex
		failNow bool
	)

	release := make(chan struct{})
	r.m.beforeWrite = func() error {
		mu.Lock()
		failing := failNow
		mu.Unlock()

		if failing {
			return syscall.EIO
		}

		<-release

		return nil
	}

	v := r.m.Watch(t.Context())
	t.Cleanup(v.Close)
	<-r.opened

	_, err := r.m.StartTake(t.Context(), "d-1", "tk_slow", "Slow disk", "")
	require.NoError(t, err)

	video := &testVideo{width: 32, height: 32}
	r.send(t, configPacket(video))
	r.send(t, keyPacket(video, 0))

	for i := 1; i <= 40; i++ {
		r.send(t, deltaPacket(video, time.Duration(i)*100*time.Millisecond))
	}

	// The view had the frames while the writer was held: 40 frames are more than the view's queue, which it
	// drains here as it goes.
	got := 0

	for got < 3 {
		frame, _ := until(t, v, "")
		if frame != nil {
			got++
		}
	}

	mu.Lock()
	failNow = true
	mu.Unlock()
	close(release)

	take := r.finished(t, "tk_slow")
	assert.Equal(t, devicetool.TakeFailed, take.State, "no segment was closed before the failure")
	assert.Equal(t, devicetool.EndedWriterError, take.EndedBy)

	// The view goes on after the take ended.
	r.send(t, deltaPacket(video, 5*time.Second))
	r.send(t, keyPacket(video, 6*time.Second))

	frame, _ := until(t, v, "")
	assert.NotNil(t, frame)
	assert.False(t, r.stream.ended(), "the stream stays for the view")
}

// Scenario: a disk that fills while a segment is written ends the take as
// disk_low, and the segments closed before are kept.
func TestAFullDiskEndsTheTakeKeepingWhatWasWritten(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.begin(t, "tk_full")

	var (
		mu   sync.Mutex
		full bool
	)

	r.m.beforeWrite = func() error {
		mu.Lock()
		defer mu.Unlock()

		if full {
			return syscall.ENOSPC
		}

		return nil
	}

	first := &testVideo{width: 32, height: 32}
	r.send(t, configPacket(first))
	r.send(t, keyPacket(first, 0))
	r.clock.advance(time.Second)

	second := &testVideo{width: 48, height: 32}
	r.send(t, configPacket(second))

	require.Eventually(t, func() bool {
		take, err := r.m.TakeStatus("tk_full")

		return err == nil && len(take.Segments) == 1
	}, 5*time.Second, 10*time.Millisecond)

	mu.Lock()
	full = true
	mu.Unlock()

	r.send(t, keyPacket(second, 2*time.Second))

	take := r.finished(t, "tk_full")
	assert.Equal(t, devicetool.EndedDiskLow, take.EndedBy)
	assert.Equal(t, devicetool.TakeUploading, take.State, "the first segment is kept and uploaded")
	require.Len(t, take.Segments, 1)
	assert.Equal(t, int64(1000), take.Segments[0].LengthMs)
	assert.NoFileExists(t, filepath.Join(r.dir, takesDir, "tk_full", "seg-002.mp4"))
}

// Scenario: free disk below 400 MB refuses a take, and ends a running one
// before its next segment.
func TestLowDiskRefusesOrEndsATake(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)

	var (
		mu   sync.Mutex
		free uint64 = 100 << 20
	)

	r.m.opts.FreeBytes = func(string) (uint64, error) {
		mu.Lock()
		defer mu.Unlock()

		return free, nil
	}

	_, err := r.m.StartTake(t.Context(), "d-1", "tk_low", "Low", "")
	require.ErrorContains(t, err, "less than 400 MB")

	mu.Lock()
	free = 1 << 30
	mu.Unlock()

	r.begin(t, "tk_low")

	video := &testVideo{width: 32, height: 32}
	r.send(t, configPacket(video))
	r.send(t, keyPacket(video, 0))
	r.clock.advance(time.Second)

	_, err = r.m.PauseTake("tk_low")
	require.NoError(t, err)

	mu.Lock()
	free = 1 << 20
	mu.Unlock()

	_, err = r.m.ResumeTake("tk_low")
	require.NoError(t, err)
	r.send(t, keyPacket(video, 2*time.Second))

	take := r.finished(t, "tk_low")
	assert.Equal(t, devicetool.EndedDiskLow, take.EndedBy)
	require.Len(t, take.Segments, 1)
}

// Scenario: a take runs with no view open. The stream stays open for it,
// and the phone is not stopped as idle past IdleAfter; once the take ends the
// stream closes and the idle time counts again.
func TestATakeKeepsTheStreamAndThePhoneUp(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.begin(t, "tk_idle")

	video := &testVideo{width: 32, height: 32}
	r.send(t, configPacket(video))
	r.send(t, keyPacket(video, 0))

	r.clock.advance(IdleAfter + time.Minute)
	r.m.Sweep(t.Context())

	assert.Equal(t, nodeproto.DeviceRunning, r.m.Status().State, "a take is a use")
	assert.False(t, r.stream.ended())

	_, err := r.m.StopTake("tk_idle", devicetool.EndedStop, "")
	require.NoError(t, err)
	r.finished(t, "tk_idle")

	require.Eventually(t, r.stream.ended, 5*time.Second, 10*time.Millisecond, "no view and no take: the stream ends")

	r.clock.advance(IdleAfter + time.Minute)
	r.m.Sweep(t.Context())
	assert.NotEqual(t, nodeproto.DeviceRunning, r.m.Status().State)
}

// Scenario: the owner takes the phone over. The pause closes the segment at
// once: packets that come after it, queued or late, are not in the take.
// Resumed mid-GOP, the new segment starts at the next key frame. A stop while
// paused ends the take with the segment closed at the pause.
func TestAPauseDropsWhatComesUntilTheResume(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.begin(t, "tk_pause")

	video := &testVideo{width: 32, height: 32}
	r.send(t, configPacket(video))
	r.send(t, keyPacket(video, 0))
	r.clock.advance(2 * time.Second)

	paused, err := r.m.PauseTake("tk_pause")
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakePaused, paused.State)
	assert.Equal(t, []devicetool.TakePause{{AtMs: 2000}}, paused.Pauses)

	// What the owner does shows in frames after the pause: none of it is filmed.
	r.clock.advance(5 * time.Second)
	r.send(t, deltaPacket(video, 2100*time.Millisecond))
	r.send(t, keyPacket(video, 3*time.Second))

	status, err := r.m.TakeStatus("tk_pause")
	require.NoError(t, err)
	assert.Equal(t, int64(2000), status.DurationMs, "paused time is not on the timeline")

	_, err = r.m.ResumeTake("tk_pause")
	require.NoError(t, err)
	r.send(t, deltaPacket(video, 8*time.Second))
	r.clock.advance(500 * time.Millisecond)
	r.send(t, keyPacket(video, 8500*time.Millisecond))
	r.clock.advance(time.Second)

	step := r.say(t, "tk_pause", "Back to the app")
	assert.Equal(t, int64(3000), step.AtMs)

	_, err = r.m.PauseTake("tk_pause")
	require.NoError(t, err)
	r.clock.advance(time.Minute)

	stopped, err := r.m.StopTake("tk_pause", devicetool.EndedStop, "")
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakeFinalizing, stopped.State)

	take := r.finished(t, "tk_pause")
	require.Len(t, take.Segments, 2)
	assert.Equal(t, int64(2000), take.Segments[1].AtMs)
	assert.Equal(t, int64(1000), take.Segments[1].LengthMs)
	assert.Equal(t, int64(3000), take.DurationMs)
	assert.Equal(t, []devicetool.TakePause{{AtMs: 2000}, {AtMs: 3000}}, take.Pauses)

	for _, s := range take.Segments {
		p := ffprobe(t, filepath.Join(r.dir, takesDir, "tk_pause", s.File))
		require.Len(t, p.Frames, 1, "%s holds the key frame it started on only", s.File)
	}
}

// Scenario: stop twice, or a start retried with its request id: the same
// take. Another start while one runs is refused.
func TestStopAndStartAreIdempotent(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	first := r.begin(t, "tk_same")

	again, err := r.m.StartTake(t.Context(), "d-1", "tk_same", "Sign in", "req-tk_same")
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)

	_, err = r.m.StartTake(t.Context(), "d-1", "tk_other", "Other", "req-other")
	require.ErrorIs(t, err, ErrFilming)

	one, err := r.m.StopTake("tk_same", devicetool.EndedStop, "")
	require.NoError(t, err)

	two, err := r.m.StopTake("tk_same", devicetool.EndedWorkEnd, "")
	require.NoError(t, err)
	assert.Equal(t, devicetool.EndedStop, two.EndedBy, "the first end stays")
	assert.Equal(t, one.ID, two.ID)

	_, err = r.m.SayTake("tk_same", "d-1", "too late", "", nil)
	require.ErrorIs(t, err, ErrTakeEnded)
}

// Scenario: the phone stops while a take runs. The take ends phone_end.
func TestThePhoneEndingEndsTheTake(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.begin(t, "tk_phone")

	video := &testVideo{width: 32, height: 32}
	r.send(t, configPacket(video))
	r.send(t, keyPacket(video, 0))

	r.m.Stop(t.Context())

	take := r.finished(t, "tk_phone")
	assert.Equal(t, devicetool.EndedPhoneEnd, take.EndedBy)
	assert.Equal(t, devicetool.TakeUploading, take.State)
}

// Scenario: the node ends while a take is filmed. The next node finds it in
// its folder: ended restart, the segments closed kept, the file being written
// removed, and ready to upload; one with nothing closed failed.
func TestANodeThatStartsAgainEndsItsTakes(t *testing.T) {
	t.Parallel()

	r := newTakeRig(t)
	r.begin(t, "tk_cut")

	video := &testVideo{width: 32, height: 32}
	r.send(t, configPacket(video))
	r.send(t, keyPacket(video, 0))
	r.clock.advance(time.Second)

	turned := &testVideo{width: 48, height: 32}
	r.send(t, configPacket(turned))
	r.send(t, keyPacket(turned, 2*time.Second))

	require.Eventually(t, func() bool {
		take, err := r.m.TakeStatus("tk_cut")

		return err == nil && len(take.Segments) == 1
	}, 5*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(r.dir, takesDir, "tk_cut", "seg-002.mp4"))

		return err == nil
	}, 5*time.Second, 10*time.Millisecond)

	empty := filepath.Join(r.dir, takesDir, "tk_empty")
	require.NoError(t, os.MkdirAll(empty, 0o700))
	require.NoError(t, writeTakeFile(empty, devicetool.Take{ID: "tk_empty", State: devicetool.TakeRecording}))

	next := r.manager()

	cut, err := next.TakeStatus("tk_cut")
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakeUploading, cut.State)
	assert.Equal(t, devicetool.EndedRestart, cut.EndedBy)
	assert.Equal(t, int64(1000), cut.DurationMs)
	assert.NoFileExists(t, filepath.Join(r.dir, takesDir, "tk_cut", "seg-002.mp4"))

	none, err := next.TakeStatus("tk_empty")
	require.NoError(t, err)
	assert.Equal(t, devicetool.TakeFailed, none.State)

	uploads := next.Uploads()
	require.Len(t, uploads, 2)
	assert.Equal(t, []string{"seg-001.mp4", takeFile}, uploads[0].Files)
	assert.Equal(t, []string{takeFile}, uploads[1].Files)

	next.DropTake("tk_cut")
	assert.NoDirExists(t, filepath.Join(r.dir, takesDir, "tk_cut"))

	_, err = next.TakeStatus("tk_cut")
	require.ErrorIs(t, err, ErrNoTake)
}

