package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSlowModelWithNoPartialFramesIsNotIdle(t *testing.T) {
	t.Parallel()

	worker := (&ClaudeRuntime{}).newTurn()
	worker.wait.Held = true
	worker.result = &event{Type: "result", Result: "waiting"}
	worker.Line(t.Context(), nil, []byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	assert.True(t, worker.wait.IdleAt.IsZero(), "Claude may be streaming a model response without emitting partial frames")
}
