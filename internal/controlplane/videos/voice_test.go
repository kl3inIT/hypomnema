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
		voiceHash("Chạm vào Đăng nhập", speech.ProviderSoniox, speech.SonioxTTSModel, "", Language)+"."+clipFormat)
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
