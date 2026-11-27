package videos

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

const takeID = "tk_00000000000000aa"

// fakeTakes holds one available take of two scenes, filmed by d-1.
type fakeTakes struct {
	dir  string
	take devicetool.Take
}

func (f *fakeTakes) Get(id string) (devicetool.Take, error) {
	if id != f.take.ID {
		return devicetool.Take{}, errors.New("no take")
	}

	return f.take, nil
}

func (f *fakeTakes) File(id, name string) (string, error) {
	if id != f.take.ID {
		return "", errors.New("no take")
	}

	return filepath.Join(f.dir, name), nil
}

// fakeSpeech speaks a line as its text, and hears a clip as its words, a third of a second each; it counts what
// it was asked.
type fakeSpeech struct {
	mu sync.Mutex
	// spoken counts the lines spoken, by provider and text; heard the clips transcribed.
	spoken map[string]int
	heard  int
	// down are the providers that fail; failAfter fails a provider's speech after that many lines.
	down      map[string]bool
	failAfter map[string]int
	sttFails  bool
	noWords   bool
	// timed gives the words' times with the speech when they are asked, with an audio tag spoken before them, as
	// Soniox's WebSocket gives them (issue #1285, row 2).
	timed bool
	// lines are the lines spoken, in order, for a speech that hears them back.
	lines []string
	// off are the providers the owner turned off: one named is refused unless the call is pinned, as the speech
	// service refuses it.
	off []string
}

func newFakeSpeech() *fakeSpeech {
	return &fakeSpeech{spoken: map[string]int{}, down: map[string]bool{}, failAfter: map[string]int{}}
}

func (f *fakeSpeech) Speak(_ context.Context, _ AudioSource, req speech.SpeakRequest) (speech.Speech, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if req.Language != "vi" {
		return speech.Speech{}, errors.New("not Vietnamese")
	}

	if f.down[req.Provider] {
		return speech.Speech{}, errors.New(req.Provider + " is down")
	}

	if !req.Pinned && slices.Contains(f.off, req.Provider) {
		return speech.Speech{}, errors.New(req.Provider + " is turned off on this control plane")
	}

	n := 0
	for k, c := range f.spoken {
		if strings.HasPrefix(k, req.Provider+"|") {
			n += c
		}
	}

	if limit, ok := f.failAfter[req.Provider]; ok && n >= limit {
		return speech.Speech{}, errors.New(req.Provider + " ran out")
	}

	f.spoken[req.Provider+"|"+req.Voice+"|"+req.Input]++

	out := speech.Speech{Audio: []byte(req.Input), Provider: req.Provider}

	if f.timed && req.Timestamps {
		var a speech.Alignment

		for i, c := range []rune("[excited] " + req.Input) {
			at := float64(i) / 10
			a.Add(speech.Alignment{Characters: []string{string(c)}, Starts: []float64{at}, Ends: []float64{at + 0.1}})
		}

		out.Words = speech.AlignedWords("[excited] "+req.Input, a)
	}

	return out, nil
}

func (f *fakeSpeech) Transcribe(_ context.Context, _ AudioSource, req speech.TranscribeRequest,
) (speech.Transcript, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.heard++

	if f.sttFails {
		return speech.Transcript{}, errors.New("no provider answered")
	}

	r, err := req.Audio.Open()
	if err != nil {
		return speech.Transcript{}, err
	}

	raw, _ := io.ReadAll(r)
	_ = r.Close()

	t := speech.Transcript{Text: string(raw), Provider: speech.ProviderElevenLabs}
	if f.noWords {
		return t, nil
	}

	for i, w := range strings.Fields(string(raw)) {
		at := time.Duration(i) * time.Second / 3
		t.Words = append(t.Words, speech.Word{Word: w, Start: at, End: at + 300*time.Millisecond})
	}

	return t, nil
}

func (f *fakeSpeech) count(provider, text string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0
	for k, c := range f.spoken {
		if strings.HasPrefix(k, provider+"|") && strings.HasSuffix(k, "|"+text) {
			n += c
		}
	}

	return n
}

// fakeMaker makes files that say what they are; a stage named in fail fails, and block holds Compose until its
// context ends.
type fakeMaker struct {
	mu       sync.Mutex
	fail     string
	block    bool
	inputs   []compose.Input
	captions []string
	// during, when set, is called while Compose makes its scenes, after the first.
	during func()
}

func (f *fakeMaker) Compose(ctx context.Context, _ ffmpeg.Tools, in compose.Input) (compose.Result, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	fail, block, during := f.fail, f.block, f.during
	f.mu.Unlock()

	if during != nil && in.Progress != nil {
		in.Progress(1)
		during()
	}

	if block {
		<-ctx.Done()

		return compose.Result{}, ctx.Err()
	}

	if fail == "compose" {
		return compose.Result{}, errors.New("ffmpeg: exit status 1")
	}

	if err := os.WriteFile(in.Out, []byte("composed"), 0o600); err != nil {
		return compose.Result{}, err
	}

	res := compose.Result{Size: compose.Size{Width: 720, Height: 1280}, LengthMs: 4600,
		Warnings: []string{"a warning"}}
	for k := range in.Voice {
		res.Scenes = append(res.Scenes, compose.SceneResult{OutStartMs: int64(k) * 2300, OutLengthMs: 2300})
	}

	return res, nil
}

func (f *fakeMaker) Burn(_ context.Context, _ ffmpeg.Tools, video, captions, work string) (string, error) {
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()

	if fail == "burn" {
		return "", errors.New("ffmpeg: subtitles failed")
	}

	ass, err := os.ReadFile(captions) //nolint:gosec // The test's own file.
	if err != nil {
		return "", err
	}

	f.mu.Lock()
	f.captions = append(f.captions, string(ass))
	f.mu.Unlock()

	out := filepath.Join(work, "burned.mp4")
	if err := os.MkdirAll(work, 0o700); err != nil {
		return "", err
	}

	return out, os.WriteFile(out, []byte("burned:"+filepath.Base(video)), 0o600)
}

func (f *fakeMaker) Thumbnail(_ context.Context, _ ffmpeg.Tools, _, out string, _ int64) error {
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()

	if fail == "thumbnail" {
		return errors.New("ffprobe: no frame")
	}

	return os.WriteFile(out, []byte("jpeg"), 0o600)
}

type rig struct {
	svc    *Service
	speech *fakeSpeech
	maker  *fakeMaker
	takes  *fakeTakes
	dir    string
	free   uint64
	off    []string
	voices map[string]string
	// order is the owner's order of the speaking providers; empty: the default.
	order []string
	mu    sync.Mutex
}

func newRig(t *testing.T) *rig {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	r := &rig{speech: newFakeSpeech(), maker: &fakeMaker{}, dir: t.TempDir(), free: 1 << 40}
	r.takes = &fakeTakes{dir: t.TempDir(), take: devicetool.Take{
		ID: takeID, Title: "Đăng nhập", Agent: "d-1", State: devicetool.TakeAvailable, DurationMs: 6000,
		Screen:   &devicetool.TakeScreen{Width: 1080, Height: 2400},
		Segments: []devicetool.TakeSegment{{File: "seg-001.mp4", LengthMs: 6000}},
		Steps: []devicetool.TakeStep{
			{AtMs: 500, Text: "Mở ứng dụng"}, {AtMs: 3000, Text: "Chạm vào Đăng nhập", Tap: &devicetool.TakeTap{X: 1, Y: 2}},
		},
	}}

	r.svc, err = New(Config{
		DB: db, Dir: r.dir, Takes: r.takes, Speech: r.speech, Maker: r.maker, Clock: platform.SystemClock{},
		Settings: func(context.Context) (speech.Settings, error) {
			r.mu.Lock()
			defer r.mu.Unlock()

			return speech.Settings{Off: r.off, Voices: r.voices, TTS: r.order}, nil
		},
		Tools:     func(context.Context) (ffmpeg.Tools, error) { return ffmpeg.Tools{FFmpeg: "/x/ffmpeg"}, nil },
		FreeBytes: func(string) (uint64, error) { r.mu.Lock(); defer r.mu.Unlock(); return r.free, nil },
	})
	require.NoError(t, err)

	// The fake ffmpeg draws no preset: the rig's makes keep the captions made before the presets, with no frame,
	// unless a test sets another default.
	r.setDefault(t, legacyLook)

	return r
}

// setDefault sets the saved default look.
func (r *rig) setDefault(t *testing.T, l Look) {
	t.Helper()

	require.NoError(t, store.New(r.svc.db).SetVideoSettings(t.Context(), store.SetVideoSettingsParams{
		Look: l.encode(), Now: 1,
	}))
}

func (r *rig) make(t *testing.T, requestID string) (Video, Revision) {
	t.Helper()

	v, rev, err := r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: "d-1", ByName: "worker", RequestID: requestID})
	require.NoError(t, err)

	return v, rev
}

// runAll makes every queued revision.
func (r *rig) runAll(t *testing.T) {
	t.Helper()

	for r.svc.next(t.Context()) {
	}
}

func (r *rig) revision(t *testing.T, id string, rev int) Revision {
	t.Helper()

	v, err := r.svc.Get(t.Context(), id)
	require.NoError(t, err)

	for _, x := range v.Revisions {
		if x.Rev == rev {
			return x
		}
	}

	t.Fatalf("no revision %d", rev)

	return Revision{}
}

// noFiles says that a revision left no MP4 and no work folder.
func (r *rig) noFiles(t *testing.T, id string, rev int) {
	t.Helper()

	_, err := os.Stat(r.svc.revDir(id, rev, videoFile))
	require.ErrorIs(t, err, os.ErrNotExist, "no MP4 is left")

	_, err = os.Stat(r.svc.revDir(id, rev, workDir))
	assert.ErrorIs(t, err, os.ErrNotExist, "no work folder is left")
}

// A make queues revision 1 with the take's lines; the runner voices each scene with the first provider of the
// owner's order in Vietnamese, composes, transcribes each clip, burns the captions and puts the MP4 and its first
// frame in place; the revision is then in review, with who spoke it.
func TestARevisionIsMadeAndPutInPlace(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.voices = map[string]string{speech.ProviderSoniox: "Linh"}

	v, rev := r.make(t, "r1")
	assert.Equal(t, Queued, rev.State)
	assert.Equal(t, 1, rev.Rev)
	assert.Equal(t, []string{"Mở ứng dụng", "Chạm vào Đăng nhập"}, rev.Lines)
	assert.Equal(t, "Đăng nhập", v.Title)
	assert.True(t, rev.Captions)
	assert.Equal(t, "9:16", rev.Aspect)

	r.runAll(t)

	got := r.revision(t, v.ID, 1)
	assert.Equal(t, Review, got.State)
	assert.Equal(t, speech.ProviderSoniox, got.Provider)
	assert.Equal(t, speech.SonioxTTSModel, got.Model)
	assert.Equal(t, "Linh", got.UsedVoice)
	assert.Equal(t, int64(4600), got.LengthMs)
	assert.Equal(t, []string{"a warning"}, got.Warnings)

	assert.Equal(t, 1, r.speech.count(speech.ProviderSoniox, "Mở ứng dụng"))
	assert.Equal(t, 1, r.speech.count(speech.ProviderSoniox, "Chạm vào Đăng nhập"))
	assert.Equal(t, 2, r.speech.heard, "each scene's clip is transcribed")

	require.Len(t, r.maker.inputs, 1)
	assert.Equal(t, compose.Size{Width: 1080, Height: 2400}, r.maker.inputs[0].Screen, "the take's screen places taps")
	assert.Equal(t, r.takes.dir, r.maker.inputs[0].Dir)

	require.Len(t, r.maker.captions, 1)
	assert.Contains(t, r.maker.captions[0], "Dialogue: 0,0:00:02.30,", //nolint:misspell // ASS's own word.
		"scene 2's words start at its place on the video")

	mp4, err := r.svc.File(t.Context(), v.ID, 1)
	require.NoError(t, err)

	raw, err := os.ReadFile(mp4) //nolint:gosec // The test's own file.
	require.NoError(t, err)
	assert.Equal(t, "burned:composed.mp4", string(raw))

	_, err = r.svc.Thumbnail(t.Context(), v.ID, 1)
	require.NoError(t, err)

	_, err = os.Stat(r.svc.revDir(v.ID, 1, workDir))
	assert.ErrorIs(t, err, os.ErrNotExist, "the work folder is removed")
}

// With captions off the captioning stage is skipped: nothing is transcribed or burned.
func TestCaptionsOffSkipsCaptioning(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	off := false

	v, _, err := r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: Owner, Captions: &off, Aspect: "16:9"})
	require.NoError(t, err)

	r.runAll(t)

	got := r.revision(t, v.ID, 1)
	assert.Equal(t, Review, got.State)
	assert.Equal(t, 0, r.speech.heard)
	assert.Empty(t, r.maker.captions)
	assert.Equal(t, compose.Landscape, r.maker.inputs[0].Aspect)
}

// The same request id answers the same revision; with another request it is refused.
func TestTheSameRequestIDAnswersTheSameRevision(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	v1, rev1 := r.make(t, "r1")
	v2, rev2 := r.make(t, "r1")
	assert.Equal(t, v1.ID, v2.ID)
	assert.Equal(t, rev1.Rev, rev2.Rev)
	assert.Len(t, v2.Revisions, 1)

	_, _, err := r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: "d-1", RequestID: "r1", Aspect: "16:9"})
	require.ErrorIs(t, err, ErrIdempotency)

	// Another agent's same id is its own.
	_, rev3, err := r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: "d-2", RequestID: "r1"})
	require.NoError(t, err)
	assert.Equal(t, 2, rev3.Rev)
}

func TestTheQueueHoldsTen(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	for range MaxQueued {
		r.make(t, "")
	}

	_, _, err := r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: Owner})
	require.ErrorIs(t, err, ErrConflict)
	assert.Contains(t, err.Error(), "10 revisions are queued")
}

func TestAMakeNeedsAnAvailableTakeWithSteps(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	r.takes.take.State = devicetool.TakeUploading
	_, _, err := r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: Owner})
	require.ErrorIs(t, err, ErrConflict)

	r.takes.take.State, r.takes.take.Steps = devicetool.TakeAvailable, nil
	_, _, err = r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: Owner})
	require.ErrorIs(t, err, ErrConflict)
	assert.Contains(t, err.Error(), "take has no steps")

	_, _, err = r.svc.Make(t.Context(), MakeRequest{Take: "tk_00000000000000bb", By: Owner})
	require.ErrorIs(t, err, ErrNotFound)
}

// A revision being made when the control plane stopped is failed as interrupted at the next start, at the stage
// it was in, and its files are removed; one still queued stays queued.
func TestARestartInterruptsEachStage(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	q := store.New(r.svc.db)
	now := time.Now().UnixMilli()

	for _, stage := range []string{Voicing, Composing, Captioning} {
		v, rev := r.make(t, "")

		n, err := q.ClaimRevision(t.Context(), store.ClaimRevisionParams{VideoID: v.ID, Rev: int64(rev.Rev), Now: now})
		require.NoError(t, err)
		require.Equal(t, int64(1), n)

		if stage != Voicing {
			require.NoError(t, r.svc.advance(t.Context(), v.ID, rev.Rev, Voicing, stage))
		}

		require.NoError(t, os.MkdirAll(r.svc.revDir(v.ID, rev.Rev, workDir), 0o700))
	}

	v, _ := r.make(t, "")

	r.svc.interrupt(t.Context())

	got, err := r.svc.Get(t.Context(), v.ID)
	require.NoError(t, err)
	require.Len(t, got.Revisions, 4)

	for k, stage := range []string{Voicing, Composing, Captioning} {
		rev := got.Revisions[k]
		assert.Equal(t, Failed, rev.State)
		assert.Equal(t, stage, rev.FailedStage)
		assert.Equal(t, "interrupted", rev.FailedCause)
		r.noFiles(t, v.ID, rev.Rev)
	}

	assert.Equal(t, Queued, got.Revisions[3].State)
	assert.Empty(t, r.speech.spoken, "no paid speech is asked again on its own")
}

// Each failure fails the revision at its stage and leaves no MP4.
//
// Each case runs in a synctest bubble, on its fake clock: the 50 ms of the timeout pass only while Compose waits,
// as the stages before it take no time there. On the wall clock a loaded runner spent them in voicing (#1006).
func TestAFailureLeavesNoMP4(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name, stage, cause string
		set                func(r *rig)
	}{
		{"speech", Voicing, "is down", func(r *rig) {
			r.speech.down[speech.ProviderSoniox], r.speech.down[speech.ProviderElevenLabs] = true, true
		}},
		{"speech-to-text", Captioning, "no provider answered", func(r *rig) { r.speech.sttFails = true }},
		{"no word times", Captioning, "no word times", func(r *rig) { r.speech.noWords = true }},
		{"ffmpeg", Composing, "exit status 1", func(r *rig) { r.maker.fail = "compose" }},
		{"burn", Captioning, "subtitles failed", func(r *rig) { r.maker.fail = "burn" }},
		{"ffprobe", Captioning, "no frame", func(r *rig) { r.maker.fail = "thumbnail" }},
		{"timeout", Composing, "longer than", func(r *rig) { r.maker.block, r.svc.cfg.Timeout = true, 50*time.Millisecond }},
		{"disk full", Voicing, "MB of disk free", func(r *rig) { r.free = 1 << 20 }},
		{"no ffmpeg", Composing, "ffmpeg is not installed", func(r *rig) {
			r.svc.cfg.Tools = func(context.Context) (ffmpeg.Tools, error) { return ffmpeg.Tools{}, errors.New("off") }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				r := newRig(t)
				c.set(r)

				v, _ := r.make(t, "")
				r.runAll(t)

				got := r.revision(t, v.ID, 1)
				assert.Equal(t, Failed, got.State)
				assert.Equal(t, c.stage, got.FailedStage)
				assert.Contains(t, got.FailedCause, c.cause)
				r.noFiles(t, v.ID, 1)

				_, err := r.svc.File(t.Context(), v.ID, 1)
				require.ErrorIs(t, err, ErrNotFound)
			})
		})
	}
}

// The first provider that answers speaks every scene: when it fails on scene 1 the next one speaks them all; when
// it fails on a later scene the revision fails, with no voice of another provider mixed in.
func TestOneProviderSpeaksEveryScene(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.speech.down[speech.ProviderSoniox] = true
	r.voices = map[string]string{speech.ProviderSoniox: "Linh", speech.ProviderElevenLabs: "voice-id"}

	v, _ := r.make(t, "")
	r.runAll(t)

	got := r.revision(t, v.ID, 1)
	require.Equal(t, Review, got.State, got.FailedCause)
	assert.Equal(t, speech.ProviderElevenLabs, got.Provider)
	assert.Equal(t, "voice-id", got.UsedVoice, "each provider's own voice")
	assert.Equal(t, 1, r.speech.count(speech.ProviderElevenLabs, "Chạm vào Đăng nhập"))

	r2 := newRig(t)
	r2.speech.failAfter[speech.ProviderSoniox] = 1

	v2, _ := r2.make(t, "")
	r2.runAll(t)

	got = r2.revision(t, v2.ID, 1)
	assert.Equal(t, Failed, got.State)
	assert.Equal(t, Voicing, got.FailedStage)
	assert.Contains(t, got.FailedCause, "scene 2")
	assert.Equal(t, 0, r2.speech.count(speech.ProviderElevenLabs, "Chạm vào Đăng nhập"), "no other voice mixed in")

	// A provider the owner switched off is not asked.
	r3 := newRig(t)
	r3.off = []string{speech.ProviderSoniox}

	v3, _ := r3.make(t, "")
	r3.runAll(t)
	assert.Equal(t, speech.ProviderElevenLabs, r3.revision(t, v3.ID, 1).Provider)
	assert.Equal(t, 0, r3.speech.count(speech.ProviderSoniox, "Mở ứng dụng"))
}

// Changed lines queue a new revision, which reuses the voice of each unchanged line: only the changed one is
// spoken again. The revision asked of is changes_requested with the note; a note alone starts nothing.
func TestChangedLinesReuseTheOtherClips(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	v, _ := r.make(t, "")
	r.runAll(t)

	got, err := r.svc.RequestChanges(t.Context(), v.ID, 1, ChangeRequest{Note: "Nói chậm hơn"})
	require.NoError(t, err)
	require.Len(t, got.Revisions, 1, "a note alone starts nothing")
	assert.Equal(t, ChangesRequested, got.Revisions[0].State)
	assert.Equal(t, "Nói chậm hơn", got.Revisions[0].Note)

	got, err = r.svc.RequestChanges(t.Context(), v.ID, 1, ChangeRequest{
		Lines: []string{"Mở ứng dụng", "Chạm nút Đăng nhập"}, Note: "Sửa câu 2", RequestID: "c1",
	})
	require.NoError(t, err)
	require.Len(t, got.Revisions, 2)
	assert.Equal(t, Queued, got.Revisions[1].State)
	assert.Equal(t, 1, got.Revisions[1].Parent)

	again, err := r.svc.RequestChanges(t.Context(), v.ID, 1, ChangeRequest{
		Lines: []string{"Mở ứng dụng", "Chạm nút Đăng nhập"}, Note: "Sửa câu 2", RequestID: "c1",
	})
	require.NoError(t, err)
	assert.Len(t, again.Revisions, 2, "a retried request queues nothing more")

	r.runAll(t)

	assert.Equal(t, Review, r.revision(t, v.ID, 2).State)
	assert.Equal(t, 1, r.speech.count(speech.ProviderSoniox, "Mở ứng dụng"), "the unchanged line's clip is reused")
	assert.Equal(t, 1, r.speech.count(speech.ProviderSoniox, "Chạm nút Đăng nhập"))
	assert.Equal(t, 3, r.speech.heard, "only the changed clip is transcribed")
	assert.Len(t, r.maker.inputs, 2, "the video is composed again whole")

	_, err = r.svc.RequestChanges(t.Context(), v.ID, 2, ChangeRequest{Lines: []string{"one"}})
	require.ErrorIs(t, err, ErrInvalid, "as many lines as the revision has")

	// A retry by make: the latest revision's lines, every clip from the cache.
	_, rev, err := r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: Owner})
	require.NoError(t, err)
	assert.Equal(t, []string{"Mở ứng dụng", "Chạm nút Đăng nhập"}, rev.Lines)
	assert.Equal(t, 2, rev.Parent)
	r.runAll(t)
	assert.Equal(t, 1, r.speech.count(speech.ProviderSoniox, "Chạm nút Đăng nhập"))
	assert.Equal(t, 3, r.speech.heard, "the same clips reuse their word times")
}

// One revision is approved at a time; an approved revision is never changed by a change request.
func TestApprovingAnotherRevisionMovesTheOldBack(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	v, _ := r.make(t, "")
	r.make(t, "")
	r.runAll(t)

	got, err := r.svc.Approve(t.Context(), v.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, Approved, got.Revisions[0].State)

	before := got.Revisions[0]

	_, err = r.svc.RequestChanges(t.Context(), v.ID, 1, ChangeRequest{Lines: []string{"a", "b"}, Note: "x"})
	require.ErrorIs(t, err, ErrConflict)
	assert.Equal(t, before, r.revision(t, v.ID, 1), "the approved revision is unchanged")

	got, err = r.svc.Approve(t.Context(), v.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, Review, got.Revisions[0].State)
	assert.Equal(t, Approved, got.Revisions[1].State)

	_, err = r.svc.Approve(t.Context(), v.ID, 2)
	require.NoError(t, err, "approved already")

	r.make(t, "")
	_, err = r.svc.Approve(t.Context(), v.ID, 3)
	require.ErrorIs(t, err, ErrConflict, "a queued revision is not approved")
}

// A video, and a take, with a revision queued or being made is not deleted; approve and delete at once leave
// either an approved video or none.
func TestDeleteWaitsForTheRunner(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	v, _ := r.make(t, "")

	busy, err := r.svc.TakeInWork(t.Context(), takeID)
	require.NoError(t, err)
	assert.True(t, busy, "the take may not be deleted")

	require.ErrorIs(t, r.svc.Delete(t.Context(), v.ID), ErrConflict)

	r.runAll(t)

	busy, err = r.svc.TakeInWork(t.Context(), takeID)
	require.NoError(t, err)
	assert.False(t, busy)

	var wg sync.WaitGroup

	var approveErr, deleteErr error

	wg.Go(func() { _, approveErr = r.svc.Approve(t.Context(), v.ID, 1) })
	wg.Go(func() { deleteErr = r.svc.Delete(t.Context(), v.ID) })
	wg.Wait()

	require.NoError(t, deleteErr)

	if approveErr != nil {
		require.ErrorIs(t, approveErr, ErrNotFound)
	}

	_, err = r.svc.Get(t.Context(), v.ID)
	require.ErrorIs(t, err, ErrNotFound)

	_, err = os.Stat(filepath.Join(r.dir, v.ID))
	assert.ErrorIs(t, err, os.ErrNotExist, "its files are removed")
}

// The runner runs apart from any request: a make wakes it.
func TestTheRunnerWakesOnAMake(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)

		r.svc.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	v, _ := r.make(t, "")

	require.Eventually(t, func() bool { return r.revision(t, v.ID, 1).State == Review }, 5*time.Second,
		10*time.Millisecond)
}

// While a revision is made, each stage counts the scenes it has done, as
// aidemo's job status does: composing the first of two scenes says 1 of 2;
// once it is made no count is left.
func TestARevisionBeingMadeSaysItsScenesDone(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	v, _ := r.make(t, "")

	var seen Revision

	r.maker.during = func() { seen = r.revision(t, v.ID, 1) }
	r.runAll(t)

	assert.Equal(t, Composing, seen.State)
	assert.Equal(t, &Progress{ScenesDone: 1, ScenesTotal: 2}, seen.Progress)

	made := r.revision(t, v.ID, 1)
	assert.Equal(t, Review, made.State)
	assert.Nil(t, made.Progress)
}
