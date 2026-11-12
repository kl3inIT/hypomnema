package superset

import (
	"encoding/json"
	"strings"
)

// The query a chart runs, built from the settings Superset stores with it
// (its form_data), for a chart that has no saved query. Each kind of chart
// is built as Superset's own plugin for it builds it, so that the rows are
// the ones its dashboard shows:
//
//   - every kind starts from what Superset's buildQueryObject takes of the
//     settings: the filters, the time range, the row limit, the time grain;
//   - table: plugin-chart-table's buildQuery (raw columns in their order, or
//     groups and metrics ordered by the sort metric or the first metric,
//     with percent metrics as a contribution);
//   - big_number_total: BigNumberTotal's buildQuery (one metric, no groups);
//   - pie: Pie's buildQuery (groups, one metric, ordered by it when asked);
//   - echarts_timeseries_line and echarts_timeseries_bar: Timeseries'
//     buildQuery (the x axis with its time grain, the groups as series, the
//     metrics). The rows are the query's own, one for each x and series:
//     the chart turns them into one column a series after, and that is not
//     done here.
//
// The table's rules were tried on three charts of a dashboard on 2026-11-12
// (160, 274 and 21 rows, the dashboard's own counts). A kind that is not
// here is refused by name, not guessed at.

// builders are the kinds of chart whose query is built here.
var builders = map[string]func(formData map[string]any, extra defaults) map[string]any{
	"table":                   tableQuery,
	"big_number_total":        bigNumberQuery,
	"pie":                     pieQuery,
	"echarts_timeseries_line": timeseriesQuery,
	"echarts_timeseries_bar":  timeseriesQuery,
}

// defaults are a dashboard's filters at the values they start with, for one
// chart: what Superset adds to the chart's query when the dashboard opens
// (each filter's defaultDataMask.extraFormData, for the filters the chart is
// in the scope of).
type defaults struct {
	// filters and adhoc are added to the chart's own.
	filters []any
	adhoc   []any
	// timeRange, granularity and timeGrain replace the chart's own when a
	// filter sets them.
	timeRange   string
	granularity string
	timeGrain   string
}

// dashboardDefaults gathers the defaults of the filters that apply to a
// chart.
func dashboardDefaults(filters []nativeFilter, chart int64) defaults {
	var d defaults

	for _, f := range filters {
		if !f.applies(chart) {
			continue
		}

		extra := f.Default.Extra
		d.filters = append(d.filters, list(extra["filters"])...)
		d.adhoc = append(d.adhoc, list(extra["adhoc_filters"])...)

		if v := text(extra["time_range"]); v != "" {
			d.timeRange = v
		}

		if v := text(extra["granularity_sqla"]); v != "" {
			d.granularity = v
		}

		if v := text(extra["time_grain_sqla"]); v != "" {
			d.timeGrain = v
		}
	}

	return d
}

const (
	temporalRange = "TEMPORAL_RANGE"
	noFilter      = "No filter"
	baseAxis      = "BASE_AXIS"
)

// apply adds the dashboard's defaults to one query: its filters are added to
// the query's, and a time range, time column or time grain it sets replaces
// the query's own, the time range in every filter on a time column too.
func (d defaults) apply(query map[string]any) {
	filters := append(list(query["filters"]), d.filters...)
	extras, _ := query["extras"].(map[string]any)

	if extras == nil {
		extras = map[string]any{}
	}

	simple, where, having := adhocFilters(d.adhoc)
	filters = append(filters, simple...)

	for key, added := range map[string][]string{"where": where, "having": having} {
		if len(added) == 0 {
			continue
		}

		if had := text(extras[key]); had != "" {
			added = append([]string{had}, added...)
		}

		extras[key] = strings.Join(added, " AND ")
	}

	if d.timeRange != "" {
		query["time_range"] = d.timeRange

		for _, f := range filters {
			if filter, ok := f.(map[string]any); ok && text(filter["op"]) == temporalRange {
				filter["val"] = d.timeRange
			}
		}
	}

	if d.granularity != "" {
		query["granularity"] = d.granularity
	}

	if d.timeGrain != "" {
		extras["time_grain_sqla"] = d.timeGrain

		for _, c := range list(query["columns"]) {
			if column, ok := c.(map[string]any); ok && text(column["columnType"]) == baseAxis {
				column["timeGrain"] = d.timeGrain
			}
		}
	}

	query["filters"], query["extras"] = filters, extras
}

// dropNoFilter removes the filters on a time column that filter nothing: a
// chart keeps one with the range "No filter" (tried 2026-11-12).
func dropNoFilter(query map[string]any) {
	kept := []any{}

	for _, f := range list(query["filters"]) {
		filter, ok := f.(map[string]any)
		if ok && text(filter["op"]) == temporalRange && (text(filter["val"]) == "" || text(filter["val"]) == noFilter) {
			continue
		}

		kept = append(kept, f)
	}

	query["filters"] = kept
}

// adhocFilters reads a chart's filters as a query takes them: a simple one
// as a column, an operator and a value; one written as SQL into the WHERE or
// the HAVING clause.
func adhocFilters(given []any) (simple []any, where, having []string) {
	for _, g := range given {
		f, ok := g.(map[string]any)
		if !ok {
			continue
		}

		switch text(f["expressionType"]) {
		case "SIMPLE":
			filter := map[string]any{"col": f["subject"], "op": f["operator"]}
			if value, has := f["comparator"]; has && value != nil {
				filter["val"] = value
			}

			simple = append(simple, filter)
		case "SQL":
			expression := text(f["sqlExpression"])
			if expression == "" {
				continue
			}

			if text(f["clause"]) == "HAVING" {
				having = append(having, "("+expression+")")
			} else {
				where = append(where, "("+expression+")")
			}
		}
	}

	return simple, where, having
}

// baseQuery is what every kind of chart takes of its settings.
func baseQuery(formData map[string]any) map[string]any {
	simple, where, having := adhocFilters(list(formData["adhoc_filters"]))
	if simple == nil {
		simple = []any{}
	}

	extras := map[string]any{"where": strings.Join(where, " AND "), "having": strings.Join(having, " AND ")}
	if grain := text(formData["time_grain_sqla"]); grain != "" {
		extras["time_grain_sqla"] = grain
	}

	query := map[string]any{"filters": simple, "extras": extras, "orderby": []any{}}

	for from, to := range map[string]string{
		"row_limit": "row_limit", "time_range": "time_range", "granularity_sqla": "granularity",
	} {
		if value, has := formData[from]; has && value != nil && value != "" {
			query[to] = value
		}
	}

	return query
}

// tableQuery is the query of a table: its columns as they are, in the order
// the chart names, or its groups and metrics.
func tableQuery(formData map[string]any, _ defaults) map[string]any {
	query := baseQuery(formData)

	mode := text(formData["query_mode"])
	if mode != "raw" && mode != "aggregate" {
		mode = "aggregate"
		if len(list(formData["all_columns"])) > 0 {
			mode = "raw"
		}
	}

	if mode == "raw" {
		// No metrics at all, as the plugin sends: the rows as they are. An empty list of metrics would group
		// the rows by every column and so drop the ones that repeat (tried on Superset 6.1.0, 2026-11-12).
		query["columns"] = list(formData["all_columns"])
		query["orderby"] = orderByColumns(list(formData["order_by_cols"]))

		return query
	}

	metrics := list(formData["metrics"])
	percent := list(formData["percent_metrics"])

	if sort := formData["timeseries_limit_metric"]; present(sort) {
		query["orderby"] = []any{[]any{sort, !truth(formData["order_desc"], true)}}
	} else if len(metrics) > 0 {
		query["orderby"] = []any{[]any{metrics[0], false}}
	}

	if len(percent) > 0 {
		labels, renamed := []any{}, []any{}

		for _, m := range percent {
			if !contains(metrics, m) {
				metrics = append(metrics, m)
			}

			labels, renamed = append(labels, metricLabel(m)), append(renamed, "%"+metricLabel(m))
		}

		query["post_processing"] = []any{map[string]any{
			"operation": "contribution", "options": map[string]any{"columns": labels, "rename_columns": renamed},
		}}
	}

	// A group that is a time column is cut to the chart's time grain.
	grain, temporal := text(formData["time_grain_sqla"]), map[string]any{}
	if lookup, ok := formData["temporal_columns_lookup"].(map[string]any); ok {
		temporal = lookup
	}

	columns := []any{}

	for _, c := range list(formData["groupby"]) {
		if name, ok := c.(string); ok && grain != "" && truth(temporal[name], false) {
			c = axisColumn(name, grain)
		}

		columns = append(columns, c)
	}

	query["columns"], query["metrics"] = columns, metrics

	return query
}

// bigNumberQuery is the query of a single number: one metric.
func bigNumberQuery(formData map[string]any, _ defaults) map[string]any {
	query := baseQuery(formData)
	query["columns"] = []any{}
	query["metrics"] = []any{}

	if metric := formData["metric"]; present(metric) {
		query["metrics"] = []any{metric}
	}

	return query
}

// pieQuery is the query of a pie: its groups and one metric.
func pieQuery(formData map[string]any, _ defaults) map[string]any {
	query := bigNumberQuery(formData, defaults{})
	query["columns"] = list(formData["groupby"])

	if metric := formData["metric"]; present(metric) && truth(formData["sort_by_metric"], false) {
		query["orderby"] = []any{[]any{metric, false}}
	}

	return query
}

// timeseriesQuery is the query of a line or a bar chart over an x axis: the
// x axis cut to the time grain, the groups as series, the metrics.
func timeseriesQuery(formData map[string]any, extra defaults) map[string]any {
	query := baseQuery(formData)
	groups := list(formData["groupby"])
	metrics := list(formData["metrics"])
	columns := []any{}

	grain := text(formData["time_grain_sqla"])
	if extra.timeGrain != "" {
		grain = extra.timeGrain
	}

	switch axis := formData["x_axis"].(type) {
	case string:
		if axis != "" {
			columns = append(columns, axisColumn(axis, grain))
		}
	case map[string]any:
		columns = append(columns, axis)
	}

	if len(columns) == 0 {
		// A chart from before the x axis was a setting is a series over its time column.
		query["is_timeseries"] = true
	}

	query["columns"] = append(columns, groups...)
	query["series_columns"] = groups
	query["metrics"] = metrics

	sort := formData["series_limit_metric"]
	if !present(sort) {
		sort = formData["timeseries_limit_metric"]
	}

	descending := truth(formData["order_desc"], true)

	if present(sort) {
		query["orderby"] = []any{[]any{sort, !descending}}
		query["series_limit_metric"] = sort
	} else if len(metrics) > 0 {
		query["orderby"] = []any{[]any{metrics[0], false}}
	}

	query["order_desc"] = descending

	for _, key := range []string{"series_limit", "limit"} {
		if limit, has := formData[key]; has && present(limit) {
			query["series_limit"] = limit

			break
		}
	}

	if offsets := list(formData["time_compare"]); len(offsets) > 0 {
		query["time_offsets"] = offsets
	}

	return query
}

// axisColumn is a column as the x axis: cut to a time grain when it is a
// time column, which Superset decides.
func axisColumn(name, grain string) map[string]any {
	column := map[string]any{"columnType": baseAxis, "sqlExpression": name, "label": name, "expressionType": "SQL"}
	if grain != "" {
		column["timeGrain"] = grain
	}

	return column
}

// orderByColumns reads a table's order: each entry is a JSON text of a
// column and whether it ascends, as the chart stores it.
func orderByColumns(given []any) []any {
	out := []any{}

	for _, g := range given {
		raw, ok := g.(string)
		if !ok {
			continue
		}

		var pair []any
		if decodeJSON(raw, &pair) == nil && len(pair) == 2 {
			out = append(out, pair)
		}
	}

	return out
}

// metricLabel is the name of a metric's column: a saved metric's own name,
// or the label of one written in the chart.
func metricLabel(metric any) string {
	if m, ok := metric.(map[string]any); ok {
		return text(m["label"])
	}

	return text(metric)
}

func contains(all []any, one any) bool {
	for _, a := range all {
		if metricLabel(a) == metricLabel(one) {
			return true
		}
	}

	return false
}

// list reads a setting that is a list; one value alone is a list of it, and
// none an empty list, never null: Superset reads a null apart.
func list(v any) []any {
	switch l := v.(type) {
	case []any:
		return l
	case nil:
		return []any{}
	case string:
		if l == "" {
			return []any{}
		}
	}

	return []any{v}
}

func text(v any) string {
	s, _ := v.(string)

	return s
}

// present says a setting has a value: not absent, not empty.
func present(v any) bool {
	switch s := v.(type) {
	case nil:
		return false
	case string:
		return s != ""
	}

	return true
}

// truth reads a setting that is yes or no, with what it is when absent.
func truth(v any, absent bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}

	return absent
}

// decodeJSON reads JSON keeping each number as it is written.
func decodeJSON(raw string, out any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()

	return decoder.Decode(out)
}
