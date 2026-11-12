package clientapi

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Scenario: the web's form asks for a schedule in words and its next three
// times; a schedule a Routine may not have is a bad request that says why.
func TestTheFormPreviewsASchedule(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("web", bearertoken.Owner)

	cron, zone := "0 7 * * 1-5", "Asia/Ho_Chi_Minh"
	ok, err := c.PreviewRoutineScheduleWithResponse(t.Context(), openapi.RoutineSchedule{Cron: &cron, Timezone: &zone})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, ok.StatusCode(), string(ok.Body))
	assert.Equal(t, "every weekday at 7:00 Ho Chi Minh time", ok.JSON200.Schedule)
	require.Len(t, ok.JSON200.Next, 3)
	assert.True(t, ok.JSON200.Next[0].Before(ok.JSON200.Next[1]))

	four := 4
	bad, err := c.PreviewRoutineScheduleWithResponse(t.Context(), openapi.RoutineSchedule{EveryMinutes: &four})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, bad.Body, bad.StatusCode(), http.StatusBadRequest))
	assert.Contains(t, string(bad.Body), "every 5 to 1440 minutes")
}

// Scenario: a Routine has run three times. The runs come two at a time: the
// first page says how many there are and where the next begins; the last
// has no next.
func TestARoutinesRunsArePaged(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	c, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	r, err := w.svc.PutRoutine(ctx, orchestration.RoutineSpec{
		Bot: orchestration.DefaultBot, Name: "sync", Kind: orchestration.RoutineScript, Script: "sync.sh",
		ScriptText: "#!/bin/sh\n", EveryMinutes: 60,
	}, orchestration.RoutineAuthor{})
	require.NoError(t, err)

	for range 3 {
		started, err := w.svc.RunRoutineNow(ctx, r.ID)
		require.NoError(t, err)
		_, err = w.svc.FinishRoutineRun(ctx, started.Run.ID, orchestration.RunResult{State: orchestration.RunSucceeded})
		require.NoError(t, err)
	}

	limit := 2
	first, err := c.ListRoutineRunsWithResponse(ctx, r.ID, &openapi.ListRoutineRunsParams{Limit: &limit})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, first.StatusCode(), string(first.Body))
	assert.Len(t, first.JSON200.Data, 2)
	assert.Equal(t, 3, first.JSON200.Total)
	require.NotNil(t, first.JSON200.NextOffset)
	assert.Equal(t, 2, *first.JSON200.NextOffset)

	last, err := c.ListRoutineRunsWithResponse(ctx, r.ID,
		&openapi.ListRoutineRunsParams{Limit: &limit, Offset: first.JSON200.NextOffset})
	require.NoError(t, err)
	assert.Len(t, last.JSON200.Data, 1)
	assert.Nil(t, last.JSON200.NextOffset)
}
