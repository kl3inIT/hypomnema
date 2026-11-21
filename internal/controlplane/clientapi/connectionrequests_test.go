package clientapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
)

// Scenario (issue #471, part D): a Bot asks for a Connection from its main chat. Only the owner's token lists and
// answers the request: the Bot's own pi, the main Bot's pi, which may change other Bots from its chat, a Routine's
// script and the coordinator token are refused, and grant nothing. The owner's grant adds the Connection to the
// Bot's list, once; the same answer again is the same request, a contrary one a conflict; the chat's history keeps
// the line that wakes the Bot.
func TestOnlyTheOwnerAnswersAConnectionRequest(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	shared, _ := w.token("pi-coordinator", bearertoken.Coordinator)
	ctx := t.Context()

	for _, label := range []string{"work", "team"} {
		_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
			Integration: "google", Label: label, Secret: "s-" + label,
		})
		require.NoError(t, err)
		_, err = w.svc.SetConnectionChats(ctx, "google", label, true)
		require.NoError(t, err)
	}

	hr, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "hr",
		Connections: orchestration.AllowList{Limited: true, Names: []string{"google:work"}}})
	require.NoError(t, err)

	main, _, err := w.svc.OpenMainChat(ctx, hr.ID, "", "", orchestration.ChatModel{})
	require.NoError(t, err)

	mine, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: hr.ID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh", ScriptText: "#!/bin/sh\n",
		EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	script, err := w.svc.AddRoutineToken(ctx, "pi-coordinator", routine.ID)
	require.NoError(t, err)

	asked, err := w.svc.RequestConnection(ctx, hr.ID, main.ID, "google:team", "to read the team's calendar")
	require.NoError(t, err)

	for name, c := range map[string]*openapi.ClientWithResponses{
		"the Bot's own pi":      w.piToken("pi-coordinator", main.ID, hr.ID),
		"the main Bot's pi":     w.piToken("pi-coordinator", mine.ID, orchestration.DefaultBotID),
		"a routine's script":    w.client(script),
		"the coordinator token": shared,
	} {
		list, err := c.ListConnectionRequestsWithResponse(ctx, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, list.StatusCode(), name)

		decide, err := c.DecideConnectionRequestWithResponse(ctx, asked.ID,
			openapi.ConnectionRequestDecision{Decision: openapi.Grant})
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, decide.StatusCode(), name)
	}

	still, err := w.svc.GetBot(ctx, hr.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"google:work"}, still.Connections.Names, "nothing granted")

	pending := openapi.ConnectionRequestStatePending
	list, err := owner.ListConnectionRequestsWithResponse(ctx,
		&openapi.ListConnectionRequestsParams{State: &pending, ChatId: &main.ID})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, list.StatusCode(), string(list.Body))
	require.Len(t, list.JSON200.Data, 1)

	got := list.JSON200.Data[0]
	assert.Equal(t, asked.ID, got.Id)
	assert.Equal(t, hr.ID, got.Bot)
	assert.Equal(t, "google", got.Integration)
	assert.Equal(t, "team", got.Label)
	assert.Equal(t, "to read the team's calendar", got.Reason)
	assert.Nil(t, got.DecidedAt)

	decide, err := owner.DecideConnectionRequestWithResponse(ctx, asked.ID,
		openapi.ConnectionRequestDecision{Decision: openapi.Grant})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, decide.StatusCode(), string(decide.Body))
	assert.Equal(t, openapi.ConnectionRequestStateGranted, decide.JSON200.State)
	assert.NotNil(t, decide.JSON200.DecidedAt)

	again, err := owner.DecideConnectionRequestWithResponse(ctx, asked.ID,
		openapi.ConnectionRequestDecision{Decision: openapi.Grant})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, again.StatusCode())

	contrary, err := owner.DecideConnectionRequestWithResponse(ctx, asked.ID,
		openapi.ConnectionRequestDecision{Decision: openapi.Decline})
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, contrary.StatusCode())

	granted, err := w.svc.GetBot(ctx, hr.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"google:work", "google:team"}, granted.Connections.Names)

	page, err := owner.ListChatMessagesWithResponse(ctx, main.ID, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, page.StatusCode(), string(page.Body))

	lines := 0

	for _, m := range page.JSON200.Data {
		if strings.HasPrefix(m.Text, "Connection granted: ") {
			lines++

			assert.Equal(t, openapi.ChatRoleSystem, m.Role)
		}
	}

	assert.Equal(t, 1, lines, "the history keeps the line")
}
