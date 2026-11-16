package clientcli

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset/supersettest"
)

// Scenario (issue #562): the owner limits a Bot to the dashboards it may
// read of a Superset Connection, naming one by its slug, which is kept as its
// number. The Bot lists, reads and reads a chart of that dashboard, a saved
// chart's query being what runs; a chart that is not on it is refused; its
// own requests to the API are refused, naming its dashboards. Limited to
// another dashboard, it no longer sees this one: the list leaves it out and a
// read by number or slug is answered as a dashboard that does not exist,
// before any chart's query runs, and the refusal is logged. Its dashboard's
// charts are read with their filters set, and a filter's values listed, on
// that dashboard only (issue #648). The owner, the coordinator token and a
// Bot without a scope read and relay as before.
func TestABotLimitedToDashboardsReadsOnlyThem(t *testing.T) { //nolint:paralleltest // sets HYPOMNEMA_CHAT_ID
	w := newSupersetWorld(t, 200*time.Millisecond)
	w.connect(t)

	_, owner, err := w.svc.AddToken(t.Context(), "scope-owner", bearertoken.Owner)
	require.NoError(t, err)

	_, err = w.svc.SetConnectionChats(t.Context(), integration.SupersetID, "vmaps", true)
	require.NoError(t, err)

	var limited openapi.Bot

	w.jsonCmd(t, &limited, "", "bots", "set", "community", "--superset-dashboards",
		"vmaps="+supersettest.DashboardSlug)
	assert.Equal(t, map[string]openapi.ConnectionScope{
		"superset:vmaps": {Dashboards: []int64{supersettest.Dashboard}},
	}, limited.Scopes, "a slug is kept as its number")

	shown, err := w.cmd(t, "bots", "show", "community")
	require.NoError(t, err)
	assert.Contains(t, shown, "superset:vmaps dashboards: 9\n")

	chat, err := w.svc.CreateChat(t.Context(), limited.Id, "Community figures")
	require.NoError(t, err)
	t.Setenv(chatEnv, chat.ID)

	_, _, err = w.svc.AddToken(t.Context(), "pi-coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	secret, err := w.svc.AddBotToken(t.Context(), "pi-coordinator", chat.ID, limited.Id)
	require.NoError(t, err)
	w.loginWith(t, secret)

	found, err := w.cmd(t, "superset", "dashboards")
	require.NoError(t, err)
	assert.Equal(t, "9\tProduct usage\n", found)

	_, err = w.cmd(t, "superset", "dashboard", supersettest.DashboardSlug)
	require.NoError(t, err)

	raw, err := w.cmd(t, "superset", "chart", "9", strconv.Itoa(supersettest.ByDay))
	require.NoError(t, err)
	assert.Contains(t, raw, `"source":"saved"`)

	assert.Equal(t, []any{"day"}, w.superset.LastQuery()["columns"], "the saved chart's query is what runs")

	_, err = w.cmd(t, "superset", "chart", "9", "999")
	require.ErrorContains(t, err, "dashboard 9 has no chart 999")

	_, err = w.cmd(t, "superset", "chart", "9", strconv.Itoa(supersettest.UnitShare), "--filter", "Unit=Support")
	require.NoError(t, err)
	assert.Equal(t, []any{map[string]any{"col": "unit", "op": "IN", "val": []any{"Support"}}},
		w.superset.LastQuery()["filters"], "a limited Bot sets its dashboard's filters")

	values, err := w.cmd(t, "superset", "filter-values", "9", "Unit")
	require.NoError(t, err)
	assert.Contains(t, values, "40\tSales\n")

	posts := w.sent(http.MethodPost)

	_, err = w.cmdIn(t, `{"queries":[{"columns":["x"]}]}`, "superset", "api", "POST", "/api/v1/chart/data", "--data", "-")
	require.ErrorContains(t, err, "only through its dashboards (9)")
	assert.Equal(t, posts, w.sent(http.MethodPost), "the Bot's own request never reached Superset")

	// Limited to another dashboard, and to one that does not exist: this one is answered as that one is.
	w.loginWith(t, owner)

	_, err = w.cmd(t, "bots", "set", "community", "--superset-dashboards", "vmaps=8,77")
	require.NoError(t, err)

	w.loginWith(t, secret)

	found, err = w.cmd(t, "superset", "dashboards")
	require.NoError(t, err)
	assert.NotContains(t, found, "Product usage")

	_, missing := w.cmd(t, "superset", "dashboard", "77")
	require.ErrorContains(t, missing, "dashboard 77 not found")

	_, err = w.cmd(t, "superset", "dashboard", "9")
	require.ErrorContains(t, err, "dashboard 9 not found")
	assert.Equal(t, missing.Error(), strings.ReplaceAll(err.Error(), "dashboard 9 ", "dashboard 77 "),
		"out of scope reads as missing")

	_, err = w.cmd(t, "superset", "dashboard", supersettest.DashboardSlug)
	require.ErrorContains(t, err, "dashboard "+supersettest.DashboardSlug+" not found")

	_, err = w.cmd(t, "superset", "chart", supersettest.DashboardSlug, strconv.Itoa(supersettest.ActivePeople))
	require.ErrorContains(t, err, "not found")

	_, err = w.cmd(t, "superset", "chart", "9", strconv.Itoa(supersettest.UnitShare), "--filter", "Unit=Support")
	require.ErrorContains(t, err, "dashboard 9 not found")

	_, err = w.cmd(t, "superset", "filter-values", "9", "Unit")
	require.ErrorContains(t, err, "dashboard 9 not found")
	assert.Equal(t, posts, w.sent(http.MethodPost), "no chart's query ran for a dashboard out of scope")

	_, err = w.cmd(t, "superset", "api", "GET", "/api/v1/chart/166")
	require.ErrorContains(t, err, "only through its dashboards (8, 77)")

	assert.Contains(t, w.logs.String(), `refused="out of scope"`)

	// The owner, the coordinator token and the main Bot, which has no scope, are not limited.
	for _, login := range []func(){
		func() { w.loginWith(t, owner) },
		func() { w.login(t, bearertoken.Coordinator) },
		func() {
			main, err := w.svc.CreateChat(t.Context(), "", "Main")
			require.NoError(t, err)
			t.Setenv(chatEnv, main.ID)

			s, err := w.svc.AddBotToken(t.Context(), "pi-coordinator", main.ID, main.Bot)
			require.NoError(t, err)
			w.loginWith(t, s)
		},
	} {
		login()

		found, err = w.cmd(t, "superset", "dashboards")
		require.NoError(t, err)
		assert.Equal(t, "9\tProduct usage\n12\tSales pipeline\n", found, "every dashboard the account sees")

		out, err := w.cmd(t, "superset", "api", "GET", "/api/v1/chart/166")
		require.NoError(t, err)
		assert.Contains(t, out, "Usage by unit")
	}

	w.neverShown(t, raw)
}
