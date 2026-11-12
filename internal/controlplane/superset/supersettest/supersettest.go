// Package supersettest is a stand-in for Apache Superset's API, for tests. It
// answers as Superset 6.1.0 answered on 2026-11-12 (issue #203): a local
// account signs in for an access and a refresh token; a request with no
// token is a 401, one with a token Superset cannot read a 422, one whose
// token ended a 401; a request that is not a GET needs the CSRF token with
// the session cookie it came with and a Referer; a chart has a saved query
// or it has none; a chart's data is asked for with a query. Its one
// dashboard has the kinds of chart of the first dashboard read: tables (raw
// and grouped), a single number, a pie, a line and a bar over time, and one
// of a kind that is not read.
package supersettest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The account that exists at the stand-in, and what it runs.
const (
	User     = "hypomnema-reader"
	Password = "superset-password-never-printed"
	Version  = "6.1.0"
)

// The dashboard and its charts.
const (
	Dashboard     = 9
	DashboardSlug = "product-usage"
	// ByUnit is a grouped table without a saved query: 21 rows.
	ByUnit = 166
	// ActivePeople and InactivePeople are raw tables without a saved query,
	// lists of people: 160 and 274 rows.
	ActivePeople   = 164
	InactivePeople = 165
	// ByDay is a table with a saved query: 30 rows, from Superset's cache.
	ByDay = 167
	// People is a single number; UnitShare a pie the dashboard's Unit filter
	// applies to; Daily a line over time the dashboard's time filter applies
	// to; Monthly a bar chart over time.
	People    = 168
	UnitShare = 169
	Daily     = 170
	Monthly   = 171
	// Flows is of a kind whose query is not built, with no saved query.
	Flows = 172
)

// chart is a chart as the stand-in keeps it.
type chart struct {
	id      int64
	name    string
	dataset int64
	// params is the chart's stored settings; saved its saved query, or "".
	params map[string]any
	saved  string
	rows   int
	cached string
}

// Request is one request the stand-in received, without its token.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   string
	// CSRF, Cookie and Referer are what a request that is not a GET came with.
	CSRF    string
	Cookie  string
	Referer string
}

// Superset is the stand-in.
type Superset struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	password  string
	access    map[string]bool
	refresh   map[string]bool
	csrf      map[string]string // token → session cookie
	issued    int
	logins    int
	refreshes int
	requests  []Request
	queries   []map[string]any
	charts    map[int64]*chart
}

// New starts a stand-in that is closed when the test ends.
func New(t *testing.T) *Superset {
	t.Helper()

	s := &Superset{
		t: t, password: Password, access: map[string]bool{}, refresh: map[string]bool{}, csrf: map[string]string{},
		charts: map[int64]*chart{},
	}

	for _, c := range fixtures() {
		s.charts[c.id] = c
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /version", s.version)
	mux.HandleFunc("POST /api/v1/security/login", s.login)
	mux.HandleFunc("POST /api/v1/security/refresh", s.refreshToken)
	mux.HandleFunc("/api/v1/", s.api)

	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)

	return s
}

func table(id int64, name string, dataset int64, rows int, params map[string]any) *chart {
	params["datasource"] = fmt.Sprintf("%d__table", dataset)
	params["slice_id"] = id

	return &chart{id: id, name: name, dataset: dataset, rows: rows, params: params}
}

func fixtures() []*chart {
	noTime := map[string]any{
		"expressionType": "SIMPLE", "subject": "day", "operator": "TEMPORAL_RANGE", "comparator": "No filter",
		"clause": "WHERE",
	}

	byDay := table(ByDay, "Usage by day", 29, 30, map[string]any{
		"viz_type": "table", "query_mode": "aggregate", "groupby": []any{"day"}, "metrics": []any{"count"},
	})
	byDay.cached = "2026-11-12T03:04:05"
	byDay.saved = `{"datasource":{"id":29,"type":"table"},"force":false,"queries":[{"columns":["day"],` +
		`"metrics":["count"],"filters":[],"extras":{"having":"","where":""},"orderby":[["count",false]],` +
		`"row_limit":1000}],"form_data":{"slice_id":167,"viz_type":"table","datasource":"29__table"},` +
		`"result_format":"json","result_type":"full"}`

	return []*chart{
		table(ByUnit, "Usage by unit", 27, 21, map[string]any{
			"viz_type": "table", "query_mode": "aggregate", "groupby": []any{"unit"},
			"metrics": []any{"count"}, "percent_metrics": []any{}, "order_desc": true, "row_limit": 1000,
			"adhoc_filters": []any{noTime, map[string]any{
				"expressionType": "SQL", "clause": "WHERE", "sqlExpression": "unit IS NOT NULL",
			}},
			"time_range": "No filter",
		}),
		table(ActivePeople, "Active people", 28, 160, map[string]any{
			"viz_type": "table", "query_mode": "raw", "all_columns": []any{"full_name", "username", "unit"},
			"order_by_cols": []any{`["full_name", true]`}, "row_limit": 5000,
			"adhoc_filters": []any{map[string]any{
				"expressionType": "SIMPLE", "subject": "active", "operator": "==", "comparator": true, "clause": "WHERE",
			}},
		}),
		table(InactivePeople, "Inactive people", 28, 274, map[string]any{
			"viz_type": "table", "all_columns": []any{"full_name", "username", "unit"}, "row_limit": 5000,
		}),
		byDay,
		table(People, "People", 28, 1, map[string]any{"viz_type": "big_number_total", "metric": "count"}),
		table(UnitShare, "Share by unit", 27, 5, map[string]any{
			"viz_type": "pie", "groupby": []any{"unit"}, "metric": "count", "row_limit": 5, "sort_by_metric": true,
		}),
		table(Daily, "Daily usage", 29, 60, map[string]any{
			"viz_type": "echarts_timeseries_line", "x_axis": "day", "time_grain_sqla": "P1D", "groupby": []any{"unit"},
			"metrics": []any{"count"}, "row_limit": 10000, "adhoc_filters": []any{noTime},
		}),
		table(Monthly, "Monthly usage", 29, 12, map[string]any{
			"viz_type": "echarts_timeseries_bar", "x_axis": "day", "time_grain_sqla": "P1M", "groupby": []any{},
			"metrics": []any{"count"}, "row_limit": 10000,
		}),
		table(Flows, "Flows", 27, 8, map[string]any{"viz_type": "sankey_v2", "source": "from", "target": "to"}),
	}
}

// Signed returns a way to send requests to the stand-in as the account,
// signed in the way Superset wants: a test of what is read needs no control
// plane to sign it in.
func (s *Superset) Signed() func(ctx context.Context, method, target string, body []byte) (int, []byte, error) {
	s.mu.Lock()
	s.issued++
	access := fmt.Sprintf("access-%d", s.issued)
	csrf, session := fmt.Sprintf("csrf-%d", s.issued), fmt.Sprintf("s-%d", s.issued)
	s.access[access], s.csrf[csrf] = true, session
	s.mu.Unlock()

	return func(ctx context.Context, method, target string, body []byte) (int, []byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, s.srv.URL+target, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}

		req.Header.Set("Authorization", "Bearer "+access)

		if method != http.MethodGet {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Csrftoken", csrf)
			req.Header.Set("Cookie", "session="+session)
			req.Header.Set("Referer", s.srv.URL)
		}

		resp, err := s.srv.Client().Do(req)
		if err != nil {
			return 0, nil, err
		}

		defer resp.Body.Close() //nolint:errcheck // A test's own server.

		answer, err := io.ReadAll(resp.Body)

		return resp.StatusCode, answer, err
	}
}

// URL is the stand-in's address.
func (s *Superset) URL() string { return s.srv.URL }

// SetPassword changes the account's password at Superset.
func (s *Superset) SetPassword(password string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.password = password
}

// ExpireAccessTokens ends every access token: the next request with one is
// refused with a 401, and a refresh token still gives a new one.
func (s *Superset) ExpireAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.access = map[string]bool{}
}

// EndSessions ends every access and refresh token, as a restart of Superset
// with another key does: only the password signs in again.
func (s *Superset) EndSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.access, s.refresh = map[string]bool{}, map[string]bool{}
}

// EndCSRFTokens ends every CSRF token, as the end of the session it was
// given to does: the next request that is not a GET is refused with a 400.
func (s *Superset) EndCSRFTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.csrf = map[string]string{}
}

// SignIns is how often the account signed in with its password, refused or
// not, and how often a refresh token was used.
func (s *Superset) SignIns() (logins, refreshes int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.logins, s.refreshes
}

// Requests are the API requests the stand-in received, in order.
func (s *Superset) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Request(nil), s.requests...)
}

// LastQuery is the first query of the last request for a chart's data.
func (s *Superset) LastQuery() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.queries) == 0 {
		return nil
	}

	return s.queries[len(s.queries)-1]
}

func (s *Superset) version(w http.ResponseWriter, _ *http.Request) {
	s.answer(w, http.StatusOK, map[string]any{"build_number": nil, "version_sha": "", "version_string": Version})
}

func (s *Superset) login(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var asked struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Provider string `json:"provider"`
		Refresh  bool   `json:"refresh"`
	}

	s.logins++

	if json.NewDecoder(r.Body).Decode(&asked) != nil || asked.Username != User || asked.Password != s.password ||
		asked.Provider != "db" {
		s.answer(w, http.StatusUnauthorized, map[string]any{"message": "Not authorized"})

		return
	}

	s.issued++
	access, refresh := fmt.Sprintf("access-%d", s.issued), fmt.Sprintf("refresh-%d", s.issued)
	s.access[access] = true

	out := map[string]any{"access_token": access}
	if asked.Refresh {
		s.refresh[refresh] = true
		out["refresh_token"] = refresh
	}

	s.answer(w, http.StatusOK, out)
}

func (s *Superset) refreshToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.refresh[token] {
		s.answer(w, http.StatusUnauthorized, map[string]any{"msg": "Token has expired"})

		return
	}

	s.refreshes++
	s.issued++
	access := fmt.Sprintf("access-%d", s.issued)
	s.access[access] = true
	s.answer(w, http.StatusOK, map[string]any{"access_token": access})
}

// api answers every request under /api/v1/ but the sign-in: the token first,
// then the CSRF token of a request that is not a GET, then the request.
func (s *Superset) api(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	body, _ := io.ReadAll(r.Body)

	header := r.Header.Get("Authorization")
	token, bearer := strings.CutPrefix(header, "Bearer ")

	switch {
	case header == "":
		s.answer(w, http.StatusUnauthorized, map[string]any{"msg": "Missing Authorization Header"})

		return
	case !bearer || !strings.HasPrefix(token, "access-"):
		s.answer(w, http.StatusUnprocessableEntity, map[string]any{"msg": "Signature verification failed"})

		return
	case !s.access[token]:
		s.answer(w, http.StatusUnauthorized, map[string]any{"msg": "Token has expired"})

		return
	}

	s.requests = append(s.requests, Request{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(body),
		CSRF: r.Header.Get("X-Csrftoken"), Cookie: r.Header.Get("Cookie"), Referer: r.Header.Get("Referer"),
	})

	if r.Method != http.MethodGet {
		session, known := s.csrf[r.Header.Get("X-Csrftoken")]
		if !known || r.Header.Get("Cookie") != "session="+session || r.Header.Get("Referer") == "" {
			s.answer(w, http.StatusBadRequest, map[string]any{"errors": []any{map[string]any{
				"message": "400 Bad Request: The CSRF token is missing.", "error_type": "GENERIC_BACKEND_ERROR",
			}}})

			return
		}
	}

	s.route(w, r, body)
}

func (s *Superset) route(w http.ResponseWriter, r *http.Request, body []byte) {
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	parts := strings.Split(path, "/")
	dashboard := len(parts) >= 2 && parts[0] == "dashboard" &&
		(parts[1] == strconv.Itoa(Dashboard) || parts[1] == DashboardSlug)

	switch {
	case r.Method == http.MethodGet && path == "security/csrf_token":
		s.issued++
		token, session := fmt.Sprintf("csrf-%d", s.issued), fmt.Sprintf("s-%d", s.issued)
		s.csrf[token] = session
		http.SetCookie(w, &http.Cookie{
			Name: "session", Value: session, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		})
		s.answer(w, http.StatusOK, map[string]any{"result": token})
	case r.Method == http.MethodGet && path == "dashboard":
		s.dashboards(w, r)
	case r.Method == http.MethodGet && dashboard && len(parts) == 2:
		s.answer(w, http.StatusOK, map[string]any{"result": map[string]any{
			"id": Dashboard, "dashboard_title": "Product usage", "slug": DashboardSlug, "json_metadata": metadata(),
		}})
	case r.Method == http.MethodGet && dashboard && len(parts) == 3 && parts[2] == "charts":
		s.dashboardCharts(w)
	case r.Method == http.MethodGet && dashboard && len(parts) == 3 && parts[2] == "datasets":
		s.answer(w, http.StatusOK, map[string]any{"result": []any{
			map[string]any{"id": 27, "table_name": "usage_by_unit"}, map[string]any{"id": 28, "table_name": "people"},
			map[string]any{"id": 29, "table_name": "usage_by_day"},
		}})
	case r.Method == http.MethodPost && path == "chart/data":
		s.chartData(w, body)
	case len(parts) >= 2 && parts[0] == "chart":
		s.chart(w, r, parts)
	case r.Method == http.MethodPost && path == "sqllab/execute":
		s.answer(w, http.StatusOK, map[string]any{"status": "success", "data": []any{map[string]any{"one": 1}}})
	default:
		s.answer(w, http.StatusNotFound, map[string]any{"message": "Not found"})
	}
}

// metadata is the dashboard's metadata: a filter on the unit that starts at
// Sales and applies to the pie, and a time filter that starts at the last
// quarter and applies to the line.
func metadata() string {
	return text(map[string]any{"native_filter_configuration": []any{
		map[string]any{
			"id": "NATIVE_FILTER-unit", "name": "Unit", "filterType": "filter_select", "type": "NATIVE_FILTER",
			"targets": []any{map[string]any{"datasetId": 27, "column": map[string]any{"name": "unit"}}},
			"defaultDataMask": map[string]any{
				"extraFormData": map[string]any{"filters": []any{
					map[string]any{"col": "unit", "op": "IN", "val": []any{"Sales"}},
				}},
				"filterState": map[string]any{"value": []any{"Sales"}},
			},
			"chartsInScope": []any{UnitShare},
		},
		map[string]any{
			"id": "NATIVE_FILTER-time", "name": "Time", "filterType": "filter_time", "type": "NATIVE_FILTER",
			"targets": []any{map[string]any{}},
			"defaultDataMask": map[string]any{
				"extraFormData": map[string]any{"time_range": "Last quarter"},
				"filterState":   map[string]any{"value": "Last quarter"},
			},
			"chartsInScope": []any{Daily},
		},
		map[string]any{"id": "NATIVE_FILTER_DIVIDER-1", "name": "More", "type": "DIVIDER"},
	}})
}

// text is a value as the JSON text Superset keeps it as: a chart's settings, a dashboard's metadata.
func text(v map[string]any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic("the stand-in's own fixture is not JSON: " + err.Error())
	}

	return string(raw)
}

func (s *Superset) dashboards(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Filters []struct {
			Col   string `json:"col"`
			Opr   string `json:"opr"`
			Value string `json:"value"`
		} `json:"filters"`
	}

	if raw := r.URL.Query().Get("q"); raw != "" && json.Unmarshal([]byte(raw), &q) != nil {
		s.answer(w, http.StatusBadRequest, map[string]any{"message": "Not a valid rison/json argument"})

		return
	}

	all := []map[string]any{
		{"id": Dashboard, "dashboard_title": "Product usage", "slug": DashboardSlug, "published": true,
			"changed_on_utc": "2026-11-10T08:00:00.000000+0000"},
		{"id": 12, "dashboard_title": "Sales pipeline", "slug": nil, "published": false,
			"changed_on_utc": "2026-11-01T08:00:00.000000+0000"},
	}

	result := []any{}

	for _, d := range all {
		keep := true

		for _, f := range q.Filters {
			title, _ := d["dashboard_title"].(string)
			keep = keep && f.Col == "dashboard_title" && f.Opr == "ct" &&
				strings.Contains(strings.ToLower(title), strings.ToLower(f.Value))
		}

		if keep {
			result = append(result, d)
		}
	}

	s.answer(w, http.StatusOK, map[string]any{"count": len(result), "result": result})
}

func (s *Superset) dashboardCharts(w http.ResponseWriter) {
	result := []any{}

	for _, id := range []int64{ActivePeople, InactivePeople, ByUnit, ByDay, People, UnitShare, Daily, Monthly, Flows} {
		c := s.charts[id]
		result = append(result, map[string]any{"id": c.id, "slice_name": c.name, "form_data": c.params})
	}

	s.answer(w, http.StatusOK, map[string]any{"result": result})
}

// chart answers the requests on one chart: reading it, its data by its
// saved query, changing it and deleting it.
func (s *Superset) chart(w http.ResponseWriter, r *http.Request, parts []string) {
	id, err := strconv.ParseInt(parts[1], 10, 64)

	c, known := s.charts[id]
	if err != nil || !known {
		s.answer(w, http.StatusNotFound, map[string]any{"message": "Not found"})

		return
	}

	switch {
	case r.Method == http.MethodGet && len(parts) == 2:
		var saved any
		if c.saved != "" {
			saved = c.saved
		}

		s.answer(w, http.StatusOK, map[string]any{"id": c.id, "result": map[string]any{
			"slice_name": c.name, "viz_type": c.params["viz_type"], "params": text(c.params), "query_context": saved,
			"datasource_id": c.dataset, "datasource_type": "table",
		}})
	case r.Method == http.MethodGet && len(parts) == 3 && parts[2] == "data":
		if c.saved == "" {
			s.answer(w, http.StatusBadRequest, map[string]any{
				"message": "Chart has no query context saved. Please save the chart again.",
			})

			return
		}

		s.answer(w, http.StatusOK, map[string]any{"result": []any{s.result(c, []string{"day", "count"})}})
	case r.Method == http.MethodPut && len(parts) == 2:
		s.answer(w, http.StatusOK, map[string]any{"id": c.id, "result": map[string]any{"slice_name": "renamed"}})
	case r.Method == http.MethodDelete && len(parts) == 2:
		delete(s.charts, id)
		s.answer(w, http.StatusOK, map[string]any{"message": "OK"})
	default:
		s.answer(w, http.StatusNotFound, map[string]any{"message": "Not found"})
	}
}

// chartData answers a query for a chart's data. The chart is the one the
// query's form_data names; the columns are the query's own.
func (s *Superset) chartData(w http.ResponseWriter, body []byte) {
	var asked struct {
		Datasource struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"datasource"`
		Queries  []map[string]any `json:"queries"`
		FormData struct {
			SliceID int64 `json:"slice_id"`
		} `json:"form_data"`
		Format string `json:"result_format"`
		Type   string `json:"result_type"`
	}

	c, known := (*chart)(nil), false
	if json.Unmarshal(body, &asked) == nil {
		c, known = s.charts[asked.FormData.SliceID]
	}

	if !known || len(asked.Queries) == 0 || asked.Datasource.ID != c.dataset || asked.Datasource.Type != "table" ||
		asked.Format != "json" || asked.Type != "full" {
		s.answer(w, http.StatusBadRequest, map[string]any{"message": "Request is incorrect"})

		return
	}

	query := asked.Queries[0]
	s.queries = append(s.queries, query)

	columns := []string{}

	for _, key := range []string{"columns", "metrics"} {
		list, _ := query[key].([]any)
		for _, item := range list {
			switch v := item.(type) {
			case string:
				columns = append(columns, v)
			case map[string]any:
				label, _ := v["label"].(string)
				columns = append(columns, label)
			}
		}
	}

	s.answer(w, http.StatusOK, map[string]any{"result": []any{s.result(c, columns)}})
}

// result is a chart's rows as Superset answers them: one object a row. A
// column of names holds people's names, which a log must never hold.
func (s *Superset) result(c *chart, columns []string) map[string]any {
	data := make([]any, 0, c.rows)

	for i := 1; i <= c.rows; i++ {
		row := map[string]any{}

		for _, column := range columns {
			switch column {
			case "count":
				row[column] = i * 10
			case "full_name":
				row[column] = fmt.Sprintf("Person Number %d", i)
			default:
				row[column] = fmt.Sprintf("%s-%d", column, i)
			}
		}

		data = append(data, row)
	}

	var cached any
	if c.cached != "" {
		cached = c.cached
	}

	return map[string]any{
		"rowcount": c.rows, "colnames": columns, "coltypes": make([]int, len(columns)), "data": data,
		"query":       fmt.Sprintf("SELECT %s FROM dataset_%d LIMIT %d", strings.Join(columns, ", "), c.dataset, c.rows),
		"cached_dttm": cached, "error": nil, "status": "success",
	}
}

func (s *Superset) answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.t.Errorf("the stand-in could not write its answer: %v", err)
	}
}
