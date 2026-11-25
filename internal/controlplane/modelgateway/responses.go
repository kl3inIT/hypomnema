package modelgateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// A model served on chat (an OpenCode chat model, an OpenAI-compatible
// provider) is asked through its chat adapter in chat's format and its answer
// turned back into Responses events (package responseschat, issue #1144), as
// CLIProxyAPI's openai→openai-responses translator and LiteLLM's bridge do.
//
// Left out, with where a reference has it: a Claude or Antigravity
// subscription and an OpenCode model on Messages, which 9router-go and
// CLIProxyAPI translate too; /v1/responses/compact; an answer that is not a
// stream, which 9router-go does not fold for Codex either; CLIProxyAPI's
// WebSocket.

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

	raw, err := s.readRequest(w, r, key, started)
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

	model, err := s.modelOf(r.Context(), req.Model)
	if errors.Is(err, ErrModelNotServed) {
		writeError(w, http.StatusNotFound, typeInvalidRequest, codeModelNotServed, notServedMessage(req.Model))

		return
	}

	if errors.Is(err, ErrNotFound) || (err == nil && model.Capability != CapabilityChat) {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "model_not_found",
			fmt.Sprintf("the gateway does not serve a chat model named %q", req.Model))

		return
	}

	if err != nil {
		s.internal(w, r, "modelgateway.model_failed", err)

		return
	}

	refused, run := s.refuseOverflow(w, r, key, model)
	if refused {
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

	record := Usage{KeyID: key.ID, Model: model.Name, Stream: true, started: started}
	serving, served := withServed(r)
	s.serveResponses(w, serving, raw, session, model, &record, served)
	s.noteOverflow(r.Context(), key, model, run, record)

	if record.Attempts == 0 {
		// Refused before any target was tried (model_not_served_here): nothing was served, so nothing is recorded,
		// as an image or audio request refused before any provider is not (RecordImageCall, RecordAudioCall;
		// issue #1147).
		return
	}

	record.AccountID = served.id
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
	w http.ResponseWriter, r *http.Request, raw []byte, session string, model Model, record *Usage, served *served,
) {
	ctx := r.Context()

	var (
		attempts []responsesAttempt
		targets  []upstream
	)

	for _, target := range model.Targets {
		u, adapter, err := s.svc.upstreamFor(ctx, target)
		if err != nil {
			continue
		}

		way := s.svc.responsesWayOf(ctx, u.Kind, u.Model)
		// Every target but an OpenAI-compatible provider runs under the accounts logged in on the control plane.
		if way == responsesNotServed || (s.accounts == nil && !adapter.declared()) {
			continue
		}

		attempts = append(attempts, responsesAttempt{upstream: u, adapter: adapter, way: way})
		targets = append(targets, u)
	}

	if len(attempts) == 0 {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "model_not_served_here",
			fmt.Sprintf("model %q is not served from a Codex subscription, an OpenCode model on Responses or chat, "+
				"or an OpenAI-compatible provider, the ones /v1/responses serves", model.Name))

		return
	}

	var cooling modelCooling
	for i, a := range attempts {
		target := a.upstream
		last := i == len(attempts)-1
		noteServed(ctx, "")
		record.Attempts, record.Provider, record.UpstreamModel = i+1, target.Provider, target.Model
		record.Status = 0

		var (
			resp *http.Response
			err  error
		)

		if a.way == responsesTranslated {
			resp, err = s.responsesAsChat(ctx, raw, a, chatCall{keyID: record.KeyID, model: model.Name, session: session})
		} else {
			resp, err = s.passResponses(ctx, raw, session, target)
		}

		var bad *badMessagesError
		if errors.As(err, &bad) {
			writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", bad.Error())

			return
		}
		if err != nil {
			if ctx.Err() != nil {
				record.Outcome = OutcomeClientGone

				return
			}
			if cooling.add(err) {
				continue
			}

			s.logger.WarnContext(ctx, "a gateway target gave no answer",
				slog.String("event", "modelgateway.target_failed"), slog.String("provider", target.Provider),
				slog.String("model", model.Name), slog.String("error", err.Error()))

			continue
		}

		record.Status = resp.StatusCode
		if retryable(resp.StatusCode) && !last {
			cooling.other = true
			drain(resp)

			continue
		}

		if resp.StatusCode != http.StatusOK {
			// The backend's own status and body, as 9router-go passes them.
			record.Outcome = OutcomeUpstreamError

			if retry := resp.Header.Get("Retry-After"); retry != "" {
				w.Header().Set("Retry-After", retry)
			}

			recordRefusal(ctx, record, copyBody(w, resp, resp.Header.Get("Content-Type"), maxErrorBytes), len(raw))
			_ = resp.Body.Close()

			return
		}

		var onFailure func(*codex.Failure)
		if target.Kind == "codex_subscription" && served.id != "" {
			onFailure = func(failure *codex.Failure) {
				s.codexCool(ctx, AccountToken{AccountID: served.id}, target.Model, failure)
				s.recordResponsesCooling(ctx, r.URL.Path, model.Name, cooling, targets[i:])
			}
		}
		s.relayResponses(w, r, len(raw), resp, record, onFailure)
		_ = resp.Body.Close()

		return
	}

	if resp := s.coolingResponse(ctx, r.URL.Path, cooling, record); resp != nil {
		relayCooling(w, resp)
		_ = resp.Body.Close()
		return
	}
	record.Outcome = OutcomeTransportError

	writeError(w, http.StatusBadGateway, typeServer, "upstream_unavailable",
		fmt.Sprintf("no provider of model %q could be reached", model.Name))
}

// responsesAttempt is one target /v1/responses can send a request to, and how.
type responsesAttempt struct {
	upstream upstream
	adapter  chatAdapter
	way      responsesWay
}

func (s *Server) recordResponsesCooling(
	ctx context.Context, route, model string, cooling modelCooling, targets []upstream,
) {
	if cooling.other {
		return
	}
	kept := context.WithoutCancel(ctx)
	for _, target := range targets {
		runtime := codexRuntime
		if p, ok := openCodeOf(target.Kind); ok {
			runtime = p.runtime
		}
		var modelReset func(string) time.Time
		if runtime == codexRuntime {
			modelReset = s.codexModelReset(target.Model)
		}
		until, err := s.accounts.FirstFree(kept, runtime, modelReset)
		if err != nil || until.IsZero() {
			return
		}
		cooling.add(&CoolingError{Until: until})
	}
	s.recordModelCooling(kept, route, model, cooling.first, s.svc.clock.Now())
}

// passResponses sends a Responses request to one target: a Codex subscription
// under its accounts, or an OpenCode Go or Zen model under the provider's keys,
// the client's body with the model's name at OpenCode and as a stream (issues
// #854, #965). A body that
// cannot be sent, the client's to fix, is a *badMessagesError.
func (s *Server) passResponses(
	ctx context.Context, raw []byte, session string, target upstream,
) (*http.Response, error) {
	if p, ok := openCodeOf(target.Kind); ok {
		body, err := withModel(raw, target.Model, map[string]json.RawMessage{"stream": json.RawMessage("true")})
		if err != nil {
			return nil, &badMessagesError{err: err}
		}

		return s.underOpenCode(ctx, p, target.Model, openCodeGoSession(session), goWireResponses, body,
			"text/event-stream", nil)
	}

	built, err := codex.PassThrough(raw, codex.PassOptions{Model: target.Model, Session: session})
	if err != nil {
		return nil, &badMessagesError{err: err}
	}

	return s.underModelAccounts(ctx, codexRuntime, listedModel(target.Model), built.CacheKey, nil,
		s.codexModelReset(target.Model),
		func(token AccountToken) (*http.Response, error) {
			if resting := s.codexResting(token, target.Model); resting != nil {
				return resting, nil
			}

			resp, err := s.codexCall(ctx, &token, listedModel(target.Model), built)
			if err != nil {
				return nil, err
			}

			if resp.StatusCode != http.StatusOK {
				return s.codexRefused(ctx, token, target.Model, resp), nil
			}

			held, failure := s.holdCodexOpening(resp)
			if failure != nil {
				// Refused before any output: the next account is tried, as for a
				// 503 (issue #1135); an overload rests that model of the account
				// only (issue #1147).
				s.codexCool(ctx, token, target.Model, failure)
				noteStreamRefusal(ctx, failure.Text())

				return codexRefusal(failure, s.svc.clock.Now()), nil
			}

			s.accountState.served(token.AccountID)

			return held, nil
		})
}

// relayResponses copies the backend's event stream to the client line for
// line, and reads the usage from the event that ends the answer.
func (s *Server) relayResponses(
	w http.ResponseWriter, r *http.Request, asked int, resp *http.Response, record *Usage, onFailure func(*codex.Failure),
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
	idle := watchStreamIdle(resp.Body, s.idleTimeout)
	defer idle.timer.Stop()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxEventBytes)

	var (
		completed, clientGone bool
		// answered is when the event that completes the answer was passed on (issue #635).
		answered bool
		// output says an event past the stream's opening was passed on (codex.Opening).
		output  bool
		sent    int
		failure string
	)

	// searches are the model's own web searches, by their item's id (issue #723).
	searches := map[string]bool{}

	for scanner.Scan() {
		idle.reset()

		line := scanner.Bytes()

		if payload, isData := bytes.CutPrefix(line, []byte("data:")); isData {
			noteSearch(payload, searches)
			noteStreamFailure(&failure, payload)
			output = output || !codex.Opening(payload) && failure == ""
			if onFailure != nil && !completed && bytes.Contains(payload, []byte(`"response.failed"`)) {
				if parsed := codex.ParseResponseFailure(payload, s.svc.clock.Now()); parsed != nil && parsed.UsageLimit() {
					onFailure(parsed)
					onFailure = nil
				}
			}
		}

		// response.incomplete ends an answer too, one cut at its token limit or filtered (the Responses API's
		// terminal events; package responseschat sends it for a chat finish_reason length or content_filter): the
		// answer was passed on whole and its usage is read from it.
		if payload, isData := bytes.CutPrefix(line, []byte("data:")); isData &&
			(bytes.Contains(payload, []byte(`"response.completed"`)) ||
				bytes.Contains(payload, []byte(`"response.incomplete"`))) {
			var event responsesCompleted
			if json.Unmarshal(payload, &event) == nil &&
				(event.Type == "response.completed" || event.Type == "response.incomplete") {
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

		answered = completed
		sent += len(line)

		// An empty line ends an event; the first event passed on is the first chunk (issue #856).
		if len(line) == 0 {
			flush()
			record.firstChunk(s.svc.clock.Now())
		}
	}

	flush()

	switch {
	case answered:
		// The whole answer was passed on. The Codex CLI returns as it reads response.completed and closes the
		// connection (codex-rs/codex-api/src/sse/responses.rs), which is not a client gone (issue #635).
		record.Outcome = OutcomeOK
	case failure != "":
		record.Outcome, record.Error = OutcomeTruncated, failure
	case clientGone || r.Context().Err() != nil:
		record.Outcome = OutcomeClientGone
	default:
		record.Outcome, record.Error = OutcomeTruncated, idle.endReason(scanner.Err(), "response.completed")
	}

	if !output {
		// A stream that ended before any output gave none: its frames echo the
		// request, they are no answer (issue #1135).
		sent = 0
	}

	record.Tokens.estimate(asked, sent)
	record.WebSearches = int64(len(searches))
}

// noteSearch notes a web search the model made: an output item of type
// web_search_call, as LiteLLM counts them (litellm_core_utils/llm_cost_calc/
// tool_call_cost_tracking.py:577-584). Each is named by its id, so an item
// that is told again (added, then done) is one search.
func noteSearch(payload []byte, searches map[string]bool) {
	if !bytes.Contains(payload, []byte(`"web_search_call"`)) {
		return
	}

	var event struct {
		Type string `json:"type"`
		Item struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"item"`
	}

	if json.Unmarshal(payload, &event) != nil || event.Item.Type != "web_search_call" ||
		(event.Type != "response.output_item.added" && event.Type != "response.output_item.done") {
		return
	}

	id := event.Item.ID
	if id == "" {
		// An item with no id is counted once, when it is done.
		if event.Type != "response.output_item.done" {
			return
		}

		id = fmt.Sprintf("#%d", len(searches))
	}

	searches[id] = true
}
