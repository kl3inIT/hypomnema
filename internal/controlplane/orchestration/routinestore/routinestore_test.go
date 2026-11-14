package routinestore_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// The root starts and ends a Routine's run, answers what a run asked and
// removes a Bot inside its own transaction: the Tx functions write and read
// with the caller's queries and open nothing of their own, so they see what
// that transaction wrote and do not wait for the one connection, which the
// transaction holds (issue #404).
func TestTheTxFunctionsUseTheCallersTransaction(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	clock := &fakeClock{now: time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)}
	bot := orchestration.DefaultBot

	err = storeutil.Write(ctx, db, clock, func() {}, func(q *store.Queries, now int64) error {
		require.NoError(t, q.InsertChat(ctx, store.InsertChatParams{ID: "cht_made_in", Title: "Sync", Bot: bot, Now: now}))
		require.NoError(t, q.InsertRoutine(ctx, store.InsertRoutineParams{
			ID: "rtn_sync", Bot: bot, Name: "sync", Kind: string(routinestore.RoutineScript), Script: "sync.sh",
			ScriptText: "#!/bin/sh\n", EveryMinutes: sql.NullInt64{Int64: 60, Valid: true},
			AnchorAt: sql.NullInt64{Int64: now, Valid: true}, TimeoutSeconds: 600, CreatedBy: "owner",
			ChatID: sql.NullString{String: "cht_made_in", Valid: true}, NextRunAt: sql.NullInt64{Int64: now, Valid: true},
			Now: now,
		}))

		routines, err := routinestore.CountRoutinesOfBotTx(ctx, q, bot)
		require.NoError(t, err)
		assert.Equal(t, int64(1), routines, "the Routine not yet committed")

		due, more, err := routinestore.ClaimDueTx(ctx, q, now, 0)
		require.NoError(t, err)
		assert.False(t, more)
		require.Len(t, due, 1)

		started, err := routinestore.StartRunTx(ctx, q, now, due[0], "rrn_first", routinestore.StartedBySchedule, "")
		require.NoError(t, err)
		assert.Equal(t, routinestore.RunRunning, started.Run.State)

		live, err := routinestore.LiveRunTx(ctx, q, "rtn_sync")
		require.NoError(t, err)
		require.NotNil(t, live)
		assert.Equal(t, "rrn_first", live.ID)

		routine, err := routinestore.GetRoutineTx(ctx, q, "rtn_sync")
		require.NoError(t, err)
		assert.Equal(t, "sync", routine.Name)

		require.NoError(t, routinestore.WaitRunTx(ctx, q, "rrn_first"))
		require.NoError(t, routinestore.ResumeRunTx(ctx, q, "rrn_first"))

		require.NoError(t, routinestore.GrantTx(ctx, q, now, routinestore.RoutineGrant{
			RoutineID: "rtn_sync", Integration: "lark", Label: "work", Action: "send", ApprovalID: "apr_1",
		}))

		granted, err := routinestore.GrantedTx(ctx, q, "rtn_sync", "lark", "work", "send")
		require.NoError(t, err)
		assert.True(t, granted)

		routineID, runID, err := routinestore.RunOfAskerTx(ctx, q, "", "cht_nothing")
		require.NoError(t, err)
		assert.Empty(t, routineID+runID, "no live run was opened for that chat")

		require.NoError(t, routinestore.EndRunTx(ctx, q, now, "rrn_first", routinestore.RunResult{
			State: routinestore.RunSucceeded,
		}))

		settled, err := routinestore.SettleRunTx(ctx, q, now, "rrn_first", routinestore.RunResult{
			State: routinestore.RunSucceeded,
		})
		require.NoError(t, err)
		assert.Equal(t, routinestore.RunSucceeded, settled.Run.State)

		require.NoError(t, routinestore.DetachFromChatTx(ctx, q, "cht_made_in"))

		row, err := q.GetRoutine(ctx, "rtn_sync")
		require.NoError(t, err)
		assert.False(t, row.ChatID.Valid, "the Routine lost the chat")

		return nil
	})
	require.NoError(t, err, "a Tx function that opened its own would wait for the connection until the timeout")
}
