package clientcli

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset/supersettest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// supersetWorld is a control plane as it is wired for Superset: the service
// that signs accounts in, the vault, the service proxy that decides and
// relays, and the client API; with a stand-in for one Superset. A request
// that asks waits for the owner for hold.
type supersetWorld struct {
	*world

	superset *supersettest.Superset
	logs     *syncBuffer
}

func newSupersetWorld(t *testing.T, hold time.Duration) *supersetWorld {
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

	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	integrations := integration.New(svc, secrets, platform.SystemClock{}, platform.NewID, integration.Superset())

	agents := agentapi.New(svc, func(context.Context, string, string) (agentapi.Token, error) {
		return agentapi.Token{}, orchestration.ErrNotFound
	}, logger)

	proxy, err := serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: svc, Logger: logger, Hold: hold,
		Token: func(context.Context, string, string) (string, error) { return "", orchestration.ErrNotFound },
		Send: func(ctx context.Context, id, label string, r serviceproxy.Sent) (serviceproxy.Answer, error) {
			answer, err := integrations.Call(ctx, id, label, integration.AccountRequest{
				Method: r.Method, Path: r.Path, RawQuery: r.RawQuery, ContentType: r.ContentType, Body: r.Body,
			})

			return serviceproxy.Answer{Status: answer.Status, ContentType: answer.ContentType, Body: answer.Body}, err
		},
	})
	require.NoError(t, err)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	require.NoError(t, clientapi.New(svc, logger, stop).WithIntegrations(integrations).WithApprovals(proxy).
		WithRelay(proxy).Register(mux))

	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	return &supersetWorld{
		world:    &world{svc: svc, url: hs.URL, home: filepath.Join(t.TempDir(), "home")},
		superset: supersettest.New(t), logs: logs,
	}
}

// connect is the owner connecting the stand-in as superset:vmaps, with the
// password in a file.
func (w *supersetWorld) connect(t *testing.T) {
	t.Helper()

	w.login(t, bearertoken.Owner)

	file := filepath.Join(t.TempDir(), "password")
	require.NoError(t, os.WriteFile(file, []byte(supersettest.Password+"\n"), 0o600))

	out, err := w.cmd(t, "integrations", "connect", "superset", "--label", "vmaps", "--url", w.superset.URL(),
		"--user", supersettest.User, "--password-file", file)
	require.NoError(t, err, out)
}

// neverShown checks that no text holds the password, a token of Superset's
// or a row of a chart.
func (w *supersetWorld) neverShown(t *testing.T, texts ...string) {
	t.Helper()

	for _, text := range append(texts, w.logs.String()) {
		assert.NotContains(t, text, supersettest.Password)
		assert.NotContains(t, text, "access-")
		assert.NotContains(t, text, "refresh-")
	}

	assert.NotContains(t, w.logs.String(), "Person Number", "a log never holds a row")
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

// Scenario: the owner connects his company's Superset from the terminal, as
// he connects a Lark app or a Google account: the same command, with the
// address, the user name and the password in a file. A wrong password is
// refused and nothing is kept; the right one shows the account, the address
// and Superset's version. The password is never an argument and never
// printed or logged. A coordinator cannot connect an account.
func TestConnectingASupersetAccountFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newSupersetWorld(t, time.Second)
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "superset\taccount\tnot connected\n")

	_, err = w.cmd(t, "integrations", "connect", "superset", "--label", "vmaps", "--url", w.superset.URL())
	require.ErrorContains(t, err, "--password-file", "the password comes from a file")

	wrong, err := w.cmdIn(t, "not-the-password\n", "integrations", "connect", "superset", "--label", "vmaps", "--url",
		w.superset.URL(), "--user", supersettest.User, "--password-file", "-")
	require.ErrorContains(t, err, "the sign-in did not complete")
	assert.NotContains(t, err.Error(), "not-the-password")

	out, err = w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, out, "superset\taccount\tnot connected\n", "a refused sign-in keeps nothing")

	connected, err := w.cmdIn(t, supersettest.Password+"\n", "integrations", "connect", "superset", "--label", "vmaps",
		"--url", w.superset.URL()+"/", "--user", supersettest.User, "--password-file", "-")
	require.NoError(t, err)
	assert.Equal(t, "superset:vmaps is connected as "+supersettest.User+" at "+w.superset.URL()+", version 6.1.0\n",
		connected)

	listed, err := w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, listed, "superset\taccount\tvmaps ("+supersettest.User+" at "+w.superset.URL()+")\n")

	var asJSON struct {
		Services []openapi.Integration `json:"services"`
	}

	w.jsonCmd(t, &asJSON, "", "integrations", "list")
	require.Len(t, asJSON.Services, 1)
	assert.Equal(t, openapi.IntegrationKindAccount, asJSON.Services[0].Kind)
	require.Len(t, asJSON.Services[0].Connections, 1)
	assert.Equal(t, "6.1.0", *asJSON.Services[0].Connections[0].Version)
	assert.False(t, asJSON.Services[0].Connections[0].SignInRequired)

	w.neverShown(t, wrong, connected, listed)

	out, err = w.cmd(t, "integrations", "disconnect", "superset:vmaps")
	require.NoError(t, err)
	assert.Equal(t, "superset:vmaps is forgotten\n", out)

	w.login(t, bearertoken.Coordinator)

	_, err = w.cmdIn(t, supersettest.Password+"\n", "integrations", "connect", "superset", "--label", "other", "--url",
		w.superset.URL(), "--user", supersettest.User, "--password-file", "-")
	require.ErrorContains(t, err, "only the owner")
}

// Scenario: the owner asks for the figures of a dashboard from the terminal.
// He finds the dashboard by its title, sees its charts and filters, and reads
// three tables: 160 and 274 people and 21 units, the dashboard's own counts.
// A chart comes as one JSON object with its columns, rows, the SQL Superset
// ran and when the figures were computed, or as CSV, or as the SQL alone. A
// chart of a kind that is not read says so by name. Each read is logged with
// who asked, the account, the dashboard, the chart and the number of rows,
// and never a row.
func TestReadingADashboardsChartsFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newSupersetWorld(t, time.Second)
	w.connect(t)

	found, err := w.cmd(t, "superset", "dashboards", "--search", "usage")
	require.NoError(t, err)
	assert.Equal(t, "9\tProduct usage\n", found)

	shown, err := w.cmd(t, "superset", "dashboard", "9")
	require.NoError(t, err)
	assert.Contains(t, shown, "9\tProduct usage\n")
	assert.Contains(t, shown, "  164\tActive people\ttable\tpeople\n")
	assert.Contains(t, shown, "  172\tFlows\tsankey_v2\tusage_by_unit\t(not read yet unless it has a saved query)\n")
	assert.Contains(t, shown, "  Unit\tfilter_select\tunit\t[\"Sales\"]\n")

	var people openapi.SupersetChartData

	raw, err := w.cmd(t, "superset", "chart", "9", strconv.Itoa(supersettest.ActivePeople))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(raw), &people))
	assert.Equal(t, 160, people.RowCount)
	assert.Len(t, people.Rows, 160)
	assert.Equal(t, []string{"full_name", "username", "unit"}, people.Columns)
	assert.Equal(t, []any{"Person Number 1", "username-1", "unit-1"}, people.Rows[0])
	assert.Equal(t, openapi.Built, people.Source)
	assert.Contains(t, people.Sql, "FROM dataset_28")
	assert.WithinDuration(t, time.Now(), people.ComputedAt, time.Minute)

	asCSV, err := w.cmd(t, "superset", "chart", "9", strconv.Itoa(supersettest.InactivePeople), "--format", "csv",
		"--connection", "vmaps")
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(asCSV), "\n")
	assert.Len(t, lines, 275, "a header line and 274 people")
	assert.Equal(t, "full_name,username,unit", lines[0])
	assert.Equal(t, "Person Number 274,username-274,unit-274", lines[274])

	var units openapi.SupersetChartData

	raw, err = w.cmd(t, "superset", "chart", supersettest.DashboardSlug, strconv.Itoa(supersettest.ByUnit))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(raw), &units))
	assert.Equal(t, 21, units.RowCount)

	var days openapi.SupersetChartData

	raw, err = w.cmd(t, "superset", "chart", "9", strconv.Itoa(supersettest.ByDay))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(raw), &days))
	assert.Equal(t, openapi.Saved, days.Source)
	assert.True(t, days.Cached)
	assert.Equal(t, time.Date(2026, 11, 12, 3, 4, 5, 0, time.UTC), days.ComputedAt.UTC())

	sql, err := w.cmd(t, "superset", "chart", "9", strconv.Itoa(supersettest.ByUnit), "--sql")
	require.NoError(t, err)
	assert.Equal(t, "SELECT unit, count FROM dataset_27 LIMIT 21\n", sql)

	_, err = w.cmd(t, "superset", "chart", "9", strconv.Itoa(supersettest.Flows))
	require.ErrorContains(t, err, "not read yet: sankey_v2")

	_, err = w.cmd(t, "superset", "chart", "9", "999")
	require.ErrorContains(t, err, "dashboard 9 has no chart 999")

	_, err = w.cmd(t, "superset", "dashboards", "--connection", "other")
	require.ErrorContains(t, err, "not_found")

	logs := w.logs.String()
	assert.Contains(t, logs, "event=superset.read")
	assert.Contains(t, logs, "connection=superset:vmaps action=superset.charts.read rows=160 dashboard=9 chart=164")
	assert.Contains(t, logs, "rows=274 dashboard=9 chart=165")
	assert.Contains(t, logs, `agent="owner token `)
	assert.Contains(t, logs, "action=superset.dashboards.read verdict=allowed")

	w.neverShown(t, found, shown, asCSV, sql)
}

// Scenario: the owner sends requests to Superset's API that have no command
// of their own. A read runs and its answer is printed as Superset sent it.
// A change asks: the request is held, and when the owner does not answer in
// time the command says it waits and nothing reaches Superset. He approves
// it; the same command then passes, once, and asks again the time after.
// What he set to never is refused, and a read he set to never refuses the
// reading commands too. A path that climbs, hides a slash or names another
// address is refused before anything is decided. An answer of Superset that
// is not a success is printed and fails the command with its status.
func TestSupersetAPIFollowsTheOwnersAnswerForTheAction(t *testing.T) {
	t.Parallel()

	w := newSupersetWorld(t, 200*time.Millisecond)
	w.connect(t)

	read, err := w.cmd(t, "superset", "api", "get", "/api/v1/chart/166")
	require.NoError(t, err)
	assert.Contains(t, read, `"slice_name":"Usage by unit"`)

	var enveloped openapi.SupersetAnswer

	w.jsonCmd(t, &enveloped, "", "superset", "api", "GET", "/api/v1/chart/166")
	assert.Equal(t, 200, enveloped.Status)
	assert.Equal(t, "superset.charts.read", enveloped.Action)
	assert.False(t, enveloped.Base64)

	rename := []string{"superset", "api", "PUT", "/api/v1/chart/166", "--data", "-"}

	_, err = w.cmdIn(t, `{"slice_name":"renamed"}`, rename...)
	require.ErrorContains(t, err, "Waiting for the owner's approval (apr_")
	require.ErrorContains(t, err, "was NOT run")
	assert.Zero(t, w.sent(http.MethodPut), "a held request does not reach Superset")

	pending, err := w.svc.ListApprovals(t.Context(), orchestration.ApprovalFilter{Status: orchestration.ApprovalPending})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "superset.charts.write", pending[0].Action)
	assert.Equal(t, "owner", pending[0].AskedBy)
	assert.Equal(t, "Create or change: PUT /api/v1/chart/166", pending[0].Title, "the owner sees which request")
	assert.Equal(t, orchestration.Field{Label: "Request", Value: "PUT /api/v1/chart/166"}, pending[0].Payload[0])
	assert.Equal(t, orchestration.Field{Label: "slice_name", Value: "renamed"}, pending[0].Payload[1])

	waiting, err := w.cmd(t, "approvals")
	require.NoError(t, err)
	assert.Contains(t, waiting, pending[0].ID)

	_, err = w.cmd(t, "approvals", "approve", pending[0].ID)
	require.NoError(t, err)

	renamed, err := w.cmdIn(t, `{"slice_name":"renamed"}`, rename...)
	require.NoError(t, err)
	assert.Contains(t, renamed, `"slice_name":"renamed"`)
	assert.Equal(t, 1, w.sent(http.MethodPut), "the approved request ran once")

	_, err = w.cmdIn(t, `{"slice_name":"renamed"}`, rename...)
	require.ErrorContains(t, err, "Waiting for the owner's approval", "an approval is for one run")
	assert.Equal(t, 1, w.sent(http.MethodPut))

	_, err = w.cmdIn(t, `{"sql":"select 1","database_id":1}`, "superset", "api", "POST", "/api/v1/sqllab/execute/",
		"--data", "-")
	require.ErrorContains(t, err, "Waiting for the owner's approval", "running SQL asks")

	out, err := w.cmd(t, "integrations", "actions", "set", "superset:vmaps", "superset.charts.delete", "never")
	require.NoError(t, err, out)

	_, err = w.cmd(t, "superset", "api", "DELETE", "/api/v1/chart/166")
	require.ErrorContains(t, err, "never")
	assert.Zero(t, w.sent(http.MethodDelete))

	for _, path := range []string{
		"/api/v1/chart/../security/users/", "/api/v1/chart%2F166", "//superset.example.com/api/v1/chart/166",
		"https://superset.example.com/api/v1/chart/166", "api/v1/chart/166",
	} {
		_, err = w.cmd(t, "superset", "api", "GET", path)
		require.ErrorContains(t, err, "Refused", path)
	}

	missing, err := w.cmd(t, "superset", "api", "GET", "/api/v1/chart/999")
	require.ErrorContains(t, err, "Superset answered 404")
	assert.Contains(t, missing, "Not found")

	actions, err := w.cmd(t, "integrations", "actions", "superset:vmaps")
	require.NoError(t, err)
	assert.Contains(t, actions, "superset.sql.run")
	assert.Contains(t, actions, "superset.charts.read")

	out, err = w.cmd(t, "integrations", "actions", "set", "superset:vmaps", "superset.charts.read", "never")
	require.NoError(t, err, out)

	_, err = w.cmd(t, "superset", "chart", "9", strconv.Itoa(supersettest.ByUnit))
	require.ErrorContains(t, err, "never", "the reading commands obey the owner's answer too")

	logs := w.logs.String()
	assert.Contains(t, logs, "action=superset.charts.write verdict=waiting")
	assert.Contains(t, logs, "action=superset.charts.write verdict=\"approved before\"")
	assert.Contains(t, logs, "action=superset.charts.delete verdict=never")
	assert.Contains(t, logs, "verdict=\"malformed:")
	assert.Contains(t, logs, "event=superset.relayed")

	w.neverShown(t, read, renamed, missing)
}

// Scenario: a Bot is asked for a figure of a dashboard. Until the owner
// switches the Superset account on for chats, the Bot's commands are refused
// as if the account did not exist. Switched on, the Bot reads the dashboard.
// A change it asks for waits for the owner in the Bot's chat, as "pi", so
// that the Bot is woken there when he answers.
func TestABotUsesSupersetOnlyWhenTheAccountIsOnForChats(t *testing.T) { //nolint:paralleltest // sets HYPOMNEMA_CHAT_ID
	w := newSupersetWorld(t, 200*time.Millisecond)
	w.connect(t)

	chat, err := w.svc.CreateChat(t.Context(), "", "The usage figures")
	require.NoError(t, err)
	t.Setenv(chatEnv, chat.ID)

	w.login(t, bearertoken.Coordinator)

	for _, command := range [][]string{
		{"superset", "dashboards"},
		{"superset", "dashboard", "9"},
		{"superset", "chart", "9", "164"},
		{"superset", "api", "GET", "/api/v1/chart/166"},
		{"superset", "dashboards", "--connection", "missing"},
	} {
		_, err = w.cmd(t, command...)
		require.ErrorContains(t, err, "this agent was not given superset:", strings.Join(command, " "))
	}

	assert.Empty(t, w.superset.Requests(), "nothing reached Superset for the Bot")

	_, err = w.svc.SetConnectionChats(t.Context(), integration.SupersetID, "vmaps", true)
	require.NoError(t, err)

	out, err := w.cmd(t, "superset", "dashboards")
	require.NoError(t, err)
	assert.Contains(t, out, "9\tProduct usage\n")

	raw, err := w.cmd(t, "superset", "chart", "9", strconv.Itoa(supersettest.ByUnit))
	require.NoError(t, err)
	assert.Contains(t, raw, `"row_count":21`)

	_, err = w.cmdIn(t, `{"slice_name":"renamed"}`, "superset", "api", "PUT", "/api/v1/chart/166", "--data", "-")
	require.ErrorContains(t, err, "Waiting for the owner's approval")
	require.ErrorContains(t, err, "end your turn")

	pending, err := w.svc.ListApprovals(t.Context(), orchestration.ApprovalFilter{Status: orchestration.ApprovalPending})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, chat.ID, pending[0].ChatID)
	assert.Equal(t, "pi", pending[0].AskedBy)

	_, err = w.cmd(t, "integrations", "actions", "superset:vmaps")
	require.ErrorContains(t, err, "only the owner", "a Bot does not see or set the owner's answers")

	assert.Contains(t, w.logs.String(), `agent="coordinator token `)
	w.neverShown(t, out, raw)
}
