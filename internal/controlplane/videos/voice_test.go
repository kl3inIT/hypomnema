package videos

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// The speech caches (row 5): a clip whose MP3 alone was lost is spoken and heard again, its old word times removed
// first; a clip with no word times fails the captions at once.
func TestTheWordTimesFollowTheirClip(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	v, _ := r.make(t, "")
	r.runAll(t)
	assert.Equal(t, 2, r.speech.heard, "two distinct clips, two Transcribes")

	clip := filepath.Join(r.dir, voiceDir,
		voiceHash("Chạm vào Đăng nhập", speech.ProviderSoniox, speech.SonioxTTSModel, "", Language, 0)+"."+clipFormat)
	require.FileExists(t, clip)
	require.NoError(t, os.WriteFile(clip+".words.json", []byte(`[{"Word":"STALE","Start":0,"End":0.3}]`), 0o600))
	require.NoError(t, os.Remove(clip))

	r.make(t, "")
	r.runAll(t)

	require.Equal(t, Review, r.revision(t, v.ID, 2).State)
	assert.Equal(t, 2, r.speech.count(speech.ProviderSoniox, "Chạm vào Đăng nhập"), "1 Speak")
	assert.Equal(t, 3, r.speech.heard, "1 Transcribe")
	assert.NotContains(t, r.maker.captions[1], "STALE", "the new words come from the new transcript")
	assert.Contains(t, r.maker.captions[1], "Chạm")

	words, err := os.ReadFile(clip + ".words.json") //nolint:gosec // The test's own file.
	require.NoError(t, err)
	assert.NotContains(t, string(words), "STALE")
}

// A transcript with no word times fails the revision at captioning once: a clip used by two scenes is heard once.
func TestNoWordTimesFailAtOnce(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.speech.noWords = true
	r.takes.take.Steps[1].Text = r.takes.take.Steps[0].Text

	v, _ := r.make(t, "")
	r.runAll(t)

	got := r.revision(t, v.ID, 1)
	assert.Equal(t, Failed, got.State)
	assert.Equal(t, Captioning, got.FailedStage)
	assert.Equal(t, "scene 1: the transcript has no word times", got.FailedCause)
	assert.Equal(t, 1, r.speech.heard)
}

// A clip whose speech came with its words' times is captioned from them (issue #1285, row 2): no clip is heard
// again, and the audio tag spoken before the words is in no caption.
func TestAClipTimedByItsSpeechIsNotHeardAgain(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.speech.timed = true

	v, _ := r.make(t, "")
	r.runAll(t)

	require.Equal(t, Review, r.revision(t, v.ID, 1).State)
	assert.Equal(t, 0, r.speech.heard, "no Transcribe")
	require.Len(t, r.maker.captions, 1)
	assert.Contains(t, r.maker.captions[0], "Chạm")
	assert.NotContains(t, r.maker.captions[0], "excited")

	clip := filepath.Join(r.dir, voiceDir,
		voiceHash("Chạm vào Đăng nhập", speech.ProviderSoniox, speech.SonioxTTSModel, "", Language, 0)+"."+clipFormat)
	words, err := os.ReadFile(clip + ".words.json") //nolint:gosec // The test's own file.
	require.NoError(t, err)
	assert.Contains(t, string(words), `"Word":"Chạm"`)
}

// A video is spoken at the speed it was made with (issue #1285, row 3; the owner, 2026-11-27): every line is asked
// with it, the revision says it, a clip of another speed is not reused, a retry or a look change keeps it, and a
// speed out of 0.7-1.3 is refused before anything is queued.
func TestAVideoIsSpokenAtItsSpeed(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	_, _, err := r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: "d-1", Speed: 1.5})
	require.ErrorIs(t, err, ErrInvalid)

	v, rev, err := r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: "d-1", Speed: 1.2})
	require.NoError(t, err)
	assert.InDelta(t, 1.2, rev.Speed, 1e-9)
	r.runAll(t)

	require.Equal(t, Review, r.revision(t, v.ID, 1).State)
	assert.Equal(t, []float64{1.2}, r.speech.speeds["Chạm vào Đăng nhập"])

	_, _, err = r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: "d-1"})
	require.NoError(t, err)
	r.runAll(t)
	assert.Equal(t, []float64{1.2, 0}, r.speech.speeds["Chạm vào Đăng nhập"], "another speed, another clip")

	_, look, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-speed", Look: &LookInput{
		Frame: &FrameInput{Chrome: ptr("full")},
	}})
	require.NoError(t, err)
	assert.InDelta(t, 1.2, look.Speed, 1e-9, "a look change keeps the speed")
	r.runAll(t)
	assert.Len(t, r.speech.speeds["Chạm vào Đăng nhập"], 2, "its clip at 1.2 is reused")
}
