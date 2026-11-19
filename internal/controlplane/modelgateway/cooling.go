package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
)

type modelCooling struct {
	first time.Time
	other bool
}

func (c *modelCooling) add(err error) bool {
	var cooling *CoolingError
	switch {
	case errors.As(err, &cooling):
		if c.first.IsZero() || cooling.Until.Before(c.first) {
			c.first = cooling.Until
		}
		return true
	case errors.Is(err, ErrNoAccount):
		return true
	default:
		c.other = true
		return false
	}
}

func (s *Server) coolingResponse(ctx context.Context, route string, c modelCooling, record *Usage) *http.Response {
	if c.other || c.first.IsZero() {
		return nil
	}

	now := s.svc.clock.Now()
	kept := context.WithoutCancel(ctx)
	if err := s.svc.write(kept, func(q *store.Queries, _ int64) error {
		return q.RecordModelCooling(kept, store.RecordModelCoolingParams{
			Route: route, Model: record.Model, ObservedAt: now.UnixMilli(), ResetsAt: c.first.UnixMilli(),
		})
	}); err != nil {
		s.logger.ErrorContext(ctx, "recording when a gateway model is free", slog.String("model", record.Model),
			slog.String("route", route), slog.String("error", err.Error()))
	}

	noteServed(ctx, "")
	record.Status, record.Outcome = http.StatusTooManyRequests, OutcomeNoAccount
	record.Error = accountsCoolingMessage

	return accountCoolingResponse(route, c.first, now)
}

func accountCoolingResponse(route string, until, now time.Time) *http.Response {
	kind, envelope := "rate_limit_error", ""
	if route == "/v1/messages" || route == "/v1/messages/count_tokens" {
		envelope = "error"
	} else if route == "/v1/responses" && until.Sub(now) > codexWaitsUpTo {
		kind = "usage_limit_reached"
	}
	var body struct {
		Type  string `json:"type,omitempty"`
		Error struct {
			Message  string `json:"message"`
			Type     string `json:"type"`
			Code     string `json:"code"`
			ResetsAt int64  `json:"resets_at"`
		} `json:"error"`
	}
	body.Type = envelope
	body.Error.Message, body.Error.Type, body.Error.Code = accountsCoolingMessage, kind, "account_unavailable"
	body.Error.ResetsAt = until.Add(time.Second - time.Nanosecond).Unix()
	encoded, _ := json.Marshal(body) //nolint:errchkjson // Strings and a number always encode.
	wait := max(0, (until.Sub(now)+time.Second-time.Nanosecond)/time.Second)
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": {"application/json"}, "Retry-After": {strconv.Itoa(int(wait))}},
		Body:       io.NopCloser(bytes.NewReader(encoded)),
	}
}

func relayCooling(w http.ResponseWriter, resp *http.Response) {
	w.Header().Set("Retry-After", resp.Header.Get("Retry-After"))
	copyBody(w, resp, resp.Header.Get("Content-Type"), maxErrorBytes)
}
