package agentapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario: a worker keeps a fact about the owner with its Dispatch's key,
// and the answer says how full that memory is. A change over the limit, a
// target it does not know, and the memory of a Dispatch that does not exist
// are refused in words the worker can act on. Only a worker writes here: the
// owner's own token is refused. The log names the worker, never the fact
// (issue #21).
func TestAWorkerChangesAMemoryWithItsDispatchsKey(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	key, err := w.svc.AddAgentSession(ctx, "dsp_1", nil)
	require.NoError(t, err)

	status, body := w.call(http.MethodPost, agenttool.PathMemory, key,
		agenttool.MemoryChange{Target: "owner", Action: "add", Text: "The owner writes in Vietnamese."})
	require.Equal(t, http.StatusOK, status, string(body))

	var answer agenttool.MemoryAnswer
	require.NoError(t, json.Unmarshal(body, &answer))
	assert.Equal(t, agenttool.MemoryAnswer{Memory: "The owner writes in Vietnamese.", Used: 31, Limit: 1375}, answer)

	status, body = w.call(http.MethodPost, agenttool.PathMemory, key,
		agenttool.MemoryChange{Target: "owner", Action: "add", Text: strings.Repeat("x", 1400)})
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Contains(t, string(body), "over its limit of 1375")

	status, body = w.call(http.MethodPost, agenttool.PathMemory, key,
		agenttool.MemoryChange{Target: "agent", Action: "add", Text: "x"})
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Contains(t, string(body), "dispatch dsp_1")

	status, _ = w.call(http.MethodPost, agenttool.PathMemory, key, agenttool.MemoryChange{Target: "team", Action: "add"})
	assert.Equal(t, http.StatusBadRequest, status)

	_, owner, err := w.svc.AddToken(ctx, "me", bearertoken.Owner)
	require.NoError(t, err)

	status, _ = w.call(http.MethodPost, agenttool.PathMemory, owner,
		agenttool.MemoryChange{Target: "owner", Action: "add", Text: "x"})
	assert.Equal(t, http.StatusForbidden, status)

	assert.Contains(t, w.logs.String(), "dispatch dsp_1")
	assert.NotContains(t, w.logs.String(), "Vietnamese")
}
