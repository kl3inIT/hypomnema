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
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/internal/store"
)

// modelCooling is what the targets of one model said when none answered: the first moment an account of one is free
// again, that no account's list has the model (notServed, issue #814), and whether a target failed otherwise.
type modelCooling struct {
	first time.Time
	// code is why the account free first rests (issue #1305).
	code      CoolingCode
	notServed bool
	other     bool
}

func (c *modelCooling) add(err error) bool {
	var cooling *CoolingError
	switch {
	case errors.As(err, &cooling):
		if c.first.IsZero() || cooling.Until.Before(c.first) {
			c.first, c.code = cooling.Until, cooling.Code
		}
		return true
	case errors.Is(err, ErrNoAccount):
		return true
	case errors.Is(err, ErrModelNotServed):
		// The next target may serve the model: a target no account has it on does not answer for the others.
		c.notServed = true
		return true
	default:
		c.other = true
		return false
	}
}

// coolingResponse is the answer when no target of a model answered and none failed otherwise: 429 until the first
// account is free again, or, when no target cools and no account's list has the model on one, 404 model_not_served
// (issue #814); nil otherwise.
func (s *Server) coolingResponse(ctx context.Context, route string, c modelCooling, record *Usage) *http.Response {
	if c.other {
		return nil
	}

	if c.first.IsZero() {
		if !c.notServed {
			return nil
		}

		noteServed(ctx, "")
		record.Status, record.Outcome = http.StatusNotFound, OutcomeUpstreamError
		record.Error = notServedMessage(record.Model)

		return notServedResponse(route, record.Error)
	}

	now := s.svc.clock.Now()
	s.recordModelCooling(ctx, route, record.Model, c.first, now)

	noteServed(ctx, "")
	record.Status, record.Outcome = http.StatusTooManyRequests, OutcomeNoAccount
	record.Error = accountsCoolingMessage

	return accountCoolingResponse(route, c.first, c.code, now)
}

// recordModelCooling writes when a model is free again on a route, which the scheduler reads to hold back work on it
// (orchestration placeTask). Only a request that may use every account speaks for them all: a key limited to some
// accounts (issue #1277) that finds them all resting says nothing of the others, which still serve.
func (s *Server) recordModelCooling(ctx context.Context, route, model string, until, now time.Time) {
	if _, limited := KeyAccounts(ctx); limited {
		return
	}

	kept := context.WithoutCancel(ctx)
	if err := s.svc.write(kept, func(q *store.Queries, _ int64) error {
		return q.RecordModelCooling(kept, store.RecordModelCoolingParams{
			Route: route, Model: model, ObservedAt: now.UnixMilli(), ResetsAt: until.UnixMilli(),
		})
	}); err != nil {
		s.logger.ErrorContext(ctx, "recording when a gateway model is free", slog.String("model", model),
			slog.String("route", route), slog.String("error", err.Error()))
	}
}

// accountCoolingResponse is the gateway's 429 when no account can serve, in the route's shape: until when (resets_at,
// Retry-After) and why the first account free again rests (cooling_code, issue #1305), said in its message too, so a
// client's log says it, as LiteLLM's RouterRateLimitError names its cause in its type and message
// (litellm/types/router.py:1273-1303).
func accountCoolingResponse(route string, until time.Time, code CoolingCode, now time.Time) *http.Response {
	kind, envelope := "rate_limit_error", ""
	if route == "/v1/messages" || route == "/v1/messages/count_tokens" {
		envelope = "error"
	} else if route == "/v1/responses" && until.Sub(now) > codexWaitsUpTo {
		kind = "usage_limit_reached"
	}
	var body struct {
		Type  string `json:"type,omitempty"`
		Error struct {
			Message     string      `json:"message"`
			Type        string      `json:"type"`
			Code        string      `json:"code"`
			ResetsAt    int64       `json:"resets_at"`
			CoolingCode CoolingCode `json:"cooling_code,omitempty"`
		} `json:"error"`
	}
	body.Type = envelope
	body.Error.Message, body.Error.Type, body.Error.Code = coolingMessage(code), kind, "account_unavailable"
	body.Error.CoolingCode = code
	body.Error.ResetsAt = until.Add(time.Second - time.Nanosecond).Unix()
	encoded, _ := json.Marshal(body) //nolint:errchkjson // Strings and a number always encode.
	wait := max(0, (until.Sub(now)+time.Second-time.Nanosecond)/time.Second)
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": {"application/json"}, "Retry-After": {strconv.Itoa(int(wait))}},
		Body:       io.NopCloser(bytes.NewReader(encoded)),
	}
}

// notServedResponse is the gateway's 404 for a model no account's list has, in the route's shape.
func notServedResponse(route, message string) *http.Response {
	if route == "/v1/messages" || route == "/v1/messages/count_tokens" {
		encoded, _ := json.Marshal(map[string]any{ //nolint:errchkjson // Strings always encode.
			"type": "error", "error": map[string]string{"type": "not_found_error", "message": message},
		})

		return &http.Response{
			StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(bytes.NewReader(encoded)),
		}
	}

	return openAIError(http.StatusNotFound, typeInvalidRequest, codeModelNotServed, message, 0)
}

// relayCooling passes the gateway's own answer for a model none of whose targets answered.
func relayCooling(w http.ResponseWriter, resp *http.Response) {
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		w.Header().Set("Retry-After", retry)
	}
	copyBody(w, resp, resp.Header.Get("Content-Type"), maxErrorBytes)
}

// accountsCooling is the gateway's 429 in OpenAI's shape when every account of a provider rests: the wait, and why
// the first account free again rests (issue #1305).
func accountsCooling(wait time.Duration, code CoolingCode) *http.Response {
	var body errorBody

	body.Error.Message, body.Error.Type, body.Error.Code = coolingMessage(code), "rate_limit_error", "account_unavailable"
	body.Error.CoolingCode = code

	encoded, _ := json.Marshal(body) //nolint:errchkjson // A struct of strings always encodes.
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(encoded)),
	}

	if wait > 0 {
		// As openAIError says the wait.
		resp.Header.Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	}

	return resp
}

// coolingMessage is the gateway's words when no account can serve, with why the first one free again rests.
func coolingMessage(code CoolingCode) string {
	if code == "" || code == CoolingUnknown {
		return accountsCoolingMessage
	}

	return accountsCoolingMessage + " (" + strings.ReplaceAll(string(code), "_", " ") + ")"
}
