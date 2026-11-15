package agentapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario (issue #567, rule 8): the owner's Google account "work" was found
// to have Gmail read only. An agent that asks which accounts it may use is
// told, with the account, the actions it cannot do and why, so that it knows
// before it tries; an action that is granted or unknown is not listed.
func TestAnAgentIsToldWhatItsAccountCannotDo(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, owner, err := w.svc.AddToken(ctx, "me", bearertoken.Owner)
	require.NoError(t, err)

	_, err = w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "google", Label: "work", Account: "owner@example.com", Secret: "conn-google-work",
	})
	require.NoError(t, err)
	require.NoError(t, w.svc.SetConnectionAccess(ctx, "google", "work", orchestration.ConnectionAccess{
		CheckedAt: time.Now(), Actions: map[string]orchestration.ActionAccess{
			"gmail.read":    {Access: orchestration.AccessGranted, Reason: "granted gmail:read"},
			"gmail.send":    {Access: orchestration.AccessMissing, Reason: "gmail granted read only"},
			"calendar.read": {Access: orchestration.AccessUnknown},
		},
	}))

	status, body := w.call(http.MethodGet, agenttool.PathConnections+"?integration=google", owner, nil)
	require.Equal(t, http.StatusOK, status)

	var list agenttool.ConnectionList
	require.NoError(t, json.Unmarshal(body, &list))
	assert.Equal(t, agenttool.ConnectionList{Data: []agenttool.Connection{{
		Label: "work", Account: "owner@example.com", Cannot: []agenttool.Cannot{{
			Action: "gmail.send", Service: "Gmail", Name: "Send mail", Reason: "gmail granted read only",
		}},
	}}}, list)
}
