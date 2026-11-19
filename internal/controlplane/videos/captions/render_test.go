package captions

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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

// rendered is a preset's ASS for cues and its frames as placed.
func rendered(t *testing.T, preset string, pos Position, w, h int, cues []Cue, rings []compose.Ring,
) (string, *renderer, []placedFrame) {
	t.Helper()

	ass, err := Render(cues, Look{Preset: preset, Position: pos}, w, h, rings)
	require.NoError(t, err)

	r, err := newRenderer(preset, w, h)
	require.NoError(t, err)

	frames, err := r.place(cues, pos, rings)
	require.NoError(t, err)
	require.NotEmpty(t, frames)

	return ass, r, frames
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

			ass, err := Render([]Cue{lineCue()}, Look{Preset: p.ID}, 720, 1280, nil)
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

				ass, r, frames := rendered(t, preset, PositionPreset, size[0], size[1], []Cue{lineCue()}, nil)
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

// Every preset's block is at most 0.94 of the width, measured on the pixels drawn: the text, its outline and the
// box; a long token included, at 720 px, in the presets without a box (check 4, and the must-fix of #935).
func TestEveryBlockFitsTheWidthAsDrawn(t *testing.T) {
	t.Parallel()

	tl := tools(t)

	type check struct {
		preset string
		size   [2]int
		text   string
	}

	var checks []check

	for _, p := range Presets {
		if p.ID == Legacy {
			continue // Classic keeps the size it had (#935): it is not measured by the cap.
		}

		for _, size := range sizes {
			checks = append(checks, check{p.ID, size, line})
		}
	}

	for _, preset := range []string{"one-word", "word-pair", "clean"} {
		checks = append(checks, check{preset, sizes[0], strings.Repeat("Ư", 60)})
	}

	// The probe of the plan's review: W×100 drew 678 px in One Word against 676.8 px planned from the advances.
	checks = append(checks, check{"one-word", sizes[0], strings.Repeat("W", 100)})

	for _, c := range checks {
		t.Run(fmt.Sprintf("%s-%dx%d-%d", c.preset, c.size[0], c.size[1], len([]rune(c.text))), func(t *testing.T) {
			t.Parallel()

			w, h := c.size[0], c.size[1]
			ass, r, frames := rendered(t, c.preset, PositionPreset, w, h, []Cue{cue(0.3, strings.Fields(c.text)...)}, nil)

			for _, pf := range frames {
				img, _ := burn(t, tl, ass, w, h, mid(pf), "error")

				drawn := notGrey(img)
				if r.p.Bar {
					band := over(r.p.Box)
					_, y, _, bh := r.boxRect(pf)
					row := int(y + bh/2)
					assert.True(t, near(rgb(img, 0, row), band, 8), "the band starts at 0")
					assert.True(t, near(rgb(img, w-1, row), band, 8), "the band ends at W")

					drawn = func(x, y int) bool {
						return !near(rgb(img, x, y), band, 0) && rgb(img, x, y) != [3]int{grey, grey, grey}
					}
				}

				all, ok := bbox(img, drawn)
				require.True(t, ok, "something is drawn at %.2f s", mid(pf))
				assert.LessOrEqual(t, float64(all.x1-all.x0+1), 0.94*float64(w),
					"%s at %.2f s: %d px wide, %v", c.preset, mid(pf), all.x1-all.x0+1, shownWords(pf))
			}
		})
	}
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
	ass, _, frames := rendered(t, "karaoke", PositionPreset, 1280, 720, []Cue{c}, nil)
	first, _ := burn(t, tl, ass, 1280, 720, mid(frames[0]), "error")
	last, _ := burn(t, tl, ass, 1280, 720, mid(frames[len(frames)-1]), "error")
	assert.Greater(t, count(first), 300, "three words to come, in grey")
	assert.Greater(t, count(first), 5*count(last), "none to come at the last word")

	// Pop at the first word draws what a cue of only that word draws.
	ass, _, frames = rendered(t, "pop", PositionPreset, 720, 1280, []Cue{c}, nil)
	popped, _ := burn(t, tl, ass, 720, 1280, mid(frames[0]), "error")

	alone, err := Render([]Cue{cue(0.3, "Ổn")}, Look{Preset: "pop"}, 720, 1280, nil)
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

		ass, r, frames := rendered(t, preset, pos, w, h, []Cue{lineCue()}, rings)

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

	ass, err := Render([]Cue{cue(0.4, strings.Fields(line)[:4]...)}, Look{Preset: Legacy}, 720, 1280, nil)
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
