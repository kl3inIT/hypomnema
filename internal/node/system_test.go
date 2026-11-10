package node

import (
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
