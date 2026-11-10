package modelgateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/codex"
)

// POST /v1/responses (issue #107): for a client that speaks OpenAI's
// Responses format, which the Codex CLI does and nothing else. The request is
// passed to the Codex backend under an account logged in on the control
// plane, and the backend's event stream goes back as it comes. It follows
// 9router-go: the body's preparation (package codex, PassThrough), no header
// of the client's passed on but its session (internal/proxy/grokcli.go
// ForwardCodex), the stream copied and not re-framed (proxy/sse_copy.go), the
// usage read from the event that ends an answer
// (translator/responses_usage.go), a refusal passed on with its status and
// body (handlers/chat/responses.go).
//
// Left out, with where a reference has it: a model served by another kind of
// provider, which 9router-go and CLIProxyAPI translate to chat and back;
// /v1/responses/compact; an answer that is not a stream, which 9router-go
// does not fold for Codex either; CLIProxyAPI's WebSocket.

// sessionHeaders are where a client names its session, in the order
// 9router-go reads them (handlerutil.SessionHeaderKeys); the Codex CLI sends
// session-id.
var sessionHeaders = []string{"x-session-id", "session-id", "session_id"}

type responsesRequest struct {
	Model  string `json:"model"`
	Stream *bool  `json:"stream"`
}

// responsesCompleted is what the gateway reads of the event that ends an answer.
type responsesCompleted struct {
	Type     string `json:"type"`
	Response struct {
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			InputDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

// responses answers POST /v1/responses.
func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	started := s.svc.clock.Now()

	key, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, typeInvalidRequest, "request_too_large",
				fmt.Sprintf("the request is over %d bytes", maxRequestBytes))

			return
		}

		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", "the request body could not be read")

		return
	}

	var req responsesRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.Model == "" {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request",
			"the request is a JSON object that names a model")

		return
	}

	if req.Stream != nil && !*req.Stream {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "stream_required",
			"/v1/responses answers as a stream only")

		return
	}

	if !key.Allows(req.Model) {
		writeError(w, http.StatusForbidden, typePermission, "model_not_allowed",
			fmt.Sprintf("this key may not use model %q", req.Model))

		return
	}

	model, err := s.svc.GetModel(r.Context(), req.Model)
	if errors.Is(err, ErrNotFound) || (err == nil && model.Capability != CapabilityChat) {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "model_not_found",
			fmt.Sprintf("the gateway does not serve a chat model named %q", req.Model))

		return
	}

	if err != nil {
		s.internal(w, r, "modelgateway.model_failed", err)

		return
	}

	session := ""
	for _, name := range sessionHeaders {
		if session = r.Header.Get(name); session != "" {
			break
		}
	}

	if session == "" {
		session = cacheKey(key.ID)
	}

	record := Usage{KeyID: key.ID, Model: model.Name, Stream: true}
	s.serveResponses(w, r, raw, session, model, &record)
	record.Duration = s.svc.clock.Now().Sub(started)

	// The client may be gone by now; the record is kept all the same.
	if err := s.svc.recordUsage(context.WithoutCancel(r.Context()), record); err != nil {
		s.logger.ErrorContext(r.Context(), "recording gateway usage",
			slog.String("event", "modelgateway.usage_failed"), slog.String("error", err.Error()))
	}
}

// serveResponses tries the model's Codex targets in order, as serveChat tries
// every target, and fills the record.
func (s *Server) serveResponses(
	w http.ResponseWriter, r *http.Request, raw []byte, session string, model Model, record *Usage,
) {
	ctx := r.Context()

	var targets []upstream

	for _, target := range model.Targets {
		u, _, err := s.svc.upstreamFor(ctx, target)
		if err == nil && u.Kind == "codex_subscription" {
			targets = append(targets, u)
		}
	}

	if len(targets) == 0 || s.accounts == nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "model_not_served_here",
			fmt.Sprintf("model %q is not served from a Codex subscription, the one kind /v1/responses passes on to",
				model.Name))

		return
	}

	for i, target := range targets {
		last := i == len(targets)-1
		record.Attempts, record.Provider, record.UpstreamModel = i+1, target.Provider, target.Model
		record.Status = 0

		built, err := codex.PassThrough(raw, codex.PassOptions{Model: target.Model, Session: session})
		if err != nil {
			writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", err.Error())

			return
		}

		resp, err := s.underAccounts(ctx, codexRuntime, nil, func(token AccountToken) (*http.Response, error) {
			resp, err := s.codexCall(ctx, &token, built)
			if err != nil {
				return nil, err
			}

			if resp.StatusCode != http.StatusOK {
				return s.codexRefused(ctx, token, resp), nil
			}

			s.accountState.served(token.AccountID)

			return resp, nil
		})
		if err != nil {
			if ctx.Err() != nil {
				record.Outcome = OutcomeClientGone

				return
			}

			s.logger.WarnContext(ctx, "a gateway target gave no answer",
				slog.String("event", "modelgateway.target_failed"), slog.String("provider", target.Provider),
				slog.String("model", model.Name), slog.String("error", err.Error()))

			continue
		}

		record.Status = resp.StatusCode

		if retryable(resp.StatusCode) && !last {
			drain(resp)

			continue
		}

		if resp.StatusCode != http.StatusOK {
			// The backend's own status and body, as 9router-go passes them.
			record.Outcome = OutcomeUpstreamError

			if retry := resp.Header.Get("Retry-After"); retry != "" {
				w.Header().Set("Retry-After", retry)
			}

			copyBody(w, resp, resp.Header.Get("Content-Type"), maxErrorBytes)
			_ = resp.Body.Close()

			return
		}

		s.relayResponses(w, r, len(raw), resp, record)
		_ = resp.Body.Close()

		return
	}

	record.Outcome = OutcomeTransportError

	writeError(w, http.StatusBadGateway, typeServer, "upstream_unavailable",
		fmt.Sprintf("no provider of model %q could be reached", model.Name))
}

// relayResponses copies the backend's event stream to the client line for
// line, and reads the usage from the event that ends the answer.
func (s *Server) relayResponses(
	w http.ResponseWriter, r *http.Request, asked int, resp *http.Response, record *Usage,
) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	flush()

	// A scanner blocks in Read and takes no deadline, so a silent backend is
	// ended by closing its body, as in the chat route.
	idle := time.AfterFunc(s.idleTimeout, func() { _ = resp.Body.Close() })
	defer idle.Stop()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxEventBytes)

	var (
		completed, clientGone bool
		sent                  int
	)

	for scanner.Scan() {
		idle.Reset(s.idleTimeout)

		line := scanner.Bytes()

		if payload, isData := bytes.CutPrefix(line, []byte("data:")); isData &&
			bytes.Contains(payload, []byte(`"response.completed"`)) {
			var event responsesCompleted
			if json.Unmarshal(payload, &event) == nil && event.Type == "response.completed" {
				completed = true
				usage := event.Response.Usage
				record.Tokens = Tokens{
					Prompt: usage.InputTokens, Completion: usage.OutputTokens,
					Cached: usage.InputDetails.CachedTokens, Reasoning: usage.OutputDetails.ReasoningTokens,
				}
			}
		}

		if _, err := w.Write(append(line, '\n')); err != nil {
			clientGone = true

			break
		}

		sent += len(line)

		// An empty line ends an event.
		if len(line) == 0 {
			flush()
		}
	}

	flush()

	switch {
	case clientGone || r.Context().Err() != nil:
		record.Outcome = OutcomeClientGone
	case completed:
		record.Outcome = OutcomeOK
	default:
		// The answer ended without the event that completes it: the backend
		// said it failed, or went away. The client has what was sent.
		record.Outcome = OutcomeTruncated
	}

	record.Tokens.estimate(asked, sent)
}
