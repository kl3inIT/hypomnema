package agentapi_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario (issue #1058): a Bot's pi reports a background command started and ended with its own token, and the end
// wakes its chat once; the pi that follows in the chat cannot end it, and no other agent reports one.
func TestABotsPiReportsItsBackgroundCommandsAndTheEndWakesItsChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	pi, chatID := w.botPi(orchestration.Bot{Name: "maps"})
	next, err := w.svc.AddBotToken(ctx, "pi", chatID, w.mustBotOf(chatID))
	require.NoError(t, err)
	_, owner, err := w.svc.AddToken(ctx, "laptop", bearertoken.Owner)
	require.NoError(t, err)

	started := agenttool.BackgroundReport{Kind: agenttool.BackgroundStarted, ID: "bg_0123456789ab"}
	status, body := w.call(http.MethodPost, agenttool.PathBackground, owner, started)
	assert.Equal(t, http.StatusForbidden, status, string(body))

	status, body = w.call(http.MethodPost, agenttool.PathBackground, pi, started)
	require.Equal(t, http.StatusOK, status, string(body))

	code := 0
	ended := agenttool.BackgroundReport{Kind: agenttool.BackgroundEnded, ID: "bg_0123456789ab", ExitCode: &code,
		Output: "fetched 29530 places\n", OutputChars: 21}

	status, body = w.call(http.MethodPost, agenttool.PathBackground, next, ended)
	require.Equal(t, http.StatusOK, status, string(body))
	assert.Empty(t, w.wakes(chatID), "another pi's report wakes nothing")

	status, body = w.call(http.MethodPost, agenttool.PathBackground, pi, ended)
	require.Equal(t, http.StatusOK, status, string(body))
	status, body = w.call(http.MethodPost, agenttool.PathBackground, pi, ended)
	require.Equal(t, http.StatusOK, status, string(body))

	assert.Equal(t, []string{"Background command bg_0123456789ab exited with code 0 after 0s:\nfetched 29530 places"},
		w.wakes(chatID), "the end wakes the chat once")

	status, body = w.call(http.MethodPost, agenttool.PathBackground, pi,
		agenttool.BackgroundReport{Kind: agenttool.BackgroundEnded, ID: "bg_0123456789ab"})
	assert.Equal(t, http.StatusUnprocessableEntity, status, string(body))

	status, body = w.call(http.MethodPost, agenttool.PathBackground, pi, agenttool.BackgroundReport{Kind: "paused"})
	assert.Equal(t, http.StatusBadRequest, status, string(body))
}

func (w *world) mustBotOf(chatID string) string {
	w.t.Helper()

	chat, err := w.svc.GetChat(w.t.Context(), chatID)
	require.NoError(w.t, err)

	return chat.Bot
}

// wakes are the texts of the chat's wake-ups pi has not had yet.
func (w *world) wakes(chatID string) []string {
	w.t.Helper()

	msgs, err := w.svc.Undelivered(w.t.Context(), chatID)
	require.NoError(w.t, err)

	var out []string

	for _, m := range msgs {
		if m.Kind == orchestration.ChatWake {
			out = append(out, m.Text)
		}
	}

	return out
}
