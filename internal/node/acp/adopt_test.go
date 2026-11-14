package acp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Scenario: the daemon restarts after the session was opened and the turn
// ended without finish. The adopted turn cancels that session on a stop, and
// its outcome is the one the earlier daemon read (issue #403).
func TestAnAdoptedTurnKeepsItsSessionAndOutcome(t *testing.T) {
	t.Parallel()

	rt := &ACPRuntime{}
	before := &turn{
		rt: rt, session: "ses_fake", done: true, failure: "the turn ended with refusal",
		usage: &nodeproto.Usage{Input: 1200, Output: 300},
	}
	before.text.WriteString("401 Unauthorized")

	after := rt.Adopted(before.State())

	var cancel struct {
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal(after.Interrupt(), &cancel))
	assert.Equal(t, "session/cancel", cancel.Method)
	assert.Equal(t, "ses_fake", cancel.Params.SessionID)

	assert.Equal(t, before.Result(), after.Result())

	ex := nodeproto.Exit{Kind: "exited"}
	assert.Equal(t, before.Outcome(ex), after.Outcome(ex))
}
