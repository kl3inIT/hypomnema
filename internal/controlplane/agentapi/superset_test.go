package agentapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset/supersettest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// lockedBuffer is a log the proxy's held requests and the API write at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// supersetWorld is the agent API wired as the control plane wires it for
// Superset: the service that signs accounts in, the vault, the service proxy
// that decides and relays, with a stand-in for one Superset connected as
// superset:vmaps. A request that asks waits for the owner for 200 ms.
type supersetWorld struct {
	*world

	superset *supersettest.Superset
	proxy    *serviceproxy.Proxy
	agents   *agentapi.Server
	log      *lockedBuffer
}

func newSupersetWorld(t *testing.T, labels ...string) *supersetWorld {
	t.Helper()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	secrets := vault.New(db, platform.SystemClock{}, platform.NewID, keys,
		func(ctx context.Context, ref string) ([]string, error) {
			name, _ := vault.NameFromRef(ref)

			return svc.ConnectionsUsingSecret(ctx, name)
		})

	log := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(log, nil))
	integrations := integration.New(svc, secrets, platform.SystemClock{}, platform.NewID, integration.Superset())

	agents := agentapi.New(svc, nil, logger)

	proxy, err := serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: svc, Logger: logger, Hold: 200 * time.Millisecond,
		Send: func(ctx context.Context, id, label string, r serviceproxy.Sent) (serviceproxy.Answer, error) {
			answer, err := integrations.Call(ctx, id, label, integration.AccountRequest{
				Method: r.Method, Path: r.Path, RawQuery: r.RawQuery, ContentType: r.ContentType, Body: r.Body,
			})

			return serviceproxy.Answer{Status: answer.Status, ContentType: answer.ContentType, Body: answer.Body}, err
		},
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	agents.WithRelay(proxy).Register(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	stand := supersettest.New(t)

	if len(labels) == 0 {
		labels = []string{"vmaps"}
	}

	for _, label := range labels {
		_, err = integrations.ConnectAccount(ctx, integration.SupersetID, label, integration.Account{
			Address: stand.URL() + "/", User: supersettest.User, Password: supersettest.Password,
		})
		require.NoError(t, err)
	}

	return &supersetWorld{
		world:    &world{t: t, svc: svc, srv: srv, logs: &bytes.Buffer{}},
		superset: stand, proxy: proxy, agents: agents, log: log,
	}
}

func (w *supersetWorld) sent(method string) int {
	n := 0

	for _, r := range w.superset.Requests() {
		if r.Method == method {
			n++
		}
	}

	return n
}

// supersetToolsOf are the Superset tools among an agent's.
func (w *supersetWorld) supersetToolsOf(key string) []agenttool.BrowserTool {
	w.t.Helper()

	var out []agenttool.BrowserTool

	for _, tool := range w.tools(key) {
		switch tool.Name {
		case "superset_dashboards", "superset_dashboard", "superset_chart_data", "superset_api":
			out = append(out, tool)
		}
	}

	return out
}

// Scenario: the owner gives a Run his Superset account, superset:vmaps
// (issue #536). Its worker, which has no client API, finds Superset's four
// tools on its one list, without an account to name since it has one, and
// reads a dashboard and a chart's rows with them. A Bot's pi is offered none
// until the owner switches the account on for chats, as its commands are
// refused until then; the coordinator token, with no MCP, never. A change
// the worker asks for through superset_api waits for the owner's approval
// as the command's does, under the worker's Dispatch, and tells it to ask
// with its ask tool; approved, the same call passes once. The Bot's waits in
// its chat. A worker whose Run was not given the account has no tool and
// its call is refused. Nothing logged holds the password, a token or a row.
func TestAWorkerAndABotUseSupersetByItsToolsWhenGivenTheAccount(t *testing.T) {
	t.Parallel()

	w := newSupersetWorld(t)
	ctx := t.Context()

	worker := w.workerOf("n1", "", "superset:vmaps")
	other := w.workerOf("n2", "")

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	tools := w.supersetToolsOf(worker)
	assert.Equal(t, []string{"superset_dashboards", "superset_dashboard", "superset_chart_data", "superset_api"},
		toolNames(tools))
	assert.NotContains(t, string(tools[0].InputSchema), "account", "one account: nothing to name")
	assert.Empty(t, w.supersetToolsOf(other), "a Run not given the account")
	assert.Empty(t, w.supersetToolsOf(pi), "not on for chats yet")
	assert.Empty(t, w.tools(coordinator))

	for _, key := range []string{pi, other} {
		status, text, _ := w.use(key, "superset_dashboards", `{}`)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Contains(t, text, "no tool superset_dashboards")
	}
	assert.Empty(t, w.superset.Requests(), "nothing reached Superset for an agent not given it")

	_, text, isError := w.use(worker, "superset_dashboards", `{"search":"usage"}`)
	require.False(t, isError, text)
	assert.Contains(t, text, `"title":"Product usage"`)

	_, text, isError = w.use(worker, "superset_dashboard", `{"dashboard":9}`)
	require.False(t, isError, text)
	assert.Contains(t, text, `"charts":[`)

	_, rows, isError := w.use(worker, "superset_chart_data",
		`{"dashboard":"9","chart":`+strconv.Itoa(supersettest.ByUnit)+`}`)
	require.False(t, isError, rows)
	assert.Contains(t, rows, `"row_count":21`)

	_, text, isError = w.use(worker, "superset_dashboards", `{"account":"other"}`)
	assert.True(t, isError)
	assert.Equal(t, "this agent was not given superset:other", text)

	rename := `{"method":"PUT","path":"/api/v1/chart/166","body":{"slice_name":"renamed"}}`

	_, text, isError = w.use(worker, "superset_api", rename)
	assert.True(t, isError)
	assert.Contains(t, text, "Waiting for the owner's approval (apr_")
	assert.Contains(t, text, "your ask tool")
	assert.Zero(t, w.sent(http.MethodPut), "a held request does not reach Superset")

	pending, err := w.svc.ListApprovals(ctx, orchestration.ApprovalFilter{Status: orchestration.ApprovalPending})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "worker", pending[0].AskedBy)
	assert.NotEmpty(t, pending[0].DispatchID)
	assert.Equal(t, "Create or change: PUT /api/v1/chart/166", pending[0].Title)

	_, err = w.proxy.Decide(ctx, pending[0].ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)

	_, text, isError = w.use(worker, "superset_api", rename)
	require.False(t, isError, text)
	assert.Contains(t, text, `"status":200`)
	assert.Contains(t, text, `"action":"superset.charts.write"`)
	assert.Equal(t, 1, w.sent(http.MethodPut), "the approved request ran once")

	_, text, isError = w.use(worker, "superset_api", `{"method":"GET","path":"/api/v1/chart/999"}`)
	assert.True(t, isError, "Superset's refusal is the tool's error")
	assert.Contains(t, text, `"status":404`)

	_, err = w.svc.SetConnectionChats(ctx, integration.SupersetID, "vmaps", true)
	require.NoError(t, err)

	assert.Len(t, w.supersetToolsOf(pi), 4, "on for chats, the Bot's pi has them")

	_, text, isError = w.use(pi, "superset_api", rename)
	assert.True(t, isError)
	assert.Contains(t, text, "end your turn")

	pending, err = w.svc.ListApprovals(ctx, orchestration.ApprovalFilter{Status: orchestration.ApprovalPending})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "pi", pending[0].AskedBy)
	assert.Equal(t, chat.ID, pending[0].ChatID)

	logs := w.log.String()
	assert.Contains(t, logs, "event=agentapi.superset")
	assert.Contains(t, logs, "connection=superset:vmaps")

	for _, text := range []string{logs, rows} {
		assert.NotContains(t, text, supersettest.Password)
		assert.NotContains(t, text, "access-")
		assert.NotContains(t, text, "refresh-")
	}

	assert.NotContains(t, logs, "Person Number", "a log never holds a row")
}

// Scenario: a Run is given two Superset accounts. Its worker's tools then
// take the account, one of the two, and a call that names none is told the
// labels and reaches nothing; nothing is guessed, as with the commands.
func TestAWorkerWithTwoSupersetAccountsNamesOne(t *testing.T) {
	t.Parallel()

	w := newSupersetWorld(t, "vmaps", "sales")

	worker := w.workerOf("n1", "", "superset:vmaps", "superset:sales")

	tools := w.supersetToolsOf(worker)
	require.Len(t, tools, 4)

	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}

	require.NoError(t, json.Unmarshal(tools[0].InputSchema, &schema))
	assert.Equal(t, []string{"sales", "vmaps"}, schema.Properties["account"].Enum)
	assert.Contains(t, schema.Required, "account")

	before := len(w.superset.Requests())

	_, text, isError := w.use(worker, "superset_dashboards", `{}`)
	assert.True(t, isError)
	assert.Equal(t, "name the account: sales, vmaps", text)
	assert.Len(t, w.superset.Requests(), before)

	_, text, isError = w.use(worker, "superset_dashboards", `{"account":"sales"}`)
	require.False(t, isError, text)
	assert.Contains(t, text, `"title":"Product usage"`)
}
