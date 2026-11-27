package videos

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/captions"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The look of each revision (issue #908, rows 1-5, 11, 14 and 15; #937).

// legacyLook is the Classic look, as the looks' migration wrote it for every revision made before them.
var legacyLook = Look{Captions: CaptionsLook{Preset: captions.Legacy}, Frame: compose.Frame{Chrome: compose.ChromeFull}}

func ptr[T any](v T) *T { return &v }

// framedRig is a rig whose take can be framed (its segment's size is known) and whose default is the migration's:
// Clean, android, midnight.
func framedRig(t *testing.T) *rig {
	t.Helper()

	r := newRig(t)
	r.takes.take.Segments[0].Width, r.takes.take.Segments[0].Height = 576, 1280
	r.setDefault(t, DefaultLook())

	return r
}

// turn makes the rig's take one whose phone turned: two segments of other shapes (node/device's
// TestANewConfigurationStartsANewSegment).
func (r *rig) turn() {
	r.takes.take.Segments = []devicetool.TakeSegment{
		{File: "seg-001.mp4", AtMs: 0, LengthMs: 3000, Width: 576, Height: 1280},
		{File: "seg-002.mp4", AtMs: 3000, LengthMs: 3000, Width: 1280, Height: 576},
	}
}

func (r *rig) savedDefault(t *testing.T) Look {
	t.Helper()

	c, err := r.svc.Looks(t.Context())
	require.NoError(t, err)

	return c.Default
}

func (r *rig) ownerMake(t *testing.T, key string, look *LookInput) (Video, Revision, error) {
	t.Helper()

	return r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: Owner, ByName: Owner, RequestID: key, Look: look})
}

// The catalog lists every preset, Classic included, autodemo's eleven backgrounds, the four chromes, and the
// migration's default: Clean, android, midnight.
func TestTheCatalogAndItsFirstDefault(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	require.NoError(t, store.New(r.svc.db).SetVideoSettings(t.Context(), store.SetVideoSettingsParams{
		Look: `{"captions":{"preset":"clean","position":"preset"},` +
			`"frame":{"chrome":"android","padding":48,"radius":40,"shadow":true,"safe_top":false},` +
			`"background":{"preset":"midnight"}}`, Now: 1,
	}))

	c, err := r.svc.Looks(t.Context())
	require.NoError(t, err)
	assert.Equal(t, DefaultLook(), c.Default, "the migration's default is DefaultLook")
	assert.Len(t, c.Presets, len(captions.Presets))
	assert.Contains(t, c.Presets, PresetInfo{ID: captions.Legacy, Name: "Classic"})
	assert.Len(t, c.Backgrounds, 11)
	assert.Equal(t, []compose.Chrome{"full", "none", "android", "iphone"}, c.Chromes)
}

// A make without a look takes the saved default; a sent section replaces the default's, its fields left out taking
// the references' defaults; ids unknown and values out of range are refused.
func TestAMakeResolvesItsLook(t *testing.T) {
	t.Parallel()

	r := framedRig(t)

	_, rev, err := r.ownerMake(t, "", nil)
	require.NoError(t, err)
	assert.Equal(t, DefaultLook(), rev.Look)

	_, rev, err = r.ownerMake(t, "", &LookInput{Frame: &FrameInput{Chrome: ptr("none"), Radius: ptr(20)}})
	require.NoError(t, err)
	assert.Equal(t, compose.Frame{Chrome: compose.ChromeNone, Padding: 48, Radius: 20, Shadow: true}, rev.Look.Frame,
		"a sent radius is used as sent")
	assert.Equal(t, DefaultLook().Captions, rev.Look.Captions, "the captions not sent are the default's")

	_, rev, err = r.ownerMake(t, "", &LookInput{Frame: &FrameInput{}})
	require.NoError(t, err)
	assert.Equal(t, compose.DefaultFrame(compose.ChromeNone), rev.Look.Frame, "aidemo's frame: no chrome, radius 14")

	for name, bad := range map[string]*LookInput{
		"preset":           {Captions: &CaptionsInput{Preset: ptr("comic")}},
		"position":         {Captions: &CaptionsInput{Position: ptr("left")}},
		"chrome":           {Frame: &FrameInput{Chrome: ptr("mac")}},
		"padding":          {Frame: &FrameInput{Padding: ptr(401)}},
		"radius":           {Frame: &FrameInput{Radius: ptr(-1)}},
		"background":       {Background: &BackgroundInput{Preset: ptr("plaid")}},
		"one color":        {Background: &BackgroundInput{Colors: &[]string{"#000000"}}},
		"no colors":        {Background: &BackgroundInput{Colors: &[]string{}}},
		"not hex":          {Background: &BackgroundInput{Colors: &[]string{"#000000", "red"}}},
		"both":             {Background: &BackgroundInput{Preset: ptr("ocean"), Colors: &[]string{"#000000", "#ffffff"}}},
		"empty chrome":     {Frame: &FrameInput{Chrome: ptr("")}},
		"empty background": {Background: &BackgroundInput{Preset: ptr("")}},
		"empty preset":     {Captions: &CaptionsInput{Preset: ptr("")}},
	} {
		_, _, err := r.ownerMake(t, "", bad)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
}

// A make that sends no look keeps the hash it had before the looks: its retry answers the same revision after the
// default changed, as a request made before the migration does. The same key with other sections is refused.
func TestARetriedMakeAnswersTheSameRevision(t *testing.T) {
	t.Parallel()

	r := framedRig(t)

	_, first, err := r.ownerMake(t, "k1", nil)
	require.NoError(t, err)

	v, err := r.svc.OfTake(t.Context(), takeID)
	require.NoError(t, err)

	row, err := store.New(r.svc.db).GetRevision(t.Context(), store.GetRevisionParams{VideoID: v.ID, Rev: 1})
	require.NoError(t, err)
	assert.Equal(t, requestHash("make", takeID, "", "9:16", "true", ""), row.RequestHash,
		"the hash of a make before the looks")

	_, _, err = r.ownerMake(t, "k2", &LookInput{Captions: &CaptionsInput{Preset: ptr("mono")}})
	require.NoError(t, err)

	_, again, err := r.ownerMake(t, "k1", nil)
	require.NoError(t, err)
	assert.Equal(t, first, again, "the same revision, after the default changed")

	_, _, err = r.ownerMake(t, "k2", &LookInput{Captions: &CaptionsInput{Preset: ptr("bold")}})
	require.ErrorIs(t, err, ErrIdempotency)

	_, _, err = r.ownerMake(t, "k1", &LookInput{})
	require.ErrorIs(t, err, ErrIdempotency, "a look sent, even empty, is another request")
}

// The default takes only the sections an owner's make sent, merged into it; a replay, an agent's make and a
// fallback leave it as it is.
func TestTheDefaultTakesOnlyTheSentSections(t *testing.T) {
	t.Parallel()

	r := framedRig(t)

	_, _, err := r.ownerMake(t, "k1", &LookInput{Captions: &CaptionsInput{Preset: ptr("bold")}})
	require.NoError(t, err)

	want := DefaultLook()
	want.Captions = CaptionsLook{Preset: "bold", Position: captions.PositionPreset}
	assert.Equal(t, want, r.savedDefault(t), "only the captions changed")

	_, _, err = r.ownerMake(t, "k2", &LookInput{Background: &BackgroundInput{Colors: &[]string{"#112233", "#445566"}}})
	require.NoError(t, err)

	want.Background = &compose.Background{Colors: []string{"#112233", "#445566"}}
	assert.Equal(t, want, r.savedDefault(t))

	_, _, err = r.ownerMake(t, "k1", &LookInput{Captions: &CaptionsInput{Preset: ptr("bold")}})
	require.NoError(t, err)
	assert.Equal(t, want, r.savedDefault(t), "unchanged by a replay")

	_, _, err = r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: "d-1", Look: &LookInput{
		Captions: &CaptionsInput{Preset: ptr("pop")},
	}})
	require.NoError(t, err)
	assert.Equal(t, want, r.savedDefault(t), "unchanged by an agent's make")

	r.turn()

	_, rev, err := r.ownerMake(t, "k3", &LookInput{Captions: &CaptionsInput{Preset: ptr("mono")}})
	require.NoError(t, err)
	assert.Equal(t, compose.ChromeAndroid, rev.Look.FellBackFrom)

	want.Captions.Preset = "mono"
	assert.Equal(t, want, r.savedDefault(t), "the captions sent, and the default's frame kept: not the fallback")
}

// On a take whose phone turned (row 11): a frame from the default is stored full, with the chrome it fell back
// from, and the revision warns; a frame sent is refused. The default keeps its frame.
func TestATurnedTakeFallsBackOrRefusesAFrame(t *testing.T) {
	t.Parallel()

	r := framedRig(t)
	r.setDefault(t, func() Look { l := DefaultLook(); l.Captions.Preset = captions.Legacy; return l }())
	r.turn()

	v, rev, err := r.ownerMake(t, "", nil)
	require.NoError(t, err)
	assert.Equal(t, compose.ChromeFull, rev.Look.Frame.Chrome)
	assert.Equal(t, compose.ChromeAndroid, rev.Look.FellBackFrom)

	r.runAll(t)

	made := r.revision(t, v.ID, 1)
	require.Equal(t, Review, made.State, made.FailedCause)
	assert.Contains(t, made.Warnings, "the phone turned during the take, so the video has no android frame")
	assert.False(t, r.maker.inputs[0].Frame.Framed(), "made full")
	assert.Equal(t, compose.ChromeAndroid, r.savedDefault(t).Frame.Chrome, "the default keeps android")

	_, _, err = r.ownerMake(t, "", &LookInput{Frame: &FrameInput{Chrome: ptr("android")}})
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorIs(t, err, compose.ErrTurnedTake)

	_, _, err = r.ownerMake(t, "", &LookInput{Frame: &FrameInput{Chrome: ptr("full")}})
	require.NoError(t, err, "full is never refused")

	// The look sample: the default flow resolves full; a frame sent is refused before anything is drawn.
	p, err := r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID})
	require.NoError(t, err)
	assert.Equal(t, compose.ChromeFull, p.look.Frame.Chrome)

	_, err = r.svc.Preview(t.Context(), PreviewRequest{Take: takeID, Look: &LookInput{
		Frame: &FrameInput{Chrome: ptr("android")},
	}})
	require.ErrorIs(t, err, ErrConflict)

	// A look change of the fallen-back revision keeps its frame, fallback included, when no frame is sent.
	_, changed, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-1", Look: &LookInput{
		Captions: &CaptionsInput{Preset: ptr("bold")},
	}})
	require.NoError(t, err)
	assert.Equal(t, compose.ChromeFull, changed.Look.Frame.Chrome)
	assert.Equal(t, compose.ChromeAndroid, changed.Look.FellBackFrom)

	_, _, err = r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-2", Look: &LookInput{
		Frame: &FrameInput{Chrome: ptr("iphone")},
	}})
	require.ErrorIs(t, err, ErrConflict)
}

// Request changes copies the look of the revision it changes, framed or legacy, as it copies its voice.
func TestRequestChangesKeepsTheLook(t *testing.T) {
	t.Parallel()

	framed := DefaultLook()
	framed.Captions.Preset = captions.Legacy // The fake ffmpeg draws no preset.

	for name, look := range map[string]Look{"legacy": legacyLook, "framed": framed} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := framedRig(t)
			r.setDefault(t, look)

			v, rev := r.make(t, "")
			r.runAll(t)
			r.setDefault(t, func() Look { l := DefaultLook(); l.Frame.Chrome = compose.ChromeIPhone; return l }())

			got, err := r.svc.RequestChanges(t.Context(), v.ID, rev.Rev, ChangeRequest{Lines: []string{"Một", "Hai"}})
			require.NoError(t, err)
			require.Len(t, got.Revisions, 2)
			assert.Equal(t, r.revision(t, v.ID, 1).Look, got.Revisions[1].Look)
		})
	}
}

// A look change (row 4) numbers the new revision after the latest, with the revision viewed as its parent; it
// copies that revision's lines and takes the sections not sent from its look; the revision changed stays as it is.
func TestAChangedLookIsANewRevisionOfTheOneViewed(t *testing.T) {
	t.Parallel()

	r := framedRig(t)

	v, _, err := r.ownerMake(t, "", &LookInput{
		Frame: &FrameInput{Chrome: ptr("iphone")}, Background: &BackgroundInput{Preset: ptr("ocean")},
		Captions: &CaptionsInput{Preset: ptr(captions.Legacy)},
	})
	require.NoError(t, err)
	r.runAll(t)

	_, err = r.svc.RequestChanges(t.Context(), v.ID, 1, ChangeRequest{Lines: []string{"Một", "Hai"}})
	require.NoError(t, err)
	r.runAll(t)
	_, err = r.svc.RequestChanges(t.Context(), v.ID, 2, ChangeRequest{Lines: []string{"Ba", "Bốn"}})
	require.NoError(t, err)
	r.runAll(t)

	before := r.revision(t, v.ID, 1)

	_, rev, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "l1", Look: &LookInput{
		Captions: &CaptionsInput{Preset: ptr("mono"), Position: ptr("top")},
	}})
	require.NoError(t, err)
	assert.Equal(t, 4, rev.Rev, "after the latest, 3")
	assert.Equal(t, 1, rev.Parent, "the revision viewed")
	assert.Equal(t, before.Lines, rev.Lines, "its lines, not the latest's")
	assert.Equal(t, CaptionsLook{Preset: "mono", Position: captions.PositionTop}, rev.Look.Captions)
	assert.Equal(t, before.Look.Frame, rev.Look.Frame, "a caption change keeps the frame")
	assert.Equal(t, before.Look.Background, rev.Look.Background, "and the background")
	assert.Equal(t, before.Aspect, rev.Aspect)
	assert.Equal(t, before.Captions, rev.Captions)
	assert.Equal(t, before, r.revision(t, v.ID, 1), "the revision changed is as it was")

	_, again, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "l1", Look: &LookInput{
		Captions: &CaptionsInput{Preset: ptr("mono"), Position: ptr("top")},
	}})
	require.NoError(t, err)
	assert.Equal(t, 4, again.Rev, "a replay")

	_, _, err = r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "l1"})
	require.ErrorIs(t, err, ErrIdempotency)

	// full and none are kept apart.
	_, full, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-3", Look: &LookInput{
		Frame: &FrameInput{Chrome: ptr("full")},
	}})
	require.NoError(t, err)
	_, none, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-4", Look: &LookInput{
		Frame: &FrameInput{Chrome: ptr("none")},
	}})
	require.NoError(t, err)
	assert.Equal(t, compose.ChromeFull, full.Look.Frame.Chrome)
	assert.Equal(t, compose.ChromeNone, none.Look.Frame.Chrome)

	// Only a made revision's look is changed.
	_, _, err = r.svc.ChangeLook(t.Context(), v.ID, 4, LookChange{RequestID: "id-5"})
	require.ErrorIs(t, err, ErrConflict, "a queued one")

	_, _, err = r.svc.ChangeLook(t.Context(), v.ID, 9, LookChange{RequestID: "id-6"})
	require.ErrorIs(t, err, ErrNotFound)

	// A video whose take was deleted.
	r.takes.take.ID = "tk_00000000000000ff"

	_, _, err = r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-7"})
	require.ErrorIs(t, err, ErrConflict)
	assert.Contains(t, err.Error(), "the take was deleted")
}

// A look change is spoken with the voice its revision was, from the cache: no Speak and no Transcribe, after the
// owner changed the providers' order and voices, and with the provider's own default voice (an empty one), which
// it keeps. The revision's MP4 is not touched.
func TestAChangedLookReusesTheVoice(t *testing.T) {
	t.Parallel()

	r := framedRig(t)
	r.setDefault(t, legacyLook)

	v, _ := r.make(t, "")
	r.runAll(t)

	src := r.revision(t, v.ID, 1)
	require.Equal(t, speech.ProviderSoniox, src.Provider)
	require.Empty(t, src.UsedVoice)

	mp4, err := r.svc.File(t.Context(), v.ID, 1)
	require.NoError(t, err)
	before, err := os.ReadFile(mp4) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	spoken, heard := len(r.speech.spoken), r.speech.heard
	r.mu.Lock()
	r.off = []string{speech.ProviderSoniox}
	r.voices = map[string]string{speech.ProviderSoniox: "Linh", speech.ProviderElevenLabs: "x"}
	r.mu.Unlock()

	r.svc.cfg.Settings = nil // A pinned revision never reads them.

	_, rev, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-8", Look: &LookInput{
		Captions: &CaptionsInput{Preset: ptr(captions.Legacy)}, Frame: &FrameInput{Chrome: ptr("none")},
	}})
	require.NoError(t, err)
	r.runAll(t)

	made := r.revision(t, v.ID, rev.Rev)
	require.Equal(t, Review, made.State, made.FailedCause)
	assert.Equal(t, speech.ProviderSoniox, made.Provider, "the pinned provider, though switched off")
	assert.Empty(t, made.UsedVoice, "the empty voice kept, not Linh")
	assert.Len(t, r.speech.spoken, spoken, "no Speak")
	assert.Equal(t, heard, r.speech.heard, "no Transcribe")

	after, err := os.ReadFile(mp4) //nolint:gosec // The test's own file.
	require.NoError(t, err)
	assert.Equal(t, before, after, "the source's MP4 is unchanged")
}

// "Make again" of a failed revision (#937) makes it as it was asked: its lines, not the latest ones; its stored
// look, not the saved default; the provider and voice it was spoken with, not the owner's order now, so no clip
// is asked again.
func TestARetryMakesTheFailedRevisionAsItWas(t *testing.T) {
	t.Parallel()

	r := framedRig(t)
	r.setDefault(t, legacyLook)
	r.voices = map[string]string{speech.ProviderSoniox: "Linh"}
	r.maker.fail = "compose"

	v, _, err := r.ownerMake(t, "", &LookInput{Frame: &FrameInput{Chrome: ptr("none")}})
	require.NoError(t, err)
	r.runAll(t)

	failed := r.revision(t, v.ID, 1)
	require.Equal(t, Failed, failed.State)
	require.Equal(t, speech.ProviderSoniox, failed.Provider, "voiced before it failed")

	// Later revisions with other lines, a new default and another order of providers.
	r.maker.fail = ""
	r.make(t, "")
	r.runAll(t)
	_, err = r.svc.RequestChanges(t.Context(), v.ID, 2, ChangeRequest{Lines: []string{"Ba", "Bốn"}})
	require.NoError(t, err)
	r.runAll(t)

	r.setDefault(t, DefaultLook())
	r.mu.Lock()
	r.off = []string{speech.ProviderSoniox}
	r.voices = map[string]string{speech.ProviderElevenLabs: "x"}
	r.mu.Unlock()

	spoken := len(r.speech.spoken)

	_, rev, err := r.svc.Retry(t.Context(), v.ID, 1, Owner, "again")
	require.NoError(t, err)
	assert.Equal(t, 4, rev.Rev)
	assert.Equal(t, 1, rev.Parent)
	assert.Equal(t, failed.Lines, rev.Lines, "the failed revision's lines, not the latest's")
	assert.Equal(t, failed.Look, rev.Look, "its stored look, not the default")

	_, again, err := r.svc.Retry(t.Context(), v.ID, 1, Owner, "again")
	require.NoError(t, err)
	assert.Equal(t, 4, again.Rev, "a replay")

	r.runAll(t)

	made := r.revision(t, v.ID, 4)
	require.Equal(t, Review, made.State, made.FailedCause)
	assert.Equal(t, speech.ProviderSoniox, made.Provider)
	assert.Equal(t, "Linh", made.UsedVoice)
	assert.Len(t, r.speech.spoken, spoken, "no new speech")

	_, _, err = r.svc.Retry(t.Context(), v.ID, 4, Owner, "id-9")
	require.ErrorIs(t, err, ErrConflict, "only a failed revision is made again")
}

// The look sample with a revision starts from that revision's look, as a look change does, not from the default;
// without one, from the default. Its line is the revision's first, else the latest's, else the take's first.
func TestTheLookSampleStartsFromTheRevisionGiven(t *testing.T) {
	t.Parallel()

	r := framedRig(t)

	p, err := r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID})
	require.NoError(t, err)
	assert.Equal(t, DefaultLook(), p.look)
	assert.Equal(t, "Mở ứng dụng", p.line, "the take's first scene")

	_, _, err = r.ownerMake(t, "", &LookInput{
		Frame: &FrameInput{Chrome: ptr("iphone"), SafeTop: ptr(true)}, Background: &BackgroundInput{Preset: ptr("peach")},
		Captions: &CaptionsInput{Preset: ptr(captions.Legacy)},
	})
	require.NoError(t, err)
	r.setDefault(t, DefaultLook())

	v, err := r.svc.OfTake(t.Context(), takeID)
	require.NoError(t, err)

	r.runAll(t)
	_, err = r.svc.RequestChanges(t.Context(), v.ID, 1, ChangeRequest{Lines: []string{"Một hai", "Ba"}})
	require.NoError(t, err)

	src := r.revision(t, v.ID, 1)

	p, err = r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID, Rev: 1, Look: &LookInput{
		Captions: &CaptionsInput{Preset: ptr("pop")},
	}})
	require.NoError(t, err)
	assert.Equal(t, src.Look.Frame, p.look.Frame, "the revision's frame, not the default's android")
	assert.Equal(t, src.Look.Background, p.look.Background)
	assert.Equal(t, "pop", p.look.Captions.Preset)
	assert.Equal(t, "Mở ứng dụng", p.line, "the revision's first line")

	p, err = r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID})
	require.NoError(t, err)
	assert.Equal(t, DefaultLook(), p.look)
	assert.Equal(t, "Một hai", p.line, "the latest revision's first line")

	_, err = r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID, Rev: 7})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = r.svc.planPreview(t.Context(), PreviewRequest{Take: "tk_00000000000000bb"})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID, Aspect: "4:3"})
	require.ErrorIs(t, err, ErrInvalid)

	_, err = r.svc.planPreview(t.Context(), PreviewRequest{Take: takeID, Look: &LookInput{
		Background: &BackgroundInput{Colors: &[]string{}},
	}})
	require.ErrorIs(t, err, ErrInvalid, "colors sent empty are refused")
}

// A revision whose first scene was spoken and whose second failed is made again by the provider and voice that
// spoke the first (review of #1001, P1): its clip is reused, and the provider the owner now puts first is not asked.
func TestARetryAfterAFailedSceneKeepsTheFirstScenesVoice(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.voices = map[string]string{speech.ProviderSoniox: "Linh"}
	r.speech.failAfter[speech.ProviderSoniox] = 1

	v, _ := r.make(t, "")
	r.runAll(t)

	failed := r.revision(t, v.ID, 1)
	require.Equal(t, Failed, failed.State)
	require.Contains(t, failed.FailedCause, "scene 2")
	assert.Equal(t, speech.ProviderSoniox, failed.Provider, "the provider is kept once it spoke the first scene")
	assert.Equal(t, "Linh", failed.UsedVoice)

	r.speech.mu.Lock()
	delete(r.speech.failAfter, speech.ProviderSoniox)
	r.speech.mu.Unlock()
	r.mu.Lock()
	r.order = []string{speech.ProviderElevenLabs, speech.ProviderSoniox}
	r.mu.Unlock()

	_, rev, err := r.svc.Retry(t.Context(), v.ID, 1, Owner, "id-10")
	require.NoError(t, err)
	r.runAll(t)

	made := r.revision(t, v.ID, rev.Rev)
	require.Equal(t, Review, made.State, made.FailedCause)
	assert.Equal(t, speech.ProviderSoniox, made.Provider)
	assert.Equal(t, 1, r.speech.count(speech.ProviderSoniox, "Mở ứng dụng"), "scene 1's clip is reused")
	assert.Equal(t, 0, r.speech.count(speech.ProviderElevenLabs, "Mở ứng dụng"), "no other voice")
	assert.Equal(t, 0, r.speech.count(speech.ProviderElevenLabs, "Chạm vào Đăng nhập"))
}

// A pinned revision whose clip was lost since is spoken again by its pinned provider, though the owner turned that
// provider off: the call is pinned, so the speech service neither reads the settings nor refuses it (review of
// #1001, P2).
func TestAPinnedRevisionSpeaksALostClipWithItsProvider(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	v, _ := r.make(t, "")
	r.runAll(t)
	require.Equal(t, speech.ProviderSoniox, r.revision(t, v.ID, 1).Provider)

	clip := filepath.Join(r.dir, voiceDir,
		voiceHash("Chạm vào Đăng nhập", speech.ProviderSoniox, speech.SonioxTTSModel, "", Language)+"."+clipFormat)
	require.NoError(t, os.Remove(clip))

	r.mu.Lock()
	r.off = []string{speech.ProviderSoniox}
	r.mu.Unlock()
	r.speech.mu.Lock()
	r.speech.off = []string{speech.ProviderSoniox}
	r.speech.mu.Unlock()

	_, rev, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{RequestID: "id-11", Look: &LookInput{
		Frame: &FrameInput{Chrome: ptr("full")},
	}})
	require.NoError(t, err)
	r.runAll(t)

	made := r.revision(t, v.ID, rev.Rev)
	require.Equal(t, Review, made.State, made.FailedCause)
	assert.Equal(t, speech.ProviderSoniox, made.Provider)
	assert.Equal(t, 2, r.speech.count(speech.ProviderSoniox, "Chạm vào Đăng nhập"), "the lost clip, spoken again")
}

// A look change and a retry need a request id (row 4; review of #1001): without one, the same request sent twice
// would queue two revisions.
func TestALookChangeAndARetryNeedARequestID(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.maker.fail = "compose"

	v, _ := r.make(t, "")
	r.runAll(t)

	_, _, err := r.svc.Retry(t.Context(), v.ID, 1, Owner, "")
	require.ErrorIs(t, err, ErrInvalid)

	r.maker.fail = ""
	_, retried, err := r.svc.Retry(t.Context(), v.ID, 1, Owner, "again")
	require.NoError(t, err)
	r.runAll(t)

	_, _, err = r.svc.ChangeLook(t.Context(), v.ID, retried.Rev, LookChange{Look: &LookInput{
		Frame: &FrameInput{Chrome: ptr("full")},
	}})
	require.ErrorIs(t, err, ErrInvalid)

	got, err := r.svc.Get(t.Context(), v.ID)
	require.NoError(t, err)
	assert.Len(t, got.Revisions, 2, "nothing queued without a request id")
}
