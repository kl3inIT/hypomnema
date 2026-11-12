package clientapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset"
)

// Superset (issue #203): the reads of a dashboard's charts, and any other
// request to a Connection's API. The owner may ask, and a coordinator token
// when the owner switched the Connection on for chats, as for every
// Connection; a worker has no token of this API. Every request to Superset
// goes through the service proxy's decision (Relay), so the owner's answer
// for its action holds here as it does for an agent's tool. No token and no
// password is in an answer or a log line, and no row is logged.

// Relay sends a request to a Connection that has its own address, through
// the decision of the control plane's service proxy.
type Relay interface {
	Relay(ctx context.Context, c serviceproxy.Call) (serviceproxy.Answer, serviceproxy.Action, error)
}

// WithRelay sets the service proxy that relays requests to Superset.
func (s *Server) WithRelay(r Relay) *Server {
	s.relay = r

	return s
}

// supersetCall is who asks for a Superset Connection, as the relay takes it.
// A coordinator token is refused a Connection the owner did not switch on
// for chats, with the same answer as for one that does not exist: an agent
// learns nothing of accounts it may not use (the agent API's rule).
func (s *Server) supersetCall(ctx context.Context, label string, chatID, runKey *string,
) (serviceproxy.Call, error) {
	if s.relay == nil {
		return serviceproxy.Call{}, errNoApprovals
	}

	call := serviceproxy.Call{Integration: serviceproxy.SupersetID, Label: label}
	tok := principal(ctx)

	conn, err := s.svc.ConnectionOf(ctx, serviceproxy.SupersetID, label)
	if err != nil && !errors.Is(err, orchestration.ErrNotFound) {
		return serviceproxy.Call{}, err
	}

	switch tok.Kind {
	case bearertoken.Owner:
		if err != nil {
			return serviceproxy.Call{}, err
		}

		call.Caller = serviceproxy.Caller{Name: "owner token " + tok.ID}
	case bearertoken.Coordinator, bearertoken.Bot:
		if err != nil || !conn.Chats {
			return serviceproxy.Call{}, fmt.Errorf("%w: this agent was not given %s", errForbidden,
				orchestration.Use(serviceproxy.SupersetID, label))
		}

		// A pi's token asks from its own chat, whatever chat the request names (issue #260).
		chat, err := chatOf(tok, val(chatID))
		if err != nil {
			return serviceproxy.Call{}, err
		}

		call.Caller = serviceproxy.Caller{Name: callerName(tok), Coordinator: true, ChatID: tok.Chat}
		call.ChatID = chat
		call.RunKey = val(runKey)
	default:
		return serviceproxy.Call{}, fmt.Errorf("%w: only the owner and a coordinator may use a Superset connection",
			errForbidden)
	}

	return call, nil
}

// relayOf is the way the reads reach Superset for one caller: each request
// goes through the service proxy's decision.
func (s *Server) relayOf(call serviceproxy.Call) superset.Relay {
	return func(ctx context.Context, method, target string, body []byte) (int, []byte, error) {
		call.Method, call.Target, call.Body, call.ContentType = method, target, body, ""
		if body != nil {
			call.ContentType = "application/json"
		}

		answer, _, err := s.relay.Relay(ctx, call)

		return answer.Status, answer.Body, relayError(err)
	}
}

// relayError turns a request the proxy did not send into this API's refusal,
// with what the proxy says to do next.
func relayError(err error) error {
	var refused *serviceproxy.RefusedError
	if errors.As(err, &refused) {
		return fmt.Errorf("%w: %s", errForbidden, refused.Message)
	}

	return err
}

// logSupersetRead writes who read what of a Connection and how many rows it
// was. The rows are never logged.
func (s *Server) logSupersetRead(
	ctx context.Context, call serviceproxy.Call, action, dashboard, chart string, rows int,
) {
	attrs := []any{
		slog.String("event", "superset.read"), slog.String("agent", call.Caller.Name),
		slog.String("connection", orchestration.Use(call.Integration, call.Label)), slog.String("action", action),
		slog.Int("rows", rows),
	}

	if dashboard != "" {
		attrs = append(attrs, slog.String("dashboard", dashboard))
	}

	if chart != "" {
		attrs = append(attrs, slog.String("chart", chart))
	}

	s.logger.InfoContext(ctx, "a read of a Superset connection", attrs...)
}

// ListSupersetDashboards lists a Connection's dashboards by title.
func (s *Server) ListSupersetDashboards(ctx context.Context, req openapi.ListSupersetDashboardsRequestObject,
) (openapi.ListSupersetDashboardsResponseObject, error) {
	call, err := s.supersetCall(ctx, req.Label, req.Params.ChatId, req.Params.HypomnemaRoutineRun)
	if err != nil {
		return nil, err
	}

	found, err := superset.Dashboards(ctx, s.relayOf(call), val(req.Params.Q))
	if err != nil {
		return nil, err
	}

	s.logSupersetRead(ctx, call, "superset.dashboards.read", "", "", len(found))

	out := openapi.ListSupersetDashboards200JSONResponse{Data: make([]openapi.SupersetDashboardSummary, 0, len(found))}

	for _, d := range found {
		summary := openapi.SupersetDashboardSummary{Id: d.ID, Title: d.Title, Slug: opt(d.Slug), Published: d.Published}
		if !d.ChangedAt.IsZero() {
			summary.ChangedAt = &d.ChangedAt
		}

		out.Data = append(out.Data, summary)
	}

	return out, nil
}

// GetSupersetDashboard returns a dashboard with its charts and its filters.
func (s *Server) GetSupersetDashboard(ctx context.Context, req openapi.GetSupersetDashboardRequestObject,
) (openapi.GetSupersetDashboardResponseObject, error) {
	call, err := s.supersetCall(ctx, req.Label, req.Params.ChatId, req.Params.HypomnemaRoutineRun)
	if err != nil {
		return nil, err
	}

	d, err := superset.GetDashboard(ctx, s.relayOf(call), req.Dashboard)
	if err != nil {
		return nil, err
	}

	s.logSupersetRead(ctx, call, "superset.dashboards.read", req.Dashboard, "", len(d.Charts))

	out := openapi.GetSupersetDashboard200JSONResponse{
		Id: d.ID, Title: d.Title, Slug: opt(d.Slug),
		Charts:  make([]openapi.SupersetChart, 0, len(d.Charts)),
		Filters: make([]openapi.SupersetFilter, 0, len(d.Filters)),
	}

	for _, c := range d.Charts {
		out.Charts = append(out.Charts, openapi.SupersetChart{
			Id: c.ID, Name: c.Name, Kind: c.Kind, DatasetId: c.DatasetID, Dataset: opt(c.Dataset), Read: c.Read,
		})
	}

	for _, f := range d.Filters {
		filter := openapi.SupersetFilter{Id: f.ID, Name: f.Name, Kind: f.Kind, Column: opt(f.Column), Charts: f.Charts}
		if f.DatasetID != 0 {
			filter.DatasetId = &f.DatasetID
		}

		if len(f.Default) > 0 {
			filter.Default = f.Default
		}

		out.Filters = append(out.Filters, filter)
	}

	return out, nil
}

// GetSupersetChartData returns a chart's rows as its dashboard shows them.
func (s *Server) GetSupersetChartData(ctx context.Context, req openapi.GetSupersetChartDataRequestObject,
) (openapi.GetSupersetChartDataResponseObject, error) {
	call, err := s.supersetCall(ctx, req.Label, req.Params.ChatId, req.Params.HypomnemaRoutineRun)
	if err != nil {
		return nil, err
	}

	chart := strconv.FormatInt(req.Chart, 10)

	data, err := superset.GetChartData(ctx, s.relayOf(call), req.Dashboard, chart, s.now())
	if err != nil {
		return nil, err
	}

	s.logSupersetRead(ctx, call, "superset.charts.read", req.Dashboard, chart, len(data.Rows))

	if req.Params.Format != nil && *req.Params.Format == openapi.Csv {
		raw, err := superset.CSV(data)
		if err != nil {
			return nil, err
		}

		return openapi.GetSupersetChartData200TextcsvResponse{Body: bytes.NewReader(raw), ContentLength: int64(len(raw))}, nil
	}

	return openapi.GetSupersetChartData200JSONResponse{
		DashboardId: data.DashboardID, ChartId: data.ChartID, Name: data.Name, Kind: data.Kind,
		Source: openapi.SupersetChartDataSource(data.Source), Columns: data.Columns, Rows: data.Rows,
		RowCount: len(data.Rows), Sql: data.SQL, ComputedAt: data.ComputedAt, Cached: data.Cached,
	}, nil
}

// RelaySupersetRequest sends one request to a Connection's API and returns
// Superset's answer unchanged, whatever its status.
func (s *Server) RelaySupersetRequest(ctx context.Context, req openapi.RelaySupersetRequestRequestObject,
) (openapi.RelaySupersetRequestResponseObject, error) {
	call, err := s.supersetCall(ctx, req.Label, req.Body.ChatId, req.Params.HypomnemaRoutineRun)
	if err != nil {
		return nil, err
	}

	call.Method, call.Target = string(req.Body.Method), req.Body.Path
	if req.Body.Body != nil {
		call.Body, call.ContentType = []byte(*req.Body.Body), "application/json"
	}

	answer, action, err := s.relay.Relay(ctx, call)
	if err != nil {
		return nil, relayError(err)
	}

	s.logger.InfoContext(ctx, "a request relayed to a Superset connection",
		slog.String("event", "superset.relayed"), slog.String("agent", call.Caller.Name),
		slog.String("connection", orchestration.Use(call.Integration, call.Label)), slog.String("action", action.ID),
		slog.String("method", call.Method), slog.Int("status", answer.Status), slog.Int("bytes", len(answer.Body)))

	out := openapi.RelaySupersetRequest200JSONResponse{
		Status: answer.Status, ContentType: answer.ContentType, Body: string(answer.Body), Action: action.ID,
	}

	// A picture or an archive is not text: it travels in base64.
	if !utf8.Valid(answer.Body) {
		out.Body, out.Base64 = base64.StdEncoding.EncodeToString(answer.Body), true
	}

	return out, nil
}
