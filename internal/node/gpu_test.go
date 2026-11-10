package node

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

func ptr(v float64) *float64 { return &v }

// Scenario: a machine with two NVIDIA cards, as nvidia-smi prints them. Each
// card becomes a GPU with its use, its memory in bytes, its temperature and
// its power, under a name without the vendor's prefix.
func TestNvidiaSmiOutputBecomesGPUs(t *testing.T) {
	t.Parallel()

	out := "0, NVIDIA GeForce RTX 4090, 74, 22733, 24564, 87, 312.45\n" +
		"1, NVIDIA RTX A6000, 41, 615, 49140, 3, 22.10\n"

	assert.Equal(t, []nodeproto.GPUStats{
		{
			Index: 0, Name: "GeForce RTX 4090", UsagePercent: 87,
			MemoryUsedBytes: 22733 << 20, MemoryTotalBytes: 24564 << 20,
			TemperatureCelsius: ptr(74), PowerWatts: ptr(312.45),
		},
		{
			Index: 1, Name: "RTX A6000", UsagePercent: 3,
			MemoryUsedBytes: 615 << 20, MemoryTotalBytes: 49140 << 20,
			TemperatureCelsius: ptr(41), PowerWatts: ptr(22.10),
		},
	}, parseNvidiaSmi([]byte(out)))
}

// Scenario: what nvidia-smi prints when it cannot answer. A card that does
// not report its power keeps the rest; a line that is not a card's, such as
// a driver error, is skipped; and a card whose use is unknown is not shown
// with a made-up zero.
func TestNvidiaSmiOutputItCannotAnswerIsLeftOut(t *testing.T) {
	t.Parallel()

	out := "0, Tesla T4, 55, 1024, 15360, 40, [N/A]\n" +
		"NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver.\n" +
		"1, Tesla T4, 50, 512, 15360, [N/A], 30.00\n" +
		"\n"

	got := parseNvidiaSmi([]byte(out))
	require.Len(t, got, 1)
	assert.Equal(t, 0, got[0].Index)
	assert.InDelta(t, 40, got[0].UsagePercent, 0)
	assert.Nil(t, got[0].PowerWatts)
	require.NotNil(t, got[0].TemperatureCelsius)

	assert.Empty(t, parseNvidiaSmi([]byte("No devices were found\n")))
	assert.Empty(t, parseNvidiaSmi(nil))
}
