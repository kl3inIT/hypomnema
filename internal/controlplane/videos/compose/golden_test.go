package compose

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The goldens of the frame and the backgrounds (#908, PR 2, check 2): PNGs Chromium drew from aidemo's frame HTML
// fed these layouts and from autodemo's background(), by testdata/frames/render.mjs (see its README). The Go
// raster must be within a mean of 3/255 of them, with at most 2 % of pixels off by more than 24/255.

var update = flag.Bool("update", false, "write testdata/frames/spec.json, which render.mjs draws the goldens from")

const goldenDir = "testdata/frames"

// goldenSolid is the background the frames are drawn on, aidemo's padColor, so the frame is compared on its own.
const goldenSolid = "#0e1322"

type goldenFrame struct {
	Name   string `json:"name"`
	Layout Layout `json:"layout"`
}

type goldenBackground struct {
	Name       string     `json:"name"`
	Width      int        `json:"width"`
	Height     int        `json:"height"`
	Background Background `json:"background"`
}

type goldenSpec struct {
	Solid       string             `json:"solid"`
	Frames      []goldenFrame      `json:"frames"`
	Backgrounds []goldenBackground `json:"backgrounds"`
}

func goldenCases(t *testing.T) goldenSpec {
	t.Helper()

	spec := goldenSpec{Solid: goldenSolid}
	segment := Size{576, 1280}

	for _, out := range []Size{{720, 1280}, {1280, 720}} {
		aspect := "portrait"
		if out.Width > out.Height {
			aspect = "landscape"
		}

		for _, f := range []Frame{
			DefaultFrame(ChromeAndroid), withSafeTop(DefaultFrame(ChromeAndroid)),
			DefaultFrame(ChromeIPhone), withSafeTop(DefaultFrame(ChromeIPhone)), DefaultFrame(ChromeNone),
		} {
			l, err := FrameLayout(f, segment, out)
			require.NoError(t, err)

			name := fmt.Sprintf("frame-%s-%s", f.Chrome, aspect)
			if f.SafeTop {
				name = fmt.Sprintf("frame-%s-safe-%s", f.Chrome, aspect)
			}

			spec.Frames = append(spec.Frames, goldenFrame{Name: name, Layout: l})
		}

		// A quarter of the video's size: Chrome dithers its gradients, so a golden of the full size is a megabyte;
		// background() scales with the canvas (its unit U is a 1080th of the short side).
		small := Size{out.Width / 4, out.Height / 4}

		for _, b := range Backgrounds {
			spec.Backgrounds = append(spec.Backgrounds, goldenBackground{
				Name: fmt.Sprintf("background-%s-%s", b, aspect), Width: small.Width, Height: small.Height,
				Background: Background{Preset: b},
			})
		}

		spec.Backgrounds = append(spec.Backgrounds, goldenBackground{
			Name: "background-colors-" + aspect, Width: small.Width, Height: small.Height,
			Background: Background{Colors: []string{"#ff0066", "#00ccaa"}},
		})
	}

	return spec
}

// The spec the goldens were drawn from is what the layouts give now: a change of geometry draws them again.
func TestTheGoldensAreOfTheseLayouts(t *testing.T) {
	t.Parallel()

	want := goldenCases(t)
	path := filepath.Join(goldenDir, "spec.json")

	if *update {
		raw, err := json.MarshalIndent(want, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(raw, '\n'), 0o600))
	}

	raw, err := os.ReadFile(path) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	var got goldenSpec
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, want, got, "run `go test -run TestTheGoldensAreOfTheseLayouts -update`, then render.mjs")
}

func TestTheFrameIsDrawnAsChromeDrawsIt(t *testing.T) {
	t.Parallel()

	solid := hexRGBA(goldenSolid)

	for _, g := range goldenCases(t).Frames {
		t.Run(g.Name, func(t *testing.T) {
			t.Parallel()

			got := plate(g.Layout, func(c *canvas) { c.each(func(_, _ float64) (rgba, float64) { return solid, 1 }) })
			want := readGolden(t, g.Name)
			assertLooksLike(t, want, got, 3, nil)

			// The whole picture is mostly the ground, and on so dark a ground a missing shadow moves its mean by
			// 1/255 only: the pixels the frame draws on it, its shadow included, are held on their own to a mean of
			// 1.5/255 (they are within 0.7; without the shadow, 2.9 or more).
			ground := [3]float64{solid.r * 255, solid.g * 255, solid.b * 255}
			assertLooksLike(t, want, got, 1.5, func(c color.Color) bool {
				r, g, b, a := c.RGBA()
				px := overMagenta(r, g, b, a)

				return a == 0xffff && max(math.Abs(px[0]-ground[0]), math.Abs(px[1]-ground[1]),
					math.Abs(px[2]-ground[2])) > 2
			})
		})
	}
}

func TestTheBackgroundsAreDrawnAsAutodemoDrawsThem(t *testing.T) {
	t.Parallel()

	for _, g := range goldenCases(t).Backgrounds {
		t.Run(g.Name, func(t *testing.T) {
			t.Parallel()

			c := newCanvas(g.Width, g.Height)
			paintBackground(c, g.Background)
			assertLooksLike(t, readGolden(t, g.Name), c.image(), 3, nil)
		})
	}
}

func readGolden(t *testing.T, name string) image.Image {
	t.Helper()

	f, err := os.Open(filepath.Join(goldenDir, name+".png")) //nolint:gosec // The test's own file.
	require.NoError(t, err)

	defer func() { _ = f.Close() }()

	img, err := png.Decode(f)
	require.NoError(t, err)

	return img
}

// assertLooksLike compares two pictures over magenta, so a hole is compared too: a mean difference of at most
// meanMax/255 a channel, and at most 2 % of pixels with a channel off by more than 24/255. With only, just the
// pixels of want it says.
func assertLooksLike(t *testing.T, want, got image.Image, meanMax float64, only func(color.Color) bool) {
	t.Helper()

	require.Equal(t, want.Bounds().Size(), got.Bounds().Size())

	var (
		sum     float64
		over, n int
	)

	b := want.Bounds()

	for y := range b.Dy() {
		for x := range b.Dx() {
			c := want.At(b.Min.X+x, b.Min.Y+y)
			if only != nil && !only(c) {
				continue
			}

			n++
			w := overMagenta(c.RGBA())
			g := overMagenta(got.At(got.Bounds().Min.X+x, got.Bounds().Min.Y+y).RGBA())
			worst := 0.0

			for k := range 3 {
				d := math.Abs(w[k] - g[k])
				sum += d
				worst = math.Max(worst, d)
			}

			if worst > 24 {
				over++
			}
		}
	}

	require.Positive(t, n)

	mean := sum / float64(3*n)
	assert.LessOrEqual(t, mean, meanMax, "mean difference")
	assert.LessOrEqual(t, float64(over)/float64(n), 0.02, "share of pixels off by more than 24")
}

// overMagenta is a premultiplied 16-bit color over magenta, in 0-255.
func overMagenta(r, g, b, a uint32) [3]float64 {
	k := 1 - float64(a)/0xffff

	return [3]float64{float64(r)/257 + 254*k, float64(g) / 257, float64(b)/257 + 253*k}
}
