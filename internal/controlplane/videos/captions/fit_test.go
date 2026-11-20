package captions

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The drawn width of each frame is from its first to its last column that is not the grey; none is 0.
func TestReadWidths(t *testing.T) {
	t.Parallel()

	const w, h = 10, 2

	frame := func(cols ...int) []byte {
		f := bytes.Repeat([]byte{0x80}, w*h*3)
		for _, c := range cols {
			f[(w+c)*3+1] = 0x81 // the second row, one channel a level off
		}

		return f
	}

	raw := append(append(frame(2, 7), frame()...), frame(0, 9)...)

	got, err := readWidths(bytes.NewReader(raw), w, h, 3)
	require.NoError(t, err)
	assert.Equal(t, []float64{6, 0, 10}, got)

	_, err = readWidths(bytes.NewReader(raw[:len(raw)-1]), w, h, 3)
	require.Error(t, err, "a frame cut short")
}
