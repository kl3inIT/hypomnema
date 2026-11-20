package clientcli

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// BigQuery from the terminal (issue #976): the GA4 export's datasets and tables, a table's schema, and one query,
// for the owner and a Routine's script. The control plane holds the service account's key and runs every call;
// nothing here holds a key or a token. The commands are the shape of MCP Toolbox's BigQuery tools
// (list_table_ids, get_table_info, execute_sql with dry_run) and of `bq query --dry_run`.

const bigqueryID = "bigquery"

const (
	// queryTimeout and maxQueryTimeout bound a query's job: Superset's SQLLAB_TIMEOUT, and the most the control
	// plane runs one (bigquery.DefaultTimeout, bigquery.MaxTimeout).
	queryTimeout    = 30 * time.Second
	maxQueryTimeout = 10 * time.Minute
	// runHold is how long a Routine's run waits for the owner's Approval (serviceproxy.DefaultRunHold).
	runHold = 30 * time.Minute
	// querySlack covers the dry run, reading the rows and the network around the job.
	querySlack = 2 * time.Minute
)

// QueryClientTimeout is how long `bigquery query` waits for its answer: a Routine's run may wait runHold for the
// owner's Approval, then the job runs up to its --timeout (design v3, decision 23). A Routine whose script runs the
// command needs a timeout_seconds at least this long, or its launcher stops the script first.
func QueryClientTimeout(job time.Duration) time.Duration { return runHold + job + querySlack }

func bigqueryFlags(fs *flag.FlagSet) {
	fs.String("connection", "", "the BigQuery account's label (default: the only one connected)")
}

// bigqueryLabel is the Connection a command is for: the one named, or the only one there is.
func bigqueryLabel(ctx context.Context, e *env, fs *flag.FlagSet) (string, error) {
	if label := str(fs, "connection"); label != "" {
		return label, nil
	}

	service, err := serviceOf(ctx, e, bigqueryID)
	if err != nil {
		return "", err
	}

	if service == nil || len(service.Connections) == 0 {
		return "", errors.New("no BigQuery account is connected; the owner connects one with `hypomnema integrations " +
			"connect bigquery --label ga4 --key-file KEY.json --dataset PROJECT.DATASET`")
	}

	if len(service.Connections) > 1 {
		labels := make([]string, 0, len(service.Connections))
		for _, c := range service.Connections {
			labels = append(labels, c.Label)
		}

		return "", fmt.Errorf("several BigQuery accounts are connected (%s); name one with --connection",
			strings.Join(labels, ", "))
	}

	return service.Connections[0].Label, nil
}

// integrationsConnectServiceAccount connects BigQuery with a service account's JSON key, read from a file or
// standard input and never an argument: arguments show in the shell's history and in the process list. Without a
// file, the key of the label is kept and the settings are checked again.
func integrationsConnectServiceAccount(ctx context.Context, e *env, fs *flag.FlagSet, id string) error {
	label := str(fs, "label")

	datasets := []string{}
	if given, _ := fs.Lookup("dataset").Value.(*listFlag); given != nil {
		datasets = append(datasets, *given...)
	}

	if len(datasets) == 0 {
		return fmt.Errorf("pass --dataset with the GA4 export dataset as project.dataset, and --key-file with the "+
			"service account's JSON key, or - for standard input (%s)", id)
	}

	body := openapi.NewAccountConnection{Datasets: &datasets}

	if file := str(fs, "key-file"); file != "" {
		raw, err := e.read(file)
		if err != nil {
			return err
		}

		key := strings.TrimSpace(raw)
		body.ServiceAccountKey = &key
	}

	if p := str(fs, "billing-project"); p != "" {
		body.BillingProject = &p
	}

	if l := str(fs, "location"); l != "" {
		body.Location = &l
	}

	if size := str(fs, "max-bytes"); size != "" {
		n, err := parseSize(size)
		if err != nil {
			return err
		}

		body.MaxBytesBilled = &n
	}

	resp, err := e.api.PutIntegrationConnectionWithResponse(ctx, id, label, body)
	if err != nil {
		return fmt.Errorf("connecting %s:%s: %w", id, label, err)
	}

	if resp.JSON200 == nil {
		return apiError("connecting "+id+":"+label, resp.StatusCode(), resp.JSONDefault)
	}

	c := resp.JSON200

	return e.print(fs, c, func() {
		e.printf("%s:%s is connected as %s\n", id, c.Label, c.Account)

		if b := c.Bigquery; b != nil {
			e.printf("datasets %s, billed to %s, at most %s a query\n", strings.Join(b.Datasets, ", "),
				b.BillingProject, binarySize(b.MaxBytesBilled))
		}
	})
}

// integrationsMaxBytes changes a BigQuery Connection's cap on the bytes one query may bill.
func integrationsMaxBytes(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id, label, ok := strings.Cut(fs.Arg(0), ":")
	if !ok || label == "" {
		return errors.New("name the account as <integration>:<label>, such as bigquery:ga4")
	}

	n, err := parseSize(fs.Arg(1))
	if err != nil {
		return err
	}

	resp, err := e.api.PatchIntegrationConnectionWithResponse(ctx, id, label, openapi.ConnectionUse{MaxBytesBilled: &n})
	if err != nil {
		return fmt.Errorf("changing the cap of %s:%s: %w", id, label, err)
	}

	if resp.JSON200 == nil {
		return apiError("changing the cap of "+id+":"+label, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		e.printf("%s:%s bills at most %s a query\n", id, label, binarySize(n))
	})
}

func bigqueryDatasets(ctx context.Context, e *env, fs *flag.FlagSet) error {
	label, err := bigqueryLabel(ctx, e, fs)
	if err != nil {
		return err
	}

	resp, err := e.api.ListBigQueryDatasetsWithResponse(ctx, label, &openapi.ListBigQueryDatasetsParams{
		ChatId: askingChat(), HypomnemaRoutineRun: askingRun(),
	})
	if err != nil {
		return fmt.Errorf("listing the datasets of bigquery:%s: %w", label, err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the datasets of bigquery:"+label, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("no dataset\n")
		}

		for _, d := range resp.JSON200.Data {
			e.printf("%s.%s\n", d.Project, d.Dataset)
		}
	})
}

func bigqueryTables(ctx context.Context, e *env, fs *flag.FlagSet) error {
	label, err := bigqueryLabel(ctx, e, fs)
	if err != nil {
		return err
	}

	doing := fmt.Sprintf("listing the tables of %s on bigquery:%s", fs.Arg(0), label)

	resp, err := e.api.ListBigQueryTablesWithResponse(ctx, label, fs.Arg(0), &openapi.ListBigQueryTablesParams{
		ChatId: askingChat(), HypomnemaRoutineRun: askingRun(),
	})
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}

	if resp.JSON200 == nil {
		return apiError(doing, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, t := range resp.JSON200.Data {
			e.printf("%s\t%s\n", t.Table, t.Type)
		}

		if resp.JSON200.Truncated {
			e.printf("(more tables than are listed)\n")
		}
	})
}

func bigqueryTable(ctx context.Context, e *env, fs *flag.FlagSet) error {
	label, err := bigqueryLabel(ctx, e, fs)
	if err != nil {
		return err
	}

	doing := fmt.Sprintf("reading table %s on bigquery:%s", fs.Arg(0), label)

	resp, err := e.api.GetBigQueryTableWithResponse(ctx, label, fs.Arg(0), &openapi.GetBigQueryTableParams{
		ChatId: askingChat(), HypomnemaRoutineRun: askingRun(),
	})
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}

	if resp.JSON200 == nil {
		return apiError(doing, resp.StatusCode(), resp.JSONDefault)
	}

	t := resp.JSON200

	return e.print(fs, t, func() {
		e.printf("%s.%s.%s\t%s\t%d rows\t%s\n", t.Project, t.Dataset, t.Table, t.Type, t.Rows, binarySize(t.Bytes))

		var walk func(prefix string, fields []openapi.BigQueryField)

		walk = func(prefix string, fields []openapi.BigQueryField) {
			for _, f := range fields {
				e.printf("  %s%s\t%s\t%s\n", prefix, f.Name, f.Type, val(f.Mode))

				if f.Fields != nil {
					walk(prefix+f.Name+".", *f.Fields)
				}
			}
		}

		walk("", t.Schema)
	})
}

// bigqueryQuery runs one query and prints its answer: the JSON as it came, or the rows as CSV with a header line.
func bigqueryQuery(ctx context.Context, e *env, fs *flag.FlagSet) error {
	sql := strings.Join(fs.Args(), " ")

	if file := str(fs, "sql-file"); file != "" {
		if sql != "" {
			return fmt.Errorf("%w: give the query as an argument or with --sql-file, not both", ErrUsage)
		}

		text, err := e.read(file)
		if err != nil {
			return err
		}

		sql = text
	}

	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("%w: give the query as an argument, or --sql-file FILE (- for standard input)", ErrUsage)
	}

	format := str(fs, "format")
	if format != "json" && format != "csv" {
		return fmt.Errorf("%w: --format is json or csv", ErrUsage)
	}

	timeout, err := queryTimeoutOf(fs)
	if err != nil {
		return err
	}

	label, err := bigqueryLabel(ctx, e, fs)
	if err != nil {
		return err
	}

	body := openapi.BigQueryQuery{Sql: sql, ChatId: askingChat()}

	if boolean(fs, "dry-run") {
		dry := true
		body.DryRun = &dry
	}

	if given := str(fs, "max-rows"); given != "" {
		n, err := strconv.Atoi(given)
		if err != nil || n < 1 || n > 10000 {
			return fmt.Errorf("%w: --max-rows is 1 to 10000", ErrUsage)
		}

		body.MaxRows = &n
	}

	seconds := int(timeout / time.Second)
	body.TimeoutSeconds = &seconds

	doing := "querying bigquery:" + label

	resp, err := e.api.RunBigQueryQueryWithResponse(ctx, label,
		&openapi.RunBigQueryQueryParams{HypomnemaRoutineRun: askingRun()}, body)
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}

	if resp.JSON200 == nil {
		return apiError(doing, resp.StatusCode(), resp.JSONDefault)
	}

	if format == "csv" {
		return writeCSV(e, resp.Body)
	}

	_, err = e.out.Write(resp.Body)

	return err
}

// queryTimeoutOf reads --timeout: at most ten minutes, a whole number of seconds.
func queryTimeoutOf(fs *flag.FlagSet) (time.Duration, error) {
	given := str(fs, "timeout")
	if given == "" {
		return queryTimeout, nil
	}

	d, err := time.ParseDuration(given)
	if err != nil || d < time.Second || d > maxQueryTimeout {
		return 0, fmt.Errorf("%w: --timeout is a duration from 1s to %s, such as 2m", ErrUsage, maxQueryTimeout)
	}

	return d.Truncate(time.Second), nil
}

// queryClientTimeoutOf is the client's timeout for the query command, from its flags; an unreadable --timeout
// takes the longest, and the command refuses it before sending.
func queryClientTimeoutOf(fs *flag.FlagSet) time.Duration {
	d, err := queryTimeoutOf(fs)
	if err != nil {
		d = maxQueryTimeout
	}

	return QueryClientTimeout(d)
}

// writeCSV prints the answer's rows as CSV. The answer is read again with its numbers as they were written: an
// integer past 2^53 read as a float64 would lose its last digits.
func writeCSV(e *env, body []byte) error {
	var r struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}

	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()

	if err := d.Decode(&r); err != nil {
		return fmt.Errorf("reading the answer: %w", err)
	}

	w := csv.NewWriter(e.out)

	if err := w.Write(r.Columns); err != nil {
		return err
	}

	for _, row := range r.Rows {
		cells := make([]string, len(row))

		for i, v := range row {
			switch x := v.(type) {
			case nil:
			case string:
				cells[i] = x
			case json.Number:
				cells[i] = x.String()
			default:
				raw, err := json.Marshal(x)
				if err != nil {
					return err
				}

				cells[i] = string(raw)
			}
		}

		if err := w.Write(cells); err != nil {
			return err
		}
	}

	w.Flush()

	return w.Error()
}

// parseSize reads a size as the owner writes it: 10GiB, 500MB, 1TiB, or a number of bytes.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)

	units := []struct {
		suffix string
		mult   float64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1},
	}

	mult := 1.0

	for _, u := range units {
		if rest, ok := strings.CutSuffix(s, u.suffix); ok {
			s, mult = strings.TrimSpace(rest), u.mult

			break
		}
	}

	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n*mult < 1 || n*mult > 1<<62 {
		return 0, fmt.Errorf("%w: a size is a positive number with a unit, such as 10GiB", ErrUsage)
	}

	return int64(n * mult), nil
}

// binarySize writes a number of bytes as the owner writes a cap: 10GiB.
func binarySize(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	f, i := float64(n), 0

	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}

	if f == float64(int64(f)) {
		return fmt.Sprintf("%d%s", int64(f), units[i])
	}

	return fmt.Sprintf("%.1f%s", f, units[i])
}
