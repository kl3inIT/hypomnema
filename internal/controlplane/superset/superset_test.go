package superset_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset/supersettest"
)

var now = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

func chartData(t *testing.T, s *supersettest.Superset, chart int) (superset.ChartData, error) {
	t.Helper()

	return filtered(t, s, chart, nil)
}

func filtered(t *testing.T, s *supersettest.Superset, chart int, filters superset.Filters) (superset.ChartData, error) {
	t.Helper()

	return superset.GetChartData(t.Context(), s.Signed(), strconv.Itoa(supersettest.Dashboard), strconv.Itoa(chart),
		filters, now)
}

// Scenario: someone looks for the dashboard about usage, then reads what is
// on it. The search is by title; the dashboard comes with each chart's kind
// and dataset and whether a chart of that kind can be read, and with its
// filters and the value each starts with. It is found by its slug too.
func TestADashboardIsFoundByItsTitleAndReadWithItsChartsAndFilters(t *testing.T) {
	t.Parallel()

	s := supersettest.New(t)

	all, err := superset.Dashboards(t.Context(), s.Signed(), "")
	require.NoError(t, err)
	assert.Len(t, all, 2)

	found, err := superset.Dashboards(t.Context(), s.Signed(), "usage")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, int64(supersettest.Dashboard), found[0].ID)
	assert.Equal(t, "Product usage", found[0].Title)
	assert.True(t, found[0].Published)
	assert.Equal(t, 2026, found[0].ChangedAt.Year())

	d, err := superset.GetDashboard(t.Context(), s.Signed(), supersettest.DashboardSlug)
	require.NoError(t, err)
	assert.Equal(t, "Product usage", d.Title)
	require.Len(t, d.Charts, 9)

	charts := map[int64]superset.Chart{}
	for _, c := range d.Charts {
		charts[c.ID] = c
	}

	assert.Equal(t, superset.Chart{
		ID: supersettest.ActivePeople, Name: "Active people", Kind: "table", DatasetID: 28, Dataset: "people", Read: true,
	}, charts[supersettest.ActivePeople])
	assert.Equal(t, "pie", charts[supersettest.UnitShare].Kind)
	assert.Equal(t, "sankey_v2", charts[supersettest.Flows].Kind)
	assert.False(t, charts[supersettest.Flows].Read, "a kind whose query is not built is marked")

	require.Len(t, d.Filters, 2, "a divider between filters is not a filter")
	assert.Equal(t, "Unit", d.Filters[0].Name)
	assert.Equal(t, "filter_select", d.Filters[0].Kind)
	assert.Equal(t, "unit", d.Filters[0].Column)
	assert.JSONEq(t, `["Sales"]`, string(d.Filters[0].Default))
	assert.Equal(t, []int64{supersettest.UnitShare}, d.Filters[0].Charts)
	assert.JSONEq(t, `"Last quarter"`, string(d.Filters[1].Default))

	_, err = superset.GetDashboard(t.Context(), s.Signed(), "404")
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	_, err = superset.GetDashboard(t.Context(), s.Signed(), "../chart/1")
	require.ErrorIs(t, err, orchestration.ErrInvalid, "a dashboard is named by a number or a slug only")
}

// Scenario: the three tables of the dashboard are read as data. None has a
// saved query, so each query is built from the chart's settings as Superset's
// table builds it: the list of people asks for its columns in the chart's
// order, with no metrics at all, so that no row is dropped; the grouped table
// asks for its groups and metric, ordered by the metric, with its SQL filter,
// and without the time filter that filters nothing. The row counts are the
// dashboard's: 160, 274 and 21.
func TestATableWithoutASavedQueryIsReadByAQueryBuiltFromItsSettings(t *testing.T) {
	t.Parallel()

	s := supersettest.New(t)

	people, err := chartData(t, s, supersettest.ActivePeople)
	require.NoError(t, err)
	assert.Equal(t, superset.SourceBuilt, people.Source)
	assert.Equal(t, "table", people.Kind)
	assert.Equal(t, []string{"full_name", "username", "unit"}, people.Columns)
	require.Len(t, people.Rows, 160)
	assert.Equal(t, []any{"Person Number 1", "username-1", "unit-1"}, people.Rows[0])
	assert.Contains(t, people.SQL, "FROM dataset_28")
	assert.Equal(t, now, people.ComputedAt, "figures that are not from Superset's cache are as of the call")
	assert.False(t, people.Cached)

	query := s.LastQuery()
	assert.Equal(t, []any{"full_name", "username", "unit"}, query["columns"])
	assert.NotContains(t, query, "metrics", "a list of rows has no metrics, or Superset would group it")
	assert.Equal(t, []any{[]any{"full_name", true}}, query["orderby"])
	assert.InDelta(t, 5000, query["row_limit"], 0)
	assert.Equal(t, []any{map[string]any{"col": "active", "op": "==", "val": true}}, query["filters"])

	inactive, err := chartData(t, s, supersettest.InactivePeople)
	require.NoError(t, err)
	assert.Len(t, inactive.Rows, 274, "a table that names columns and no mode is a list of rows")

	units, err := chartData(t, s, supersettest.ByUnit)
	require.NoError(t, err)
	assert.Equal(t, []string{"unit", "count"}, units.Columns)
	require.Len(t, units.Rows, 21)
	assert.Equal(t, json.Number("10"), units.Rows[0][1], "a figure is kept as Superset wrote it")

	query = s.LastQuery()
	assert.Equal(t, []any{"unit"}, query["columns"])
	assert.Equal(t, []any{"count"}, query["metrics"])
	assert.Equal(t, []any{[]any{"count", false}}, query["orderby"], "the first metric, the largest first")
	assert.Empty(t, query["filters"], "a time filter of No filter is left out")
	assert.Equal(t, map[string]any{"where": "(unit IS NOT NULL)", "having": ""}, query["extras"])
	assert.Equal(t, "No filter", query["time_range"])
}

// Scenario: a chart that Superset saved a query with is read by that query,
// not by one built here, and the figures carry the time Superset cached them.
func TestAChartWithASavedQueryIsReadByIt(t *testing.T) {
	t.Parallel()

	s := supersettest.New(t)

	days, err := chartData(t, s, supersettest.ByDay)
	require.NoError(t, err)
	assert.Equal(t, superset.SourceSaved, days.Source)
	assert.Len(t, days.Rows, 30)
	assert.True(t, days.Cached)
	assert.Equal(t, time.Date(2026, 11, 12, 3, 4, 5, 0, time.UTC), days.ComputedAt)

	query := s.LastQuery()
	assert.Equal(t, []any{[]any{"count", false}}, query["orderby"], "the saved query is sent as it was saved")
	assert.InDelta(t, 1000, query["row_limit"], 0)
}

// Scenario: the other kinds of chart on the dashboard are read. A single
// number asks for its one metric; a pie for its groups and metric, ordered by
// the metric; a line and a bar over time for the x axis cut to the chart's
// time grain, the groups as series and the metrics. The dashboard's filters
// are applied at the values they start with, to the charts they reach: the
// pie gets the Unit filter, the line the time range, which also replaces the
// range of the chart's own time filter.
func TestTheOtherKindsAreBuiltAsSupersetBuildsThemWithTheDashboardsFilters(t *testing.T) {
	t.Parallel()

	s := supersettest.New(t)

	number, err := chartData(t, s, supersettest.People)
	require.NoError(t, err)
	assert.Equal(t, []string{"count"}, number.Columns)
	assert.Len(t, number.Rows, 1)
	assert.Empty(t, s.LastQuery()["columns"])
	assert.Equal(t, []any{"count"}, s.LastQuery()["metrics"])

	pie, err := chartData(t, s, supersettest.UnitShare)
	require.NoError(t, err)
	assert.Len(t, pie.Rows, 5)

	query := s.LastQuery()
	assert.Equal(t, []any{"unit"}, query["columns"])
	assert.Equal(t, []any{"count"}, query["metrics"])
	assert.Equal(t, []any{[]any{"count", false}}, query["orderby"])
	assert.Equal(t, []any{map[string]any{"col": "unit", "op": "IN", "val": []any{"Sales"}}}, query["filters"],
		"the dashboard's Unit filter reaches the pie")

	line, err := chartData(t, s, supersettest.Daily)
	require.NoError(t, err)
	assert.Equal(t, []string{"day", "unit", "count"}, line.Columns)
	assert.Len(t, line.Rows, 60)

	query = s.LastQuery()
	assert.Equal(t, []any{
		map[string]any{
			"columnType": "BASE_AXIS", "sqlExpression": "day", "label": "day", "expressionType": "SQL", "timeGrain": "P1D",
		},
		"unit",
	}, query["columns"])
	assert.Equal(t, []any{"unit"}, query["series_columns"])
	assert.Equal(t, "Last quarter", query["time_range"], "the dashboard's time filter reaches the line")
	assert.Equal(t, []any{map[string]any{"col": "day", "op": "TEMPORAL_RANGE", "val": "Last quarter"}}, query["filters"])
	assert.Equal(t, map[string]any{"where": "", "having": "", "time_grain_sqla": "P1D"}, query["extras"])

	bar, err := chartData(t, s, supersettest.Monthly)
	require.NoError(t, err)
	assert.Equal(t, []string{"day", "count"}, bar.Columns)
	assert.Len(t, bar.Rows, 12)
	assert.NotContains(t, s.LastQuery(), "time_range", "the time filter does not reach the bar chart")
}

// Scenario: a chart of a kind whose query is not built here, and that has no
// saved query, is not guessed at: the answer names the kind. A chart that is
// not on the dashboard is not read through it.
func TestAKindThatIsNotBuiltIsRefusedByName(t *testing.T) {
	t.Parallel()

	s := supersettest.New(t)

	_, err := chartData(t, s, supersettest.Flows)
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	assert.Contains(t, err.Error(), "not read yet: sankey_v2")
	assert.Nil(t, s.LastQuery(), "no query was sent")

	_, err = chartData(t, s, 999)
	require.ErrorIs(t, err, orchestration.ErrNotFound)
}

// Scenario: a chart's rows are written as CSV: a header line, then each row;
// a value with a comma is quoted, an empty value is an empty field.
func TestAChartsRowsAsCSV(t *testing.T) {
	t.Parallel()

	raw, err := superset.CSV(superset.ChartData{
		Columns: []string{"name", "count", "active", "note"},
		Rows: [][]any{
			{"Lan, Nguyen", json.Number("12"), true, nil},
			{"Minh", json.Number("3.5"), false, []any{"a", "b"}},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"name,count,active,note", `"Lan, Nguyen",12,true,`, `Minh,3.5,false,"[""a"",""b""]"`, "",
	}, strings.Split(string(raw), "\n"))
}

// Scenario: someone reads the pie for another unit than the dashboard starts
// with, as they would pick it in the filter bar. The value replaces the Unit
// filter's default on the pie, the one chart in its scope; the rest of the
// pie's query is its own. Cleared, the filter filters nothing, as clearing it
// in the bar does. A chart the filter does not reach is refused, naming the
// filters that do reach it, and a filter the dashboard does not have is
// refused naming those it has: no query is sent to Superset either way.
func TestASelectFilterValueReplacesItsDefaultOnlyOnChartsInItsScope(t *testing.T) {
	t.Parallel()

	s := supersettest.New(t)

	_, err := filtered(t, s, supersettest.UnitShare, superset.Filters{"Unit": {"Support", "Sales Ops"}})
	require.NoError(t, err)

	query := s.LastQuery()
	assert.Equal(t, []any{map[string]any{"col": "unit", "op": "IN", "val": []any{"Support", "Sales Ops"}}},
		query["filters"], "the value replaces the default Sales")
	assert.Equal(t, []any{"unit"}, query["columns"])
	assert.Equal(t, []any{"count"}, query["metrics"])

	_, err = filtered(t, s, supersettest.UnitShare, superset.Filters{"NATIVE_FILTER-unit": {"Support"}})
	require.NoError(t, err)
	assert.Equal(t, []any{map[string]any{"col": "unit", "op": "IN", "val": []any{"Support"}}},
		s.LastQuery()["filters"], "a filter is named by its id too")

	_, err = filtered(t, s, supersettest.UnitShare, superset.Filters{"Unit": {}})
	require.NoError(t, err)
	assert.Empty(t, s.LastQuery()["filters"], "a cleared filter filters nothing")

	sent := len(s.Requests())

	_, err = filtered(t, s, supersettest.Daily, superset.Filters{"Unit": {"Support"}})
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	assert.Contains(t, err.Error(),
		`filter "Unit" of dashboard 9 does not apply to chart 170; the filters that do: "Time"`)

	_, err = filtered(t, s, supersettest.UnitShare, superset.Filters{"Source": {"Batch"}})
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	assert.Contains(t, err.Error(), `dashboard 9 has no filter "Source"; its filters: "Unit", "Time"`)

	for _, r := range s.Requests()[sent:] {
		assert.NotEqual(t, "/api/v1/chart/data", r.Path, "a refused filter sends no query")
	}
}

// Scenario: the line is read for last week instead of the last quarter. The
// range replaces the default in the query's time range and in the chart's own
// time filter. Cleared, or set to No filter, the dashboard sets no range, as
// Superset's time filter sets none, and the chart's own filter of No filter
// is left out. A time filter takes one range only.
func TestATimeFilterValueSetsTheChartsRange(t *testing.T) {
	t.Parallel()

	s := supersettest.New(t)

	_, err := filtered(t, s, supersettest.Daily, superset.Filters{"Time": {"Last week"}})
	require.NoError(t, err)

	query := s.LastQuery()
	assert.Equal(t, "Last week", query["time_range"])
	assert.Equal(t, []any{map[string]any{"col": "day", "op": "TEMPORAL_RANGE", "val": "Last week"}}, query["filters"])

	for _, cleared := range [][]string{{}, {"No filter"}} {
		_, err = filtered(t, s, supersettest.Daily, superset.Filters{"Time": cleared})
		require.NoError(t, err)
		assert.NotContains(t, s.LastQuery(), "time_range", "no range is set for %q", cleared)
		assert.Empty(t, s.LastQuery()["filters"])
	}

	_, err = filtered(t, s, supersettest.Daily, superset.Filters{"Time": {"Last week", "Last month"}})
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	assert.Contains(t, err.Error(), "takes one value")
}

// Scenario: a value is written with quotes and SQL in it. It is sent as the
// IN filter's value, a datum Superset binds, and never reaches the query's
// SQL clauses.
func TestAFilterValueWithSQLIsSentAsAValueOnly(t *testing.T) {
	t.Parallel()

	s := supersettest.New(t)
	value := `Sales') OR 1=1; DROP TABLE units; --`

	_, err := filtered(t, s, supersettest.UnitShare, superset.Filters{"Unit": {value}})
	require.NoError(t, err)

	query := s.LastQuery()
	assert.Equal(t, []any{map[string]any{"col": "unit", "op": "IN", "val": []any{value}}}, query["filters"])
	assert.Equal(t, map[string]any{"where": "", "having": ""}, query["extras"], "no SQL clause is written")
}

// Scenario: someone asks which units the Unit filter offers, then those with
// "sales" in them. The answer is the column's values on the filter's dataset
// with their counts, the most frequent first, as Superset's filter bar lists
// them; the search is sent as an ILIKE value. A filter that is not a select
// has no list, and one the dashboard does not have is refused by name.
func TestAFiltersValuesAreListedWithTheirCounts(t *testing.T) {
	t.Parallel()

	s := supersettest.New(t)
	relay, dashboard := s.Signed(), strconv.Itoa(supersettest.Dashboard)

	all, err := superset.FilterValues(t.Context(), relay, dashboard, "Unit", "")
	require.NoError(t, err)
	require.Len(t, all, len(supersettest.UnitValues))
	assert.Equal(t, superset.FilterValue{Value: "Sales", Count: 40}, all[0])

	query := s.LastQuery()
	assert.Equal(t, []any{"unit"}, query["columns"])
	assert.Empty(t, query["filters"])
	assert.InDelta(t, 1000, query["row_limit"], 0)

	count := map[string]any{
		"expressionType": "SIMPLE", "aggregate": "COUNT", "column": map[string]any{"column_name": "unit"},
		"label": "count",
	}
	assert.Equal(t, []any{count}, query["metrics"])
	assert.Equal(t, []any{[]any{count, false}}, query["orderby"], "the most frequent first")

	some, err := superset.FilterValues(t.Context(), relay, dashboard, "Unit", "sales")
	require.NoError(t, err)
	assert.Equal(t, []superset.FilterValue{
		{Value: "Sales", Count: 40}, {Value: "Sales Ops", Count: 7},
	}, some)
	assert.Equal(t, []any{map[string]any{"col": "unit", "op": "ILIKE", "val": "%sales%"}}, s.LastQuery()["filters"])

	_, err = superset.FilterValues(t.Context(), relay, dashboard, "Time", "")
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	assert.Contains(t, err.Error(), "only a select filter")

	_, err = superset.FilterValues(t.Context(), relay, dashboard, "Source", "")
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	assert.Contains(t, err.Error(), `its filters: "Unit", "Time"`)
}

// Scenario: filter values are written as the CLI and the API take them:
// name=value, repeated for several values, name= to clear. A value keeps an
// "=" it holds; a filter both cleared and given values, or a line with no
// name, is refused.
func TestFilterValuesAreReadFromNameEqualsValue(t *testing.T) {
	t.Parallel()

	got, err := superset.ParseFilters([]string{"Unit=Sales", "Unit=Support", "Time=", "Note=a=b"})
	require.NoError(t, err)
	assert.Equal(t, superset.Filters{"Unit": {"Sales", "Support"}, "Time": {}, "Note": {"a=b"}}, got)
	assert.Equal(t, []string{"Note=a=b", "Time=", "Unit=Sales", "Unit=Support"}, got.Strings())

	for _, bad := range [][]string{{"Unit=Sales", "Unit="}, {"=Sales"}, {"Sales"}} {
		_, err := superset.ParseFilters(bad)
		require.ErrorIs(t, err, orchestration.ErrInvalid, "%q", bad)
	}
}
