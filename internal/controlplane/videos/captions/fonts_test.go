package captions

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/text/unicode/norm"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// The OS/2 and head values the reader gives are the files' own, as read from their bytes with Python's struct
// (head offset 18, OS/2 offsets 74 and 76), and unitsPerEm is sfnt's (issue #908, PR 1, check 3).
func TestReadMetricsGivesTheFilesValues(t *testing.T) {
	t.Parallel()

	for file, want := range map[string]metrics{
		"BeVietnamPro-ExtraBold.ttf": {1000, 1261, 265},
		"BeVietnamPro-Bold.ttf":      {1000, 1261, 265},
		"BeVietnamPro-Medium.ttf":    {1000, 1261, 265},
		"BeVietnamPro-SemiBold.ttf":  {1000, 1261, 265},
		"JetBrainsMono-SemiBold.ttf": {1000, 1165, 400},
	} {
		raw, err := ffmpeg.FontFile(file)
		require.NoError(t, err, file)

		got, err := readMetrics(raw)
		require.NoError(t, err, file)
		assert.Equal(t, want, got, file)

		parsed, err := sfnt.Parse(raw)
		require.NoError(t, err)
		assert.Equal(t, got.UnitsPerEm, int(parsed.UnitsPerEm()), file)
	}

	_, err := readMetrics([]byte("not a font"))
	require.Error(t, err)
}

// vietnamese is every letter of Vietnamese, lower and upper case: the consonants, and each vowel with each tone.
func vietnamese() []string {
	out := []string{"b", "c", "d", "đ", "g", "h", "k", "l", "m", "n", "p", "q", "r", "s", "t", "v", "x"}

	for _, v := range []string{"a", "ă", "â", "e", "ê", "i", "o", "ô", "ơ", "u", "ư", "y"} {
		for _, tone := range []string{"", "\u0300", "\u0301", "\u0309", "\u0303", "\u0323"} {
			out = append(out, norm.NFC.String(v+tone))
		}
	}

	n := len(out)
	for _, l := range out[:n] {
		out = append(out, norm.NFC.String(strings.ToUpper(l)))
	}

	return out
}

func TestEveryFontHasEveryVietnameseLetter(t *testing.T) {
	t.Parallel()

	letters := vietnamese()
	require.Len(t, letters, 2*(17+12*6))

	for name := range fontFiles {
		f, err := faceOf(name)
		require.NoError(t, err)

		var buf sfnt.Buffer

		for _, l := range letters {
			runes := []rune(l)
			require.Len(t, runes, 1, "%q is one precomposed letter", l)

			idx, err := f.font.GlyphIndex(&buf, runes[0])
			require.NoError(t, err)
			assert.NotZero(t, idx, "%s has no glyph for %q", name, l)
		}
	}
}

// A preset's em is given to libass through the font's OS/2 metrics: the worked numbers of #908.
func TestTheEmIsGivenThroughTheFontsMetrics(t *testing.T) {
	t.Parallel()

	f, err := faceOf(fontBold)
	require.NoError(t, err)
	assert.InDelta(t, 64*1.526, f.assSize(64), 1e-9, "Clean at 9:16: an em of 64 px")
	assert.InDelta(t, 36*1.526, f.assSize(36), 1e-9, "Clean at 16:9: an em of 36 px")

	r, err := newRenderer("one-word", 720, 1280)
	require.NoError(t, err)

	b, err := r.measure([][]word{{{text: "A"}}}, 1280*0.062*1.75, 1)
	require.NoError(t, err)
	assert.InDelta(t, 138.88, b.em, 1e-9, "One Word before the width cap")
}
