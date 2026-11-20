package clientcli

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery/bigquerytest"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// BigQuery from the terminal (issue #976): a control plane wired as it is for BigQuery (the integration service
// with the key, the vault, the service proxy that decides, the client API), against a fake Google token endpoint
// and a fake BigQuery. The key is made in the test; no real credential is anywhere here.

const bqFakeToken = "ya29.fake-token-of-the-cli-test"

type bigqueryCLIWorld struct {
	*world

	fake *bigquerytest.Server
	logs *syncBuffer
	key  string
	pem  string
}

func newBigQueryCLIWorld(t *testing.T, hold time.Duration) *bigqueryCLIWorld {
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

	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+bqFakeToken+`","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(tokens.Close)

	fake := bigquerytest.New(t)
	fake.Tables["data-project.analytics_1"] = []bigquerytest.Table{
		{ID: "a_view", Type: "VIEW"}, {ID: "events_20261119", Type: "TABLE"},
	}
	fake.Columns, fake.Rows = []string{"d", "users"}, [][]string{{"2026-11-19", "131"}, {"2026-11-20", "1,2"}}

	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	integrations := integration.New(svc, secrets, platform.SystemClock{}, platform.NewID, integration.BigQuery()).
		WithGoogle(tokens.URL, fake.URL).WithLogger(logger).
		WithChecker(func(context.Context, string, string) error { return nil })

	agents := agentapi.New(svc, func(context.Context, string, string) (agentapi.Token, error) {
		return agentapi.Token{}, orchestration.ErrNotFound
	}, logger)

	proxy, err := serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: svc, Logger: logger, Hold: hold, RunHold: hold,
		Token: func(context.Context, string, string) (string, error) { return "", orchestration.ErrNotFound },
	})
	require.NoError(t, err)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	require.NoError(t, clientapi.New(svc, logger, stop).WithIntegrations(integrations).WithApprovals(proxy).
		WithBigQuery(integrations, proxy).Register(mux))

	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	private, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)

	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	key, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "key-project", "private_key_id": "k1", "private_key": pemKey,
		"client_email": "hypomnema-ga4@data-project.iam.gserviceaccount.com",
		"token_uri":    "https://oauth2.googleapis.com/token",
	})
	require.NoError(t, err)

	return &bigqueryCLIWorld{
		world: &world{svc: svc, url: hs.URL, home: filepath.Join(t.TempDir(), "home")},
		fake:  fake, logs: logs, key: string(key), pem: pemKey,
	}
}

// connect is the owner connecting bigquery:ga4 with the key on standard input.
func (w *bigqueryCLIWorld) connect(t *testing.T) string {
	t.Helper()

	if _, err := LoadConfig(w.home); err != nil {
		w.login(t, bearertoken.Owner)
	}

	out, err := w.cmdIn(t, w.key, "integrations", "connect", "bigquery", "--label", "ga4", "--key-file", "-",
		"--dataset", "data-project.analytics_1", "--billing-project", "billing-project")
	require.NoError(t, err, out)

	return out
}

// neverShown checks that no text holds the key, the token or a row.
func (w *bigqueryCLIWorld) neverShown(t *testing.T, texts ...string) {
	t.Helper()

	for _, text := range append(texts, w.logs.String()) {
		assert.NotContains(t, text, w.pem[40:90])
		assert.NotContains(t, text, "private_key")
		assert.NotContains(t, text, bqFakeToken)
	}

	assert.NotContains(t, w.logs.String(), "2026-11-19", "a log never holds a row")
}

// Scenario: the owner connects BigQuery from the terminal with the key on standard input and the GA4 dataset;
// the account, the datasets and the cap are printed, never the key. He lists the tables, reads one, dry-runs a
// query, runs it as JSON and as CSV, lowers the cap and disconnects, told that the key still exists at Google.
func TestTheOwnerUsesBigQueryFromTheCLI(t *testing.T) {
	t.Parallel()

	w := newBigQueryCLIWorld(t, time.Second)
	w.login(t, bearertoken.Owner)

	_, err := w.cmdIn(t, w.key, "integrations", "connect", "bigquery", "--label", "ga4", "--key-file", "-")
	require.ErrorContains(t, err, "--dataset")

	connected := w.connect(t)
	assert.Equal(t, "bigquery:ga4 is connected as hypomnema-ga4@data-project.iam.gserviceaccount.com\n"+
		"datasets data-project.analytics_1, billed to billing-project, at most 10GiB a query\n", connected)

	listed, err := w.cmd(t, "integrations", "list")
	require.NoError(t, err)
	assert.Contains(t, listed, "bigquery\tserviceaccount\tga4 (hypomnema-ga4@data-project.iam.gserviceaccount.com: "+
		"data-project.analytics_1)")

	tables, err := w.cmd(t, "bigquery", "tables", "data-project.analytics_1")
	require.NoError(t, err)
	assert.Equal(t, "a_view\tVIEW\nevents_20261119\tTABLE\n", tables)

	table, err := w.cmd(t, "bigquery", "table", "data-project.analytics_1.events_20261119")
	require.NoError(t, err)
	assert.Contains(t, table, "  event_params.key\tSTRING")

	sql := "SELECT d, COUNT(*) AS users FROM `data-project.analytics_1.events_*` WHERE _TABLE_SUFFIX BETWEEN " +
		"'20261119' AND '20261120' GROUP BY d"

	dry, err := w.cmdIn(t, sql, "bigquery", "query", "--sql-file", "-", "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, dry, `"dry_run":true`)
	assert.Contains(t, dry, `"bytes_processed":1048576`)
	assert.Empty(t, w.fake.Jobs(), "a dry run sends no job")

	asJSON, err := w.cmd(t, "bigquery", "query", sql)
	require.NoError(t, err)

	var result openapi.BigQueryResult
	require.NoError(t, json.Unmarshal([]byte(asJSON), &result))
	assert.Equal(t, int64(2), result.TotalRows)
	assert.True(t, strings.HasPrefix(*result.JobId, "hyp-"))

	asCSV, err := w.cmd(t, "bigquery", "query", sql, "--format", "csv")
	require.NoError(t, err)
	assert.Equal(t, "d,users\n2026-11-19,131\n2026-11-20,\"1,2\"\n", asCSV)

	_, err = w.cmd(t, "bigquery", "query", "SELECT 1; SELECT 2")
	require.ErrorContains(t, err, "a query is one statement")

	_, err = w.cmd(t, "bigquery", "query", "SELECT 1", "--timeout", "11m")
	require.ErrorContains(t, err, "--timeout")

	capped, err := w.cmd(t, "integrations", "max-bytes", "bigquery:ga4", "1GiB")
	require.NoError(t, err)
	assert.Equal(t, "bigquery:ga4 bills at most 1GiB a query\n", capped)

	w.fake.DryRun = map[string]any{"statementType": "SELECT", "totalBytesProcessed": strconv.Itoa(2 << 30)}
	_, err = w.cmd(t, "bigquery", "query", sql)
	require.ErrorContains(t, err, "over the cap of 1GiB")
	assert.Len(t, w.fake.Jobs(), 2, "the refused query sent no job")

	gone, err := w.cmd(t, "integrations", "disconnect", "bigquery:ga4")
	require.NoError(t, err)
	assert.Contains(t, gone, "bigquery:ga4 is forgotten\nthe service account's key still exists at Google")

	w.neverShown(t, connected, listed, tables, table, dry, asJSON, asCSV)
}

// Scenario: a coordinator may use the Connection only once the owner switched it on for chats; a worker's token
// is no client API token at all.
func TestACoordinatorQueriesOnlyAConnectionOnForChats(t *testing.T) {
	t.Parallel()

	w := newBigQueryCLIWorld(t, time.Second)
	w.connect(t)

	pi := &world{svc: w.svc, url: w.url, home: filepath.Join(t.TempDir(), "coordinator")}
	pi.login(t, bearertoken.Coordinator)

	_, err := pi.cmd(t, "bigquery", "query", "--connection", "ga4", "SELECT 1")
	require.ErrorContains(t, err, "this agent was not given bigquery:ga4")

	_, err = w.svc.SetConnectionChats(t.Context(), integration.BigQueryID, "ga4", true)
	require.NoError(t, err)

	out, err := pi.cmd(t, "bigquery", "query", "--connection", "ga4", "SELECT 1")
	require.NoError(t, err)
	assert.Contains(t, out, `"statement_type":"SELECT"`)
}

// Scenario (review round 3, P2): a script Routine queries BigQuery and the owner set queries to ask. Its
// launcher's `routines script` hands out the Routine's limit, 2520 seconds: the 30 minutes an approval may wait,
// the query's --timeout of 10 minutes and 2 to spare, as long as the command's own client waits. The query waits
// for the owner as an Approval of the run; he allows it always for this Routine, the same command goes on, and the
// next run's query passes unasked.
func TestARoutinesBigQueryQueryWaitsWithinItsLimit(t *testing.T) { //nolint:paralleltest // sets the launcher's env
	w := newBigQueryCLIWorld(t, 20*time.Second)
	w.connect(t)
	owner := w.world

	_, err := w.svc.SetConnectionChats(t.Context(), integration.BigQueryID, "ga4", true)
	require.NoError(t, err)

	_, err = owner.cmd(t, "integrations", "actions", "set", "bigquery:ga4", "bigquery.query", "ask")
	require.NoError(t, err)

	limit := int(QueryClientTimeout(10*time.Minute) / time.Second)
	require.Equal(t, 2520, limit, "30 minutes of approval, 10 of query, 2 to spare")

	report, err := w.svc.Routines().PutRoutine(t.Context(), routinestore.RoutineSpec{
		Bot: orchestration.DefaultBotID, Name: "ga4-weekly", Kind: routinestore.RoutineScript, Script: "ga4.sh",
		ScriptText:     "#!/bin/sh\nhypomnema bigquery query --sql-file week.sql --timeout 10m --format csv\n",
		EveryMinutes:   60,
		TimeoutSeconds: limit,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)

	script := &world{svc: w.svc, url: w.url, home: filepath.Join(t.TempDir(), "coordinator")}
	script.login(t, bearertoken.Coordinator)

	into := filepath.Join(t.TempDir(), "routines")

	// launch is the launcher starting a run: `routines script` writes the script and the run's key and prints
	// the file's name and the seconds the launcher gives `timeout`.
	launch := func() routinestore.RoutineRun {
		started, err := w.svc.RunRoutineNow(t.Context(), report.ID)
		require.NoError(t, err)

		said, err := script.cmd(t, "routines", "script", report.ID, "--bot", "coordinator", "--into", into)
		require.NoError(t, err)
		assert.Equal(t, "ga4.sh\n2520\n", said, "the launcher's limit is the Routine's timeout_seconds")

		return started.Run
	}

	first := launch()

	t.Setenv(routineEnv, report.ID)
	t.Setenv(agenttool.EnvRoutineRun, runKeyFile(into, report.ID))

	query := []string{"bigquery", "query", "SELECT 1", "--timeout", "10m", "--format", "csv"}
	done := make(chan error, 1)

	go func() {
		_, err := script.cmd(t, query...)
		done <- err
	}()

	var pending []orchestration.Approval

	require.Eventually(t, func() bool {
		pending, err = w.svc.ListApprovals(t.Context(), orchestration.ApprovalFilter{RoutineID: report.ID})

		return err == nil && len(pending) == 1
	}, 10*time.Second, 10*time.Millisecond, "the query waits for the owner")
	assert.Equal(t, first.ID, pending[0].RoutineRunID, "the Approval is the run's: the command sent its key")
	assert.Equal(t, "bigquery.query", pending[0].Action)

	var decided openapi.Approval
	owner.jsonCmd(t, &decided, "", "approvals", "approve", pending[0].ID, "--routine")
	require.NoError(t, <-done, "the same command goes on where it waited")

	_, err = w.svc.FinishRoutineRun(t.Context(), first.ID, routinestore.RunResult{State: routinestore.RunSucceeded})
	require.NoError(t, err)

	launch()

	_, err = script.cmd(t, query...)
	require.NoError(t, err, "a later run's query is not asked")
	assert.NotContains(t, w.logs.String(), "hrk_", "a run's key is in no log line")
}

// Scenario: the query command's client waits for an approval of a Routine's run, the job's --timeout and two
// minutes: at least 42 minutes with --timeout 10m, more than the 35 the other long commands wait.
func TestTheQueryCommandOutwaitsAnApprovalAndItsJob(t *testing.T) {
	t.Parallel()

	assert.Equal(t, serviceproxy.DefaultRunHold, runHold, "the wait of a Routine's run is the proxy's")

	c, _ := find([]string{"bigquery", "query"})
	require.NotNil(t, c)
	require.NotNil(t, c.timeout)

	for given, want := range map[string]time.Duration{
		"10m": 42 * time.Minute, "": 32*time.Minute + 30*time.Second, "1m": 33 * time.Minute, "nonsense": 42 * time.Minute,
	} {
		fs := flagSet(c, io.Discard)
		args := []string{"SELECT 1"}

		if given != "" {
			args = append(args, "--timeout", given)
		}

		require.NoError(t, parse(fs, args))
		assert.Equal(t, want, c.timeout(fs), given)
	}

	for _, name := range [][]string{
		{"bigquery", "datasets"}, {"bigquery", "tables"}, {"bigquery", "table"},
	} {
		c, _ := find(name)
		require.NotNil(t, c)
		assert.True(t, c.long, "%v waits as a long command", name)
	}

}
