// Package bigquerytest is a fake BigQuery for tests (issue #976): an httptest server that answers the REST calls
// the bigquery package makes, as MCP Toolbox's tests answer theirs (bigquerygettableinfo_test.go:203-239), and
// records each request, so a test can say that a refused query sent no job.
package bigquerytest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
	// Authorization is the request's header, to check the token the control plane sent.
	Authorization string
	// Done is closed when the request's context ended: the client gave up on it.
	Done <-chan struct{}
}

// IsJobInsert says whether it is a jobs.insert; IsDryRun whether that job is a dry run.
func (r Request) IsJobInsert() bool {
	return r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/jobs")
}

// IsDryRun says whether a jobs.insert is a dry run.
func (r Request) IsDryRun() bool {
	config, _ := r.Body["configuration"].(map[string]any)
	dry, _ := config["dryRun"].(bool)

	return r.IsJobInsert() && dry
}

// Table is a table of a dataset the fake has.
type Table struct {
	ID   string
	Type string
}

// Server is the fake. Set its fields before the first request; they are read under its lock.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	requests []Request

	// Datasets are the datasets of each project.
	Datasets map[string][]string
	// Tables are the tables of each "project.dataset".
	Tables map[string][]Table
	// DryRun is the statistics.query a dry run answers with; nil answers none. DryRunStatus answers the dry
	// run with that status and an error instead.
	DryRun       map[string]any
	DryRunStatus int
	// DryRunLocation is the location the dry run's job reference names.
	DryRunLocation string
	// HangInsert makes the real jobs.insert never answer; HangJob makes the job never complete.
	HangInsert bool
	HangJob    bool
	// JobStatement is the statement type the ended job reports; empty is SELECT.
	JobStatement string
	// Columns and Rows are the job's result, each value as BigQuery's REST sends it. Schema, when set, is the
	// result's fields in place of Columns as STRINGs, and RawRows its cells in place of Rows (a REPEATED cell is
	// a list of {"v": …}).
	Columns []string
	Rows    [][]string
	Schema  []map[string]any
	RawRows [][]any
	// ResultsStatus answers getQueryResults with that status; ResultsDelay holds each answer of it.
	ResultsStatus int
	ResultsDelay  time.Duration
	// JobRunning makes jobs.get say the job runs.
	JobRunning bool
	// DryRunMessage is the message of a refused dry run.
	DryRunMessage string
	// CancelStatus answers jobs.cancel; zero is 200.
	CancelStatus int
	// RowStatus answers tabledata.list; zero is 200.
	RowStatus int
}

// New starts a fake that answers a GA4 query.
func New(t *testing.T) *Server {
	t.Helper()

	s := &Server{
		Datasets: map[string][]string{},
		Tables:   map[string][]Table{},
		DryRun: map[string]any{
			"statementType": "SELECT", "totalBytesProcessed": "1048576",
		},
		DryRunLocation: "US",
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)

	return s
}

// Requests are those received, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Request(nil), s.requests...)
}

// Jobs are the jobs.insert requests that were no dry run, and the jobs.query requests: what may bill.
func (s *Server) Jobs() []Request {
	var out []Request

	for _, r := range s.Requests() {
		if (r.IsJobInsert() && !r.IsDryRun()) || (r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/queries")) {
			out = append(out, r)
		}
	}

	return out
}

// Cancels are the jobs.cancel requests.
func (s *Server) Cancels() []Request {
	var out []Request

	for _, r := range s.Requests() {
		if strings.HasSuffix(r.Path, "/cancel") {
			out = append(out, r)
		}
	}

	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)

	var body map[string]any

	_ = json.Unmarshal(raw, &body)

	path := strings.TrimPrefix(r.URL.Path, "/bigquery/v2")
	got := Request{
		Method: r.Method, Path: path, Query: r.URL.RawQuery, Body: body,
		Authorization: r.Header.Get("Authorization"), Done: r.Context().Done(),
	}

	s.mu.Lock()
	s.requests = append(s.requests, got)
	s.mu.Unlock()

	parts := strings.Split(strings.Trim(path, "/"), "/")
	// projects/{p}/...
	if len(parts) < 3 || parts[0] != "projects" {
		http.Error(w, `{"error":{"code":404,"message":"no such path"}}`, http.StatusNotFound)

		return
	}

	project := parts[1]

	switch {
	case r.Method == http.MethodGet && len(parts) == 3 && parts[2] == "datasets":
		s.datasets(w, project)
	case r.Method == http.MethodGet && len(parts) == 5 && parts[2] == "datasets" && parts[4] == "tables":
		s.tables(w, project, parts[3])
	case r.Method == http.MethodGet && len(parts) == 6 && parts[2] == "datasets" && parts[4] == "tables":
		s.table(w, project, parts[3], parts[5])
	case r.Method == http.MethodGet && len(parts) == 7 && parts[6] == "data":
		s.tableData(w)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "jobs":
		s.insert(w, r, project, body)
	case r.Method == http.MethodPost && len(parts) == 5 && parts[2] == "jobs" && parts[4] == "cancel":
		s.cancel(w, project, parts[3], r.URL.Query().Get("location"))
	case r.Method == http.MethodGet && len(parts) == 4 && parts[2] == "jobs":
		s.job(w, project, parts[3], r.URL.Query().Get("location"))
	case r.Method == http.MethodGet && len(parts) == 4 && parts[2] == "queries":
		s.results(w, r)
	default:
		writeError(w, http.StatusNotFound, "notFound", "no such path "+path)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, status int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	//nolint:errchkjson // maps of strings and numbers always encode; the status is already sent.
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": status, "message": message, "errors": []map[string]any{{"reason": reason, "message": message}},
	}})
}

func (s *Server) datasets(w http.ResponseWriter, project string) {
	s.mu.Lock()
	names := s.Datasets[project]
	s.mu.Unlock()

	list := []map[string]any{}
	for _, d := range names {
		list = append(list, map[string]any{
			"id": project + ":" + d, "datasetReference": map[string]any{"projectId": project, "datasetId": d},
		})
	}

	writeJSON(w, map[string]any{"datasets": list})
}

func (s *Server) tables(w http.ResponseWriter, project, dataset string) {
	s.mu.Lock()
	tables, ok := s.Tables[project+"."+dataset]
	s.mu.Unlock()

	if !ok {
		writeError(w, http.StatusNotFound, "notFound", "Not found: Dataset "+project+":"+dataset)

		return
	}

	list := []map[string]any{}
	for _, t := range tables {
		list = append(list, map[string]any{
			"type": t.Type, "tableReference": map[string]any{"projectId": project, "datasetId": dataset, "tableId": t.ID},
		})
	}

	writeJSON(w, map[string]any{"tables": list, "totalItems": len(list)})
}

func (s *Server) table(w http.ResponseWriter, project, dataset, table string) {
	s.mu.Lock()
	tables := s.Tables[project+"."+dataset]
	s.mu.Unlock()

	for _, t := range tables {
		if t.ID != table {
			continue
		}

		writeJSON(w, map[string]any{
			"tableReference": map[string]any{"projectId": project, "datasetId": dataset, "tableId": table},
			"type":           t.Type, "numRows": "12", "numBytes": "3456",
			"creationTime": fmt.Sprint(time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC).UnixMilli()),
			"schema": map[string]any{"fields": []map[string]any{
				{"name": "event_date", "type": "STRING"},
				{"name": "event_name", "type": "STRING"},
				{"name": "event_params", "type": "RECORD", "mode": "REPEATED", "fields": []map[string]any{
					{"name": "key", "type": "STRING"},
				}},
			}},
		})

		return
	}

	writeError(w, http.StatusNotFound, "notFound", "Not found: Table "+project+":"+dataset+"."+table)
}

func (s *Server) tableData(w http.ResponseWriter) {
	s.mu.Lock()
	status := s.RowStatus
	s.mu.Unlock()

	if status != 0 {
		writeError(w, status, "accessDenied", "Access Denied: tabledata.list")

		return
	}

	writeJSON(w, map[string]any{"totalRows": "1", "rows": []map[string]any{
		{"f": []map[string]any{{"v": "20261119"}, {"v": "a row the check drops"}, {"v": []any{}}}},
	}})
}

func (s *Server) insert(w http.ResponseWriter, r *http.Request, project string, body map[string]any) {
	config, _ := body["configuration"].(map[string]any)
	ref, _ := body["jobReference"].(map[string]any)

	s.mu.Lock()
	dryStats, dryStatus, location := s.DryRun, s.DryRunStatus, s.DryRunLocation
	hang, hangJob := s.HangInsert, s.HangJob
	s.mu.Unlock()

	if dry, _ := config["dryRun"].(bool); dry {
		if dryStatus != 0 {
			s.mu.Lock()
			message := s.DryRunMessage
			s.mu.Unlock()

			if message == "" {
				message = "Syntax error: the fake refused the query"
			}

			writeError(w, dryStatus, "invalidQuery", message)

			return
		}

		job := map[string]any{
			"jobReference": map[string]any{"projectId": project, "location": location},
			"status":       map[string]any{"state": "DONE"},
		}
		if dryStats != nil {
			job["statistics"] = map[string]any{"query": dryStats}
		}

		writeJSON(w, job)

		return
	}

	if hang {
		<-r.Context().Done()

		return
	}

	state := "DONE"
	if hangJob {
		state = "RUNNING"
	}

	writeJSON(w, map[string]any{
		"jobReference":  ref,
		"configuration": config,
		"status":        map[string]any{"state": state},
	})
}

func (s *Server) cancel(w http.ResponseWriter, project, job, location string) {
	s.mu.Lock()
	status := s.CancelStatus
	s.mu.Unlock()

	if status != 0 {
		writeError(w, status, "notFound", "Not found: Job "+project+":"+location+"."+job)

		return
	}

	writeJSON(w, map[string]any{"job": map[string]any{
		"jobReference": map[string]any{"projectId": project, "jobId": job, "location": location},
		"status":       map[string]any{"state": "RUNNING"},
	}})
}

func (s *Server) job(w http.ResponseWriter, project, job, location string) {
	s.mu.Lock()
	statement, hang := s.JobStatement, s.HangJob || s.JobRunning
	s.mu.Unlock()

	if statement == "" {
		statement = "SELECT"
	}

	state := "DONE"
	if hang {
		state = "RUNNING"
	}

	writeJSON(w, map[string]any{
		"jobReference":  map[string]any{"projectId": project, "jobId": job, "location": location},
		"configuration": map[string]any{"query": map[string]any{"query": "SELECT"}},
		"status":        map[string]any{"state": state},
		"statistics": map[string]any{
			"creationTime": "1763510400000", "startTime": "1763510400000", "endTime": "1763510401000",
			"query": map[string]any{
				"statementType": statement, "totalBytesProcessed": "1048576", "totalBytesBilled": "10485760",
			},
		},
	})
}

func (s *Server) results(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	columns, hang, fields, status, delay := s.Columns, s.HangJob, s.Schema, s.ResultsStatus, s.ResultsDelay
	rows := s.RawRows

	if rows == nil {
		for _, r := range s.Rows {
			row := make([]any, len(r))
			for i, v := range r {
				row[i] = v
			}

			rows = append(rows, row)
		}
	}
	s.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	if status != 0 {
		writeError(w, status, "accessDenied", "Access Denied: the fake refused the results")

		return
	}

	if hang {
		writeJSON(w, map[string]any{"jobComplete": false})

		return
	}

	if fields == nil {
		for _, c := range columns {
			fields = append(fields, map[string]any{"name": c, "type": "STRING"})
		}
	}

	start := 0
	if token := r.URL.Query().Get("pageToken"); token != "" {
		_, _ = fmt.Sscan(token, &start)
	}

	page := rows[min(start, len(rows)):]

	size := len(page)
	if asked := r.URL.Query().Get("maxResults"); asked != "" {
		_, _ = fmt.Sscan(asked, &size)
	}

	if size > len(page) {
		size = len(page)
	}

	out := map[string]any{
		"jobComplete": true, "totalRows": fmt.Sprint(len(rows)),
		"schema": map[string]any{"fields": fields},
	}

	list := []map[string]any{}

	for _, row := range page[:size] {
		cells := []map[string]any{}
		for _, v := range row {
			cells = append(cells, map[string]any{"v": v})
		}

		list = append(list, map[string]any{"f": cells})
	}

	out["rows"] = list
	if start+size < len(rows) && size > 0 {
		out["pageToken"] = fmt.Sprint(start + size)
	}

	writeJSON(w, out)
}
