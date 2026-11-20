package captions

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// The captions as libass draws them, before any encode (issue #908, PR 1, checks 3-5): each ASS file is burned
// on a grey frame, color=0x808080,format=rgb24,subtitles, written as a PNG, and measured by its pixels.

// tools are the pinned ffmpeg, which CI installs with
// `go run ./internal/controlplane/videos/ffmpeg/installffmpeg <dir>` and names in HYPOMNEMA_FFMPEG_DIR. Without
// it the test skips, unless HYPOMNEMA_NEED_FFMPEG says it is needed.
func tools(t *testing.T) ffmpeg.Tools {
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

// line is the check's line, its stacked diacritics the tallest Vietnamese has.
const line = "ỂẪ Ổn định hệ thống đăng nhập"

// lineCue is the line as one cue, each word 0.3 s.
func lineCue() Cue { return cue(0.3, strings.Fields(line)...) }

const grey = 128

// burn draws an ASS file on a grey w × h frame at a time, and returns the frame and what ffmpeg logged.
func burn(t *testing.T, tl ffmpeg.Tools, ass string, w, h int, at float64, loglevel string) (*image.NRGBA, string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "captions.ass"), []byte(ass), 0o600))
	require.NoError(t, os.Symlink(tl.FontsDir, filepath.Join(dir, "fonts")))

	//nolint:gosec // The pinned ffmpeg.
	cmd := exec.CommandContext(t.Context(), tl.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", loglevel,
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x808080:s=%dx%d:r=30:d=%.3f", w, h, at+1),
		"-vf", "format=rgb24,subtitles=filename=captions.ass:fontsdir=fonts",
		"-ss", fmt.Sprintf("%.3f", at), "-frames:v", "1", "-c:v", "png", "-f", "image2", "frame.png")
	cmd.Dir = dir

	var stderr bytes.Buffer

	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), stderr.String())

	f, err := os.Open(filepath.Join(dir, "frame.png")) //nolint:gosec // The test's folder.
	require.NoError(t, err)

	defer func() { _ = f.Close() }()

	img, err := png.Decode(f)
	require.NoError(t, err)

	out := image.NewNRGBA(img.Bounds())
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			out.Set(x, y, img.At(x, y))
		}
	}

	return out, stderr.String()
}

func rgb(img *image.NRGBA, x, y int) [3]int {
	c := img.NRGBAAt(x, y)

	return [3]int{int(c.R), int(c.G), int(c.B)}
}

func near(a, b [3]int, tol int) bool {
	for i := range 3 {
		if a[i]-b[i] > tol || b[i]-a[i] > tol {
			return false
		}
	}

	return true
}

// over is a #RRGGBB[AA] color laid over the grey as ffmpeg draws it on this frame: in the limited range the
// frame is tagged with, as on the videos made (yuv420p), black at 16 and white at 235.
func over(hex string) [3]int {
	r, g, b, a := rgba(hex)
	mix := func(c int) int { return ((16+c*219/255)*a + grey*(255-a) + 127) / 255 }

	return [3]int{mix(r), mix(g), mix(b)}
}

// rect is a rectangle of pixels, inclusive.
type rect struct{ x0, y0, x1, y1 int }

// bbox is the bounding box of the pixels that match; ok is false for none.
func bbox(img *image.NRGBA, match func(x, y int) bool) (rect, bool) {
	r, ok := rect{x0: 1 << 30, y0: 1 << 30, x1: -1, y1: -1}, false

	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			if match(x, y) {
				r.x0, r.y0, r.x1, r.y1 = min(r.x0, x), min(r.y0, y), max(r.x1, x), max(r.y1, y)
				ok = true
			}
		}
	}

	return r, ok
}

func notGrey(img *image.NRGBA) func(x, y int) bool {
	return func(x, y int) bool { return rgb(img, x, y) != [3]int{grey, grey, grey} }
}

// rendered is a preset's ASS for cues, as Render makes it, and its frames as placed, fitted by libass.
func rendered(t *testing.T, tl ffmpeg.Tools, preset string, pos Position, w, h int, cues []Cue,
	rings []compose.Ring,
) (string, *renderer, []placedFrame) {
	t.Helper()

	r, err := newRenderer(preset, w, h)
	require.NoError(t, err)

	frames, err := r.render(t.Context(), tl, cues, pos, rings)
	require.NoError(t, err)
	require.NotEmpty(t, frames)

	var ass strings.Builder

	ass.WriteString(r.header(w, h))

	for _, pf := range frames {
		ass.WriteString(r.events(pf))
	}

	return ass.String(), r, frames
}

func mid(pf placedFrame) float64 { return (pf.from + pf.to) / 2 }

var sizes = [][2]int{{720, 1280}, {1280, 720}}

// Each preset's font is the one libass picks, by its PostScript name (check 3).
func TestFontselectPicksEachPresetsFont(t *testing.T) {
	t.Parallel()

	tl := tools(t)
	ps := map[string]string{
		fontExtraBold: "BeVietnamPro-ExtraBold", fontBold: "BeVietnamPro-Bold",
		fontMedium: "BeVietnamPro-Medium", fontMono: "JetBrainsMono-SemiBold", FontName: "BeVietnamPro-SemiBold",
	}

	for _, p := range Presets {
		t.Run(p.ID, func(t *testing.T) {
			t.Parallel()

			ass, err := Render(t.Context(), tl, []Cue{lineCue()}, Look{Preset: p.ID}, 720, 1280, nil)
			require.NoError(t, err)

			name := p.Font
			if p.ID == Legacy {
				name = FontName
			}

			_, log := burn(t, tl, ass, 720, 1280, 0.1, "info")
			assert.Regexp(t, `fontselect: \(`+regexp.QuoteMeta(name)+`, 400, 0\) -> \S+, 0, `+
				regexp.QuoteMeta(ps[name])+`\n`, log)
			assert.NotContains(t, log, "fontselect: failed")
		})
	}
}

// A box preset draws its box where it was laid out, in its color over the grey, with all its text inside it and
// the line's stacked diacritics below its top (check 4).
func TestBoxPresetsDrawTheirBox(t *testing.T) {
	t.Parallel()

	tl := tools(t)

	for _, preset := range []string{"karaoke", "clean", "boxed", "mono"} {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s-%dx%d", preset, size[0], size[1]), func(t *testing.T) {
				t.Parallel()

				ass, r, frames := rendered(t, tl, preset, PositionPreset, size[0], size[1], []Cue{lineCue()}, nil)
				img, _ := burn(t, tl, ass, size[0], size[1], mid(frames[0]), "error")

				x, y, w, h := r.boxRect(frames[0])
				want := rect{int(x), int(y), int(x+w) - 1, int(y+h) - 1}
				boxColor := over(r.p.Box)

				box, ok := bbox(img, func(x, y int) bool { return near(rgb(img, x, y), boxColor, 8) })
				require.True(t, ok, "the box is drawn")
				assert.InDelta(t, want.x0, box.x0, 2)
				assert.InDelta(t, want.y0, box.y0, 2)
				assert.InDelta(t, want.x1, box.x1, 2)
				assert.InDelta(t, want.y1, box.y1, 2)

				all, _ := bbox(img, notGrey(img))
				assert.GreaterOrEqual(t, all.x0, want.x0-2, "no text left of the box")
				assert.GreaterOrEqual(t, all.y0, want.y0-2, "no text above the box")
				assert.LessOrEqual(t, all.x1, want.x1+2, "no text right of the box")
				assert.LessOrEqual(t, all.y1, want.y1+2, "no text below the box")

				// The text's top, away from the rounded corners: the diacritics of ỂẪ are inside the box.
				radius := int(min(h*radiusOfH, frames[0].block.em*radiusEm)) + 2
				text, ok := bbox(img, func(px, py int) bool {
					return px > want.x0+radius && px < want.x1-radius && py >= want.y0 && py <= want.y1 &&
						!near(rgb(img, px, py), boxColor, 8)
				})
				require.True(t, ok, "the text is drawn")
				assert.Greater(t, text.y0, want.y0+2, "ỂẪ are below the box's top")
			})
		}
	}
}

// sweepTokens are each printable ASCII letter and digit and each Vietnamese letter, lower and upper case, repeated
// to overflow a block even at the 20 px floor (a narrow glyph is about 5 px there), and the reviews' probes of #943:
// N×100 (One Word, Word Pair), j×999 (Minimal), a×999 (Bar), W×100, I×1000, ﬃ×35, and an i with eight graves
// stacked on it, then forty i, eight graves again (One Word drew it 684 px, measured 662).
func sweepTokens(w int) []string {
	n := int(math.Ceil(2.2 * SafeFraction * float64(w) / 5))

	seen := map[string]bool{}

	var letters []string

	for c := '0'; c <= 'z'; c++ {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			letters = append(letters, string(c))
		}
	}

	letters = append(letters, vietnamese()...)

	var out []string

	for _, l := range letters {
		if !seen[l] {
			seen[l] = true
			out = append(out, strings.Repeat(l, n))
		}
	}

	// The third review's: marks stacked on a letter at both ends reach far above the line, past where it was
	// measured then.
	marks := strings.Repeat("\u0300", 8)

	return append(out, strings.Repeat("N", 100), strings.Repeat("j", 999), strings.Repeat("a", 999),
		strings.Repeat("W", 100), strings.Repeat("I", 1000), strings.Repeat("ﬃ", 35),
		"i"+marks+strings.Repeat("i", 40)+marks)
}

// burnEach burns an ASS file whose cue i shows from i s to i+0.8 s, and gives each, whole, its frame at i+0.5 s, one
// at a time, as they are read; it returns what ffmpeg logged.
func burnEach(t *testing.T, tl ffmpeg.Tools, ass string, w, h, n int, each func(i int, img *image.NRGBA)) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "captions.ass"), []byte(ass), 0o600))
	require.NoError(t, os.Symlink(tl.FontsDir, filepath.Join(dir, "fonts")))

	//nolint:gosec // The pinned ffmpeg.
	cmd := exec.CommandContext(t.Context(), tl.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "info",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x808080:s=%dx%d:r=2:d=%d", w, h, n),
		"-vf", `select=mod(n\,2),format=rgb24,subtitles=filename=captions.ass:fontsdir=fonts`,
		"-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
	cmd.Dir = dir

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	in := bufio.NewReaderSize(stdout, 1<<20)
	raw := make([]byte, w*h*3)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))

	for i := range n {
		if _, err := io.ReadFull(in, raw); err != nil {
			_ = cmd.Wait()
			require.NoError(t, err, "frame %d of %d: %s", i, n, stderr.String())
		}

		for p := range w * h {
			copy(img.Pix[4*p:], raw[3*p:3*p+3])
			img.Pix[4*p+3] = 255
		}

		each(i, img)
	}

	n2, _ := io.Copy(io.Discard, in)
	require.NoError(t, cmd.Wait(), stderr.String())
	require.Zero(t, n2, "one frame a cue")

	return stderr.String()
}

// Every preset's block is at most 0.94 of the width, measured on the pixels libass draws (check 4, the must-fix of
// #935, and the reviews of #943): a sweep of every letter and digit, each repeated to overflow, at both sizes;
// and the line, each of its stretches. Each is drawn with the preset's own font, none falling back.
func TestEveryBlockFitsTheWidthAsDrawn(t *testing.T) {
	t.Parallel()

	tl := tools(t)
	ps := map[string]string{
		fontExtraBold: "BeVietnamPro-ExtraBold", fontBold: "BeVietnamPro-Bold",
		fontMedium: "BeVietnamPro-Medium", fontMono: "JetBrainsMono-SemiBold",
	}

	for _, p := range Presets {
		if p.ID == Legacy {
			continue // Classic keeps the size it had (#935): it is not measured by the cap.
		}

		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s-%dx%d", p.ID, size[0], size[1]), func(t *testing.T) {
				t.Parallel()

				w, h := size[0], size[1]
				limit := 0.94 * float64(w)

				tokens := sweepTokens(w)
				cues := make([]Cue, len(tokens))

				for i, tok := range tokens {
					at := float64(i)
					cues[i] = Cue{
						Index: i + 1, StartMs: int64(i) * 1000, EndMs: int64(i)*1000 + 800, Text: tok,
						Words: []Word{{Word: tok, Start: at, End: at + 0.8}},
					}
				}

				ass, r, frames := rendered(t, tl, p.ID, PositionPreset, w, h, cues, nil)
				require.Len(t, frames, len(cues), "one block a token")

				log := burnEach(t, tl, ass, w, h, len(cues), func(i int, img *image.NRGBA) {
					got := drawnWidth(img, r)
					assert.LessOrEqual(t, got, limit, "%q×%d: %.0f px wide",
						string([]rune(tokens[i])[0]), len([]rune(tokens[i])), got)
				})

				for _, l := range strings.Split(log, "\n") {
					if strings.Contains(l, "fontselect:") {
						assert.True(t, strings.HasSuffix(l, ", "+ps[p.Font]), "drawn with the preset's font: %s", l)
					}
				}
			})
		}
	}

	// The line, at each of its stretches: blocks of one and two lines, of several words.
	for _, p := range Presets {
		if p.ID == Legacy {
			continue
		}

		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s-%dx%d-line", p.ID, size[0], size[1]), func(t *testing.T) {
				t.Parallel()

				w, h := size[0], size[1]
				ass, r, frames := rendered(t, tl, p.ID, PositionPreset, w, h, []Cue{lineCue()}, nil)

				for _, pf := range frames {
					img, _ := burn(t, tl, ass, w, h, mid(pf), "error")
					assert.LessOrEqual(t, drawnWidth(img, r), 0.94*float64(w), "%v", shownWords(pf))
				}
			})
		}
	}
}

// drawnWidth is the width of what is drawn in a frame: every pixel not the grey; for a bar, its
// text, the band (the video's width) left out, as its own first and last columns are drawn a level off its color.
func drawnWidth(img *image.NRGBA, r *renderer) float64 {
	w := img.Bounds().Dx()
	drawn := notGrey(img)

	if r.p.Bar {
		band := over(r.p.Box)
		drawn = func(x, y int) bool {
			return x > 0 && x < w-1 && !near(rgb(img, x, y), band, 0) && rgb(img, x, y) != [3]int{grey, grey, grey}
		}
	}

	all, ok := bbox(img, drawn)
	if !ok {
		return 0
	}

	return float64(all.x1 - all.x0 + 1)
}

// Karaoke greys the words not spoken yet, #9A9A9A; Pop does not show them (check 4).
func TestKaraokeGreysAndPopHidesTheWordsToCome(t *testing.T) {
	t.Parallel()

	tl := tools(t)
	future := over("#9A9A9A")

	count := func(img *image.NRGBA) int {
		n := 0

		for y := range img.Bounds().Dy() {
			for x := range img.Bounds().Dx() {
				if near(rgb(img, x, y), future, 8) {
					n++
				}
			}
		}

		return n
	}

	c := cue(0.3, "Ổn", "định", "hệ", "thống")
	ass, _, frames := rendered(t, tl, "karaoke", PositionPreset, 1280, 720, []Cue{c}, nil)
	first, _ := burn(t, tl, ass, 1280, 720, mid(frames[0]), "error")
	last, _ := burn(t, tl, ass, 1280, 720, mid(frames[len(frames)-1]), "error")
	assert.Greater(t, count(first), 300, "three words to come, in grey")
	assert.Greater(t, count(first), 5*count(last), "none to come at the last word")

	// Pop at the first word draws what a cue of only that word draws.
	ass, _, frames = rendered(t, tl, "pop", PositionPreset, 720, 1280, []Cue{c}, nil)
	popped, _ := burn(t, tl, ass, 720, 1280, mid(frames[0]), "error")

	alone, err := Render(t.Context(), tl, []Cue{cue(0.3, "Ổn")}, Look{Preset: "pop"}, 720, 1280, nil)
	require.NoError(t, err)

	want, _ := burn(t, tl, alone, 720, 1280, mid(frames[0]), "error")
	assert.Equal(t, want.Pix, popped.Pix, "the words to come are hidden")
}

// Where the blocks are drawn (check 4): a preset's own place on a wide video is the bottom; a chosen center holds;
// a ring in the bottom block moves the whole cue to the top; a bar moves flush with the top.
func TestPlacementAsDrawn(t *testing.T) {
	t.Parallel()

	tl := tools(t)
	drawnBox := func(t *testing.T, preset string, pos Position, w, h int, rings []compose.Ring) []rect {
		t.Helper()

		ass, r, frames := rendered(t, tl, preset, pos, w, h, []Cue{lineCue()}, rings)

		var out []rect

		for _, pf := range frames {
			img, _ := burn(t, tl, ass, w, h, mid(pf), "error")
			box, ok := bbox(img, func(x, y int) bool { return near(rgb(img, x, y), over(r.p.Box), 8) })
			require.True(t, ok)

			out = append(out, box)
		}

		return out
	}

	t.Run("landscape preset is bottom", func(t *testing.T) {
		t.Parallel()

		for _, b := range drawnBox(t, "karaoke", PositionPreset, 1280, 720, nil) {
			assert.InDelta(t, 720-50-1, b.y1, 2)
		}
	})

	t.Run("a chosen center holds", func(t *testing.T) {
		t.Parallel()

		for _, b := range drawnBox(t, "karaoke", PositionCenter, 1280, 720, nil) {
			assert.InDelta(t, 360, float64(b.y0+b.y1+1)/2, 2)
		}
	})

	t.Run("a ring flips the whole cue", func(t *testing.T) {
		t.Parallel()

		ring := []compose.Ring{{AtMs: 1500, X: 360, Y: 1150, Size: 101}}
		boxes := drawnBox(t, "clean", PositionPreset, 720, 1280, ring)
		require.Len(t, boxes, 2, "two windows, the second after the ring is gone")

		for _, b := range boxes {
			assert.InDelta(t, 90, b.y0, 2)
		}
	})

	t.Run("a bar flips flush with the top", func(t *testing.T) {
		t.Parallel()

		ring := []compose.Ring{{AtMs: 0, X: 360, Y: 1250, Size: 101}}
		for _, b := range drawnBox(t, "bar", PositionPreset, 720, 1280, ring) {
			assert.Equal(t, 0, b.y0)
			assert.Equal(t, 0, b.x0)
			assert.Equal(t, 719, b.x1)
		}
	})
}

// Two cues that overlap in time are never drawn over each other (the fourth review of #943): One Word at 1.05 s,
// while "xong." is still said and "Bây" has started, draws what the first cue alone draws, and at 1.15 s what the
// second alone draws.
func TestOverlappingCuesAreDrawnOneAtATime(t *testing.T) {
	t.Parallel()

	tl := tools(t)
	cues := overlapping(t)

	both, err := Render(t.Context(), tl, cues, Look{Preset: "one-word"}, 720, 1280, nil)
	require.NoError(t, err)

	for _, tc := range []struct {
		at  float64
		cue Cue
	}{{1.05, cues[0]}, {1.15, cues[1]}} {
		alone, err := Render(t.Context(), tl, []Cue{tc.cue}, Look{Preset: "one-word"}, 720, 1280, nil)
		require.NoError(t, err)

		got, _ := burn(t, tl, both, 720, 1280, tc.at, "error")
		want, _ := burn(t, tl, alone, 720, 1280, tc.at, "error")
		_, drawn := bbox(want, notGrey(want))
		require.True(t, drawn, "the cue alone draws a word at %.2f s", tc.at)
		assert.Equal(t, want.Pix, got.Pix, "only %q is drawn at %.2f s", tc.cue.Text, tc.at)
	}
}

// The fifth review's case, drawn: at 670 ms the second sentence is drawn at the bottom, as the ring that showed
// while the first was drawn ended at 500 ms, before it showed (560 ms); at 300 ms the first is at the top.
func TestARingFlipsOnlyTheCueItIsDrawnWithAsDrawn(t *testing.T) {
	t.Parallel()

	tl := tools(t)
	cues, rings := ringBeforeShown(t)

	for _, preset := range []string{"clean", "one-word", "bar"} {
		t.Run(preset, func(t *testing.T) {
			t.Parallel()

			ass, err := Render(t.Context(), tl, cues, Look{Preset: preset}, 720, 1280, rings)
			require.NoError(t, err)

			for _, tc := range []struct {
				at     float64
				bottom bool
			}{{0.3, false}, {0.67, true}} {
				img, _ := burn(t, tl, ass, 720, 1280, tc.at, "error")
				drawn, ok := bbox(img, notGrey(img))
				require.True(t, ok, "drawn at %.2f s", tc.at)

				if tc.bottom {
					assert.Greater(t, drawn.y0, 640, "at %.2f s the caption is at the bottom: rows %d-%d",
						tc.at, drawn.y0, drawn.y1)
				} else {
					assert.Less(t, drawn.y1, 640, "at %.2f s the caption is at the top: rows %d-%d",
						tc.at, drawn.y0, drawn.y1)
				}
			}
		})
	}
}

// The sixth review's case, drawn: One Word with a ring from 640 ms, in the pause after "Rồi," (450 ms to 1.25 s),
// draws its words at the bottom at 300 ms and 1.5 s; the control, a ring from 250 ms while "RỒI" is drawn, moves
// both to the top.
func TestARingInAPauseFlipsNothingAsDrawn(t *testing.T) {
	t.Parallel()

	tl := tools(t)
	cues := pauseCue(t)

	for atMs, bottom := range map[int64]bool{640: true, 250: false} {
		t.Run(fmt.Sprintf("ring-at-%dms", atMs), func(t *testing.T) {
			t.Parallel()

			ring := []compose.Ring{{AtMs: atMs, X: 360, Y: 1200, Size: 101}}
			ass, err := Render(t.Context(), tl, cues, Look{Preset: "one-word"}, 720, 1280, ring)
			require.NoError(t, err)

			for _, at := range []float64{0.3, 1.5} {
				img, _ := burn(t, tl, ass, 720, 1280, at, "error")
				drawn, ok := bbox(img, notGrey(img))
				require.True(t, ok, "drawn at %.2f s", at)

				if bottom {
					assert.Greater(t, drawn.y0, 640, "at %.2f s the word is at the bottom: rows %d-%d", at, drawn.y0,
						drawn.y1)
				} else {
					assert.Less(t, drawn.y1, 640, "at %.2f s the word is at the top: rows %d-%d", at, drawn.y0, drawn.y1)
				}
			}
		})
	}
}

// Classic's video is the one made before the presets: burned with the fonts of then and of now, the stream is the
// same, byte for byte (check 5; its ASS is ASS's, TestLegacyIsTheCaptionsOfBefore).
func TestClassicIsBurnedAsBefore(t *testing.T) {
	t.Parallel()

	tl := tools(t)
	dir := t.TempDir()

	video := filepath.Join(dir, "in.mp4")
	//nolint:gosec // The pinned ffmpeg.
	cmd := exec.CommandContext(t.Context(), tl.FFmpeg, "-nostdin", "-v", "error", "-f", "lavfi",
		"-i", "testsrc2=s=720x1280:r=30:d=2", "-f", "lavfi", "-i", "sine=d=2", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", video)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	ass, err := Render(t.Context(), tl, []Cue{cue(0.4, strings.Fields(line)[:4]...)}, Look{Preset: Legacy}, 720, 1280, nil)
	require.NoError(t, err)

	captions := filepath.Join(dir, "captions.ass")
	require.NoError(t, os.WriteFile(captions, []byte(ass), 0o600))

	// The fonts folder as it was: Be Vietnam Pro SemiBold and its license.
	before := filepath.Join(dir, "fonts-before")
	require.NoError(t, os.MkdirAll(before, 0o700))

	for _, name := range []string{"BeVietnamPro-SemiBold.ttf", "OFL.txt"} {
		raw, err := ffmpeg.FontFile(name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(before, name), raw, 0o600))
	}

	md5 := func(fontsDir, work string) string {
		then := tl
		then.FontsDir = fontsDir

		burned, err := compose.Burn(t.Context(), then, video, captions, filepath.Join(dir, work))
		require.NoError(t, err)

		//nolint:gosec // The pinned ffmpeg.
		out, err := exec.CommandContext(t.Context(), tl.FFmpeg, "-nostdin", "-v", "error", "-i", burned,
			"-map", "0:v", "-c", "copy", "-f", "md5", "-").Output()
		require.NoError(t, err)

		return strings.TrimSpace(string(out))
	}

	assert.Equal(t, md5(before, "before"), md5(tl.FontsDir, "now"))
}
