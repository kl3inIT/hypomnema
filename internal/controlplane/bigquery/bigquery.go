// Package bigquery reads BigQuery for a Connection (issue #976): the datasets and tables of the GA4 export, a
// table's schema, and one GoogleSQL SELECT, checked first by a lexer (lexer.go), then by a dry run, then run as a
// job whose project, id and location are kept before it is sent, under a cap on the bytes it may bill.
//
// The control plane runs every call; an agent or a node never holds the key or a token. The client is Google's
// own (cloud.google.com/go/bigquery, Apache-2.0) for metadata, jobs and rows, and its REST client
// (google.golang.org/api/bigquery/v2) for what the first does not give: the dry run's referenced routines, a table
// list's types, and a cancel by id, as Grafana's BigQuery data source and MCP Toolbox use them (design v3, decision
// 7). What a query may read is what the data owner's IAM grants the service account (decision 6); nothing here
// filters by dataset.
package bigquery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	bq "cloud.google.com/go/bigquery"
	"golang.org/x/oauth2"
	bqrest "google.golang.org/api/bigquery/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const (
	// DefaultMaxBytes is the cap on the bytes one query job may bill on demand: 10 GiB, about $0.061 at $6.25 a
	// TiB (design v3, decision 11).
	DefaultMaxBytes = int64(10) << 30
	// DefaultMaxRows and MaxRows bound the rows a query answers with (Toolbox's maxQueryResultRows, 50;
	// Superset's DISPLAY_MAX_ROW, 10,000).
	DefaultMaxRows = 50
	MaxRows        = 10000
	// DryRunTimeout bounds a dry run (Superset's SQLLAB_VALIDATION_TIMEOUT), DefaultTimeout a query (its
	// SQLLAB_TIMEOUT), and MaxTimeout what the CLI may ask for.
	DryRunTimeout  = 10 * time.Second
	DefaultTimeout = 30 * time.Second
	MaxTimeout     = 10 * time.Minute
	// cancelTimeout bounds the request that cancels a job, on a context of its own.
	cancelTimeout = 10 * time.Second
	// maxTables bounds the tables listed of one dataset; maxDatasets those of one project.
	maxTables   = 10000
	maxDatasets = 1000
)

// Settings are what the owner set for a Connection besides its key.
type Settings struct {
	// BillingProject is where jobs run and are billed.
	BillingProject string `json:"billing_project"`
	// Datasets are the GA4 export datasets, project.dataset: checked when connecting, the tools' default, and
	// where datasets are listed. Not a filter: IAM is.
	Datasets []string `json:"datasets"`
	// Location is where jobs run; empty takes the dry run's.
	Location string `json:"location,omitempty"`
	// MaxBytesBilled caps the bytes one query job bills on demand, for its dry run and its job.
	MaxBytesBilled int64 `json:"maximum_bytes_billed"`
}

var (
	projectPattern  = regexp.MustCompile(`^([a-z][a-z0-9.-]{0,62}:)?[a-z][a-z0-9-]{4,29}$`)
	datasetPattern  = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	tablePattern    = regexp.MustCompile(`^[\p{L}\p{M}\p{N}\p{Pc}\p{Pd}\p{Zs}$*]+$`)
	locationPattern = regexp.MustCompile(`^[A-Za-z0-9-]{2,40}$`)
	labelInvalid    = regexp.MustCompile(`[^a-z0-9_-]+`)
)

// Check checks settings and fills in the cap's default. The key's project is the billing project when none is
// given.
func (s Settings) Check(keyProject string) (Settings, error) {
	if s.BillingProject == "" {
		s.BillingProject = keyProject
	}

	if !projectPattern.MatchString(s.BillingProject) {
		return s, fmt.Errorf("the billing project %q is not a project id", s.BillingProject)
	}

	if len(s.Datasets) == 0 {
		return s, errors.New("name the GA4 export dataset, as project.dataset")
	}

	seen := map[string]bool{}

	for i, d := range s.Datasets {
		ref, err := parseDataset(d, "")
		if err != nil {
			return s, err
		}

		s.Datasets[i] = ref.String()
		if seen[s.Datasets[i]] {
			return s, fmt.Errorf("dataset %s is named twice", s.Datasets[i])
		}

		seen[s.Datasets[i]] = true
	}

	if s.Location != "" && !locationPattern.MatchString(s.Location) {
		return s, fmt.Errorf("the location %q is not one, such as US, EU or asia-southeast1", s.Location)
	}

	if s.MaxBytesBilled == 0 {
		s.MaxBytesBilled = DefaultMaxBytes
	}

	if s.MaxBytesBilled < 0 {
		return s, errors.New("the bytes cap is a positive size, such as 10GiB")
	}

	return s, nil
}

// dataProject is the project of the first configured dataset: where a dataset named alone is.
func (s Settings) dataProject() string {
	if len(s.Datasets) == 0 {
		return s.BillingProject
	}

	project, _, _ := strings.Cut(s.Datasets[0], ".")

	return project
}

// DatasetRef names a dataset.
type DatasetRef struct {
	Project string `json:"project"`
	Dataset string `json:"dataset"`
}

func (d DatasetRef) String() string { return d.Project + "." + d.Dataset }

// TableRef names a table.
type TableRef struct {
	Project string `json:"project"`
	Dataset string `json:"dataset"`
	Table   string `json:"table"`
}

func (t TableRef) String() string { return t.Project + "." + t.Dataset + "." + t.Table }

// parseDataset reads project.dataset, or a dataset alone in the default project.
func parseDataset(s, defaultProject string) (DatasetRef, error) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(s), "`"), ".")

	var ref DatasetRef

	switch {
	case len(parts) == 2:
		ref = DatasetRef{Project: parts[0], Dataset: parts[1]}
	case len(parts) == 1 && defaultProject != "":
		ref = DatasetRef{Project: defaultProject, Dataset: parts[0]}
	default:
		return ref, fmt.Errorf("%w: a dataset is project.dataset, such as my-project.analytics_123", ErrInvalid)
	}

	if !projectPattern.MatchString(ref.Project) || !datasetPattern.MatchString(ref.Dataset) {
		return ref, fmt.Errorf("%w: %q is not project.dataset", ErrInvalid, s)
	}

	return ref, nil
}

// parseTable reads project.dataset.table, or dataset.table in the default project.
func parseTable(s, defaultProject string) (TableRef, error) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(s), "`"), ".")
	if len(parts) == 2 && defaultProject != "" {
		parts = append([]string{defaultProject}, parts...)
	}

	if len(parts) != 3 || !projectPattern.MatchString(parts[0]) || !datasetPattern.MatchString(parts[1]) ||
		!tablePattern.MatchString(parts[2]) {
		return TableRef{}, fmt.Errorf("%w: a table is project.dataset.table, such as "+
			"my-project.analytics_123.events_20261119", ErrInvalid)
	}

	return TableRef{Project: parts[0], Dataset: parts[1], Table: parts[2]}, nil
}

// ErrInvalid marks what the caller asked wrong: a name that is not one, a size out of bounds.
var ErrInvalid = errors.New("invalid")

// Client is one Connection's way to BigQuery.
type Client struct {
	settings Settings
	sdk      *bq.Client
	rest     *bqrest.Service
	logger   *slog.Logger
	// connection names the Connection in the log.
	connection string
	newID      func() string
}

// Config is what a Client is made of.
type Config struct {
	Settings Settings
	// Connection names the Connection in the log: bigquery:ga4.
	Connection string
	Logger     *slog.Logger
	// Options reach Google: the authenticated HTTP client, or a test's endpoint.
	Options []option.ClientOption
}

// New returns a Client. It sends nothing.
func New(ctx context.Context, cfg Config) (*Client, error) {
	sdk, err := bq.NewClient(ctx, cfg.Settings.BillingProject, cfg.Options...)
	if err != nil {
		return nil, fmt.Errorf("making the BigQuery client: %w", err)
	}

	rest, err := bqrest.NewService(ctx, cfg.Options...)
	if err != nil {
		_ = sdk.Close()

		return nil, fmt.Errorf("making the BigQuery client: %w", err)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &Client{
		settings: cfg.Settings, sdk: sdk, rest: rest, logger: logger, connection: cfg.Connection, newID: jobID,
	}, nil
}

// Close ends the client.
func (c *Client) Close() error { return c.sdk.Close() }

// jobID is a fresh job id: hyp- and a random UUID.
func jobID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms.
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80

	h := hex.EncodeToString(b[:])

	return "hyp-" + h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Datasets lists the datasets of the data projects: those of the configured datasets.
func (c *Client) Datasets(ctx context.Context) ([]DatasetRef, error) {
	var projects []string

	for _, d := range c.settings.Datasets {
		project, _, _ := strings.Cut(d, ".")
		if !slices.Contains(projects, project) {
			projects = append(projects, project)
		}
	}

	out := []DatasetRef{}

	for _, p := range projects {
		it := c.sdk.DatasetsInProject(ctx, p)

		for len(out) < maxDatasets*len(projects) {
			d, err := it.Next()
			if errors.Is(err, iterator.Done) {
				break
			}

			if err != nil {
				return nil, googleError("listing the datasets of "+p, err)
			}

			out = append(out, DatasetRef{Project: d.ProjectID, Dataset: d.DatasetID})
		}
	}

	return out, nil
}

// TableSummary is one table of a dataset, as tables.list gives it.
type TableSummary struct {
	Table string `json:"table"`
	// Type is TABLE, VIEW, MATERIALIZED_VIEW, EXTERNAL or SNAPSHOT.
	Type string `json:"type"`
}

// Tables lists the tables of a dataset, project.dataset, or a dataset of the first configured one's project. The
// SDK's list has no table's type, so it is REST's tables.list.
func (c *Client) Tables(ctx context.Context, dataset string) ([]TableSummary, bool, error) {
	ref, err := parseDataset(dataset, c.settings.dataProject())
	if err != nil {
		return nil, false, err
	}

	return c.listTables(ctx, ref)
}

func (c *Client) listTables(ctx context.Context, ref DatasetRef) ([]TableSummary, bool, error) {
	out := []TableSummary{}
	page := ""

	for {
		call := c.rest.Tables.List(ref.Project, ref.Dataset).MaxResults(1000).Context(ctx)
		if page != "" {
			call = call.PageToken(page)
		}

		list, err := call.Do()
		if err != nil {
			return nil, false, googleError("listing the tables of "+ref.String(), err)
		}

		for _, t := range list.Tables {
			if len(out) == maxTables {
				return out, true, nil
			}

			name := ""
			if t.TableReference != nil {
				name = t.TableReference.TableId
			}

			out = append(out, TableSummary{Table: name, Type: t.Type})
		}

		if page = list.NextPageToken; page == "" {
			return out, false, nil
		}
	}
}

// Field is one column of a table's schema.
type Field struct {
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Mode        string  `json:"mode,omitempty"`
	Description string  `json:"description,omitempty"`
	Fields      []Field `json:"fields,omitempty"`
}

// TableInfo is a table's metadata and schema.
type TableInfo struct {
	TableRef

	Type         string    `json:"type"`
	Description  string    `json:"description,omitempty"`
	Rows         uint64    `json:"rows"`
	Bytes        int64     `json:"bytes"`
	Created      time.Time `json:"created"`
	Modified     time.Time `json:"modified"`
	Partitioning string    `json:"partitioning,omitempty"`
	Schema       []Field   `json:"schema"`
}

// Table reads a table's metadata and schema: project.dataset.table, or dataset.table of the first configured
// dataset's project.
func (c *Client) Table(ctx context.Context, table string) (TableInfo, error) {
	ref, err := parseTable(table, c.settings.dataProject())
	if err != nil {
		return TableInfo{}, err
	}

	return c.table(ctx, ref)
}

func (c *Client) table(ctx context.Context, ref TableRef) (TableInfo, error) {
	md, err := c.sdk.DatasetInProject(ref.Project, ref.Dataset).Table(ref.Table).Metadata(ctx)
	if err != nil {
		return TableInfo{}, googleError("reading table "+ref.String(), err)
	}

	out := TableInfo{
		TableRef: ref, Type: string(md.Type), Description: md.Description, Rows: md.NumRows, Bytes: md.NumBytes,
		Created: md.CreationTime.UTC(), Modified: md.LastModifiedTime.UTC(), Schema: fields(md.Schema),
	}

	if p := md.TimePartitioning; p != nil {
		out.Partitioning = string(p.Type)
		if p.Field != "" {
			out.Partitioning += " on " + p.Field
		}
	}

	return out, nil
}

func fields(s bq.Schema) []Field {
	out := make([]Field, 0, len(s))

	for _, f := range s {
		mode := ""

		switch {
		case f.Repeated:
			mode = "REPEATED"
		case f.Required:
			mode = "REQUIRED"
		}

		field := Field{Name: f.Name, Type: string(f.Type), Mode: mode, Description: f.Description}
		if len(f.Schema) > 0 {
			field.Fields = fields(f.Schema)
		}

		out = append(out, field)
	}

	return out
}

// Request is one query an agent or the owner asks for.
type Request struct {
	SQL string
	// DryRun answers with the dry run alone: what the query would process.
	DryRun bool
	// MaxRows bounds the rows answered; zero is DefaultMaxRows.
	MaxRows int
	// Timeout bounds the job; zero is DefaultTimeout, at most MaxTimeout.
	Timeout time.Duration
	// Agent and Tool label the job and its log line.
	Agent, Tool string
}

// Result is what a query gave.
type Result struct {
	// Project, JobID and Location name the job; empty for a dry run.
	Project  string `json:"project,omitempty"`
	JobID    string `json:"job_id,omitempty"`
	Location string `json:"location,omitempty"`
	DryRun   bool   `json:"dry_run"`
	// StatementType is the dry run's, checked again on the job when it ended.
	StatementType string `json:"statement_type"`
	// BytesProcessed is what the dry run estimated, or what the job processed; BytesBilled what it billed.
	BytesProcessed int64 `json:"bytes_processed"`
	BytesBilled    int64 `json:"bytes_billed"`
	// MaxBytesBilled is the cap the dry run and the job ran under.
	MaxBytesBilled int64      `json:"maximum_bytes_billed"`
	CacheHit       bool       `json:"cache_hit,omitempty"`
	Columns        []string   `json:"columns"`
	Rows           [][]any    `json:"rows"`
	TotalRows      uint64     `json:"total_rows"`
	Truncated      bool       `json:"truncated"`
	ReferencedTabs []TableRef `json:"referenced_tables,omitempty"`
	// ComputedAt is when the job ended: the figures are as of then.
	ComputedAt time.Time `json:"computed_at"`
}

// CancelError is a job stopped at its limit, or whose answer was lost: its cancel was requested, which BigQuery
// may not honour before the job ends and bills up to the cap.
type CancelError struct {
	Project, JobID, Location string
	// Never says BigQuery answered the cancel that it has no such job: it was never made.
	Never bool
	Cause string
}

func (e *CancelError) Error() string {
	if e.Never {
		return fmt.Sprintf("the query did not start (%s): BigQuery has no job %s", e.Cause, e.JobID)
	}

	return fmt.Sprintf("the query was stopped (%s): cancel requested for job %s:%s.%s; it may still finish and be "+
		"billed up to the cap", e.Cause, e.Project, e.Location, e.JobID)
}

// Query checks a query and runs it: the lexical gate, a dry run, then the job. A refusal sends nothing further.
func (c *Client) Query(ctx context.Context, r Request) (Result, error) {
	switch {
	case r.MaxRows == 0:
		r.MaxRows = DefaultMaxRows
	case r.MaxRows < 0 || r.MaxRows > MaxRows:
		return Result{}, fmt.Errorf("%w: max_rows is 1 to %d", ErrInvalid, MaxRows)
	}

	switch {
	case r.Timeout == 0:
		r.Timeout = DefaultTimeout
	case r.Timeout < 0 || r.Timeout > MaxTimeout:
		return Result{}, fmt.Errorf("%w: a query's time is at most %s", ErrInvalid, MaxTimeout)
	}

	if err := Gate(r.SQL); err != nil {
		return Result{}, err
	}

	dry, err := c.dryRun(ctx, r.SQL)
	if err != nil {
		return Result{}, err
	}

	stats := dry.Statistics.Query
	out := Result{
		DryRun: r.DryRun, StatementType: stats.StatementType, BytesProcessed: stats.TotalBytesProcessed,
		MaxBytesBilled: c.settings.MaxBytesBilled, Columns: []string{}, Rows: [][]any{},
	}

	for _, t := range stats.ReferencedTables {
		out.ReferencedTabs = append(out.ReferencedTabs, TableRef{Project: t.ProjectId, Dataset: t.DatasetId, Table: t.TableId})
	}

	if r.DryRun {
		return out, nil
	}

	location := c.settings.Location
	if location == "" && dry.JobReference != nil {
		location = dry.JobReference.Location
	}

	return c.run(ctx, r, out, location)
}

// dryRun asks BigQuery what a query is, in the billing project, under the cap: REST's jobs.insert, since the SDK's
// statistics have no referenced routines (Toolbox's DryRunQuery, bigquerycommon/util.go:76-117).
func (c *Client) dryRun(ctx context.Context, sql string) (*bqrest.Job, error) {
	ctx, cancel := context.WithTimeout(ctx, DryRunTimeout)
	defer cancel()

	legacy := false
	job := &bqrest.Job{
		JobReference: &bqrest.JobReference{ProjectId: c.settings.BillingProject, Location: c.settings.Location},
		Configuration: &bqrest.JobConfiguration{
			DryRun: true,
			Query: &bqrest.JobConfigurationQuery{
				Query: sql, UseLegacySql: &legacy, MaximumBytesBilled: c.settings.MaxBytesBilled,
				ForceSendFields: []string{"UseLegacySql"},
			},
		},
	}

	got, err := c.rest.Jobs.Insert(c.settings.BillingProject, job).Context(ctx).Do()
	if err != nil {
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusBadRequest {
			return nil, refusedf("BigQuery refused the query in its dry run: %s", clip(apiErr.Message))
		}

		return nil, googleError("the dry run", err)
	}

	if got.Statistics == nil || got.Statistics.Query == nil {
		return nil, refusedf("the dry run gave no statistics, so what the query is cannot be told")
	}

	stats := got.Statistics.Query

	switch {
	case stats.StatementType != "SELECT":
		return nil, refusedf("the query is a %s, not a SELECT", stats.StatementType)
	case len(stats.ReferencedRoutines) > 0:
		return nil, refusedf("the query calls a function or a procedure (%s); only built-in functions are allowed",
			routineName(stats.ReferencedRoutines[0]))
	case stats.DdlTargetRoutine != nil || stats.DdlTargetTable != nil || stats.ExportDataStatistics != nil:
		return nil, refusedf("the query makes or exports something; a query only reads")
	case stats.TotalBytesProcessed > c.settings.MaxBytesBilled:
		return nil, refusedf("the query would process %s, over the cap of %s; read fewer columns or a narrower "+
			"_TABLE_SUFFIX range", Size(stats.TotalBytesProcessed), Size(c.settings.MaxBytesBilled))
	}

	return got, nil
}

func routineName(r *bqrest.RoutineReference) string {
	if r == nil {
		return "a routine"
	}

	return r.ProjectId + "." + r.DatasetId + "." + r.RoutineId
}

// run runs the job the dry run allowed. Its project, id and location are kept, and logged, before it is sent, so a
// job whose answer is lost can still be cancelled.
func (c *Client) run(ctx context.Context, r Request, out Result, location string) (Result, error) {
	out.Project, out.JobID, out.Location = c.settings.BillingProject, c.newID(), location
	digest := sha256.Sum256([]byte(r.SQL))

	c.logger.InfoContext(ctx, "a BigQuery job is sent", slog.String("event", "bigquery.job"),
		slog.String("connection", c.connection), slog.String("agent", r.Agent), slog.String("tool", r.Tool),
		slog.String("project", out.Project), slog.String("job", out.JobID), slog.String("location", out.Location),
		slog.String("sql_sha256", hex.EncodeToString(digest[:8])))

	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	q := c.sdk.Query(r.SQL)
	q.JobID, q.Location, q.ProjectID = out.JobID, out.Location, out.Project
	q.MaxBytesBilled = c.settings.MaxBytesBilled
	q.Labels = map[string]string{"hypomnema-agent": label(r.Agent), "hypomnema-tool": label(r.Tool)}

	job, err := q.Run(ctx)
	if err != nil {
		// The job may have been made though its answer did not come back.
		return out, c.cancelJob(ctx, out, "its submission got no answer: "+googleCause(err))
	}

	status, err := job.Wait(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return out, c.cancelJob(ctx, out, "it ran longer than "+r.Timeout.String())
		}

		return out, googleError("waiting for job "+out.JobID, err)
	}

	if err := status.Err(); err != nil {
		return out, googleError("job "+out.JobID, err)
	}

	stats, _ := status.Statistics.Details.(*bq.QueryStatistics)
	if stats == nil || stats.StatementType != "SELECT" {
		return out, fmt.Errorf("%w: job %s ended as no SELECT; its rows are not read", ErrRefused, out.JobID)
	}

	out.BytesProcessed, out.BytesBilled, out.CacheHit = stats.TotalBytesProcessed, stats.TotalBytesBilled, stats.CacheHit
	out.ComputedAt = status.Statistics.EndTime.UTC()

	if err := c.readRows(ctx, job, r.MaxRows, &out); err != nil {
		if ctx.Err() != nil {
			return out, fmt.Errorf("the job %s ended but its rows were not read in %s", out.JobID, r.Timeout)
		}

		return out, err
	}

	c.logger.InfoContext(ctx, "a BigQuery job ended", slog.String("event", "bigquery.query"),
		slog.String("connection", c.connection), slog.String("agent", r.Agent), slog.String("tool", r.Tool),
		slog.String("project", out.Project), slog.String("job", out.JobID), slog.String("location", out.Location),
		slog.String("statement", out.StatementType), slog.Int64("bytes_processed", out.BytesProcessed),
		slog.Int64("bytes_billed", out.BytesBilled), slog.Int("rows", len(out.Rows)),
		slog.Uint64("total_rows", out.TotalRows), slog.String("sql_sha256", hex.EncodeToString(digest[:8])))

	return out, nil
}

func (c *Client) readRows(ctx context.Context, job *bq.Job, maxRows int, out *Result) error {
	it, err := job.Read(ctx)
	if err != nil {
		return googleError("reading the rows of job "+out.JobID, err)
	}

	it.PageInfo().MaxSize = min(maxRows, 1000)

	for len(out.Rows) < maxRows {
		var row []bq.Value

		err := it.Next(&row)
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return googleError("reading the rows of job "+out.JobID, err)
		}

		values := make([]any, len(row))
		for i, v := range row {
			var f *bq.FieldSchema
			if i < len(it.Schema) {
				f = it.Schema[i]
			}

			values[i] = normalize(v, f)
		}

		out.Rows = append(out.Rows, values)
	}

	for _, f := range it.Schema {
		out.Columns = append(out.Columns, f.Name)
	}

	out.TotalRows = it.TotalRows
	out.Truncated = it.TotalRows > uint64(len(out.Rows))

	return nil
}

// cancelJob asks BigQuery to cancel a job, from the project, id and location kept before it was sent, on a
// context of its own: the caller's has ended.
func (c *Client) cancelJob(ctx context.Context, out Result, why string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()

	call := c.rest.Jobs.Cancel(out.Project, out.JobID).Context(ctx)
	if out.Location != "" {
		call = call.Location(out.Location)
	}

	_, err := call.Do()

	var apiErr *googleapi.Error

	never := errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound

	c.logger.WarnContext(ctx, "a BigQuery job's cancel was requested", slog.String("event", "bigquery.cancel"),
		slog.String("connection", c.connection), slog.String("project", out.Project), slog.String("job", out.JobID),
		slog.String("location", out.Location), slog.String("why", why), slog.Bool("never_made", never),
		slog.Bool("cancel_failed", err != nil && !never))

	return &CancelError{Project: out.Project, JobID: out.JobID, Location: out.Location, Never: never, Cause: why}
}

// normalize makes a value JSON can carry as it reads: NUMERIC as its decimal text, a date or a time as its text, a
// record as an object of its fields.
func normalize(v bq.Value, f *bq.FieldSchema) any {
	if v == nil {
		return nil
	}

	if f != nil && f.Repeated {
		list, ok := v.([]bq.Value)
		if !ok {
			return fmt.Sprint(v)
		}

		one := *f
		one.Repeated = false

		out := make([]any, len(list))
		for i, x := range list {
			out[i] = normalize(x, &one)
		}

		return out
	}

	if f != nil && f.Type == bq.RecordFieldType {
		list, ok := v.([]bq.Value)
		if !ok {
			return fmt.Sprint(v)
		}

		out := make(map[string]any, len(list))
		for i, x := range list {
			if i < len(f.Schema) {
				out[f.Schema[i].Name] = normalize(x, f.Schema[i])
			}
		}

		return out
	}

	switch x := v.(type) {
	case *big.Rat:
		text := x.FloatString(38)
		if strings.Contains(text, ".") {
			text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
		}

		return text
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case string, int64, float64, bool, []byte:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return x
	}
}

// label is a job label's value: lower-case letters, digits, dashes and underscores, at most 63.
func label(s string) string {
	s = strings.Trim(labelInvalid.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 63 {
		s = s[:63]
	}

	if s == "" {
		return "unknown"
	}

	return s
}

// clip shortens what Google said to a line or two.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		return s[:400] + "…"
	}

	return s
}

// googleCause is what Google said, without the request, a header or a token: its status, its reason and its
// message, or the token exchange's error code.
func googleCause(err error) string {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		reason := ""
		if len(apiErr.Errors) > 0 {
			reason = " " + apiErr.Errors[0].Reason
		}

		return fmt.Sprintf("BigQuery answered %d%s: %s", apiErr.Code, reason, clip(apiErr.Message))
	}

	var tokenErr *oauth2.RetrieveError
	if errors.As(err, &tokenErr) {
		code := tokenErr.ErrorCode
		if code == "" && tokenErr.Response != nil {
			code = tokenErr.Response.Status
		}

		return "Google refused the service account's token: " + clip(code)
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "it took too long"
	case errors.Is(err, context.Canceled):
		return "it was cancelled"
	}

	// Only the innermost error: the outer ones of net/http repeat the address.
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return clip(err.Error())
		}

		err = next
	}
}

// AccessError is what BigQuery refused for the account: its IAM.
type AccessError struct{ Message string }

func (e *AccessError) Error() string { return e.Message }

// TokenError is a token Google would not give for the key: the Connection is connected again.
type TokenError struct{ Message string }

func (e *TokenError) Error() string { return e.Message }

// googleError is an error of doing something, with what Google said, typed so the APIs answer it.
func googleError(doing string, err error) error {
	cause := googleCause(err)

	var tokenErr *oauth2.RetrieveError
	if errors.As(err, &tokenErr) {
		return &TokenError{Message: doing + ": " + cause}
	}

	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case http.StatusForbidden, http.StatusUnauthorized:
			return &AccessError{Message: doing + ": " + cause + ". What the account may read is its IAM roles"}
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s: %s", ErrNotFound, doing, cause)
		case http.StatusBadRequest:
			return fmt.Errorf("%w: %s: %s", ErrInvalid, doing, cause)
		}
	}

	return fmt.Errorf("%s: %s", doing, cause)
}

// ErrNotFound marks a dataset, a table or a job BigQuery does not have.
var ErrNotFound = errors.New("not found")

// Size writes a number of bytes as the owner reads it: 10GiB.
func Size(n int64) string {
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

// ParseSize reads a size as the owner writes it: 10GiB, 500MB, 1TiB, or a number of bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)

	units := []struct {
		suffix string
		mult   int64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1},
	}

	mult := int64(1)

	for _, u := range units {
		if rest, ok := strings.CutSuffix(s, u.suffix); ok {
			s, mult = strings.TrimSpace(rest), u.mult

			break
		}
	}

	n, ok := new(big.Rat).SetString(s)
	if !ok || n.Sign() <= 0 {
		return 0, fmt.Errorf("%w: a size is a positive number with a unit, such as 10GiB", ErrInvalid)
	}

	n.Mul(n, new(big.Rat).SetInt64(mult))

	if !n.IsInt() && n.Cmp(big.NewRat(1, 1)) < 0 {
		return 0, fmt.Errorf("%w: a size is at least one byte", ErrInvalid)
	}

	bytes := new(big.Int).Quo(n.Num(), n.Denom())
	if !bytes.IsInt64() {
		return 0, fmt.Errorf("%w: the size is too large", ErrInvalid)
	}

	return bytes.Int64(), nil
}

// CheckError is a connect check that failed at a step; nothing was stored.
type CheckError struct {
	Step  string
	Cause error
}

func (e *CheckError) Error() string { return "the check failed at " + e.Step + ": " + e.Cause.Error() }

func (e *CheckError) Unwrap() error { return e.Cause }

// eventsTable is a GA4 export's daily table.
var eventsTable = regexp.MustCompile(`^events_\d{8}$`)

// Check checks what a Connection can do, step by step, each free (design v3, decision 16): each configured
// dataset's tables are listed and one holding EXTERNAL tables is refused; a native table, the newest
// events_YYYYMMDD when there is one, is read once (its metadata and one row, which is dropped); and a dry run of a
// SELECT on it runs in the billing project. The token's exchange is the caller's first step.
func (c *Client) Check(ctx context.Context) error {
	for _, name := range c.settings.Datasets {
		ref, err := parseDataset(name, "")
		if err != nil {
			return &CheckError{Step: "reading the settings", Cause: err}
		}

		tables, _, err := c.listTables(ctx, ref)
		if err != nil {
			return &CheckError{Step: "listing the tables of " + ref.String(), Cause: err}
		}

		var external, native []string

		for _, t := range tables {
			switch t.Type {
			case "EXTERNAL":
				external = append(external, t.Table)
			case "TABLE":
				native = append(native, t.Table)
			}
		}

		if len(external) > 0 {
			return &CheckError{Step: "listing the tables of " + ref.String(), Cause: fmt.Errorf(
				"%w: it holds external tables (%s), whose reads the bytes cap does not cover; give the account a "+
					"dataset of native tables only", ErrRefused, strings.Join(external[:min(3, len(external))], ", "))}
		}

		pick := pickNative(native)
		if pick == "" {
			return &CheckError{Step: "listing the tables of " + ref.String(), Cause: fmt.Errorf(
				"%w: it holds no native table; a GA4 export holds events_YYYYMMDD tables", ErrRefused)}
		}

		table := TableRef{Project: ref.Project, Dataset: ref.Dataset, Table: pick}

		if _, err := c.table(ctx, table); err != nil {
			return &CheckError{Step: "reading table " + table.String(), Cause: err}
		}

		if err := c.readOneRow(ctx, table); err != nil {
			return &CheckError{Step: "reading a row of " + table.String(), Cause: err}
		}

		if _, err := c.dryRun(ctx, "SELECT 1 FROM `"+table.String()+"` LIMIT 1"); err != nil {
			return &CheckError{Step: "a dry run in project " + c.settings.BillingProject, Cause: err}
		}
	}

	return nil
}

// pickNative is the newest events_YYYYMMDD of the native tables, or the first native table when there is none.
func pickNative(native []string) string {
	var events []string

	for _, t := range native {
		if eventsTable.MatchString(t) {
			events = append(events, t)
		}
	}

	if len(events) > 0 {
		sort.Strings(events)

		return events[len(events)-1]
	}

	if len(native) > 0 {
		return native[0]
	}

	return ""
}

// readOneRow reads one row of a table, tabledata.list, which a view refuses; the row is dropped, never logged.
func (c *Client) readOneRow(ctx context.Context, ref TableRef) error {
	it := c.sdk.DatasetInProject(ref.Project, ref.Dataset).Table(ref.Table).Read(ctx)
	it.PageInfo().MaxSize = 1

	var row []bq.Value

	err := it.Next(&row)
	if err != nil && !errors.Is(err, iterator.Done) {
		return googleError("reading a row", err)
	}

	return nil
}
