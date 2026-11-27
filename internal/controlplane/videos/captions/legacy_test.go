package captions

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// styleOf is the Default style's Alignment and MarginV in an ASS file.
func styleOf(t *testing.T, ass string) (string, string) {
	t.Helper()

	for _, line := range strings.Split(ass, "\n") {
		if strings.HasPrefix(line, "Style: Default,") {
			f := strings.Split(line, ",")
			require.Len(t, f, 23)

			return f[18], f[21]
		}
	}

	t.Fatal("no Default style")

	return "", ""
}

// Classic draws at the position chosen (issue #908, row 9; review of #1001): its own, the preset's, is the ASS made
// before the presets, byte for byte; a position chosen moves it, as each preset's offsets do.
func TestClassicTakesThePositionChosen(t *testing.T) {
	t.Parallel()

	cues := []Cue{{Index: 1, StartMs: 0, EndMs: 1000, Text: "Mở ứng dụng",
		Words: []Word{
			{Word: "Mở", Start: 0, End: 0.3}, {Word: "ứng", Start: 0.3, End: 0.6}, {Word: "dụng", Start: 0.6, End: 1},
		}}}
	render := func(p Position) string {
		ass, err := Render(t.Context(), ffmpeg.Tools{}, cues, Look{Preset: Legacy, Position: p}, 720, 1280,
			[]compose.Ring{})
		require.NoError(t, err)

		return ass
	}

	assert.Equal(t, positionedASS(cues, 720, 1280, Default, PositionPreset), render(PositionPreset),
		"Classic's own is unchanged")

	for p, want := range map[Position][2]string{
		PositionBottom: {"2", "90"}, PositionLowerThird: {"2", "205"}, PositionCenter: {"5", "0"},
		PositionTop: {"8", "90"},
	} {
		align, margin := styleOf(t, render(p))
		assert.Equal(t, want, [2]string{align, margin}, "%s: alignment and vertical margin", p)
	}
}
