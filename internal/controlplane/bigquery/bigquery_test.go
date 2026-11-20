package bigquery_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery/bigquerytest"
)

// The BigQuery client against a fake BigQuery that records each request (issue #976, the acceptance checks of
// design v3). Every refusal is asserted to have sent no job that could bill.

const ga4Sample = "WITH base AS (SELECT event_name, user_pseudo_id, PARSE_DATE('%Y%m%d', event_date) AS d, " +
	"TIMESTAMP_MICROS(event_timestamp) AS ts, (SELECT value.int_value FROM UNNEST(event_params) WHERE key = " +
	"'engagement_time_msec') AS ms FROM `p.analytics_1.events_*` WHERE _TABLE_SUFFIX BETWEEN '20261101' AND " +
	"'20261119' AND event_name IN ('first_open','user_engagement','app_remove')) SELECT d, COUNT(DISTINCT " +
	"user_pseudo_id) FROM base GROUP BY d ORDER BY d;"

// field is an object of a request's JSON body.
func field(t *testing.T, body map[string]any, name string) map[string]any {
	t.Helper()

	got, ok := body[name].(map[string]any)
	require.True(t, ok, "%s is an object", name)

	return got
}

// text is a text of a request's JSON body.
func text(t *testing.T, body map[string]any, name string) string {
	t.Helper()

	got, ok := body[name].(string)
	require.True(t, ok, "%s is a text", name)

	return got
}

func client(t *testing.T, fake *bigquerytest.Server, s bigquery.Settings) *bigquery.Client {
	t.Helper()

	if s.BillingProject == "" {
		s.BillingProject = "billing-project"
	}

	if len(s.Datasets) == 0 {
		s.Datasets = []string{"data-project.analytics_1"}
	}

	if s.MaxBytesBilled == 0 {
		s.MaxBytesBilled = bigquery.DefaultMaxBytes
	}

	c, err := bigquery.New(t.Context(), bigquery.Config{
		Settings: s, Connection: "bigquery:ga4",
		Options: []option.ClientOption{option.WithEndpoint(fake.URL), option.WithoutAuthentication()},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	return c
}

// Scenario: a GA4 query in the data owner's shape, a ; inside a string, a ; and CREATE inside a comment, and a
// trailing ; all pass the lexer and reach the dry run.
func TestTheGateLetsOneSelectThrough(t *testing.T) {
	t.Parallel()

	for name, sql := range map[string]string{
		"the GA4 sample":       ga4Sample,
		"a ; in a string":      "SELECT 'a;b' AS x FROM `p.analytics_1.events_20261119`",
		"a ; in a comment":     "SELECT 1 -- ; CREATE\nFROM `p.analytics_1.events_20261119`",
		"a trailing ;":         "SELECT 1;",
		"a raw string":         `SELECT r'\d+;' AS x`,
		"a triple string":      "SELECT '''a;\nCREATE''' AS x",
		"in parentheses":       "(SELECT 1)",
		"an alias named ai":    "SELECT ai.x FROM t AS ai",
		"a hash comment":       "SELECT 1 # ; DECLARE\n",
		"a block comment last": "SELECT 1 /* ; */",
		"a column named ml":    "SELECT ml, t.ai FROM t",
		"an escaped backquote": "SELECT `a\\`b`.c FROM t",
		"a chained call":       "SELECT 'x'.(NET.HOST)()",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := bigquerytest.New(t)
			got, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{SQL: sql, DryRun: true})
			require.NoError(t, err)
			assert.Equal(t, "SELECT", got.StatementType)

			requests := fake.Requests()
			require.Len(t, requests, 1)
			assert.True(t, requests[0].IsDryRun())

			query := field(t, field(t, requests[0].Body, "configuration"), "query")
			assert.Equal(t, sql, query["query"], "the same bytes")
		})
	}
}

// Scenario: what is not one SELECT is refused by the lexer, before any request.
func TestTheGateRefusesBeforeAnyRequest(t *testing.T) {
	t.Parallel()

	for name, sql := range map[string]string{
		"a temp function":         "CREATE TEMP FUNCTION f() AS (1); SELECT f()",
		"two statements":          "SELECT 1; SELECT 2",
		"a script":                "DECLARE x INT64; SELECT x",
		"an export":               "EXPORT DATA OPTIONS(uri='gs://b/*') AS SELECT 1",
		"an external query":       "SELECT * FROM EXTERNAL_QUERY('c', 'SELECT 1')",
		"a comment in a path":     "SELECT AI /*x*/ .GENERATE('hi')",
		"a newline in a path":     "SELECT ml\n.generate_text(MODEL m, TABLE t)",
		"a backquoted first name": "SELECT `ai`.generate('hi')",
		"a backquoted whole path": "SELECT `AI.GENERATE`('hi')",
		"vector search":           "SELECT * FROM VECTOR_SEARCH(TABLE t, 'c', TABLE q)",
		"legacy SQL":              "#legacySQL\nSELECT 1",
		// Review round 1 of the code: a chained call names a multi-part function in parentheses, SAFE. comes
		// before a name, and a backquoted name is compared once its escapes are decoded.
		"a chained AI call":           "SELECT 'hi'.(AI.GENERATE)(connection_id => 'c')",
		"a chained ML call":           "SELECT t.x.(ml.generate_text)(MODEL m) FROM t",
		"a chained quoted call":       "SELECT ('x').(`ai`.generate)()",
		"a chained whole-quoted":      "SELECT 'x'.(`AI.GENERATE`)()",
		"a chained vector search":     "SELECT * FROM t.(VECTOR_SEARCH)(TABLE q, 'c')",
		"SAFE before AI":              "SELECT SAFE.AI.GENERATE('hi')",
		"a hex escape":                "SELECT `\\x41I`.GENERATE('hi')",
		"a unicode escape":            "SELECT `A\\u0049.GENERATE`('hi')",
		"an octal escape":             "SELECT `\\101i`.generate('hi')",
		"an escaped external query":   "SELECT * FROM `EXTERNAL\\x5fQUERY`('c', 'SELECT 1')",
		"a comment in a chained call": "SELECT 'x'.( AI /* c */ . GENERATE ) ('hi')",
		"legacy SQL after a comment":  "/* c */ #legacysql SELECT 1",
		"a call":                      "SELECT 1 FROM t WHERE x IN (SELECT 1) AND CALL",
		"a set":                       "SET @@dataset_id = 'x'",
		"a dash hides no comment":     "SELECT a--'\n; DROP TABLE t; SELECT '",
		"an open string":              "SELECT 'a",
		"an open comment":             "SELECT 1 /* ;",
		"an insert":                   "INSERT INTO t VALUES (1)",
		"empty":                       " -- nothing\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := bigquerytest.New(t)
			_, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{SQL: sql})
			require.ErrorIs(t, err, bigquery.ErrRefused)
			assert.Empty(t, fake.Requests(), "no request at all")
		})
	}
}

// Scenario: the dry run says what the query is; what is not a plain SELECT of native tables under the cap is
// refused and no job is sent.
func TestTheDryRunRefusesAndSendsNoJob(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct {
		stats map[string]any
		want  string
	}{
		"no statistics":     {nil, "no statistics"},
		"an insert":         {map[string]any{"statementType": "INSERT"}, "INSERT"},
		"a script":          {map[string]any{"statementType": "SCRIPT"}, "SCRIPT"},
		"a function made":   {map[string]any{"statementType": "CREATE_FUNCTION"}, "CREATE_FUNCTION"},
		"a routine called":  {map[string]any{"statementType": "SELECT", "referencedRoutines": []any{map[string]any{"projectId": "p", "datasetId": "d", "routineId": "remote_fn"}}}, "p.d.remote_fn"},
		"an export":         {map[string]any{"statementType": "SELECT", "exportDataStatistics": map[string]any{"fileCount": "1"}}, "exports"},
		"over the cap":      {map[string]any{"statementType": "SELECT", "totalBytesProcessed": "10737418241"}, "over the cap of 10GiB"},
		"a routine target":  {map[string]any{"statementType": "SELECT", "ddlTargetRoutine": map[string]any{"routineId": "f"}}, "makes"},
		"a table target":    {map[string]any{"statementType": "SELECT", "ddlTargetTable": map[string]any{"tableId": "t"}}, "makes"},
		"BigQuery refusing": {map[string]any{"refuse": true}, "dry run"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := bigquerytest.New(t)
			fake.DryRun = c.stats

			if c.stats["refuse"] == true {
				fake.DryRunStatus = 400
			}

			_, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{SQL: "SELECT 1"})
			require.ErrorIs(t, err, bigquery.ErrRefused)
			assert.Contains(t, err.Error(), c.want)
			assert.Empty(t, fake.Jobs(), "no job that could bill")
			assert.Len(t, fake.Requests(), 1, "the dry run alone")
		})
	}
}

// Scenario: a good query. The dry run goes under the cap; the job goes with the id, project and location kept
// before it was sent, the same bytes and the same cap, no destination and its labels; its rows come back.
func TestAGoodQueryRunsAsItsDryRunSaid(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.Columns = []string{"d", "users"}
	fake.Rows = [][]string{{"2026-11-18", "120"}, {"2026-11-19", "131"}}

	got, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{
		SQL: ga4Sample, Agent: "bot main in chat cht_1", Tool: "bigquery_query",
	})
	require.NoError(t, err)

	requests := fake.Requests()
	require.True(t, requests[0].IsDryRun())

	dryQuery := field(t, field(t, requests[0].Body, "configuration"), "query")
	assert.Equal(t, "10737418240", dryQuery["maximumBytesBilled"])
	assert.Equal(t, false, dryQuery["useLegacySql"])
	assert.Equal(t, "/projects/billing-project/jobs", requests[0].Path)

	jobs := fake.Jobs()
	require.Len(t, jobs, 1)

	ref := field(t, jobs[0].Body, "jobReference")
	assert.Equal(t, "billing-project", ref["projectId"])
	assert.Equal(t, "US", ref["location"], "the dry run's location, the Connection naming none")
	assert.True(t, strings.HasPrefix(text(t, ref, "jobId"), "hyp-"))
	assert.Equal(t, got.JobID, ref["jobId"])

	config := field(t, jobs[0].Body, "configuration")
	query := field(t, config, "query")
	assert.Equal(t, ga4Sample, query["query"], "the same bytes as the dry run")
	assert.Equal(t, "10737418240", query["maximumBytesBilled"])
	assert.NotContains(t, query, "destinationTable")
	assert.NotContains(t, query, "createSession")
	assert.Equal(t, map[string]any{"hypomnema-agent": "bot-main-in-chat-cht_1", "hypomnema-tool": "bigquery_query"},
		config["labels"])

	assert.Equal(t, []string{"d", "users"}, got.Columns)
	assert.Equal(t, [][]any{{"2026-11-18", "120"}, {"2026-11-19", "131"}}, got.Rows)
	assert.Equal(t, uint64(2), got.TotalRows)
	assert.False(t, got.Truncated)
	assert.Equal(t, int64(10485760), got.BytesBilled)
	assert.Equal(t, "SELECT", got.StatementType)
	assert.Empty(t, fake.Cancels())
}

// Scenario: the Connection names a location: the job runs there, whatever the dry run said.
func TestTheConnectionsLocationWins(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.Columns = []string{"x"}

	_, err := client(t, fake, bigquery.Settings{Location: "asia-southeast1"}).Query(t.Context(),
		bigquery.Request{SQL: "SELECT 1"})
	require.NoError(t, err)
	assert.Equal(t, "asia-southeast1", field(t, fake.Jobs()[0].Body, "jobReference")["location"])
}

// Scenario: the job ends as something else than a SELECT: its rows are not read.
func TestAJobThatEndsAsNoSelectIsNotRead(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.JobStatement = "SCRIPT"

	_, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{SQL: "SELECT 1"})
	require.ErrorContains(t, err, "ran but ended as no SELECT")
	require.NotErrorIs(t, err, bigquery.ErrRefused, "it ran: it is no refusal before running")

	for _, r := range fake.Requests() {
		assert.NotContains(t, r.Query, "pageToken")
		assert.False(t, strings.Contains(r.Path, "/queries/") && !strings.Contains(r.Query, "maxResults=0"),
			"no page of rows was asked for: %s?%s", r.Path, r.Query)
	}
}

// Scenario: jobs.insert is accepted but never answered. At the limit the job is canceled from the project, id and
// location kept before it was sent, on a context that outlived the request.
func TestALostSubmitIsCanceledFromWhatWasKept(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.HangInsert = true
	start := time.Now()

	_, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{
		SQL: "SELECT 1", Timeout: 300 * time.Millisecond,
	})

	// The request's own context ended at the limit; the cancel came after it, on a context of its own.
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond)

	var canceled *bigquery.CancelError
	require.ErrorAs(t, err, &canceled)
	assert.Contains(t, err.Error(), "cancel requested")

	jobs := fake.Jobs()
	require.Len(t, jobs, 1)

	ref := field(t, jobs[0].Body, "jobReference")
	cancels := fake.Cancels()
	require.Len(t, cancels, 1)
	assert.Equal(t, "/projects/"+text(t, ref, "projectId")+"/jobs/"+text(t, ref, "jobId")+"/cancel", cancels[0].Path)
	assert.Contains(t, cancels[0].Query, "location=US")
	assert.Equal(t, canceled.JobID, ref["jobId"])
}

// Scenario: a cancel BigQuery answers with 404 says the job was never made.
func TestACancelOfAJobNeverMadeSaysSo(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.HangInsert, fake.CancelStatus = true, 404

	_, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{
		SQL: "SELECT 1", Timeout: 200 * time.Millisecond,
	})

	var canceled *bigquery.CancelError
	require.ErrorAs(t, err, &canceled)
	assert.True(t, canceled.Never)
	assert.Contains(t, err.Error(), "did not start")
}

// Scenario: a job still running at the limit is canceled, and the answer says the cancel was requested.
func TestAJobRunningAtItsLimitIsCanceled(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.HangJob = true

	_, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{
		SQL: "SELECT 1", Timeout: 500 * time.Millisecond,
	})

	var canceled *bigquery.CancelError
	require.ErrorAs(t, err, &canceled)
	assert.Contains(t, err.Error(), "cancel requested")
	assert.Contains(t, err.Error(), "billed up to the cap")
	require.Len(t, fake.Cancels(), 1)
	assert.Contains(t, fake.Cancels()[0].Path, canceled.JobID)
}

// Scenario: more rows than asked for: the answer has as many, with the total and truncated.
func TestMaxRowsTruncates(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.Columns = []string{"n"}
	fake.Rows = [][]string{{"1"}, {"2"}, {"3"}, {"4"}, {"5"}}

	got, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{SQL: "SELECT n", MaxRows: 2})
	require.NoError(t, err)
	assert.Equal(t, [][]any{{"1"}, {"2"}}, got.Rows)
	assert.Equal(t, uint64(5), got.TotalRows)
	assert.True(t, got.Truncated)

	_, err = client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{SQL: "SELECT n", MaxRows: 10001})
	require.ErrorIs(t, err, bigquery.ErrInvalid)
}

// Scenario: the data project is not the billing project: datasets are listed in the data project.
func TestDatasetsAreListedInTheDataProject(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.Datasets["data-project"] = []string{"analytics_1", "analytics_2"}
	fake.Datasets["billing-project"] = []string{"billing_only"}

	got, err := client(t, fake, bigquery.Settings{}).Datasets(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []bigquery.DatasetRef{
		{Project: "data-project", Dataset: "analytics_1"}, {Project: "data-project", Dataset: "analytics_2"},
	}, got)
}

// Scenario: the tables of a dataset with their types, and a table's schema.
func TestTablesAndATablesSchema(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.Tables["data-project.analytics_1"] = []bigquerytest.Table{
		{ID: "events_20261119", Type: "TABLE"}, {ID: "summary", Type: "VIEW"},
	}
	c := client(t, fake, bigquery.Settings{})

	tables, truncated, err := c.Tables(t.Context(), "analytics_1")
	require.NoError(t, err)
	assert.False(t, truncated)
	assert.Equal(t, []bigquery.TableSummary{{Table: "events_20261119", Type: "TABLE"}, {Table: "summary", Type: "VIEW"}},
		tables)

	info, err := c.Table(t.Context(), "data-project.analytics_1.events_20261119")
	require.NoError(t, err)
	assert.Equal(t, "TABLE", info.Type)
	assert.Equal(t, uint64(12), info.Rows)
	require.Len(t, info.Schema, 3)
	assert.Equal(t, "REPEATED", info.Schema[2].Mode)
	assert.Equal(t, "key", info.Schema[2].Fields[0].Name)

	_, err = c.Table(t.Context(), "nope")
	require.ErrorIs(t, err, bigquery.ErrInvalid)

	_, err = c.Table(t.Context(), "data-project.analytics_1.missing")
	require.ErrorIs(t, err, bigquery.ErrNotFound)
}

// Scenario: the connect check, step by step: a view listed first is passed over for the newest native events_
// table, which is read once and dry-run; a dataset with an external table, or none native, is refused at its step.
func TestTheCheckReadsANativeTable(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.Tables["data-project.analytics_1"] = []bigquerytest.Table{
		{ID: "a_view", Type: "VIEW"}, {ID: "events_20261118", Type: "TABLE"}, {ID: "events_20261119", Type: "TABLE"},
		{ID: "events_intraday_20261120", Type: "TABLE"},
	}

	require.NoError(t, client(t, fake, bigquery.Settings{}).Check(t.Context()))

	var read []string

	for _, r := range fake.Requests() {
		if strings.Contains(r.Path, "/tables/") {
			read = append(read, r.Path)
		}
	}

	// The SDK reads a table's schema again before its rows.
	assert.Contains(t, read, "/projects/data-project/datasets/analytics_1/tables/events_20261119")
	assert.Contains(t, read, "/projects/data-project/datasets/analytics_1/tables/events_20261119/data")

	for _, path := range read {
		assert.Contains(t, path, "/tables/events_20261119", "the view and the older tables are never read")
	}

	last := fake.Requests()[len(fake.Requests())-1]
	require.True(t, last.IsDryRun())
	assert.Equal(t, "SELECT 1 FROM `data-project.analytics_1.events_20261119` LIMIT 1",
		field(t, field(t, last.Body, "configuration"), "query")["query"])
	assert.Empty(t, fake.Jobs())

	for name, c := range map[string]struct {
		tables []bigquerytest.Table
		step   string
	}{
		"an external table": {[]bigquerytest.Table{{ID: "events_20261119", Type: "TABLE"}, {ID: "gcs", Type: "EXTERNAL"}}, "listing the tables"},
		"views alone":       {[]bigquerytest.Table{{ID: "v", Type: "VIEW"}}, "listing the tables"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := bigquerytest.New(t)
			fake.Tables["data-project.analytics_1"] = c.tables

			err := client(t, fake, bigquery.Settings{}).Check(t.Context())

			var failed *bigquery.CheckError
			require.ErrorAs(t, err, &failed)
			assert.Contains(t, failed.Step, c.step)
			assert.Len(t, fake.Requests(), 1, "nothing after the list")
		})
	}
}

// Scenario: sizes as the owner writes them.
func TestSizes(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]int64{
		"10GiB": 10 << 30, "500MB": 500e6, "1TiB": 1 << 40, "2048": 2048, "1.5GiB": 3 << 29,
	} {
		got, err := bigquery.ParseSize(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}

	for _, in := range []string{"", "-1GiB", "0", "ten", "0.1B"} {
		_, err := bigquery.ParseSize(in)
		require.ErrorIs(t, err, bigquery.ErrInvalid, in)
	}

	assert.Equal(t, "10GiB", bigquery.Size(10<<30))
}

// Scenario: settings: the key's project bills when none is named, the cap defaults to 10 GiB, and names are
// checked.
func TestSettings(t *testing.T) {
	t.Parallel()

	got, err := bigquery.Settings{Datasets: []string{"data-project.analytics_1"}}.Check("key-project")
	require.NoError(t, err)
	assert.Equal(t, "key-project", got.BillingProject)
	assert.Equal(t, bigquery.DefaultMaxBytes, got.MaxBytesBilled)

	for _, bad := range []bigquery.Settings{
		{},
		{Datasets: []string{"analytics_1"}},
		{Datasets: []string{"p.d; DROP"}},
		{Datasets: []string{"data-project.a", "data-project.a"}},
		{Datasets: []string{"data-project.a"}, Location: "US; x"},
		{Datasets: []string{"data-project.a"}, MaxBytesBilled: -1},
	} {
		_, err := bad.Check("key-project")
		require.Error(t, err, "%+v", bad)
	}
}

// Scenario (review round 1, CodeRabbit): watching the job fails before the limit. A job that still runs is
// canceled from what was kept, with the watching error in the answer; a job that ended is not canceled.
func TestAJobWhoseWatchFailsIsSettled(t *testing.T) {
	t.Parallel()

	running := bigquerytest.New(t)
	running.ResultsStatus, running.JobRunning = 403, true

	_, err := client(t, running, bigquery.Settings{}).Query(t.Context(), bigquery.Request{SQL: "SELECT 1"})

	var canceled *bigquery.CancelError
	require.ErrorAs(t, err, &canceled)
	assert.Contains(t, err.Error(), "watching it failed")
	assert.Contains(t, err.Error(), "403")
	require.Len(t, running.Cancels(), 1)
	assert.Contains(t, running.Cancels()[0].Path, canceled.JobID)

	ended := bigquerytest.New(t)
	ended.ResultsStatus = 403

	_, err = client(t, ended, bigquery.Settings{}).Query(t.Context(), bigquery.Request{SQL: "SELECT 1"})
	require.Error(t, err)
	assert.Empty(t, ended.Cancels(), "a job known to have ended is not canceled")
}

// Scenario: the dry run named no location and the Connection none: a 404 to the cancel proves nothing, and the
// answer says the job may still run and be billed.
func TestACancelWithoutALocationKnowsNothing(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.HangInsert, fake.CancelStatus, fake.DryRunLocation = true, 404, ""

	_, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{
		SQL: "SELECT 1", Timeout: 200 * time.Millisecond,
	})

	var canceled *bigquery.CancelError
	require.ErrorAs(t, err, &canceled)
	assert.False(t, canceled.Never)
	assert.Contains(t, err.Error(), "may still run and be billed")
	assert.NotContains(t, fake.Cancels()[0].Query, "location=")
}

// Scenario: rows of 20 KiB each. The rows kept stay within the byte budget, the total and truncated say more
// exist, and the pages past the budget are not fetched.
func TestRowsStopAtTheByteBudget(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.Columns = []string{"blob"}

	big := strings.Repeat("x", 20<<10)
	for range 250 {
		fake.Rows = append(fake.Rows, []string{big})
	}

	got, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{
		SQL: "SELECT blob FROM t", MaxRows: 1000,
	})
	require.NoError(t, err)
	assert.True(t, got.Truncated)
	assert.Equal(t, uint64(250), got.TotalRows)
	assert.Less(t, len(got.Rows), 30, "512 KiB holds about 25 rows of 20 KiB")
	assert.NotEmpty(t, got.Rows)

	pages := 0

	for _, r := range fake.Requests() {
		if strings.Contains(r.Path, "/queries/") && !strings.Contains(r.Query, "maxResults=0") {
			pages++
		}
	}

	assert.Equal(t, 1, pages, "no page after the budget is fetched")
}

// Scenario: FLOAT64 cells that are NaN or infinite, alone and in a list, come back as BigQuery writes them in
// JSON, and the answer still encodes.
func TestNonFiniteFloatsAreWritten(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.Schema = []map[string]any{
		{"name": "x", "type": "FLOAT"},
		{"name": "xs", "type": "FLOAT", "mode": "REPEATED"},
	}
	fake.RawRows = [][]any{
		{"NaN", []any{map[string]any{"v": "Infinity"}, map[string]any{"v": "-Infinity"}, map[string]any{"v": "1.5"}}},
	}

	got, err := client(t, fake, bigquery.Settings{}).Query(t.Context(), bigquery.Request{SQL: "SELECT x, xs"})
	require.NoError(t, err)
	assert.Equal(t, [][]any{{"NaN", []any{"Infinity", "-Infinity", 1.5}}}, got.Rows)

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"rows":[["NaN",["Infinity","-Infinity",1.5]]]`)
}

// Scenario: a dataset of more tables than are listed is refused by the check: an external table may hide past
// the list.
func TestTheCheckRefusesAListCutShort(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)

	tables := make([]bigquerytest.Table, 0, 10001)
	for i := range 10001 {
		tables = append(tables, bigquerytest.Table{ID: fmt.Sprintf("events_%08d", i), Type: "TABLE"})
	}

	fake.Tables["data-project.analytics_1"] = tables

	err := client(t, fake, bigquery.Settings{}).Check(t.Context())

	var failed *bigquery.CheckError
	require.ErrorAs(t, err, &failed)
	assert.Contains(t, err.Error(), "more than 10000 tables")
	assert.Empty(t, fake.Jobs())
}

// Scenario (review round 1, P2): BigQuery repeats a token, a JWT and a key in an error message; the answer and
// the log carry none of them.
func TestGoogleErrorsAreRedacted(t *testing.T) {
	t.Parallel()

	fake := bigquerytest.New(t)
	fake.DryRunStatus = 400
	fake.DryRunMessage = "bad request: Authorization: Bearer ya29.leaked-token eyJhbGciOi.eyJzdWIiOi.c2lnbmF0dXJl " +
		`"private_key": "-----BEGIN PRIVATE KEY-----\nMIIsecret\n-----END PRIVATE KEY-----\n" assertion=abc.def.ghi`

	var logs bytes.Buffer

	c, err := bigquery.New(t.Context(), bigquery.Config{
		Settings: bigquery.Settings{
			BillingProject: "billing-project", Datasets: []string{"data-project.analytics_1"},
			MaxBytesBilled: bigquery.DefaultMaxBytes,
		},
		Logger:  slog.New(slog.NewTextHandler(&logs, nil)),
		Options: []option.ClientOption{option.WithEndpoint(fake.URL), option.WithoutAuthentication()},
	})
	require.NoError(t, err)

	_, err = c.Query(t.Context(), bigquery.Request{SQL: "SELECT 1"})
	require.ErrorIs(t, err, bigquery.ErrRefused)

	for _, text := range []string{err.Error(), logs.String()} {
		for _, secret := range []string{"ya29.leaked-token", "eyJhbGciOi.eyJzdWIiOi", "MIIsecret", "abc.def.ghi"} {
			assert.NotContains(t, text, secret)
		}
	}

	assert.Contains(t, err.Error(), "[redacted]")
}

// Scenario: a domain-scoped project id stays one part of a name.
func TestADomainScopedProjectIsOnePart(t *testing.T) {
	t.Parallel()

	got, err := bigquery.Settings{Datasets: []string{"example.com:my-project.analytics_1"}}.Check("key-project")
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com:my-project.analytics_1"}, got.Datasets)
}
