package agentapi_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset/supersettest"
)

// botWorkerOf is a worker's key on node of a Run the Bot made, given uses,
// as workerOf's.
func (w *supersetWorld) botWorkerOf(node, bot string, uses ...string) string {
	w.t.Helper()

	ctx := w.t.Context()

	_, err := w.svc.Profiles().ApplyProvider(ctx, "", "name: gateway\nkind: model_gateway\nmodels: [claude-opus-5-5]\n")
	require.NoError(w.t, err)
	_, _, err = w.svc.Profiles().ApplyProfile(ctx, "",
		"name: implementer\ndescription: d\nruntime: claude\nmodel: {provider: gateway, name: claude-opus-5-5}\n")
	require.NoError(w.t, err)

	n, _, err := w.svc.AddNode(ctx, node, 1)
	require.NoError(w.t, err)
	require.NoError(w.t, w.svc.NodeHello(ctx, n.ID, orchestration.Hello{WorktreeRoot: "/srv/wt",
		Runtimes: []orchestration.RuntimeInfo{{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "1.0.0"}}}))
	require.NoError(w.t, w.svc.NodeHeartbeat(ctx, n.ID))

	run, err := w.svc.CreateRun(ctx, orchestration.NewRun{Objective: "the community figures", CreatedBy: "pi",
		Bot: bot, Uses: uses})
	require.NoError(w.t, err)

	_, err = w.svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{
		{Ref: "a", Profile: "implementer", Spec: "read the dashboard"},
	})
	require.NoError(w.t, err)

	sch, err := w.svc.Schedule(ctx)
	require.NoError(w.t, err)
	require.Len(w.t, sch.Created, 1)

	key, err := w.svc.AddAgentSession(ctx, sch.Created[0], uses)
	require.NoError(w.t, err)

	return key
}

// Scenario (issue #562): a Bot gives a Run superset:vmaps; its worker reads
// it. The owner then limits the Bot to dashboard 9 of it: the worker, whose
// Run was made before, loses the account at once (read when asked, as the
// owner's switch is), and the Bot cannot give it to a new Run. The Bot's pi
// is offered the reads and not superset_api: the list leaves out the
// dashboard it was not given, which reads as one that does not exist, and
// a request it writes is refused, naming its dashboards; each refusal is
// logged as out of scope. It reads a chart of its dashboard with the
// dashboard's filters set, and a filter's values, which are logged with
// the read (issue #648); the same on another dashboard is refused before
// anything is asked of it.
func TestABotLimitedToDashboardsKeepsItsWorkersAndPiToThem(t *testing.T) {
	t.Parallel()

	w := newSupersetWorld(t)
	ctx := t.Context()

	_, err := w.svc.SetConnectionChats(ctx, integration.SupersetID, "vmaps", true)
	require.NoError(t, err)

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "community"})
	require.NoError(t, err)

	worker := w.botWorkerOf("n1", bot.ID, "superset:vmaps")
	require.Len(t, w.supersetToolsOf(worker), 5)

	_, text, isError := w.use(worker, "superset_dashboards", `{}`)
	require.False(t, isError, text)

	bot.Scopes = map[string]orchestration.ConnectionScope{"superset:vmaps": {Dashboards: []int64{9}}}
	bot, err = w.svc.PutBot(ctx, bot)
	require.NoError(t, err)

	before := len(w.superset.Requests())

	assert.Empty(t, w.supersetToolsOf(worker), "the Run made before the scope loses the account")

	status, text, _ := w.use(worker, "superset_api", `{"method":"GET","path":"/api/v1/chart/166"}`)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Contains(t, text, "no tool superset_api")
	assert.Len(t, w.superset.Requests(), before, "nothing reached Superset for the worker")

	_, err = w.svc.CreateRun(ctx, orchestration.NewRun{Objective: "again", CreatedBy: "pi", Bot: bot.ID,
		Uses: []string{"superset:vmaps"}})
	require.ErrorIs(t, err, orchestration.ErrForbidden)
	require.ErrorContains(t, err, "only some dashboards of superset:vmaps")

	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)

	_, _, err = w.svc.AddToken(ctx, "pi", "coordinator")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"superset_dashboards", "superset_dashboard", "superset_chart_data", "superset_filter_values",
	}, toolNames(w.supersetToolsOf(pi)))

	_, text, isError = w.use(pi, "superset_dashboards", `{}`)
	require.False(t, isError, text)
	assert.Contains(t, text, `"title":"Product usage"`)
	assert.NotContains(t, text, "Sales pipeline", "a dashboard it was not given is not listed")

	_, text, isError = w.use(pi, "superset_dashboard", `{"dashboard":12}`)
	assert.True(t, isError)
	assert.Contains(t, text, "dashboard 12 not found")

	_, text, isError = w.use(pi, "superset_api", `{"method":"POST","path":"/api/v1/chart/data","body":{}}`)
	assert.True(t, isError)
	assert.Contains(t, text, "only through its dashboards (9)")
	assert.Zero(t, w.sent(http.MethodPost), "its own request never reached Superset")

	assert.Contains(t, w.log.String(), `refused="out of scope"`)

	// Its own requests through the service proxy's tunnel are refused for the account; the typed chart read,
	// which applies the scope, still runs. The main Bot, which has none, keeps the tunnel.
	may := func(key string) bool {
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+key)

		ok, err := w.agents.May(ctx, r, serviceproxy.SupersetID, "vmaps")
		require.NoError(t, err)

		return ok
	}

	assert.False(t, may(pi), "a direct request to /api/v1/chart/data or /api/v1/dataset/ is refused")

	_, rows, isError := w.use(pi, "superset_chart_data",
		`{"dashboard":"9","chart":`+strconv.Itoa(supersettest.ByDay)+`}`)
	require.False(t, isError, rows)
	assert.Contains(t, rows, `"row_count":30`)

	_, rows, isError = w.use(pi, "superset_chart_data",
		`{"dashboard":"9","chart":`+strconv.Itoa(supersettest.UnitShare)+`,"filters":{"Unit":["Support"]}}`)
	require.False(t, isError, rows)
	assert.Equal(t, []any{map[string]any{"col": "unit", "op": "IN", "val": []any{"Support"}}},
		w.superset.LastQuery()["filters"], "the value replaces the dashboard's default")
	assert.Contains(t, w.log.String(), `filters="[Unit=Support]"`)

	_, text, isError = w.use(pi, "superset_filter_values", `{"dashboard":"9","filter":"Unit","search":"sup"}`)
	require.False(t, isError, text)
	assert.JSONEq(t, `{"data":[{"value":"Support","count":25}]}`, text)

	before = len(w.superset.Requests())

	for _, call := range []struct{ tool, args string }{
		{"superset_chart_data", `{"dashboard":"12","chart":166,"filters":{"Unit":["Support"]}}`},
		{"superset_filter_values", `{"dashboard":"12","filter":"Unit"}`},
	} {
		_, text, isError = w.use(pi, call.tool, call.args)
		assert.True(t, isError, call.tool)
		assert.Contains(t, text, "dashboard 12 not found", call.tool)
	}

	assert.Len(t, w.superset.Requests(), before, "nothing was asked of a dashboard out of its scope")

	mainChat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	mainPi, err := w.svc.AddBotToken(ctx, "pi", mainChat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)
	assert.True(t, may(mainPi), "a Bot without a scope keeps the tunnel")
}

// Scenario (issue #562): a Bot gives a Run superset:vmaps and is then
// removed. Its worker cannot learn what the Bot may reach, so it loses the
// account: no tool, and a call reaches nothing.
func TestTheWorkerOfARemovedBotLosesItsSupersetAccount(t *testing.T) {
	t.Parallel()

	w := newSupersetWorld(t)
	ctx := t.Context()

	_, err := w.svc.SetConnectionChats(ctx, integration.SupersetID, "vmaps", true)
	require.NoError(t, err)

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "community"})
	require.NoError(t, err)

	worker := w.botWorkerOf("n1", bot.ID, "superset:vmaps")
	require.Len(t, w.supersetToolsOf(worker), 5)

	require.NoError(t, w.svc.DeleteBot(ctx, bot.ID))

	before := len(w.superset.Requests())

	assert.Empty(t, w.supersetToolsOf(worker))

	status, text, _ := w.use(worker, "superset_chart_data", `{"dashboard":"9","chart":166}`)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Contains(t, text, "no tool superset_chart_data")
	assert.Len(t, w.superset.Requests(), before, "nothing reached Superset for the worker")
}
