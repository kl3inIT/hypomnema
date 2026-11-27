package compose

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The cards and the master (issue #1230, PR 1's acceptance): a video made with a title card and an end card shows
// each for its length, on the look's background in its accent; the voice starts after the title card, the music
// plays under both; and a video with music measures -16 ±1 LUFS integrated.

// The numbers of the design (#1230, rows 1 and 2), written out so that a change of one in the code fails here.
const (
	designTitleMs = 2600
	designEndMs   = 3000
	designFadeMs  = 350
	designLUFS    = -16.0
	designLUFSTol = 1.0
)

var accentPink = [3]int{0xff, 0x33, 0x66}

// cardsTake writes a take of one red segment of 3 s with two steps, and their voices: tones of voiceHz, 1.5 s each.
func cardsTake(t *testing.T, tools ffmpeg.Tools, dir string) []string {
	t.Helper()

	seg := colorSegment(t, tools, dir, "0.mp4", 360, 640, []string{"red"}, []int64{3000})
	writeTake(t, dir, devicetool.Take{
		ID: "tk_cards", DurationMs: 3000, Segments: []devicetool.TakeSegment{seg},
		Steps: []devicetool.TakeStep{{AtMs: 0, Text: "Mở"}, {AtMs: 1500, Text: "Đặt"}},
	})

	voices := make([]string, 2)
	for i := range voices {
		voices[i] = lavfi(t, tools, filepath.Join(dir, fmt.Sprintf("voice-%d.wav", i)),
			fmt.Sprintf("aevalsrc=0.1*sin(2*PI*%d*t):s=%d:d=1.5", voiceHz, sampleRate), "-c:a", "pcm_s16le")
	}

	return voices
}

// voiceOnset is when the voice is first heard, in ms: the first 10 ms in which its tone is over -40 dBFS.
func voiceOnset(samples []float32) float64 {
	const n = mixRate / 100

	coeff := 2 * math.Cos(2*math.Pi*voiceHz/mixRate)

	for a := 0; a+n <= len(samples); a += n {
		var s1, s2 float64

		for _, x := range samples[a : a+n] {
			s0 := float64(x) + coeff*s1 - s2
			s2, s1 = s1, s0
		}

		if 20*math.Log10(2*math.Sqrt(math.Max(s1*s1+s2*s2-coeff*s1*s2, 1e-30))/n) > -40 {
			return float64(a) * 1000 / mixRate
		}
	}

	return -1
}

// accentPixels counts the pixels of a frame within 40 of the accent.
func accentPixels(px []byte) int {
	n := 0

	for i := 0; i+2 < len(px); i += 3 {
		if within(px[i:i+3], accentPink, 40) {
			n++
		}
	}

	return n
}

func TestTheCardsOpenAndCloseTheVideo(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	voices := cardsTake(t, tools, dir)
	track := sineTrack(t, tools, filepath.Join(dir, "music.wav"), musicHz, 0.25, 30)
	bg := Background{Preset: "ocean"}
	cards := &Cards{
		Accent: "#ff3366",
		Title:  &Card{Title: "SecondBite", Subtitle: "Đặt món trong 30 giây", DurationMs: designTitleMs},
		End:    &Card{Title: "Tải SecondBite", CTAURL: "secondbite.vn", DurationMs: designEndMs},
	}

	out := filepath.Join(dir, "v.mp4")
	res, err := Compose(t.Context(), tools, Input{
		Dir: dir, Voice: voices, Aspect: Portrait, Background: bg, Cards: cards,
		Music: &Music{File: track, LengthMs: 30000}, Work: filepath.Join(dir, "work"), Out: out,
	})
	require.NoError(t, err)
	require.Len(t, res.Scenes, 2)

	// Each card for its length; the scenes after the title card.
	assert.EqualValues(t, designTitleMs, res.TitleMs)
	assert.EqualValues(t, designEndMs, res.EndMs)
	assert.EqualValues(t, designTitleMs, res.Scenes[0].OutStartMs, "the first scene starts after the title card")
	assert.Equal(t, res.Scenes[0].OutStartMs+res.Scenes[0].OutLengthMs, res.Scenes[1].OutStartMs)

	contentMs := res.Scenes[0].OutLengthMs + res.Scenes[1].OutLengthMs
	endAt := designTitleMs + contentMs
	assert.InDelta(t, endAt+designEndMs, res.LengthMs, frameMs+1, "the video is the cards and the scenes")
	assert.InDelta(t, res.LengthMs, streamMs(t, tools, out, "a:0"), frameMs+1, "the sound lasts the picture")

	// The picture: black at the start of the fade in, the look's background and the accent in each card, the take
	// between them.
	size := Size{720, 1280}
	ground := newCanvas(size.Width, size.Height)
	paintBackground(ground, bg)
	img := ground.image()
	corner := [3]int{int(img.Pix[4*(8*size.Width+8)]), int(img.Pix[4*(8*size.Width+8)+1]),
		int(img.Pix[4*(8*size.Width+8)+2])}

	first := frameAt(t, tools, out, 0, size)
	assert.True(t, within(first[3*(640*size.Width+360):], [3]int{0, 0, 0}, 20), "the title card fades in from black")

	for name, at := range map[string]int64{"title": designTitleMs / 2, "end": endAt + designEndMs/2} {
		px := frameAt(t, tools, out, at, size)
		got := pixel(px, size, 8, 8)
		assert.True(t, near(got, corner, 12), "the %s card stands on the background: %v, not %v", name, got, corner)
		assert.Greater(t, accentPixels(px), 60, "the %s card has its accent", name)
		assert.False(t, near(pixel(px, size, 360, 1200), red, 40), "the %s card is not the take", name)
	}

	for _, at := range []int64{designTitleMs + 200, endAt - 200} {
		assert.True(t, near(pixel(frameAt(t, tools, out, at, size), size, 360, 640), red, 12), "the take at %d ms", at)
	}

	// The end card's URL is in the accent: it has more of it than the title card's rule.
	assert.Greater(t, accentPixels(frameAt(t, tools, out, endAt+designEndMs/2, size)),
		2*accentPixels(frameAt(t, tools, out, designTitleMs/2, size)))

	// The sound: no voice under the cards, the voice from the first scene's start, the music under both cards.
	s := decode(t, tools, out, 1)
	assert.InDelta(t, designTitleMs, voiceOnset(s), 30, "the voice starts after the title card")

	for _, at := range []float64{300, 1300, 2300} {
		assert.Less(t, bandDB(s, at, voiceHz), -60.0, "no voice under the title card at %.0f ms", at)
		assert.Greater(t, bandDB(s, at, musicHz), -40.0, "the music under the title card at %.0f ms", at)
	}

	for _, at := range []float64{float64(endAt) + 300, float64(endAt) + 900} {
		assert.Less(t, bandDB(s, at, voiceHz), -60.0, "no voice under the end card at %.0f ms", at)
		assert.Greater(t, bandDB(s, at, musicHz), -40.0, "the music under the end card at %.0f ms", at)
	}

	// The thumbnail is the title card after its fade in, not its black first frame.
	assert.EqualValues(t, designFadeMs, res.ThumbnailAtMs())

	thumb := filepath.Join(dir, "thumb.jpg")
	require.NoError(t, Thumbnail(t.Context(), tools, out, thumb, res.ThumbnailAtMs()))

	raw, err := os.ReadFile(thumb) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	pic, err := jpeg.Decode(bytes.NewReader(raw))
	require.NoError(t, err)

	r, g, b, _ := pic.At(8, 8).RGBA()
	assert.True(t, near([3]int{int(r >> 8), int(g >> 8), int(b >> 8)}, corner, 14), "the thumbnail is the card")
}

// A speech-like voice whose clicks cap its gain, as the SecondBite trial's did (-21.2 LUFS), under a bed of
// agent-demo-video's ambient noise (TestTheBed): without the master the mix misses -16 LUFS and is warned of; with
// it, the made video measures -16 ±1 LUFS integrated and under -1 dBTP, with no warning.
func TestTheMasterIsAtItsLoudness(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	voices := musicTake(t, tools, dir, []clip{{-20, 4000}, {-20, 5000}, {-20, 3000}})

	for i, sec := range []int{4, 5, 3} {
		voices[i] = lavfi(t, tools, filepath.Join(dir, fmt.Sprintf("speech-%d.wav", i)), fmt.Sprintf(
			"aevalsrc=(random(%d)*2-1)*0.35*pow(max(0\\,sin(2*PI*3*t))\\,2)+if(lt(mod(t\\,1.3)\\,0.003)\\,0.95\\,0)"+
				":s=%d:d=%d", i, sampleRate, sec), "-c:a", "pcm_s16le")
	}

	bed := lavfi(t, tools, filepath.Join(dir, "bed.wav"),
		"anoisesrc=color=brown:r=44100:a=0.5:d=7.3,lowpass=f=300,highpass=f=45,tremolo=f=0.12:d=0.22,volume=6dB",
		"-c:a", "pcm_s16le")

	control := composeWith(t, tools, dir, voices, &Music{File: bed, LengthMs: 7300, NoMaster: true}, "control")
	master := composeWith(t, tools, dir, voices, &Music{File: bed, LengthMs: 7300}, "master")
	require.NotNil(t, control.Music)
	require.NotNil(t, master.Music)
	t.Logf("without the master: %+v; with it: %+v", *control.Music, *master.Music)

	assert.Less(t, control.Music.OutLUFS, designLUFS-2, "the voice's clicks keep the mix quiet")
	assert.Contains(t, fmt.Sprint(control.Warnings), "the mix is")

	got, err := Measure(t.Context(), tools, master.out, 0)
	require.NoError(t, err)
	assert.InDelta(t, designLUFS, got.LUFS, designLUFSTol, "the video's integrated loudness")
	assert.LessOrEqual(t, got.TPdB(), v5PeakDBTP)
	assert.InDelta(t, got.LUFS, master.Music.OutLUFS, 0.001)
	assert.Empty(t, master.Warnings)
}

func TestACardIsChecked(t *testing.T) {
	t.Parallel()

	ok := Card{Title: "SecondBite", DurationMs: designTitleMs}
	require.NoError(t, Cards{Accent: "#6c8cff", Title: &ok}.Check())
	require.NoError(t, Cards{Accent: "#6c8cff"}.Check(), "no card is no card")

	for name, c := range map[string]Cards{
		"an accent not #rrggbb": {Accent: "blue", Title: &ok},
		"no title":              {Accent: "#6c8cff", End: &Card{Title: " ", DurationMs: designEndMs}},
		"too short":             {Accent: "#6c8cff", Title: &Card{Title: "A", DurationMs: 499}},
		"too long":              {Accent: "#6c8cff", Title: &Card{Title: "A", DurationMs: 10001}},
		"two lines":             {Accent: "#6c8cff", Title: &Card{Title: "A\nB", DurationMs: designTitleMs}},
		"a long URL": {Accent: "#6c8cff", End: &Card{
			Title: "A", CTAURL: "https://" + string(bytes.Repeat([]byte("a"), 200)), DurationMs: designEndMs,
		}},
	} {
		require.Error(t, c.Check(), name)
	}

	// A card lasts its whole frames: 2600 ms is 78 frames, 2610 ms the 79 that start within it.
	assert.EqualValues(t, 2600, Cards{Title: &ok}.TitleMs())
	assert.Equal(t, int64(math.Round(79*1000.0/FPS)), Cards{End: &Card{Title: "A", DurationMs: 2610}}.EndMs())
	assert.Zero(t, Cards{}.TitleMs())
}

// The look sample of a card is the card between its fades: the background and the accent.
func TestACardStill(t *testing.T) {
	t.Parallel()

	tools := tools(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "card.jpg")
	require.NoError(t, CardStill(t.Context(), tools, CardStillInput{
		Card:   Card{Title: "Tải SecondBite", Subtitle: "Đặt món", CTAURL: "secondbite.vn", DurationMs: designEndMs},
		Accent: "#ff3366", Background: Background{Preset: "ocean"}, Aspect: Landscape,
		Work: filepath.Join(dir, "work"), Out: out,
	}))

	raw, err := os.ReadFile(out) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	pic, err := jpeg.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Equal(t, 1280, pic.Bounds().Dx())

	n := 0

	for y := range pic.Bounds().Dy() {
		for x := range pic.Bounds().Dx() {
			r, g, b, _ := pic.At(x, y).RGBA()
			if near([3]int{int(r >> 8), int(g >> 8), int(b >> 8)}, accentPink, 40) {
				n++
			}
		}
	}

	assert.Greater(t, n, 200, "the rule and the URL in the accent")
}
