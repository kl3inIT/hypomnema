package superset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Filters are values given to a dashboard's filters for one read (issue
// #648), as someone sets them in the dashboard's filter bar: each is named as
// the dashboard names it (or by its id), with its values. An empty list
// clears the filter, as clearing it in the bar does. A filter not named keeps
// the value it starts with.
//
// What may be changed is learned from Superset's guest token, which runs a
// dashboard's saved charts with the filter values its embedded bar sets and
// nothing else (superset/security/manager.py at 9455393:
// _native_filter_request_modified, query_context_modified): a value only
// replaces the filter's extraFormData, as the bar does, and is sent as data,
// never as SQL. The columns, metrics and groups stay the saved chart's.
type Filters map[string][]string

// ParseFilters reads filter values written as "name=value", one value each:
// several values of one filter are several entries; "name=" alone clears the
// filter. The name is cut at the first "=".
func ParseFilters(given []string) (Filters, error) {
	if len(given) == 0 {
		return Filters{}, nil
	}

	out := Filters{}
	cleared := map[string]bool{}

	for _, g := range given {
		name, value, ok := strings.Cut(g, "=")
		if name = strings.TrimSpace(name); !ok || name == "" {
			return nil, fmt.Errorf("%w: a filter is written as name=value, not %q", orchestration.ErrInvalid, g)
		}

		if _, has := out[name]; !has {
			out[name] = []string{}
		}

		if value == "" {
			cleared[name] = true

			continue
		}

		out[name] = append(out[name], value)
	}

	for name := range cleared {
		if len(out[name]) > 0 {
			return nil, fmt.Errorf("%w: filter %q is both cleared (%s=) and given values", orchestration.ErrInvalid,
				name, name)
		}
	}

	return out, nil
}

// Strings is the filters as ParseFilters reads them, in a stable order: for
// a log line or a request.
func (f Filters) Strings() []string {
	names := make([]string, 0, len(f))
	for name := range f {
		names = append(names, name)
	}

	sort.Strings(names)

	out := []string{}

	for _, name := range names {
		if len(f[name]) == 0 {
			out = append(out, name+"=")

			continue
		}

		for _, v := range f[name] {
			out = append(out, name+"="+v)
		}
	}

	return out
}

// The kinds of filter whose values are set here, and what each sets, as
// Superset's filter plugins set it (superset-frontend/src/filters at
// 9455393): utils.ts getSelectExtraFormData, Time, TimeGrain and TimeColumn
// FilterPlugin.tsx.
const (
	filterSelect     = "filter_select"
	filterTime       = "filter_time"
	filterTimeGrain  = "filter_timegrain"
	filterTimeColumn = "filter_timecolumn"
)

// find is the filter of a dashboard named by its name, or by its id.
func find(filters []nativeFilter, name string) (int, bool) {
	for i, f := range filters {
		if f.Name == name {
			return i, true
		}
	}

	for i, f := range filters {
		if f.ID == name {
			return i, true
		}
	}

	return 0, false
}

// filterNames is the names of filters as a refusal lists them.
func filterNames(filters []nativeFilter, keep func(nativeFilter) bool) string {
	names := []string{}

	for _, f := range filters {
		if keep(f) {
			names = append(names, strconv.Quote(f.Name))
		}
	}

	if len(names) == 0 {
		return "none"
	}

	return strings.Join(names, ", ")
}

// withValues is a dashboard's filters with the values given for one chart:
// each given filter's extraFormData is the one its plugin sets for the
// values. A filter that is not the dashboard's, or that does not apply to
// the chart, is refused with the filters that are.
func withValues(filters []nativeFilter, given Filters, dashboard string, chart int64) ([]nativeFilter, error) {
	if len(given) == 0 {
		return filters, nil
	}

	out := slices.Clone(filters)

	names := make([]string, 0, len(given))
	for name := range given {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		i, ok := find(out, name)
		if !ok {
			return nil, fmt.Errorf("%w: dashboard %s has no filter %q; its filters: %s", orchestration.ErrInvalid,
				dashboard, name, filterNames(out, func(nativeFilter) bool { return true }))
		}

		if !out[i].applies(chart) {
			return nil, fmt.Errorf("%w: filter %q of dashboard %s does not apply to chart %d; the filters that do: %s",
				orchestration.ErrInvalid, name, dashboard, chart,
				filterNames(out, func(f nativeFilter) bool { return f.applies(chart) }))
		}

		extra, err := extraFormData(out[i], given[name])
		if err != nil {
			return nil, err
		}

		out[i].Default.Extra = extra
	}

	return out, nil
}

// extraFormData is what a filter adds to a chart's query for values, as its
// plugin sets it: a select filter an IN on its column, a time filter its
// range, a time grain or time column filter its one value. No value sets
// nothing, as a cleared filter in the bar.
func extraFormData(f nativeFilter, values []string) (map[string]any, error) {
	extra := map[string]any{}

	one := func() (string, error) {
		if len(values) > 1 {
			return "", fmt.Errorf("%w: filter %q takes one value, not %d", orchestration.ErrInvalid, f.Name, len(values))
		}

		if len(values) == 0 {
			return "", nil
		}

		return values[0], nil
	}

	switch f.Kind {
	case filterSelect:
		column := targetColumn(f)
		if column == "" {
			return nil, fmt.Errorf("%w: filter %q names no column", orchestration.ErrInvalid, f.Name)
		}

		if len(values) > 0 {
			val := make([]any, 0, len(values))
			for _, v := range values {
				val = append(val, v)
			}

			extra["filters"] = []any{map[string]any{"col": column, "op": "IN", "val": val}}
		}
	case filterTime:
		v, err := one()
		if err != nil {
			return nil, err
		}

		// "No filter" is no range, as the time filter sets nothing for it.
		if v != "" && v != noFilter {
			extra["time_range"] = v
		}
	case filterTimeGrain, filterTimeColumn:
		v, err := one()
		if err != nil {
			return nil, err
		}

		key := "time_grain_sqla"
		if f.Kind == filterTimeColumn {
			key = "granularity_sqla"
		}

		if v != "" {
			extra[key] = v
		}
	default:
		return nil, fmt.Errorf("%w: filter %q is a %s, whose values are not set here yet", orchestration.ErrInvalid,
			f.Name, f.Kind)
	}

	return extra, nil
}

func targetColumn(f nativeFilter) string {
	if len(f.Targets) == 0 {
		return ""
	}

	return f.Targets[0].Column.Name
}

// maxFilterValues bounds one listing of a filter's values.
const maxFilterValues = 1000

// FilterValue is one value of a filter, with how many rows have it.
type FilterValue struct {
	Value any
	Count int64
}

// FilterValues lists the values a select filter of a dashboard offers: the
// distinct values of its column on its dataset, the most frequent first, at
// most a thousand, those containing search when it is not empty. The query
// is the one Superset's select filter asks for its list
// (superset-frontend/src/filters/components/Select/buildQuery.ts at 9455393:
// the filter's column, an ILIKE on it when searching), with a count to order
// by; the guest token allows the same, a filter's values on the column it
// targets on that dashboard (_native_filter_request_modified).
func FilterValues(ctx context.Context, relay Relay, dashboardID, filter, search string) ([]FilterValue, error) {
	if err := checkID("dashboard", dashboardID); err != nil {
		return nil, err
	}

	var dashboard dashboardAnswer
	if err := call(ctx, relay, http.MethodGet, "/api/v1/dashboard/"+dashboardID, nil, &dashboard); err != nil {
		return nil, err
	}

	filters := nativeFilters(dashboard.Result.Metadata)

	i, ok := find(filters, filter)
	if !ok {
		return nil, fmt.Errorf("%w: dashboard %s has no filter %q; its filters: %s", orchestration.ErrInvalid,
			dashboardID, filter, filterNames(filters, func(nativeFilter) bool { return true }))
	}

	f := filters[i]
	if f.Kind != filterSelect {
		return nil, fmt.Errorf("%w: filter %q is a %s; only a select filter has a list of values",
			orchestration.ErrInvalid, f.Name, f.Kind)
	}

	column := targetColumn(f)
	if column == "" || f.Targets[0].DatasetID == 0 {
		return nil, fmt.Errorf("%w: filter %q names no column", orchestration.ErrInvalid, f.Name)
	}

	count := map[string]any{
		"expressionType": "SIMPLE", "aggregate": "COUNT", "column": map[string]any{"column_name": column},
		"label": "count",
	}
	filtersOf := []any{}

	if search = strings.TrimSpace(search); search != "" {
		filtersOf = append(filtersOf, map[string]any{"col": column, "op": "ILIKE", "val": "%" + search + "%"})
	}

	payload := map[string]any{
		"datasource": map[string]any{"id": f.Targets[0].DatasetID, "type": "table"},
		"force":      false,
		"queries": []any{map[string]any{
			"columns": []any{column}, "metrics": []any{count}, "filters": filtersOf,
			"orderby": []any{[]any{count, false}}, "row_limit": maxFilterValues,
			"extras": map[string]any{"where": "", "having": ""},
		}},
		"result_format": "json",
		"result_type":   "full",
	}

	var answer struct {
		Result []struct {
			Data  []map[string]any `json:"data"`
			Error *string          `json:"error"`
		} `json:"result"`
	}

	if err := call(ctx, relay, http.MethodPost, "/api/v1/chart/data", payload, &answer); err != nil {
		return nil, err
	}

	if len(answer.Result) == 0 {
		return nil, fmt.Errorf("%w: Superset answered filter %q's values with no result", orchestration.ErrUnavailable,
			f.Name)
	}

	if e := answer.Result[0].Error; e != nil && *e != "" {
		return nil, fmt.Errorf("%w: Superset could not list filter %q's values: %s", orchestration.ErrInvalid, f.Name,
			strings.Join(strings.Fields(*e), " "))
	}

	out := make([]FilterValue, 0, len(answer.Result[0].Data))
	for _, row := range answer.Result[0].Data {
		n, _ := row["count"].(json.Number)
		count, _ := n.Int64()
		out = append(out, FilterValue{Value: row[column], Count: count})
	}

	return out, nil
}

// FilterValues lists a select filter's values of a dashboard of the scope.
func (s Scope) FilterValues(ctx context.Context, relay Relay, ref, filter, search string) ([]FilterValue, error) {
	if !s.Limited {
		return FilterValues(ctx, relay, ref, filter, search)
	}

	id, err := s.dashboardID(ctx, relay, ref)
	if err != nil {
		return nil, err
	}

	values, err := FilterValues(ctx, relay, strconv.FormatInt(id, 10), filter, search)
	if errors.Is(err, orchestration.ErrNotFound) {
		return nil, dashboardNotFound(ref, false)
	}

	return values, err
}
