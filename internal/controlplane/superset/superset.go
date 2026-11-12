// Package superset reads a Superset dashboard's charts as data (issue #203):
// the dashboards by title, a dashboard with its charts and filters, and a
// chart's rows as its dashboard shows them. It holds no address and no
// token: every request it makes is relayed by the service proxy
// (serviceproxy.Relay), so each is an action of Superset's catalog with the
// owner's answer for the Connection, as a request an agent names itself is.
//
// What is read and in which order is learned from three clients of
// Superset's API: the dashboard's charts and filters from
// Winding2020/superset-mcp (src/client/dashboard-client.ts,
// getChartQueryContext; MIT per its package.json); the order of a chart's
// data, its saved query first and a query built from its settings after,
// from Preset's sup (src/sup/clients/superset.py, get_chart_data; Business
// Source License 1.1: its behavior is learned, none of its code is here);
// the query a kind of chart runs from Superset's own chart plugins. The
// requests and answers were tried against Superset 6.1.0 on 2026-11-12.
package superset

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Relay sends one request to the Connection's Superset through the service
// proxy's decision and returns its answer unchanged. The caller binds who
// asks and which Connection.
type Relay func(ctx context.Context, method, target string, body []byte) (status int, answer []byte, err error)

// maxDashboards bounds one listing of dashboards.
const maxDashboards = 100

// DashboardSummary is a dashboard in a listing.
type DashboardSummary struct {
	ID        int64
	Title     string
	Slug      string
	Published bool
	// ChangedAt is when it was last changed; zero when Superset did not say.
	ChangedAt time.Time
}

// Chart is a chart of a dashboard.
type Chart struct {
	ID   int64
	Name string
	// Kind is Superset's name for the kind of chart: table, pie.
	Kind string
	// DatasetID and Dataset are the dataset the chart reads; the name is
	// empty when the account may not list the dashboard's datasets.
	DatasetID int64
	Dataset   string
	// Read says a chart of this kind can be read as data.
	Read bool
}

// Filter is a filter of a dashboard, with the value it has when the
// dashboard opens.
type Filter struct {
	ID   string
	Name string
	// Kind is Superset's name for the kind of filter: filter_select,
	// filter_time.
	Kind string
	// Column and DatasetID are what it filters, when it names one.
	Column    string
	DatasetID int64
	// Default is the value it starts with, as JSON; empty when it has none.
	Default json.RawMessage
	// Charts are the charts it applies to.
	Charts []int64
}

// Dashboard is a dashboard with its charts and its filters.
type Dashboard struct {
	ID      int64
	Title   string
	Slug    string
	Charts  []Chart
	Filters []Filter
}

// The ways a chart's query was come by.
const (
	// SourceSaved: the query Superset saved with the chart.
	SourceSaved = "saved"
	// SourceBuilt: a query built from the chart's settings.
	SourceBuilt = "built"
)

// ChartData is a chart's rows as its dashboard shows them when it opens.
type ChartData struct {
	DashboardID int64
	ChartID     int64
	Name        string
	Kind        string
	// Source is how the query was come by: saved or built.
	Source  string
	Columns []string
	// Rows are the values of each row, in the order of Columns.
	Rows [][]any
	// SQL is the query Superset ran.
	SQL string
	// ComputedAt is when the figures were computed: when Superset cached
	// them, or the time of this call when they were not from its cache.
	ComputedAt time.Time
	// Cached says the figures came from Superset's cache.
	Cached bool
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// call makes one request and reads Superset's JSON answer into out. An
// answer that is not a 200 is an error with Superset's status and message.
func call(ctx context.Context, relay Relay, method, target string, body, out any) error {
	var sent []byte

	if body != nil {
		var err error
		if sent, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encoding the request to Superset: %w", err)
		}
	}

	status, answer, err := relay(ctx, method, target, sent)
	if err != nil {
		return err
	}

	if status != http.StatusOK {
		return answerError(status, answer)
	}

	decoder := json.NewDecoder(bytes.NewReader(answer))
	// A figure is kept as Superset wrote it: a large count is not rounded through a float.
	decoder.UseNumber()

	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("%w: Superset's answer to %s %s is not the JSON expected", orchestration.ErrUnavailable,
			method, pathOf(target))
	}

	return nil
}

func pathOf(target string) string {
	path, _, _ := strings.Cut(target, "?")

	return path
}

// answerError is Superset's refusal as an error: the status, and its message
// when it is a short one.
func answerError(status int, answer []byte) error {
	var said struct {
		Message json.RawMessage `json:"message"`
		Msg     string          `json:"msg"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	_ = json.Unmarshal(answer, &said)

	message := said.Msg

	var text string
	if json.Unmarshal(said.Message, &text) == nil && text != "" {
		message = text
	} else if len(said.Errors) > 0 && said.Errors[0].Message != "" {
		message = said.Errors[0].Message
	}

	if message = strings.Join(strings.Fields(message), " "); len(message) > 300 {
		message = message[:300] + "…"
	}

	kind := orchestration.ErrUnavailable

	switch status {
	case http.StatusNotFound:
		kind = orchestration.ErrNotFound
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = orchestration.ErrInvalid
	case http.StatusForbidden:
		kind = orchestration.ErrConflict
	}

	if message == "" {
		return fmt.Errorf("%w: Superset answered %d", kind, status)
	}

	return fmt.Errorf("%w: Superset answered %d: %s", kind, status, message)
}

// Dashboards lists the dashboards whose title contains search, or all when
// it is empty; at most a hundred. The question is asked as Superset's own
// list is (superset-mcp, listDashboards: the query as JSON in q).
func Dashboards(ctx context.Context, relay Relay, search string) ([]DashboardSummary, error) {
	query := map[string]any{
		"page": 0, "page_size": maxDashboards, "order_column": "dashboard_title", "order_direction": "asc",
	}
	if search = strings.TrimSpace(search); search != "" {
		query["filters"] = []map[string]any{{"col": "dashboard_title", "opr": "ct", "value": search}}
	}

	q, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("encoding the search: %w", err)
	}

	var answer struct {
		Result []struct {
			ID        int64  `json:"id"`
			Title     string `json:"dashboard_title"`
			Slug      string `json:"slug"`
			Published bool   `json:"published"`
			ChangedOn string `json:"changed_on_utc"`
		} `json:"result"`
	}

	target := "/api/v1/dashboard/?q=" + url.QueryEscape(string(q))
	if err := call(ctx, relay, http.MethodGet, target, nil, &answer); err != nil {
		return nil, err
	}

	out := make([]DashboardSummary, 0, len(answer.Result))

	for _, d := range answer.Result {
		// Superset 6.1.0 writes "2026-11-12T09:35:31.504757+0000".
		changed, _ := time.Parse("2006-01-02T15:04:05.999999-0700", d.ChangedOn)
		out = append(out, DashboardSummary{
			ID: d.ID, Title: d.Title, Slug: d.Slug, Published: d.Published, ChangedAt: changed,
		})
	}

	return out, nil
}

// dashboardAnswer is what Superset says of a dashboard.
type dashboardAnswer struct {
	Result struct {
		ID       int64  `json:"id"`
		Title    string `json:"dashboard_title"`
		Slug     string `json:"slug"`
		Metadata string `json:"json_metadata"`
	} `json:"result"`
}

// chartsAnswer is what Superset says of a dashboard's charts.
type chartsAnswer struct {
	Result []struct {
		ID       int64          `json:"id"`
		Name     string         `json:"slice_name"`
		FormData map[string]any `json:"form_data"`
	} `json:"result"`
}

// nativeFilter is a filter of a dashboard as its metadata has it.
type nativeFilter struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"filterType"`
	Type    string `json:"type"`
	Targets []struct {
		DatasetID int64 `json:"datasetId"`
		Column    struct {
			Name string `json:"name"`
		} `json:"column"`
	} `json:"targets"`
	Default struct {
		Extra map[string]any `json:"extraFormData"`
		State struct {
			Value json.RawMessage `json:"value"`
		} `json:"filterState"`
	} `json:"defaultDataMask"`
	ChartsInScope *[]int64 `json:"chartsInScope"`
	Scope         struct {
		Excluded []int64 `json:"excluded"`
	} `json:"scope"`
}

// applies says whether a filter applies to a chart: the chart is in its
// scope, or it names no scope and does not exclude the chart (superset-mcp,
// getChartQueryContext).
func (f nativeFilter) applies(chart int64) bool {
	if f.ChartsInScope != nil {
		for _, id := range *f.ChartsInScope {
			if id == chart {
				return true
			}
		}

		return false
	}

	for _, id := range f.Scope.Excluded {
		if id == chart {
			return false
		}
	}

	return true
}

// nativeFilters reads a dashboard's filters from its metadata. Metadata that
// does not read has none: an old dashboard has no such filters. A divider
// between filters is not one.
func nativeFilters(metadata string) []nativeFilter {
	var parsed struct {
		Filters []nativeFilter `json:"native_filter_configuration"`
	}

	if metadata == "" || json.Unmarshal([]byte(metadata), &parsed) != nil {
		return nil
	}

	out := parsed.Filters[:0]

	for _, f := range parsed.Filters {
		if f.Type != "DIVIDER" {
			out = append(out, f)
		}
	}

	return out
}

func checkID(what, id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%w: a %s is named by its number or its slug", orchestration.ErrInvalid, what)
	}

	return nil
}

// datasetOf reads the dataset a chart's settings name: "27__table".
func datasetOf(formData map[string]any) (id int64, kind string) {
	key, _ := formData["datasource"].(string)

	number, kind, ok := strings.Cut(key, "__")
	if !ok {
		return 0, ""
	}

	id, err := strconv.ParseInt(number, 10, 64)
	if err != nil {
		return 0, ""
	}

	return id, kind
}

// GetDashboard returns a dashboard, named by its number or its slug, with
// its charts and its filters.
func GetDashboard(ctx context.Context, relay Relay, id string) (Dashboard, error) {
	if err := checkID("dashboard", id); err != nil {
		return Dashboard{}, err
	}

	var (
		dashboard dashboardAnswer
		charts    chartsAnswer
	)

	if err := call(ctx, relay, http.MethodGet, "/api/v1/dashboard/"+id, nil, &dashboard); err != nil {
		return Dashboard{}, err
	}

	if err := call(ctx, relay, http.MethodGet, "/api/v1/dashboard/"+id+"/charts", nil, &charts); err != nil {
		return Dashboard{}, err
	}

	// The names of the datasets are a courtesy: an account that may not list them still gets the charts.
	var datasets struct {
		Result []struct {
			ID   int64  `json:"id"`
			Name string `json:"table_name"`
		} `json:"result"`
	}

	names := map[int64]string{}

	if err := call(ctx, relay, http.MethodGet, "/api/v1/dashboard/"+id+"/datasets", nil, &datasets); err == nil {
		for _, d := range datasets.Result {
			names[d.ID] = d.Name
		}
	}

	out := Dashboard{
		ID: dashboard.Result.ID, Title: dashboard.Result.Title, Slug: dashboard.Result.Slug,
		Charts: make([]Chart, 0, len(charts.Result)), Filters: []Filter{},
	}

	for _, c := range charts.Result {
		kind, _ := c.FormData["viz_type"].(string)
		dataset, _ := datasetOf(c.FormData)
		_, read := builders[kind]

		out.Charts = append(out.Charts, Chart{
			ID: c.ID, Name: c.Name, Kind: kind, DatasetID: dataset, Dataset: names[dataset], Read: read,
		})
	}

	for _, f := range nativeFilters(dashboard.Result.Metadata) {
		filter := Filter{ID: f.ID, Name: f.Name, Kind: f.Kind, Charts: []int64{}}

		if len(f.Targets) > 0 {
			filter.Column, filter.DatasetID = f.Targets[0].Column.Name, f.Targets[0].DatasetID
		}

		if value := f.Default.State.Value; len(value) > 0 && string(value) != "null" {
			filter.Default = value
		}

		for _, c := range out.Charts {
			if f.applies(c.ID) {
				filter.Charts = append(filter.Charts, c.ID)
			}
		}

		out.Filters = append(out.Filters, filter)
	}

	return out, nil
}

// GetChartData returns the rows of a chart of a dashboard as the dashboard
// shows them when it opens: with the dashboard's filters at the values they
// start with. The query is the one Superset saved with the chart when it
// has one, and otherwise one built from the chart's own settings; a chart
// of a kind whose query is not built yet is refused by its kind's name.
func GetChartData(ctx context.Context, relay Relay, dashboardID, chartID string, now time.Time) (ChartData, error) {
	if err := checkID("dashboard", dashboardID); err != nil {
		return ChartData{}, err
	}

	chart, err := strconv.ParseInt(chartID, 10, 64)
	if err != nil || chart <= 0 {
		return ChartData{}, fmt.Errorf("%w: a chart is named by its number", orchestration.ErrInvalid)
	}

	var (
		dashboard dashboardAnswer
		charts    chartsAnswer
	)

	if err := call(ctx, relay, http.MethodGet, "/api/v1/dashboard/"+dashboardID, nil, &dashboard); err != nil {
		return ChartData{}, err
	}

	if err := call(ctx, relay, http.MethodGet, "/api/v1/dashboard/"+dashboardID+"/charts", nil, &charts); err != nil {
		return ChartData{}, err
	}

	on := false

	for _, c := range charts.Result {
		on = on || c.ID == chart
	}

	if !on {
		return ChartData{}, fmt.Errorf("%w: dashboard %s has no chart %d", orchestration.ErrNotFound, dashboardID, chart)
	}

	var detail struct {
		Result struct {
			Name         string  `json:"slice_name"`
			Kind         string  `json:"viz_type"`
			Params       string  `json:"params"`
			QueryContext *string `json:"query_context"`
			DatasetID    int64   `json:"datasource_id"`
			DatasetKind  string  `json:"datasource_type"`
		} `json:"result"`
	}

	if err := call(ctx, relay, http.MethodGet, "/api/v1/chart/"+chartID, nil, &detail); err != nil {
		return ChartData{}, err
	}

	formData := map[string]any{}

	if detail.Result.Params != "" {
		decoder := json.NewDecoder(strings.NewReader(detail.Result.Params))
		decoder.UseNumber()

		if err := decoder.Decode(&formData); err != nil {
			return ChartData{}, fmt.Errorf("%w: chart %d's settings are not JSON", orchestration.ErrUnavailable, chart)
		}
	}

	// The chart's record names its dataset; the settings stored with it may name an older one (a chart that
	// was imported keeps the number it had where it came from: Superset 6.1.0's own examples, 2026-11-12).
	if detail.Result.DatasetID != 0 {
		formData["datasource"] = fmt.Sprintf("%d__%s", detail.Result.DatasetID, detail.Result.DatasetKind)
	}

	kind, _ := formData["viz_type"].(string)
	if kind == "" {
		kind = detail.Result.Kind
	}

	extra := dashboardDefaults(nativeFilters(dashboard.Result.Metadata), chart)

	payload, source, err := queryOf(detail.Result.QueryContext, formData, kind, chart, extra)
	if err != nil {
		return ChartData{}, err
	}

	var answer struct {
		Result []struct {
			Columns []string         `json:"colnames"`
			Data    []map[string]any `json:"data"`
			Query   string           `json:"query"`
			Cached  *string          `json:"cached_dttm"`
			Error   *string          `json:"error"`
		} `json:"result"`
	}

	if err := call(ctx, relay, http.MethodPost, "/api/v1/chart/data", payload, &answer); err != nil {
		return ChartData{}, err
	}

	if len(answer.Result) == 0 {
		return ChartData{}, fmt.Errorf("%w: Superset answered chart %d with no result", orchestration.ErrUnavailable, chart)
	}

	// A chart may ask several queries (a table's totals); the first is the chart's rows.
	result := answer.Result[0]
	if result.Error != nil && *result.Error != "" {
		return ChartData{}, fmt.Errorf("%w: Superset could not run chart %d's query: %s", orchestration.ErrInvalid, chart,
			strings.Join(strings.Fields(*result.Error), " "))
	}

	out := ChartData{
		DashboardID: dashboard.Result.ID, ChartID: chart, Name: detail.Result.Name, Kind: kind, Source: source,
		Columns: result.Columns, Rows: make([][]any, 0, len(result.Data)), SQL: result.Query, ComputedAt: now.UTC(),
	}

	if out.Columns == nil {
		out.Columns = []string{}
	}

	for _, row := range result.Data {
		values := make([]any, 0, len(out.Columns))
		for _, column := range out.Columns {
			values = append(values, row[column])
		}

		out.Rows = append(out.Rows, values)
	}

	// Superset writes when it cached a result in UTC, without a zone.
	if result.Cached != nil && *result.Cached != "" {
		if at, err := time.Parse("2006-01-02T15:04:05", strings.Split(*result.Cached, ".")[0]); err == nil {
			out.ComputedAt, out.Cached = at.UTC(), true
		}
	}

	return out, nil
}

// queryOf is the request for a chart's data: the query saved with the chart
// when there is one, else one built from its settings (the order of sup's
// get_chart_data), each with the dashboard's filters at their defaults.
func queryOf(saved *string, formData map[string]any, kind string, chart int64, extra defaults,
) (payload map[string]any, source string, err error) {
	if saved != nil && strings.TrimSpace(*saved) != "" && strings.TrimSpace(*saved) != "{}" {
		decoder := json.NewDecoder(strings.NewReader(*saved))
		decoder.UseNumber()

		if decoder.Decode(&payload) == nil && payload["queries"] != nil {
			queries, _ := payload["queries"].([]any)
			for _, q := range queries {
				if query, ok := q.(map[string]any); ok {
					extra.apply(query)
				}
			}

			payload["result_format"], payload["result_type"], payload["force"] = "json", "full", false

			return payload, SourceSaved, nil
		}
	}

	build, known := builders[kind]
	if !known {
		return nil, "", fmt.Errorf("%w: not read yet: %s. Charts of this kind have no saved query to run and their "+
			"query is not built here yet", orchestration.ErrInvalid, kind)
	}

	datasetID, datasetKind := datasetOf(formData)
	if datasetID == 0 {
		return nil, "", fmt.Errorf("%w: chart %d names no dataset", orchestration.ErrInvalid, chart)
	}

	formData["slice_id"] = chart

	query := build(formData, extra)
	extra.apply(query)
	dropNoFilter(query)

	return map[string]any{
		"datasource": map[string]any{"id": datasetID, "type": datasetKind},
		"force":      false,
		"queries":    []any{query},
		"form_data":  formData,
		// The rows as JSON, with the query that gave them.
		"result_format": "json",
		"result_type":   "full",
	}, SourceBuilt, nil
}

// CSV writes a chart's rows as CSV with a header line. An empty value is an
// empty field; a value that is a list or an object is written as JSON.
func CSV(d ChartData) ([]byte, error) {
	var out bytes.Buffer

	w := csv.NewWriter(&out)
	if err := w.Write(d.Columns); err != nil {
		return nil, fmt.Errorf("writing the CSV: %w", err)
	}

	for _, row := range d.Rows {
		fields := make([]string, 0, len(row))

		for _, value := range row {
			switch v := value.(type) {
			case nil:
				fields = append(fields, "")
			case string:
				fields = append(fields, v)
			case json.Number:
				fields = append(fields, v.String())
			case bool:
				fields = append(fields, strconv.FormatBool(v))
			default:
				raw, err := json.Marshal(v)
				if err != nil {
					return nil, fmt.Errorf("writing the CSV: %w", err)
				}

				fields = append(fields, string(raw))
			}
		}

		if err := w.Write(fields); err != nil {
			return nil, fmt.Errorf("writing the CSV: %w", err)
		}
	}

	w.Flush()

	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("writing the CSV: %w", err)
	}

	return out.Bytes(), nil
}
