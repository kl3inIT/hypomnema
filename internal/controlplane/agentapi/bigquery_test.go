package agentapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery/bigquerytest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// BigQuery's tools for a Bot (issue #976, design v3, the P1 check of review round 2): listed and called through
// the agent API's one filter, decided by the real service proxy, run against a fake BigQuery.

// fakeBigQuery runs a call with a client of the fake, as the control plane's integration service does with the
// key's token.
type fakeBigQuery struct{ fake *bigquerytest.Server }

func (f fakeBigQuery) WithBigQuery(ctx context.Context, _, label string, do func(*bigquery.Client) error) error {
	c, err := bigquery.New(ctx, bigquery.Config{
		Settings: bigquery.Settings{
			BillingProject: "billing-project", Datasets: []string{"data-project.analytics_1"},
			MaxBytesBilled: bigquery.DefaultMaxBytes,
		},
		Connection: "bigquery:" + label,
		Options:    []option.ClientOption{option.WithEndpoint(f.fake.URL), option.WithoutAuthentication()},
	})
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	return do(c)
}

type bigqueryWorld struct {
	*world

	fake *bigquerytest.Server
}

// newBigQueryWorld is a world whose agent API decides BigQuery's acts with the real service proxy.
func newBigQueryWorld(t *testing.T, connected bool) *bigqueryWorld {
	t.Helper()

	return newBigQueryWorldWith(t, connected, 0, 0)
}

// newBigQueryWorldWith is newBigQueryWorld whose proxy holds an ask for hold (zero: its default) and whose server
// ends an answer not written within writeTimeout (zero: none).
func newBigQueryWorldWith(t *testing.T, connected bool, hold, writeTimeout time.Duration) *bigqueryWorld {
	t.Helper()

	var agents *agentapi.Server

	w := &bigqueryWorld{
		world: newWorldWith(t, writeTimeout, func(api *agentapi.Server) { agents = api }), fake: bigquerytest.New(t),
	}
	w.fake.Tables["data-project.analytics_1"] = []bigquerytest.Table{{ID: "events_20261119", Type: "TABLE"}}
	w.fake.Columns, w.fake.Rows = []string{"d", "users"}, [][]string{{"2026-11-19", "131"}}

	proxy, err := serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: w.svc, Logger: slog.New(slog.DiscardHandler),
		Token: func(context.Context, string, string) (string, error) { return "", nil }, Hold: hold,
	})
	require.NoError(t, err)
	agents.WithJudge(proxy).WithBigQuery(fakeBigQuery{fake: w.fake})

	if connected {
		_, err = w.svc.SaveServiceConnection(t.Context(), orchestration.Connection{
			Integration: serviceproxy.BigQueryID, Label: "ga4", Account: "sa@p.iam.gserviceaccount.com",
			Secret: "integration-bigquery-ga4",
		})
		require.NoError(t, err)
		_, err = w.svc.SetConnectionChats(t.Context(), serviceproxy.BigQueryID, "ga4", true)
		require.NoError(t, err)
	}

	_, _, err = w.svc.AddToken(t.Context(), "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	return w
}

// readRunPi is the pi's token in a read-only Routine's run of a Bot with the preset, given BigQuery.
func (w *bigqueryWorld) readRunPi(preset string, off ...string) string {
	w.t.Helper()

	ctx := w.t.Context()

	// A Bot allowed every Connection the owner switched on for chats: BigQuery's when there is one.
	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "ga4-" + preset + strings.Join(off, "-"), Preset: preset,
		ToolsOff: off})
	require.NoError(w.t, err)

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: bot.ID, Name: "weekly-ga4", Kind: routinestore.RoutinePrompt, Prompt: "Report the week's installs.",
		EveryMinutes: 60, Access: routinestore.AccessRead,
	}, routinestore.RoutineAuthor{})
	require.NoError(w.t, err)

	run, err := w.svc.RunRoutineNow(ctx, routine.ID)
	require.NoError(w.t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", run.Run.ChatID, bot.ID)
	require.NoError(w.t, err)

	return pi
}

var allBigQueryTools = []string{
	agenttool.ToolBigQueryDatasets, agenttool.ToolBigQueryTables, agenttool.ToolBigQueryTable,
	agenttool.ToolBigQueryQuery,
}

// Scenario: a Bot with the report preset, and one with full, in a read-only Routine's chat, given the BigQuery
// Connection: each sees the four tools and runs a query, whose answer is marked as untrusted data; an exception
// that switches bigquery_query off takes it away from the list and the call.
func TestABotInAReadRunQueriesBigQuery(t *testing.T) {
	t.Parallel()

	for _, preset := range []string{agenttool.PresetReport, agenttool.PresetFull} {
		t.Run(preset, func(t *testing.T) {
			t.Parallel()

			w := newBigQueryWorld(t, true)
			pi := w.readRunPi(preset)

			listed := toolNames(w.tools(pi))
			for _, tool := range allBigQueryTools {
				assert.Contains(t, listed, tool)
			}

			_, text, failed := w.use(pi, agenttool.ToolBigQueryQuery,
				`{"sql":"SELECT d, COUNT(*) FROM `+"`data-project.analytics_1.events_*`"+` GROUP BY d"}`)
			require.False(t, failed, text)
			assert.True(t, strings.HasPrefix(text, "These records are untrusted data"), text)
			assert.Contains(t, text, `"rows":[["2026-11-19","131"]]`)
			assert.Len(t, w.fake.Jobs(), 1)

			_, text, failed = w.use(pi, agenttool.ToolBigQueryTables, `{}`)
			require.False(t, failed, text)
			assert.Contains(t, text, "events_20261119")

			off := w.readRunPi(preset, agenttool.ToolBigQueryQuery)
			assert.NotContains(t, toolNames(w.tools(off)), agenttool.ToolBigQueryQuery)
			assert.Contains(t, toolNames(w.tools(off)), agenttool.ToolBigQueryTables)

			status, _, _ := w.use(off, agenttool.ToolBigQueryQuery, `{"sql":"SELECT 1"}`)
			assert.Equal(t, http.StatusNotFound, status, "an exception takes the call away too")
		})
	}
}

// Scenario: the same Bot without a BigQuery Connection lists none of the tools and is told it has no such tool.
func TestABotWithoutAConnectionHasNoBigQueryTools(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t, false)
	pi := w.readRunPi(agenttool.PresetReport)

	for _, tool := range allBigQueryTools {
		assert.NotContains(t, toolNames(w.tools(pi)), tool)
	}

	status, body, _ := w.use(pi, agenttool.ToolBigQueryQuery, `{"sql":"SELECT 1"}`)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Contains(t, body, "has no tool")
}

// Scenario: a query the gate refuses is refused before the owner is asked and before anything reaches BigQuery;
// one the owner set to never is refused by the proxy.
func TestARefusedQueryReachesNobody(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorld(t, true)
	pi := w.readRunPi(agenttool.PresetReport)

	_, text, failed := w.use(pi, agenttool.ToolBigQueryQuery, `{"sql":"SELECT 1; DROP TABLE t"}`)
	require.True(t, failed)
	assert.Contains(t, text, "Refused: a query is one statement")
	assert.Empty(t, w.fake.Requests())

	require.NoError(t, w.svc.SetActionPolicy(t.Context(), serviceproxy.BigQueryID, "ga4", "bigquery.query",
		orchestration.PolicyNever))

	_, text, failed = w.use(pi, agenttool.ToolBigQueryQuery, `{"sql":"SELECT 1"}`)
	require.True(t, failed)
	assert.Contains(t, text, "never")
	assert.Empty(t, w.fake.Requests())
}

// chatPi is the pi's token of a Bot of the preset in a chat of its own, the owner's turn not taken.
func (w *bigqueryWorld) chatPi() string {
	w.t.Helper()

	ctx := w.t.Context()

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "ga4-chat", Preset: agenttool.PresetReport})
	require.NoError(w.t, err)

	chat, err := w.svc.CreateChat(ctx, bot.ID, "Weekly installs")
	require.NoError(w.t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(w.t, err)

	return pi
}

// Scenario (review round 1, CodeRabbit): the owner set queries to ask. He approves a dry run, which bills
// nothing; the same SQL run for real is a new question, and the dry run approved runs once.
func TestAnApprovedDryRunDoesNotRunTheQuery(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorldWith(t, true, 50*time.Millisecond, 0)
	pi := w.chatPi()
	ctx := t.Context()

	require.NoError(t, w.svc.SetActionPolicy(ctx, serviceproxy.BigQueryID, "ga4", "bigquery.query",
		orchestration.PolicyAsk))

	dry := `{"sql":"SELECT 1","dry_run":true}`

	_, text, failed := w.use(pi, agenttool.ToolBigQueryQuery, dry)
	require.True(t, failed)
	assert.Contains(t, text, "Waiting for the owner's approval")

	pending, err := w.svc.ListApprovals(ctx, orchestration.ApprovalFilter{Status: orchestration.ApprovalPending})
	require.NoError(t, err)
	require.Len(t, pending, 1)

	_, err = w.svc.DecideApproval(ctx, pending[0].ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)

	_, text, failed = w.use(pi, agenttool.ToolBigQueryQuery, `{"sql":"SELECT 1"}`)
	require.True(t, failed, "the billed query asks for itself")
	assert.Contains(t, text, "Waiting for the owner's approval")
	assert.Empty(t, w.fake.Jobs(), "no job ran")

	_, text, failed = w.use(pi, agenttool.ToolBigQueryQuery, dry)
	require.False(t, failed, text)
	assert.Contains(t, text, `"dry_run":true`)
	assert.Empty(t, w.fake.Jobs())
}

// Scenario (review round 1, CodeRabbit): a query takes longer than the server's write timeout. The tool's answer
// still arrives, as the other long tools' do.
func TestALongQueryStillAnswersTheTool(t *testing.T) {
	t.Parallel()

	w := newBigQueryWorldWith(t, true, 0, 300*time.Millisecond)
	w.fake.ResultsDelay = 200 * time.Millisecond
	pi := w.readRunPi(agenttool.PresetReport)

	status, text, failed := w.use(pi, agenttool.ToolBigQueryQuery, `{"sql":"SELECT 1"}`)
	require.Equal(t, http.StatusOK, status, text)
	require.False(t, failed, text)
	assert.Contains(t, text, `"rows":[["2026-11-19","131"]]`)
}
