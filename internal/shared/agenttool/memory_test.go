package agenttool

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHowFullAMemoryIsIsSaid(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "memory: 0 of 1,375 characters (0%)", MemoryUsage(0, 1375))
	assert.Equal(t, "memory: 1,759 of 2,200 characters (79%)", MemoryUsage(1759, 2200))
	assert.Contains(t, MemoryUsage(1760, 2200), "(80%). It is nearly full")
}
