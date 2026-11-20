package clientapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
)

// BigQuery (issue #976): the datasets and tables of a BigQuery Connection, a table's schema, and one query. Who
// may ask is as for Superset (superset.go): the owner, and a coordinator or a Bot's token when the Connection is on
// for chats and on the Bot's list; a read run's Bot token is refused the whole API (server.go). Each call is an
// act the service proxy decides (bigquery.read, bigquery.query), with the run's key when a Routine's script asks,
// so its Approval is the run's. A query the lexer refuses is refused before it is asked. No key, JWT or token is
// in an answer or a log line, and no row is logged.

// BigQuery runs calls with a Connection's client: the integration service that holds the key.
type BigQuery interface {
	WithBigQuery(ctx context.Context, integration, label string, do func(*bigquery.Client) error) error
}

// Judge decides an act as the service proxy decides a request.
type Judge interface {
	Judge(ctx context.Context, a serviceproxy.Act) error
}

// WithBigQuery sets what runs BigQuery's calls and what decides them.
func (s *Server) WithBigQuery(b BigQuery, j Judge) *Server {
	s.bigquery, s.judge = b, j

	return s
}

var errNoBigQuery = fmt.Errorf("%w: this control plane reads no BigQuery", orchestration.ErrUnavailable)

// bigqueryAct is who asks for a BigQuery Connection, as the proxy judges it.
func (s *Server) bigqueryAct(ctx context.Context, label string, chatID, runKey *string) (serviceproxy.Act, error) {
	if s.bigquery == nil || s.judge == nil {
		return serviceproxy.Act{}, errNoBigQuery
	}

	act := serviceproxy.Act{Integration: serviceproxy.BigQueryID, Label: label}
	tok := principal(ctx)

	conn, err := s.svc.ConnectionOf(ctx, serviceproxy.BigQueryID, label)
	if err != nil && !errors.Is(err, orchestration.ErrNotFound) {
		return act, err
	}

	switch tok.Kind {
	case bearertoken.Owner:
		if err != nil {
			return act, err
		}

		act.Caller = serviceproxy.Caller{Name: "owner token " + tok.ID}
	case bearertoken.Coordinator, bearertoken.Bot:
		may, _, merr := s.mayUse(ctx, tok, conn, err == nil)
		if merr != nil {
			return act, merr
		}

		if !may {
			return act, fmt.Errorf("%w: this agent was not given %s", errForbidden,
				orchestration.Use(serviceproxy.BigQueryID, label))
		}

		chat, err := chatOf(tok, val(chatID))
		if err != nil {
			return act, err
		}

		act.Caller = serviceproxy.Caller{Name: callerName(tok), Coordinator: true, ChatID: tok.Chat}
		act.ChatID, act.RunKey = chat, val(runKey)
	default:
		return act, fmt.Errorf("%w: only the owner and a coordinator may use a BigQuery connection", errForbidden)
	}

	return act, nil
}

// judgeBigQuery decides an act: nil when it may run now.
func (s *Server) judgeBigQuery(ctx context.Context, act serviceproxy.Act, action, subject string,
	fields []orchestration.Field, parts ...string,
) error {
	digest := sha256.Sum256([]byte(strings.Join(append([]string{act.Label, action}, parts...), "\x00")))
	act.Action, act.Subject, act.Fields, act.Hash = action, subject, fields, hex.EncodeToString(digest[:])

	return relayError(s.judge.Judge(ctx, act))
}

// bigqueryError turns what BigQuery or the gate said into this API's errors. None holds a key or a token.
func bigqueryError(err error) error {
	var (
		access   *bigquery.AccessError
		canceled *bigquery.CancelError
	)

	switch {
	case err == nil:
		return nil
	case errors.Is(err, bigquery.ErrRefused), errors.As(err, &access):
		return fmt.Errorf("%w: %s", errForbidden, err.Error())
	case errors.Is(err, bigquery.ErrInvalid):
		return fmt.Errorf("%w: %s", orchestration.ErrInvalid, err.Error())
	case errors.Is(err, bigquery.ErrNotFound):
		return fmt.Errorf("%w: %s", orchestration.ErrNotFound, err.Error())
	case errors.As(err, &canceled):
		return fmt.Errorf("%w: %s", orchestration.ErrUnavailable, err.Error())
	case errors.Is(err, orchestration.ErrInvalid), errors.Is(err, orchestration.ErrNotFound),
		errors.Is(err, orchestration.ErrConflict), errors.Is(err, orchestration.ErrUnavailable):
		return err
	default:
		// What Google said of a failure of its own: the caller's to report, with no secret in it.
		return fmt.Errorf("%w: %s", orchestration.ErrUnavailable, err.Error())
	}
}

func (s *Server) logBigQuery(ctx context.Context, act serviceproxy.Act, action string, rows int) {
	s.logger.InfoContext(ctx, "a read of a BigQuery connection", slog.String("event", "bigquery.read"),
		slog.String("agent", act.Caller.Name), slog.String("connection", orchestration.Use(act.Integration, act.Label)),
		slog.String("action", action), slog.Int("rows", rows))
}

// ListBigQueryDatasets lists the datasets of a Connection's data projects.
func (s *Server) ListBigQueryDatasets(ctx context.Context, req openapi.ListBigQueryDatasetsRequestObject,
) (openapi.ListBigQueryDatasetsResponseObject, error) {
	act, err := s.bigqueryAct(ctx, req.Label, req.Params.ChatId, req.Params.HypomnemaRoutineRun)
	if err != nil {
		return nil, err
	}

	if err := s.judgeBigQuery(ctx, act, "bigquery.read", "the datasets", nil, "datasets"); err != nil {
		return nil, err
	}

	var got []bigquery.DatasetRef

	err = s.bigquery.WithBigQuery(ctx, serviceproxy.BigQueryID, req.Label, func(c *bigquery.Client) error {
		var err error
		got, err = c.Datasets(ctx)

		return err
	})
	if err != nil {
		return nil, bigqueryError(err)
	}

	s.logBigQuery(ctx, act, "bigquery.read", len(got))

	out := openapi.ListBigQueryDatasets200JSONResponse{Data: make([]openapi.BigQueryDataset, 0, len(got))}
	for _, d := range got {
		out.Data = append(out.Data, openapi.BigQueryDataset{Project: d.Project, Dataset: d.Dataset})
	}

	return out, nil
}

// ListBigQueryTables lists a dataset's tables with their types.
func (s *Server) ListBigQueryTables(ctx context.Context, req openapi.ListBigQueryTablesRequestObject,
) (openapi.ListBigQueryTablesResponseObject, error) {
	act, err := s.bigqueryAct(ctx, req.Label, req.Params.ChatId, req.Params.HypomnemaRoutineRun)
	if err != nil {
		return nil, err
	}

	if err := s.judgeBigQuery(ctx, act, "bigquery.read", "the tables of "+req.Dataset, nil, req.Dataset); err != nil {
		return nil, err
	}

	var (
		got       []bigquery.TableSummary
		truncated bool
	)

	err = s.bigquery.WithBigQuery(ctx, serviceproxy.BigQueryID, req.Label, func(c *bigquery.Client) error {
		var err error
		got, truncated, err = c.Tables(ctx, req.Dataset)

		return err
	})
	if err != nil {
		return nil, bigqueryError(err)
	}

	s.logBigQuery(ctx, act, "bigquery.read", len(got))

	out := openapi.ListBigQueryTables200JSONResponse{
		Data: make([]openapi.BigQueryTableSummary, 0, len(got)), Truncated: truncated,
	}
	for _, t := range got {
		out.Data = append(out.Data, openapi.BigQueryTableSummary{Table: t.Table, Type: t.Type})
	}

	return out, nil
}

// GetBigQueryTable reads a table's metadata and schema.
func (s *Server) GetBigQueryTable(ctx context.Context, req openapi.GetBigQueryTableRequestObject,
) (openapi.GetBigQueryTableResponseObject, error) {
	act, err := s.bigqueryAct(ctx, req.Label, req.Params.ChatId, req.Params.HypomnemaRoutineRun)
	if err != nil {
		return nil, err
	}

	if err := s.judgeBigQuery(ctx, act, "bigquery.read", req.Table, nil, req.Table); err != nil {
		return nil, err
	}

	var got bigquery.TableInfo

	err = s.bigquery.WithBigQuery(ctx, serviceproxy.BigQueryID, req.Label, func(c *bigquery.Client) error {
		var err error
		got, err = c.Table(ctx, req.Table)

		return err
	})
	if err != nil {
		return nil, bigqueryError(err)
	}

	s.logBigQuery(ctx, act, "bigquery.read", 0)

	return openapi.GetBigQueryTable200JSONResponse{
		Project: got.Project, Dataset: got.Dataset, Table: got.TableRef.Table, Type: got.Type,
		Description: opt(got.Description), Rows: int64(min(got.Rows, uint64(1<<63-1))), Bytes: got.Bytes,
		Created: got.Created, Modified: got.Modified, Partitioning: opt(got.Partitioning), Schema: toFields(got.Schema),
	}, nil
}

func toFields(in []bigquery.Field) []openapi.BigQueryField {
	out := make([]openapi.BigQueryField, 0, len(in))

	for _, f := range in {
		field := openapi.BigQueryField{Name: f.Name, Type: f.Type, Mode: opt(f.Mode), Description: opt(f.Description)}
		if len(f.Fields) > 0 {
			nested := toFields(f.Fields)
			field.Fields = &nested
		}

		out = append(out, field)
	}

	return out
}

// RunBigQueryQuery checks a query, dry-runs it and runs it under the Connection's bytes cap.
func (s *Server) RunBigQueryQuery(ctx context.Context, req openapi.RunBigQueryQueryRequestObject,
) (openapi.RunBigQueryQueryResponseObject, error) {
	act, err := s.bigqueryAct(ctx, req.Label, req.Body.ChatId, req.Params.HypomnemaRoutineRun)
	if err != nil {
		return nil, err
	}

	// A query the gate refuses is refused, never asked (design v3, decision 19).
	if err := bigquery.Gate(req.Body.Sql); err != nil {
		return nil, bigqueryError(err)
	}

	dryRun := val(req.Body.DryRun)
	fields := []orchestration.Field{{Label: "SQL", Value: req.Body.Sql}}

	if dryRun {
		fields = append(fields, orchestration.Field{Label: "Dry run", Value: "yes: nothing is billed"})
	}

	subject := strings.Join(strings.Fields(req.Body.Sql), " ")
	if len(subject) > 120 {
		subject = subject[:120] + "…"
	}

	// The mode is part of what is approved: an approval of a free dry run never lets the billed query run.
	mode := "run"
	if dryRun {
		mode = "dry-run"
	}

	if err := s.judgeBigQuery(ctx, act, "bigquery.query", subject, fields, req.Body.Sql, mode); err != nil {
		return nil, err
	}

	var got bigquery.Result

	err = s.bigquery.WithBigQuery(ctx, serviceproxy.BigQueryID, req.Label, func(c *bigquery.Client) error {
		var err error
		got, err = c.Query(ctx, bigquery.Request{
			SQL: req.Body.Sql, DryRun: dryRun, MaxRows: val(req.Body.MaxRows),
			Timeout: time.Duration(val(req.Body.TimeoutSeconds)) * time.Second, Agent: act.Caller.Name,
			Tool: "hypomnema-bigquery-query",
		})

		return err
	})
	if err != nil {
		return nil, bigqueryError(err)
	}

	s.logBigQuery(ctx, act, "bigquery.query", len(got.Rows))

	out := openapi.RunBigQueryQuery200JSONResponse{
		Project: opt(got.Project), JobId: opt(got.JobID), Location: opt(got.Location), DryRun: got.DryRun,
		StatementType: got.StatementType, BytesProcessed: got.BytesProcessed, BytesBilled: got.BytesBilled,
		MaximumBytesBilled: got.MaxBytesBilled, Columns: got.Columns, Rows: got.Rows,
		TotalRows: int64(min(got.TotalRows, uint64(1<<63-1))), Truncated: got.Truncated,
	}

	if got.CacheHit {
		out.CacheHit = &got.CacheHit
	}

	if !got.ComputedAt.IsZero() {
		out.ComputedAt = &got.ComputedAt
	}

	return out, nil
}
