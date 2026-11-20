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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	bq "cloud.google.com/go/bigquery"
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

	// The caller's list is left as it was given.
	s.Datasets = slices.Clone(s.Datasets)

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

	project := s.Datasets[0][:max(strings.LastIndex(s.Datasets[0], "."), 0)]

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

// splitRight splits a dotted name into at most n parts from the right, so that a domain-scoped project id
// (example.com:my-project) stays one part.
func splitRight(s string, n int) []string {
	var parts []string

	for len(parts) < n-1 {
		i := strings.LastIndex(s, ".")
		if i < 0 {
			break
		}

		parts = append([]string{s[i+1:]}, parts...)
		s = s[:i]
	}

	return append([]string{s}, parts...)
}

// parseDataset reads project.dataset, or a dataset alone in the default project.
func parseDataset(s, defaultProject string) (DatasetRef, error) {
	parts := splitRight(strings.Trim(strings.TrimSpace(s), "`"), 2)

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
	parts := splitRight(strings.Trim(strings.TrimSpace(s), "`"), 3)
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
	// The SDK's own logger is silenced: at GOOGLE_SDK_GO_LOGGING_LEVEL=debug it writes each request and answer,
	// SQL and rows with them, and the log holds only the job lines below (design v3, decision 27).
	options := append(slices.Clone(cfg.Options), option.WithLogger(slog.New(slog.DiscardHandler)))

	sdk, err := bq.NewClient(ctx, cfg.Settings.BillingProject, options...)
	if err != nil {
		return nil, fmt.Errorf("making the BigQuery client: %w", err)
	}

	rest, err := bqrest.NewService(ctx, options...)
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
		project := d[:max(strings.LastIndex(d, "."), 0)]
		if !slices.Contains(projects, project) {
			projects = append(projects, project)
		}
	}

	out := []DatasetRef{}

	for _, p := range projects {
		it := c.sdk.Datasets(ctx)
		it.ProjectID = p

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

// Tables lists the tables of a dataset, project.dataset, or a dataset of the first configured one's project, or
// that first one when none is named. The SDK's list has no table's type, so it is REST's tables.list.
func (c *Client) Tables(ctx context.Context, dataset string) ([]TableSummary, bool, error) {
	if strings.TrimSpace(dataset) == "" && len(c.settings.Datasets) > 0 {
		dataset = c.settings.Datasets[0]
	}

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
	// MaxBytes bounds the rows read into memory, as JSON; zero is MaxAnswerBytes. Rows past it are not fetched.
	MaxBytes int
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
// may not honor before the job ends and bills up to the cap.
type CancelError struct {
	Project, JobID, Location string
	// Never says BigQuery answered the cancel, in the job's own location, that it has no such job, for a job whose
	// submission got no answer: it was never made. A 404 proves nothing when the cancel named no location, nor
	// once BigQuery confirmed the submission: the job exists, and its state is not known (review round 3).
	Never bool
	// Unknown says the cancel itself failed, or found no job BigQuery had confirmed: whether the job runs is not
	// known.
	Unknown bool
	Cause   string
}

func (e *CancelError) Error() string {
	if e.Never {
		return fmt.Sprintf("the query did not start (%s): BigQuery has no job %s", e.Cause, e.JobID)
	}

	if e.Location == "" {
		return fmt.Sprintf("the query was stopped (%s): a cancel was sent for job %s:%s, whose location is not known, "+
			"so whether it runs is not known; it may still run and be billed up to the cap", e.Cause, e.Project, e.JobID)
	}

	if e.Unknown {
		return fmt.Sprintf("the query was stopped (%s): the cancel of job %s:%s.%s did not go through, so whether "+
			"it runs is not known; it may still run and be billed up to the cap", e.Cause, e.Project, e.Location,
			e.JobID)
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

	if r.MaxBytes <= 0 {
		r.MaxBytes = MaxAnswerBytes
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
		out.ReferencedTabs = append(out.ReferencedTabs,
			TableRef{Project: t.ProjectId, Dataset: t.DatasetId, Table: t.TableId})
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
// job whose answer is lost can still be canceled.
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
		// No token, no request: nothing was sent, so nothing is canceled, and the Connection's key is judged.
		var tokenErr *TokenError
		if errors.As(err, &tokenErr) {
			return out, googleError("submitting job "+out.JobID, err)
		}

		// The job may have been made though its answer did not come back.
		return out, c.cancelJob(ctx, out, false, "its submission got no answer: "+googleCause(err))
	}

	status, err := job.Wait(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return out, c.cancelJob(ctx, out, true, "it ran longer than "+r.Timeout.String())
		}

		// Watching failed before the limit: the job's state is read once more, and a job not known to have
		// ended is canceled, so it does not run on unwatched.
		if status, err = c.settle(ctx, job, out, err); err != nil {
			return out, err
		}
	}

	if err := status.Err(); err != nil {
		return out, googleError("job "+out.JobID, err)
	}

	stats, _ := status.Statistics.Details.(*bq.QueryStatistics)
	if stats == nil || stats.StatementType != "SELECT" {
		// It ran, so it is no refusal before running: it may have been billed.
		return out, fmt.Errorf("job %s ran but ended as no SELECT; its rows are not read, and it may have been billed "+
			"up to the cap", out.JobID)
	}

	out.BytesProcessed, out.BytesBilled, out.CacheHit = stats.TotalBytesProcessed, stats.TotalBytesBilled, stats.CacheHit
	out.ComputedAt = status.Statistics.EndTime.UTC()

	if err := c.readRows(ctx, job, r.MaxRows, r.MaxBytes, &out); err != nil {
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

// settle reads a job's state after watching it failed: its status when it ended, or, when it did not or cannot be
// told, a cancel from what was kept, with the watching error in the answer.
func (c *Client) settle(ctx context.Context, job *bq.Job, out Result, watching error) (*bq.JobStatus, error) {
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()

	status, err := job.Status(checkCtx)
	if err == nil && status.Done() {
		return status, nil
	}

	return nil, c.cancelJob(ctx, out, true, "watching it failed: "+googleCause(watching))
}

// rowPage is the rows asked for at once: a page past the byte budget is not fetched.
const rowPage = 100

func (c *Client) readRows(ctx context.Context, job *bq.Job, maxRows, maxBytes int, out *Result) error {
	it, err := job.Read(ctx)
	if err != nil {
		return googleError("reading the rows of job "+out.JobID, err)
	}

	it.PageInfo().MaxSize = min(maxRows, rowPage)
	used := 0

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

		// The rows kept stay within the answer's bytes: the row that would pass it, and the pages after it, are
		// neither kept nor fetched.
		raw, err := json.Marshal(values)
		if err != nil || used+len(raw) > maxBytes {
			break
		}

		used += len(raw)
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
// context of its own: the caller's has ended. submitted says BigQuery confirmed the job's submission.
func (c *Client) cancelJob(ctx context.Context, out Result, submitted bool, why string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()

	call := c.rest.Jobs.Cancel(out.Project, out.JobID).Context(ctx)
	if out.Location != "" {
		call = call.Location(out.Location)
	}

	_, err := call.Do()

	var apiErr *googleapi.Error

	// A 404 proves the job was never made only when the cancel named the job's location and its submission was
	// never confirmed; any other failure of the cancel leaves its state unknown.
	never := !submitted && errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound && out.Location != ""
	unknown := err != nil && !never

	c.logger.WarnContext(ctx, "a BigQuery job's cancel was requested", slog.String("event", "bigquery.cancel"),
		slog.String("connection", c.connection), slog.String("project", out.Project), slog.String("job", out.JobID),
		slog.String("location", out.Location), slog.String("why", why), slog.Bool("never_made", never),
		slog.Bool("cancel_failed", unknown))

	return &CancelError{
		Project: out.Project, JobID: out.JobID, Location: out.Location, Never: never, Unknown: unknown, Cause: why,
	}
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
	case float64:
		// JSON has no NaN or infinity: they are written as BigQuery's own JSON writes them.
		switch {
		case math.IsNaN(x):
			return "NaN"
		case math.IsInf(x, 1):
			return "Infinity"
		case math.IsInf(x, -1):
			return "-Infinity"
		}

		return x
	case string, int64, bool, []byte:
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

// secretPatterns are what may stand for a credential in a text: a PEM block, a key file's private_key, an access
// token, a bearer header, a JWT, a token or an assertion in a form.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*-----.*?(-----END [A-Z ]*-----|$)`),
	regexp.MustCompile(`"private_key"\s*:\s*"(\\.|[^"\\])*"?`),
	regexp.MustCompile(`ya29\.[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`),
	regexp.MustCompile(`(?i)(access_token|refresh_token|id_token|assertion|client_secret)=[^&\s"]+`),
}

// Redact takes out of a text what may be a credential, before it is logged or answered.
func Redact(s string) string {
	for _, p := range secretPatterns {
		s = p.ReplaceAllString(s, "[redacted]")
	}

	return s
}

// clip shortens what Google said to a line or two, credentials taken out first.
func clip(s string) string {
	s = Redact(s)

	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 400 {
		return s[:400] + "…"
	}

	return s
}

// Cause is what Google said of a failed call or token exchange, with no request, header, key or token in it.
func Cause(err error) string { return googleCause(err) }

// googleCause is what Google said, without the request, a header or a token: its status, its reason and its
// message, or the token exchange's error code.
func googleCause(err error) string {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		reason := ""
		if len(apiErr.Errors) > 0 {
			reason = " " + knownReason(apiErr.Errors[0].Reason)
		}

		return fmt.Sprintf("BigQuery answered %d%s: %s", apiErr.Code, reason, clip(apiErr.Message))
	}

	// A job's own error: its reason as a known code, its message with any credential taken out.
	var jobErr *bq.Error
	if errors.As(err, &jobErr) {
		return fmt.Sprintf("the job failed, %s: %s", knownReason(jobErr.Reason), clip(jobErr.Message))
	}

	var tokenErr *TokenError
	if errors.As(err, &tokenErr) {
		return clip(tokenErr.Message)
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "it took too long"
	case errors.Is(err, context.Canceled):
		return "it was canceled"
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

// bigqueryReasons are the reasons of BigQuery's documented error table (Google, "Troubleshoot errors"). Only these
// are repeated; a reason is the service's free text otherwise, and is not (review round 2).
var bigqueryReasons = []string{
	"accessDenied", "backendError", "badRequest", "billingNotEnabled", "billingTierLimitExceeded", "blocked",
	"bytesBilledLimitExceeded", "duplicate", "internalError", "invalid", "invalidQuery", "invalidUser",
	"jobBackendError", "jobInternalError", "notFound", "notImplemented", "proxyAuthenticationRequired",
	"quotaExceeded", "rateLimitExceeded", "resourceInUse", "resourcesExceeded", "responseTooLarge", "stopped",
	"tableUnavailable", "timeout",
}

// knownReason is the reason when BigQuery documents it, and "error" otherwise.
func knownReason(reason string) string {
	if slices.Contains(bigqueryReasons, reason) {
		return reason
	}

	return "error"
}

// AccessError is what BigQuery refused for the account: its IAM.
type AccessError struct{ Message string }

func (e *AccessError) Error() string { return e.Message }

// TokenError is a token Google did not give for the key. Refused says Google refused the key itself
// (invalid_grant and the like): the Connection is connected again. Otherwise the exchange failed for a passing
// reason, a 503 or the network, and nothing is marked.
type TokenError struct {
	Message string
	Refused bool
}

func (e *TokenError) Error() string { return e.Message }

// googleError is an error of doing something, with what Google said, typed so the APIs answer it.
func googleError(doing string, err error) error {
	cause := googleCause(err)

	var tokenErr *TokenError
	if errors.As(err, &tokenErr) {
		return &TokenError{Message: doing + ": " + tokenErr.Message, Refused: tokenErr.Refused}
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

		tables, truncated, err := c.listTables(ctx, ref)
		if err != nil {
			return &CheckError{Step: "listing the tables of " + ref.String(), Cause: err}
		}

		// A list cut short may hide an external table: the check passes only on a whole list.
		if truncated {
			return &CheckError{Step: "listing the tables of " + ref.String(), Cause: fmt.Errorf(
				"%w: it holds more than %d tables, so not all of them can be checked", ErrRefused, maxTables)}
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

// MaxAnswerBytes bounds a query's answer to an agent, as a relayed answer in a read run is bounded
// (serviceproxy/relay.go): rows past it are dropped and the answer says it was cut.
const MaxAnswerBytes = 512 << 10

// Fit drops rows from the end until the result, as JSON, is at most limit bytes, and says so in Truncated.
func (r *Result) Fit(limit int) {
	size := func() int {
		raw, err := json.Marshal(r)
		if err != nil {
			// What cannot be written is never small enough.
			return math.MaxInt
		}

		return len(raw)
	}

	if size() <= limit {
		return
	}

	// Halving keeps the rows of a large answer cheap to cut; it may drop more than the least needed.
	for len(r.Rows) > 0 && size() > limit {
		r.Rows = r.Rows[:len(r.Rows)/2]
		r.Truncated = true
	}
}
