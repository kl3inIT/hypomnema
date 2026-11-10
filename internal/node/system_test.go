package node

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Scenario: the node describes the machine it runs on. The platform is the
// binary's, and the sizes are read from the machine: memory, and the volume
// that holds the worktree root.
func TestDetectSystemReadsTheMachine(t *testing.T) {
	t.Parallel()

	got := DetectSystem(t.Context(), t.TempDir())

	assert.Equal(t, runtime.GOOS, got.OS)
	assert.Equal(t, runtime.GOARCH, got.Arch)
	assert.Positive(t, got.CPUs)
	assert.Positive(t, got.MemoryBytes)
	assert.Positive(t, got.DiskBytes)
}

// Scenario: the node measures the machine for a heartbeat. Memory and the
// worktree volume are in use, and the CPU share is a percentage.
func TestMeasureSystemReadsTheMachinesLoad(t *testing.T) {
	t.Parallel()

	got := MeasureSystem(t.Context(), t.TempDir())

	if assert.NotNil(t, got) {
		assert.Positive(t, got.MemoryUsedBytes)
		assert.Positive(t, got.DiskUsedBytes)
		assert.GreaterOrEqual(t, got.CPUPercent, 0.0)
		assert.LessOrEqual(t, got.CPUPercent, 100.0)
	}
}

// Scenario: a machine that has run no Task yet, so its worktree root is not
// made. The node still reports the disk the root will be on, in its hello and
// in each heartbeat.
func TestDiskIsReadBeforeTheWorktreeRootExists(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "not", "made", "yet")

	assert.Positive(t, DetectSystem(t.Context(), root).DiskBytes)

	if got := MeasureSystem(t.Context(), root); assert.NotNil(t, got) {
		assert.Positive(t, got.DiskUsedBytes)
	}
}
