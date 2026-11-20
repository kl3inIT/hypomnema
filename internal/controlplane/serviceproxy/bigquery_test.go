package serviceproxy_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// BigQuery's acts (issue #976): a read and a query, both reads, decided as any request is.

func (w *world) bigqueryConnection() {
	w.t.Helper()

	_, err := w.svc.SaveServiceConnection(w.t.Context(), orchestration.Connection{
		Integration: serviceproxy.BigQueryID, Label: "ga4", Account: "sa@p.iam.gserviceaccount.com",
		Secret: "integration-bigquery-ga4", Settings: `{"billing_project":"p","datasets":["p.analytics_1"]}`,
	})
	require.NoError(w.t, err)
}

func bigqueryAct(caller serviceproxy.Caller, action, runKey string) serviceproxy.Act {
	return serviceproxy.Act{
		Caller: caller, Integration: serviceproxy.BigQueryID, Label: "ga4", Action: action, RunKey: runKey,
		Subject: "SELECT 1", Fields: []orchestration.Field{{Label: "SQL", Value: "SELECT 1"}}, Hash: "h-select-1",
	}
}

// Scenario: in a read-only Routine's run (#956), BigQuery's read and query run while allowed, as Superset's
// reads do; set to ask, they are refused, not asked, since nobody may be asked in a read run.
func TestAReadRunReadsBigQueryAndIsNeverAsked(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 50*time.Millisecond)
	ctx := t.Context()
	w.bigqueryConnection()

	reader := serviceproxy.Caller{Name: "bot main in chat " + w.chat, Coordinator: true, ChatID: w.chat, ReadRun: true}

	for _, action := range []string{"bigquery.read", "bigquery.query"} {
		require.NoError(t, w.proxy.Judge(ctx, bigqueryAct(reader, action, "")), action)
	}

	require.NoError(t, w.svc.SetActionPolicy(ctx, serviceproxy.BigQueryID, "ga4", "bigquery.query",
		orchestration.PolicyAsk))

	err := w.proxy.Judge(ctx, bigqueryAct(reader, "bigquery.query", ""))

	var refused *serviceproxy.RefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, agenttool.ReadRunRefusal, refused.Message)
	assert.Empty(t, w.approvals(orchestration.ApprovalPending), "a read run asks nobody")

	require.NoError(t, w.proxy.Judge(ctx, bigqueryAct(reader, "bigquery.read", "")), "the read is still allowed")
}

// Scenario: the owner set queries to ask. A Routine's script queries with its run's key: the Approval is the run's
// and waits as long as a run waits. He answers "always for this Routine": the next run's query passes without a
// question, and another Routine's still asks.
func TestARoutinesQueryAsksOnceForTheRoutine(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	ctx := t.Context()
	w.bigqueryConnection()
	require.NoError(t, w.svc.SetActionPolicy(ctx, serviceproxy.BigQueryID, "ga4", "bigquery.query",
		orchestration.PolicyAsk))

	put := func(name string) routinestore.Routine {
		r, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
			Bot: orchestration.DefaultBotID, Name: name, Kind: routinestore.RoutineScript, Script: name + ".sh",
			ScriptText: "#!/bin/sh\nhypomnema bigquery query --sql-file q.sql\n", EveryMinutes: 60,
			TimeoutSeconds: 2700,
		}, routinestore.RoutineAuthor{})
		require.NoError(t, err)

		return r
	}

	report, other := put("ga4-report"), put("ga4-other")
	script := serviceproxy.Caller{Name: "bot main in routine " + report.ID, Coordinator: true}

	first, key := w.routineRun(report.ID)

	judged := make(chan error, 1)
	go func() { judged <- w.proxy.Judge(ctx, bigqueryAct(script, "bigquery.query", key)) }()

	asked := w.pending()
	assert.Equal(t, report.ID, asked.RoutineID)
	assert.Equal(t, first.ID, asked.RoutineRunID, "the Approval belongs to the run")
	assert.Equal(t, "Run a query: SELECT 1", asked.Title)

	_, err := w.proxy.Decide(ctx, asked.ID, true, orchestration.ScopeRoutine)
	require.NoError(t, err)
	require.NoError(t, <-judged, "the same call goes on")

	_, err = w.svc.FinishRoutineRun(ctx, first.ID, routinestore.RunResult{State: routinestore.RunSucceeded})
	require.NoError(t, err)

	_, next := w.routineRun(report.ID)
	require.NoError(t, w.proxy.Judge(ctx, bigqueryAct(script, "bigquery.query", next)), "a later run is not asked")
	assert.Empty(t, w.approvals(orchestration.ApprovalPending))

	_, otherKey := w.routineRun(other.ID)
	go func() {
		judged <- w.proxy.Judge(ctx, bigqueryAct(serviceproxy.Caller{Name: "bot main in routine " + other.ID,
			Coordinator: true}, "bigquery.query", otherKey))
	}()

	stray := w.pending()
	assert.Equal(t, other.ID, stray.RoutineID, "another Routine still asks")

	_, err = w.proxy.Decide(ctx, stray.ID, false, "")
	require.NoError(t, err)
	require.Error(t, <-judged)
}
