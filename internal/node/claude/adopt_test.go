package claude

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Scenario: the daemon restarts after Claude Code said its result, on a rate
// limit, and the worker exits after. The adopted turn's outcome is the limit
// with its reset, not an unknown exit (issue #403).
func TestAnAdoptedTurnKeepsItsResult(t *testing.T) {
	t.Parallel()

	var res event
	require.NoError(t, json.Unmarshal([]byte(`{"type":"result","subtype":"success","is_error":true,
		"result":"You've hit your limit","usage":{"input_tokens":5,"output_tokens":1}}`), &res))

	limited := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	before := &turn{result: &res, limited: &limited}
	after := (&ClaudeRuntime{}).Adopted(before.State())

	assert.Equal(t, before.Result(), after.Result())

	ex := nodeproto.Exit{Kind: "exited"}
	outcome := after.Outcome(ex)
	assert.Equal(t, "rate_limited", outcome.Class)
	assert.Equal(t, before.Outcome(ex), outcome)
}
