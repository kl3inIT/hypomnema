package agentapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Superset's tools (issue #536, step 3): one a command of `hypomnema
// superset` (issue #203), for a worker and a Bot's pi, so that a worker,
// which has no client API, can read the dashboards its Run was given. They
// are made as the references make an Integration's tools: OpenClaw's Feishu
// tools are one family of tools named after the service, each with an
// optional accountId (extensions/feishu/src/tool-account.ts), and are not
// registered at all when no enabled account has them
// (extensions/feishu/src/tool-registration.ts: registerTool returns null);
// OpenMausBot mounts its research family only when a credential is held and
// the bot's switch is on (server/drivers/agents-catalog.ts) and relays any
// other request with one generic research_call (agents-research-tools.ts);
// Superset's own MCP servers name theirs superset_<thing>_<verb>
// (aptro/superset-mcp main.py: superset_dashboard_list). So:
//
//   - superset_dashboards, superset_dashboard, superset_chart_data and
//     superset_api, the four commands, read and relayed as the client API's
//     routes are (clientapi/superset.go), through the service proxy's
//     decision (Relay), so a write waits for the owner's Approval exactly as
//     the command's does;
//   - listed only when the agent may use at least one Superset Connection: a
//     worker those its Run was given, a Bot's pi those switched on for chats
//     and on its Bot's list (the agent's may, which every call checks again);
//   - with account, the Connection's label, only when it may use several
//     (inference: OpenClaw's accountId is always there and optional, named
//     here only when there is a choice, so a one-account agent is not asked).
//
// The owner's token, the coordinator token and a Routine's script have no
// MCP and keep the commands.
//
// A Bot the owner limited to some dashboards of a Connection reads them
// through the three reads and is refused superset_api for it (issue #562,
// superset.Scope), as the commands are; superset_api is not listed when
// every account it has is limited, as OpenClaw does not register a tool no
// account may use (extensions/feishu/src/tool-registration.ts).

// The tools' names.
const (
	toolSupersetDashboards = agenttool.ToolSupersetDashboards
	toolSupersetDashboard  = agenttool.ToolSupersetDashboard
	toolSupersetChartData  = agenttool.ToolSupersetChartData
	toolSupersetAPI        = agenttool.ToolSupersetAPI
)

var supersetTools = []string{toolSupersetDashboards, toolSupersetDashboard, toolSupersetChartData, toolSupersetAPI}

// Relay sends a request to a Connection that has its own address, through
// the decision of the control plane's service proxy (serviceproxy.Proxy).
type Relay interface {
	Relay(ctx context.Context, c serviceproxy.Call) (serviceproxy.Answer, serviceproxy.Action, error)
}

// WithRelay gives agents Superset's tools.
func (s *Server) WithRelay(r Relay) *Server {
	s.relay = r

	return s
}

// supersetLabels are the Superset Connections an agent of its kind uses with
// the tools: none when it is not given them.
func (s *Server) supersetLabels(ctx context.Context, a agent) ([]string, error) {
	if s.relay == nil || !(a.dispatchID != "" || a.chatID != "" && a.bot != "") {
		return nil, nil
	}

	allowed, err := s.allowed(ctx, a, serviceproxy.SupersetID)
	if err != nil {
		return nil, err
	}

	labels := make([]string, 0, len(allowed))
	for _, c := range allowed {
		labels = append(labels, c.Label)
	}

	slices.Sort(labels)

	return labels, nil
}

// supersetToolList is Superset's tools as an agent is offered them. A
// Connection list that cannot be read takes none of the agent's other tools
// away.
func (s *Server) supersetToolList(ctx context.Context, a agent) []agenttool.BrowserTool {
	labels, err := s.supersetLabels(ctx, a)
	if err != nil {
		s.logger.WarnContext(ctx, "the Superset accounts of an agent could not be read",
			slog.String("event", "agentapi.superset_failed"), slog.String("error", err.Error()))

		return nil
	}

	if len(labels) == 0 {
		return nil
	}

	with := func(properties map[string]any, required ...string) json.RawMessage {
		if len(labels) > 1 {
			properties["account"] = map[string]any{"type": "string", "enum": labels,
				"description": "The Superset account to use."}
			required = append(required, "account")
		}

		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}

		raw, _ := json.Marshal(schema) //nolint:errchkjson // maps of strings and slices always encode

		return raw
	}

	dashboard := map[string]any{"type": "string", "description": "The dashboard's number or slug."}

	tools := []agenttool.BrowserTool{
		{
			Name: toolSupersetDashboards,
			Description: "List the owner's Superset dashboards by title: each one's number, title, slug and when " +
				"it was last changed, at most 100. Start here to find a dashboard's number.",
			InputSchema: with(map[string]any{
				"search": map[string]any{"type": "string", "description": "Only the titles that contain this."},
			}),
		},
		{
			Name: toolSupersetDashboard,
			Description: "Read a Superset dashboard: its charts (number, name, kind, dataset, and whether its " +
				"data can be read) and its filters with the value each has when the dashboard opens.",
			InputSchema: with(map[string]any{"dashboard": dashboard}, "dashboard"),
		},
		{
			Name: toolSupersetChartData,
			Description: "Read a chart's rows as its dashboard shows them when it opens: columns, rows, row_count, " +
				"the sql Superset ran, computed_at (when the figures were computed; say it when you report one) " +
				"and cached. The chart's own row limit holds. format csv gives the rows alone with a header line.",
			InputSchema: with(map[string]any{
				"dashboard": dashboard,
				"chart":     map[string]any{"type": "integer", "description": "The chart's number."},
				"format":    map[string]any{"type": "string", "enum": []string{"json", "csv"}},
			}, "dashboard", "chart"),
		},
		{
			Name: toolSupersetAPI,
			Description: "Send one request to Superset's API, for what the other superset tools do not read " +
				"(a dataset's columns, the list of charts, a saved query), and get Superset's answer as it came: " +
				"status, content_type, body (base64 when it is not text) and the action it was. A read runs at " +
				"once; creating, changing or deleting something, running SQL and administering users wait for " +
				"the owner's approval, and the answer then says what to do. Superset describes its API at GET " +
				"/api/v1/_openapi.",
			InputSchema: with(map[string]any{
				"method": map[string]any{"type": "string", "enum": []string{
					http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
				}},
				"path": map[string]any{"type": "string", "description": "/api/v1/..., with its query: " +
					`/api/v1/chart/?q={"page_size":20}`},
				"body": map[string]any{"type": "object", "description": "The request's JSON body."},
			}, "method", "path"),
		},
	}

	if slices.ContainsFunc(labels, func(label string) bool { return !a.scopeOf(label).Limited }) {
		return tools
	}

	return slices.DeleteFunc(tools, func(t agenttool.BrowserTool) bool { return t.Name == toolSupersetAPI })
}

// scopeOf is the dashboards the agent may read of a Connection.
func (a agent) scopeOf(label string) superset.Scope {
	if a.scope == nil {
		return superset.Scope{}
	}

	return a.scope(label)
}

// supersetArgs are the arguments of every Superset tool.
type supersetArgs struct {
	Account   string          `json:"account"`
	Search    string          `json:"search"`
	Dashboard json.RawMessage `json:"dashboard"`
	Chart     json.RawMessage `json:"chart"`
	Format    string          `json:"format"`
	Method    string          `json:"method"`
	Path      string          `json:"path"`
	Body      json.RawMessage `json:"body"`
}

// idOf reads a dashboard or chart named by its number or, for a dashboard,
// its slug: a model writes a number as a string as often as not.
func idOf(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}

	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}

	return ""
}

// supersetCall runs one of Superset's tools and answers with the tool's
// result as MCP has it. What the agent can act on (an account it was not
// given, a dashboard not found, a request held or refused by the owner's
// answer) is its result, marked as an error.
func (s *Server) supersetCall(w http.ResponseWriter, r *http.Request, a agent, labels []string,
	call agenttool.BrowserCall,
) {
	var in supersetArgs
	if json.Unmarshal(orEmpty(call.Arguments), &in) != nil {
		answerTool(w, call.Tool+" takes a JSON object of its arguments", true)

		return
	}

	label, fix := pickAccount(labels, strings.TrimSpace(in.Account))
	if fix != "" {
		answerTool(w, fix, true)

		return
	}

	rc := serviceproxy.Call{
		Integration: serviceproxy.SupersetID, Label: label, ChatID: a.chatID,
		Caller: serviceproxy.Caller{
			Name: a.name, DispatchID: a.dispatchID, Coordinator: a.dispatchID == "", ChatID: a.chatID,
		},
	}

	out, rows, err := s.runSuperset(r.Context(), rc, a.scopeOf(label), call.Tool, in)

	var refused *serviceproxy.RefusedError

	switch {
	case err == nil:
	case errors.As(err, &refused):
		answerTool(w, refused.Message, true)

		return
	case errors.Is(err, orchestration.ErrInvalid) || errors.Is(err, orchestration.ErrNotFound) ||
		errors.Is(err, orchestration.ErrConflict) || errors.Is(err, orchestration.ErrUnavailable) ||
		errors.Is(err, orchestration.ErrForbidden):
		if superset.OutOfScope(err) {
			// A limited Bot asked for what its dashboards leave out (issue #562), on the commands' event.
			s.logger.InfoContext(r.Context(), "a read of a Superset connection refused: out of scope",
				slog.String("event", "superset.read"), slog.String("agent", a.name), slog.String("tool", call.Tool),
				slog.String("connection", orchestration.Use(serviceproxy.SupersetID, label)),
				slog.String("refused", "out of scope"))
		}

		answerTool(w, err.Error(), true)

		return
	default:
		s.fail(w, r, err)

		return
	}

	// Who read what and how much; never a row.
	s.logger.InfoContext(r.Context(), "an agent used a Superset connection",
		slog.String("event", "agentapi.superset"), slog.String("agent", a.name), slog.String("tool", call.Tool),
		slog.String("connection", orchestration.Use(serviceproxy.SupersetID, label)), slog.Int("rows", rows))

	answerTool(w, out.text, out.isError)
}

// pickAccount is the Connection a call is for: the one named, or the only
// one; with several and none named nothing is guessed, as the commands'
// --connection is not. One not given answers as one that does not exist.
func pickAccount(labels []string, asked string) (string, string) {
	switch {
	case asked != "" && !slices.Contains(labels, asked):
		return "", "this agent was not given " + orchestration.Use(serviceproxy.SupersetID, asked)
	case asked != "":
		return asked, ""
	case len(labels) == 1:
		return labels[0], ""
	default:
		return "", "name the account: " + strings.Join(labels, ", ")
	}
}

// toolResult is a tool's text and whether it is an error.
type toolResult struct {
	text    string
	isError bool
}

// runSuperset runs one tool for a Connection and returns its text, and how
// many rows or items it read, for the log.
func (s *Server) runSuperset(ctx context.Context, rc serviceproxy.Call, scope superset.Scope, tool string,
	in supersetArgs,
) (toolResult, int, error) {
	relay := s.supersetRelay(rc)

	switch tool {
	case toolSupersetDashboards:
		found, err := scope.ListDashboards(ctx, relay, in.Search)
		if err != nil {
			return toolResult{}, 0, err
		}

		return jsonResult(dashboardsAnswer(found)), len(found), nil
	case toolSupersetDashboard:
		id := idOf(in.Dashboard)
		if id == "" {
			return toolResult{text: "name the dashboard by its number or slug", isError: true}, 0, nil
		}

		d, err := scope.Dashboard(ctx, relay, id)
		if err != nil {
			return toolResult{}, 0, err
		}

		return jsonResult(dashboardAnswer(d)), len(d.Charts), nil
	case toolSupersetChartData:
		return s.chartData(ctx, relay, scope, in)
	default:
		if err := scope.RelayRefused(orchestration.Use(rc.Integration, rc.Label)); err != nil {
			return toolResult{}, 0, err
		}

		return s.relayRequest(ctx, rc, in)
	}
}

// supersetRelay is the way the reads reach Superset for one caller: each
// request goes through the service proxy's decision, as the client API's do.
func (s *Server) supersetRelay(rc serviceproxy.Call) superset.Relay {
	return func(ctx context.Context, method, target string, body []byte) (int, []byte, error) {
		rc.Method, rc.Target, rc.Body, rc.ContentType = method, target, body, ""
		if body != nil {
			rc.ContentType = "application/json"
		}

		answer, _, err := s.relay.Relay(ctx, rc)

		return answer.Status, answer.Body, err
	}
}

func (s *Server) chartData(ctx context.Context, relay superset.Relay, scope superset.Scope, in supersetArgs,
) (toolResult, int, error) {
	dashboard, chart := idOf(in.Dashboard), idOf(in.Chart)
	if n, _ := strconv.ParseInt(chart, 10, 64); dashboard == "" || n <= 0 {
		return toolResult{text: "name the dashboard by its number or slug and the chart by its number", isError: true},
			0, nil
	}

	if in.Format != "" && in.Format != "json" && in.Format != "csv" {
		return toolResult{text: "format is json or csv", isError: true}, 0, nil
	}

	data, err := scope.ChartData(ctx, relay, dashboard, chart, time.Now().UTC())
	if err != nil {
		return toolResult{}, 0, err
	}

	if in.Format == "csv" {
		raw, err := superset.CSV(data)
		if err != nil {
			return toolResult{}, 0, err
		}

		return toolResult{text: string(raw)}, len(data.Rows), nil
	}

	return jsonResult(chartDataAnswer{
		DashboardID: data.DashboardID, ChartID: data.ChartID, Name: data.Name, Kind: data.Kind, Source: data.Source,
		Columns: data.Columns, Rows: data.Rows, RowCount: len(data.Rows), SQL: data.SQL, ComputedAt: data.ComputedAt,
		Cached: data.Cached,
	}), len(data.Rows), nil
}

// relayRequest sends superset_api's request and gives Superset's answer
// unchanged, as the client API's POST .../api does; one that is not a
// success is the tool's error, as the command fails with its status.
func (s *Server) relayRequest(ctx context.Context, rc serviceproxy.Call, in supersetArgs) (toolResult, int, error) {
	rc.Method, rc.Target = strings.ToUpper(strings.TrimSpace(in.Method)), strings.TrimSpace(in.Path)
	if rc.Method == "" || rc.Target == "" {
		return toolResult{text: "give the method and the path, /api/v1/...", isError: true}, 0, nil
	}

	if body := in.Body; len(body) > 0 && string(body) != "null" {
		// A body written as a string of JSON is sent as that JSON.
		var text string
		if json.Unmarshal(body, &text) == nil {
			body = json.RawMessage(text)
		}

		rc.Body, rc.ContentType = body, "application/json"
	}

	answer, action, err := s.relay.Relay(ctx, rc)
	if err != nil {
		return toolResult{}, 0, err
	}

	out := relayAnswer{Status: answer.Status, ContentType: answer.ContentType, Body: string(answer.Body),
		Action: action.ID}

	// A picture or an archive is not text: it travels in base64.
	if !utf8.Valid(answer.Body) {
		out.Body, out.Base64 = base64.StdEncoding.EncodeToString(answer.Body), true
	}

	result := jsonResult(out)
	result.isError = answer.Status < 200 || answer.Status > 299

	return result, 0, nil
}

// The tools' answers, in the client API's shapes (openapi.yaml: Superset…).
type (
	dashboardSummary struct {
		ID        int64      `json:"id"`
		Title     string     `json:"title"`
		Slug      string     `json:"slug,omitempty"`
		Published bool       `json:"published"`
		ChangedAt *time.Time `json:"changed_at,omitempty"`
	}
	dashboardsList struct {
		Data []dashboardSummary `json:"data"`
	}
	chartSummary struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		DatasetID int64  `json:"dataset_id"`
		Dataset   string `json:"dataset,omitempty"`
		Read      bool   `json:"read"`
	}
	filterSummary struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Kind      string          `json:"kind"`
		Column    string          `json:"column,omitempty"`
		DatasetID int64           `json:"dataset_id,omitempty"`
		Default   json.RawMessage `json:"default,omitempty"`
		Charts    []int64         `json:"charts"`
	}
	dashboardDetail struct {
		ID      int64           `json:"id"`
		Title   string          `json:"title"`
		Slug    string          `json:"slug,omitempty"`
		Charts  []chartSummary  `json:"charts"`
		Filters []filterSummary `json:"filters"`
	}
	chartDataAnswer struct {
		DashboardID int64     `json:"dashboard_id"`
		ChartID     int64     `json:"chart_id"`
		Name        string    `json:"name"`
		Kind        string    `json:"kind"`
		Source      string    `json:"source"`
		Columns     []string  `json:"columns"`
		Rows        [][]any   `json:"rows"`
		RowCount    int       `json:"row_count"`
		SQL         string    `json:"sql"`
		ComputedAt  time.Time `json:"computed_at"`
		Cached      bool      `json:"cached"`
	}
	relayAnswer struct {
		Status      int    `json:"status"`
		ContentType string `json:"content_type,omitempty"`
		Body        string `json:"body"`
		Base64      bool   `json:"base64,omitempty"`
		Action      string `json:"action,omitempty"`
	}
)

func dashboardsAnswer(found []superset.DashboardSummary) dashboardsList {
	out := dashboardsList{Data: make([]dashboardSummary, 0, len(found))}

	for _, d := range found {
		summary := dashboardSummary{ID: d.ID, Title: d.Title, Slug: d.Slug, Published: d.Published}
		if !d.ChangedAt.IsZero() {
			summary.ChangedAt = &d.ChangedAt
		}

		out.Data = append(out.Data, summary)
	}

	return out
}

func dashboardAnswer(d superset.Dashboard) dashboardDetail {
	out := dashboardDetail{ID: d.ID, Title: d.Title, Slug: d.Slug,
		Charts: make([]chartSummary, 0, len(d.Charts)), Filters: make([]filterSummary, 0, len(d.Filters))}

	for _, c := range d.Charts {
		out.Charts = append(out.Charts, chartSummary{ID: c.ID, Name: c.Name, Kind: c.Kind, DatasetID: c.DatasetID,
			Dataset: c.Dataset, Read: c.Read})
	}

	for _, f := range d.Filters {
		out.Filters = append(out.Filters, filterSummary{ID: f.ID, Name: f.Name, Kind: f.Kind, Column: f.Column,
			DatasetID: f.DatasetID, Default: f.Default, Charts: f.Charts})
	}

	return out
}

// jsonResult is an answer as the tool's text: JSON on one line.
func jsonResult(v any) toolResult {
	raw, err := json.Marshal(v)
	if err != nil {
		return toolResult{text: fmt.Sprintf("the answer could not be written: %v", err), isError: true}
	}

	return toolResult{text: string(raw)}
}
