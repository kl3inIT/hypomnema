package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Scenario: the daemon restarts after Codex said its turn failed on a
// limit, and the worker exits after. The adopted turn's outcome is the limit,
// not an unknown exit (issue #403).
func TestAnAdoptedTurnKeepsItsFailure(t *testing.T) {
	t.Parallel()

	before := &turn{failure: "429 Too Many Requests: usage limit", usage: &nodeproto.Usage{Input: 10, Output: 2}}
	after := (&CodexRuntime{}).Adopted(before.State())

	assert.Equal(t, before.Result(), after.Result())

	ex := nodeproto.Exit{Kind: "exited"}
	outcome := after.Outcome(ex)
	assert.Equal(t, "rate_limited", outcome.Class)
	assert.Equal(t, before.Outcome(ex), outcome)
}
