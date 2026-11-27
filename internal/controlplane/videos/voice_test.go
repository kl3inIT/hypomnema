package videos

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/captions"
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

// A video voiced by ElevenLabs (issue #1283, row 3) is spoken with the owner's model and asked for its words'
// times: its captions are built from ElevenLabs's alignment, with no transcription. A look change pins that model,
// though the owner chose another since, so its clips and their word times are reused.
func TestElevenLabsCaptionsComeFromItsAlignment(t *testing.T) {
	t.Parallel()

	r := framedRig(t)
	r.setDefault(t, legacyLook)
	r.order = []string{speech.ProviderElevenLabs, speech.ProviderSoniox}
	r.models = map[string]string{speech.ProviderElevenLabs: "eleven_v4_turbo"}
	r.speech.aligned = true

	v, _ := r.make(t, "")
	r.runAll(t)

	got := r.revision(t, v.ID, 1)
	require.Equal(t, Review, got.State, got.FailedCause)
	assert.Equal(t, speech.ProviderElevenLabs, got.Provider)
	assert.Equal(t, "eleven_v4_turbo", got.Model)
	assert.Equal(t, []string{"eleven_v4_turbo", "eleven_v4_turbo"}, r.speech.models)
	assert.Equal(t, 0, r.speech.heard, "no Transcribe")
	assert.Contains(t, r.maker.captions[0], "Đăng")

	clip := filepath.Join(r.dir, voiceDir,
		voiceHash("Chạm vào Đăng nhập", speech.ProviderElevenLabs, "eleven_v4_turbo", "", Language, 0)+"."+clipFormat)
	require.FileExists(t, clip+wordsSuffix)

	r.mu.Lock()
	r.models = map[string]string{speech.ProviderElevenLabs: "eleven_v3"}
	r.mu.Unlock()

	_, rev, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-1283", Look: &LookInput{
		Frame: &FrameInput{Chrome: ptr("none")},
	}})
	require.NoError(t, err)
	r.runAll(t)

	made := r.revision(t, v.ID, rev.Rev)
	require.Equal(t, Review, made.State, made.FailedCause)
	assert.Equal(t, "eleven_v4_turbo", made.Model, "the pinned model, not the owner's new choice")
	assert.Len(t, r.speech.models, 2, "no Speak")
	assert.False(t, slices.Contains(r.speech.models, "eleven_v3"))
	assert.Equal(t, 0, r.speech.heard, "no Transcribe")
}

// The words ElevenLabs's alignment gives are kept as they are (review of #1300): a dash said on its own stays a word
// of its own, and the word before it keeps its own end, not the dash's.
func TestTheAlignmentsWordsAreKeptAsTheyAre(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.order = []string{speech.ProviderElevenLabs, speech.ProviderSoniox}
	r.speech.aligned = true
	r.takes.take.Steps[0].Text = "Ủa — bánh"

	v, _ := r.make(t, "")
	r.runAll(t)
	require.Equal(t, Review, r.revision(t, v.ID, 1).State)

	clip := filepath.Join(r.dir, voiceDir,
		voiceHash("Ủa — bánh", speech.ProviderElevenLabs, speech.ElevenLabsTTSModel, "", Language, 0)+"."+clipFormat)
	raw, err := os.ReadFile(clip + wordsSuffix) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	var words []captions.Word
	require.NoError(t, json.Unmarshal(raw, &words))
	assert.Equal(t, []captions.Word{
		{Word: "Ủa", Start: 0, End: 0.4}, {Word: "—", Start: 0.5, End: 0.9}, {Word: "bánh", Start: 1, End: 1.4},
	}, words)
	assert.Equal(t, 0, r.speech.heard)
}

// A revision pinned before pins kept their model (pin_provider set, pin_model empty) is made again with the model
// it was spoken with, not the owner's choice now nor the default (review of #1300): its clips are reused.
func TestARetriedOldPinKeepsItsRecordedModel(t *testing.T) {
	t.Parallel()

	r := framedRig(t)
	r.setDefault(t, legacyLook)
	r.order = []string{speech.ProviderElevenLabs, speech.ProviderSoniox}
	r.models = map[string]string{speech.ProviderElevenLabs: "eleven_multilingual_v2"}

	v, _ := r.make(t, "")
	r.runAll(t)
	require.Equal(t, Review, r.revision(t, v.ID, 1).State)

	r.maker.fail = "compose"
	_, rev, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-old-pin", Look: &LookInput{
		Frame: &FrameInput{Chrome: ptr("none")},
	}})
	require.NoError(t, err)
	r.runAll(t)

	failed := r.revision(t, v.ID, rev.Rev)
	require.Equal(t, Failed, failed.State)
	require.Equal(t, "eleven_multilingual_v2", failed.Model)

	// As a revision pinned before the migration that added pin_model.
	_, err = r.svc.db.ExecContext(t.Context(),
		"UPDATE video_revisions SET pin_model = '' WHERE video_id = ? AND rev = ?", v.ID, rev.Rev)
	require.NoError(t, err)

	r.maker.fail = ""
	r.mu.Lock()
	r.models = nil
	r.mu.Unlock()

	spoken := len(r.speech.models)

	_, again, err := r.svc.Retry(t.Context(), v.ID, rev.Rev, Owner, "id-old-pin-again")
	require.NoError(t, err)
	r.runAll(t)

	made := r.revision(t, v.ID, again.Rev)
	require.Equal(t, Review, made.State, made.FailedCause)
	assert.Equal(t, "eleven_multilingual_v2", made.Model)
	assert.Len(t, r.speech.models, spoken, "no new speech")
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

// A look change of a video Soniox spoke with the model the owner chose then keeps that model (review of #1302), as
// ElevenLabs's does (pin_model, #1300): the owner's new choice is not read, and its clips are reused.
func TestALookChangeKeepsSonioxsRecordedModel(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.models = map[string]string{speech.ProviderSoniox: "tts-rt-v1"}

	v, _ := r.make(t, "")
	r.runAll(t)
	require.Equal(t, "tts-rt-v1", r.revision(t, v.ID, 1).Model)

	r.mu.Lock()
	r.models = map[string]string{speech.ProviderSoniox: "tts-rt-v2"}
	r.mu.Unlock()

	_, rev, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-1285", Look: &LookInput{
		Frame: &FrameInput{Chrome: ptr("full")},
	}})
	require.NoError(t, err)
	r.runAll(t)

	made := r.revision(t, v.ID, rev.Rev)
	require.Equal(t, Review, made.State, made.FailedCause)
	assert.Equal(t, "tts-rt-v1", made.Model)
	assert.Equal(t, 1, r.speech.count(speech.ProviderSoniox, "Chạm vào Đăng nhập"), "its clip is reused")
}
