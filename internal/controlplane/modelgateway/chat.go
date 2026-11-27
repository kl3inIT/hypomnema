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
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// POST /v1/chat/completions (issue #20). The client speaks OpenAI's chat
// format and so does the provider, so the request is forwarded as it came,
// following 9router-go (internal/handlers/chat/chat.go): it is read as a map
// of its top-level keys, the model's name is replaced by the provider's, and
// everything else, known or not, goes through. Each value is kept as the
// bytes the client sent, never decoded into a number or a map, because that
// is where Bifrost lost large integers and key order
// (core/schemas/chatcompletions.go, response_format). Tokens are read from
// the answer; a stream is asked for its usage whether or not the client asked,
// as Bifrost, LiteLLM, new-api and Coder's gateway do, and the usage-only
// frame is kept from a client that did not ask, as LiteLLM does.

const (
	// maxRequestBytes bounds a request body at 32 MiB (issue #675): the request limit pi's catalog
	// (ai/catalog/providers/anthropic.ts, maxRequestBytes) and Hermes (agent/image_eviction_policy.py) state for
	// Anthropic, which pi's image eviction (deploy/coordinator/pi-image-eviction.mts) keeps its 24 MB of images under,
	// as Hermes does. Anthropic's docs (platform.claude.com/docs/en/api/overview, read 2026-11-17) give 128 MB for
	// its own API and 32 MB on the cloud platforms it runs; the subscription backends the gateway forwards to state
	// none. It was 10 MiB, 9router-go's, which a chat full of phone screenshots outgrew.
	maxRequestBytes = 32 << 20
	// maxResponseBytes bounds a non-streaming answer.
	maxResponseBytes = 10 << 20
	// maxErrorBytes bounds a provider's error body passed to the client.
	maxErrorBytes = 1 << 20
	// maxLineBytes bounds one line of a stream, as Bifrost's and 9router-go's scanners.
	maxLineBytes = 10 << 20
	// DefaultStreamIdleTimeout ends a stream that sends nothing for this long.
	// The references use 2 to 6 minutes.
	DefaultStreamIdleTimeout = 5 * time.Minute
	// responseHeaderTimeout bounds the wait for a provider's first answer, as 9router-go's.
	responseHeaderTimeout = 2 * time.Minute
)

// Secrets returns the key a provider's api_key_ref names.
type Secrets func(ctx context.Context, ref string) (string, error)

// upstream is one place to send a request: a provider with its key and the
// model's name there.
type upstream struct {
	Provider string
	Kind     string
	BaseURL  string
	Model    string
	APIKey   string
}

// chatCall is one chat request on its way to one upstream.
type chatCall struct {
	upstream upstream
	// req is the client's request.
	req chatRequest
	// keyID is the gateway key the client used.
	keyID string
	// model is the name the client knows the model by.
	model string
	// session is the conversation's session when the route knows it (/v1/responses translated to chat, issue
	// #1144); chatSession reads it from the request otherwise.
	session string
}

// chatAdapter serves a chat request from one kind of provider and answers in
// OpenAI's chat format, whatever the provider speaks: the status and the body
// of what it returns are what the client gets, a stream as OpenAI's chunks.
// One exists per provider kind. A kind that is data only (openai_compatible)
// passes both ways through; a built-in kind translates and handles what is
// specific to its vendor.
type chatAdapter interface {
	// declared says the provider names a base URL and a key of its own.
	declared() bool
	do(ctx context.Context, s *Server, call chatCall) (*http.Response, error)
}

// chatAdapters are the provider kinds the gateway can serve chat from.
var chatAdapters = map[string]chatAdapter{
	"openai_compatible":        openAICompatible{},
	"codex_subscription":       codexSubscription{},
	"antigravity_subscription": antigravitySubscription{},
	"claude_subscription":      claudeSubscription{},
	openCodeGoKind:             openCode{openCodeGoProvider},
	openCodeZenKind:            openCode{openCodeZenProvider},
}

// openAICompatible serves any provider that takes OpenAI's chat format at
// <base URL>/chat/completions with a bearer key.
type openAICompatible struct{}

func (openAICompatible) declared() bool { return true }

func (openAICompatible) do(ctx context.Context, s *Server, call chatCall) (*http.Response, error) {
	u := call.upstream

	body, err := call.req.forUpstream(u.Model)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(u.BaseURL, "/") + "/chat/completions"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the request to provider %q: %w", u.Provider, err)
	}

	// The client's own headers are not forwarded, as in four of the five references.
	req.Header.Set("Authorization", "Bearer "+u.APIKey)
	req.Header.Set("Content-Type", "application/json")

	if call.req.stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling provider %q: %w", u.Provider, err)
	}

	return resp, nil
}

// chatRequest is a chat completions body as the client sent it.
type chatRequest struct {
	fields map[string]json.RawMessage
	raw    []byte
	model  string
	stream bool
	// wantsUsage says the client asked for the usage frame of a stream.
	wantsUsage bool
}

func parseChatRequest(raw []byte) (chatRequest, error) {
	req := chatRequest{raw: raw}

	if err := json.Unmarshal(raw, &req.fields); err != nil || req.fields == nil {
		return req, errors.New("the body is not a JSON object")
	}

	if err := json.Unmarshal(req.fields["model"], &req.model); err != nil || req.model == "" {
		return req, errors.New("model is required")
	}

	if v, ok := req.fields["stream"]; ok && json.Unmarshal(v, &req.stream) != nil {
		return req, errors.New("stream is true or false")
	}

	if v, ok := req.fields["stream_options"]; ok && string(v) != "null" {
		var options struct {
			IncludeUsage bool `json:"include_usage"`
		}

		if err := json.Unmarshal(v, &options); err != nil {
			return req, errors.New("stream_options is an object")
		}

		req.wantsUsage = options.IncludeUsage
	}

	return req, nil
}

// forUpstream returns the body to send: the client's, with the provider's
// name for the model and, for a stream, a request for its usage.
func (r chatRequest) forUpstream(model string) ([]byte, error) {
	fields := maps.Clone(r.fields)

	name, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("encoding the model's name: %w", err)
	}

	fields["model"] = name

	if r.stream {
		options := map[string]json.RawMessage{}
		if v, ok := fields["stream_options"]; ok && string(v) != "null" {
			if err := json.Unmarshal(v, &options); err != nil {
				return nil, fmt.Errorf("reading stream_options: %w", err)
			}
		}

		options["include_usage"] = json.RawMessage("true")

		if fields["stream_options"], err = json.Marshal(options); err != nil {
			return nil, fmt.Errorf("encoding stream_options: %w", err)
		}
	}

	body, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	return body, nil
}

// retryable are the answers after which the next target is tried: the
// provider is limiting or failing, not refusing the request.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// upstreamFor resolves a target to its provider, adapter and key.
func (s *Service) upstreamFor(ctx context.Context, t Target) (upstream, chatAdapter, error) {
	p, err := s.providers(ctx, t.Provider)
	if err != nil {
		return upstream{}, nil, fmt.Errorf("reading provider %q: %w", t.Provider, err)
	}

	adapter, ok := chatAdapters[p.Kind]

	switch {
	case !ok:
		return upstream{}, nil, fmt.Errorf("provider %q is of kind %s, which the gateway does not serve", p.Name, p.Kind)
	case !adapter.declared():
		// A built-in provider has its address in its adapter and its
		// credentials in the accounts logged in on the control plane.
		return upstream{Provider: p.Name, Kind: p.Kind, Model: t.UpstreamModel}, adapter, nil
	case p.BaseURL == "":
		return upstream{}, nil, fmt.Errorf("provider %q has no base_url", p.Name)
	case p.APIKeyRef == "":
		return upstream{}, nil, fmt.Errorf("provider %q has no api_key_ref", p.Name)
	}

	key, err := s.secrets(ctx, p.APIKeyRef)
	if err != nil {
		return upstream{}, nil, fmt.Errorf("reading the key of provider %q: %w", p.Name, err)
	}

	return upstream{
		Provider: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, Model: t.UpstreamModel, APIKey: key,
	}, adapter, nil
}

// chatCompletions answers POST /v1/chat/completions.
func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
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

	req, err := parseChatRequest(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "invalid_request", err.Error())

		return
	}

	if !key.Allows(req.model) {
		writeError(w, http.StatusForbidden, typePermission, "model_not_allowed",
			fmt.Sprintf("this key may not use model %q", req.model))

		return
	}

	model, err := s.modelOf(r.Context(), req.model)
	if errors.Is(err, ErrModelNotServed) {
		writeError(w, http.StatusNotFound, typeInvalidRequest, codeModelNotServed, notServedMessage(req.model))

		return
	}

	if errors.Is(err, ErrNotFound) || (err == nil && model.Capability != CapabilityChat) {
		writeError(w, http.StatusBadRequest, typeInvalidRequest, "model_not_found",
			fmt.Sprintf("the gateway does not serve a chat model named %q", req.model))

		return
	}

	if err != nil {
		s.internal(w, r, "modelgateway.model_failed", err)

		return
	}

	if !s.svc.KeyServes(r.Context(), key, model) {
		writeError(w, http.StatusForbidden, typePermission, "model_not_allowed", accountsRefuseMessage(req.model))

		return
	}

	record := Usage{KeyID: key.ID, Model: model.Name, Stream: req.stream, started: started}
	serving, served := withServed(withKey(r, key))
	s.serveChat(w, serving, req, model, key, &record)
	record.AccountID = served.id
	record.Duration = s.svc.clock.Now().Sub(started)

	// The client may be gone by now; the record is kept all the same.
	if err := s.svc.recordUsage(context.WithoutCancel(r.Context()), record); err != nil {
		s.logger.ErrorContext(r.Context(), "recording gateway usage",
			slog.String("event", "modelgateway.usage_failed"), slog.String("error", err.Error()))
	}
}

// serveChat tries the model's targets in order until one gives an answer to
// pass on, and fills the record. The next target is tried only while nothing
// has been sent to the client, as every reference does.
func (s *Server) serveChat(
	w http.ResponseWriter, r *http.Request, req chatRequest, model Model, key Key, record *Usage,
) {
	ctx := r.Context()
	fallbacks := sync.OnceValue(func() []string {
		if key.ChatID == "" || s.chatFallbacks == nil || ctx.Err() != nil {
			return nil
		}
		names, err := s.chatFallbacks(ctx, key.ChatID, model.Name)
		if err != nil {
			s.logger.WarnContext(ctx, "reading Bot fallbacks", slog.String("error", err.Error()))
		}
		return names
	})
	hasFallbacks := func() bool { return len(fallbacks()) != 0 }

	resp, exhausted := s.tryChat(ctx, req, model, key.ID, record, hasFallbacks)
	if exhausted {
		for _, name := range fallbacks() {
			if ctx.Err() != nil {
				break
			}
			fallback, err := s.svc.GetModel(ctx, name)
			if err != nil || fallback.Capability != CapabilityChat {
				continue
			}
			if resp != nil {
				drain(resp)
			}
			record.Model = fallback.Name
			resp, exhausted = s.tryChat(ctx, req, fallback, key.ID, record, hasFallbacks)
			if !exhausted {
				break
			}
		}
	}

	if resp != nil {
		if record.Outcome == OutcomeNoAccount {
			relayCooling(w, resp)
			_ = resp.Body.Close()
			return
		}
		s.relay(w, r, req, resp, record)
		_ = resp.Body.Close()

		return
	}
	if ctx.Err() != nil {
		record.Outcome = OutcomeClientGone

		return
	}

	record.Outcome = OutcomeTransportError
	writeError(w, http.StatusBadGateway, typeServer, "upstream_unavailable",
		fmt.Sprintf("no provider of model %q could be reached", record.Model))
}

func (s *Server) tryChat(
	ctx context.Context, req chatRequest, model Model, keyID string, record *Usage, hasFallbacks func() bool,
) (*http.Response, bool) {
	exhausted := len(model.Targets) > 0
	var cooling modelCooling
	record.Outcome, record.Error = "", ""
	for i, target := range model.Targets {
		last := i == len(model.Targets)-1
		noteServed(ctx, "")
		record.Attempts++
		record.Provider, record.UpstreamModel = target.Provider, target.UpstreamModel
		record.Status = 0

		resp, err := s.send(ctx, chatCall{req: req, keyID: keyID, model: model.Name}, target)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false
			}
			if cooling.add(err) {
				continue
			}
			exhausted = false

			s.logger.WarnContext(ctx, "a gateway target gave no answer",
				slog.String("event", "modelgateway.target_failed"), slog.String("provider", target.Provider),
				slog.String("model", model.Name), slog.String("error", err.Error()))

			continue
		}

		record.Status = resp.StatusCode
		cooling.other = true
		refused := accountRefused(resp.StatusCode)
		exhausted = exhausted && refused

		if !last && (retryable(resp.StatusCode) || (refused && hasFallbacks())) {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBytes))
			_ = resp.Body.Close()

			continue
		}

		return resp, exhausted
	}

	return s.coolingResponse(ctx, "/v1/chat/completions", cooling, record), exhausted
}

func accountRefused(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

// send makes one attempt at one target and returns the answer in OpenAI's
// chat format.
func (s *Server) send(ctx context.Context, call chatCall, target Target) (*http.Response, error) {
	u, adapter, err := s.svc.upstreamFor(ctx, target)
	if err != nil {
		return nil, err
	}

	call.upstream = u

	return adapter.do(ctx, s, call)
}

// relay passes a provider's answer to the client and fills the record.
func (s *Server) relay(w http.ResponseWriter, r *http.Request, req chatRequest, resp *http.Response, record *Usage) {
	contentType := resp.Header.Get("Content-Type")

	switch {
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		// The provider's own status and body, as 9router-go passes them.
		record.Outcome = OutcomeUpstreamError

		if retry := resp.Header.Get("Retry-After"); retry != "" {
			w.Header().Set("Retry-After", retry)
		}

		recordRefusal(r.Context(), record, copyBody(w, resp, contentType, maxErrorBytes), len(req.raw))
	case req.stream && strings.HasPrefix(contentType, "text/event-stream"):
		s.relayStream(w, r, req, resp, record)
	default:
		// A plain answer, also when a stream was asked for and the provider
		// sent JSON instead.
		body := copyBody(w, resp, contentType, maxResponseBytes)

		var answer struct {
			Usage json.RawMessage `json:"usage"`
		}

		if json.Unmarshal(body, &answer) == nil {
			record.Tokens.merge(answer.Usage)
		}

		record.Tokens.estimate(len(req.raw), len(body))
		record.Outcome = OutcomeOK
	}
}

// copyBody writes a provider's status and body to the client, up to limit
// bytes, and returns what it wrote.
func copyBody(w http.ResponseWriter, resp *http.Response, contentType string, limit int64) []byte {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, limit))

	if contentType == "" {
		contentType = "application/json"
	}

	// Content-Length and Transfer-Encoding are the server's to set for what
	// it writes; copying the provider's makes them disagree with the body
	// (9router-go, handlers/chat/chat.go).
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body) // The client went away; nothing is left to tell it.

	return body
}

// streamChunk is what the gateway reads of a chunk; the chunk itself is
// passed on as the provider sent it, since a re-encoded chunk gains or loses
// fields that strict clients trip on (Coder's gateway,
// intercept/chatcompletions/streaming.go).
type streamChunk struct {
	Usage   json.RawMessage `json:"usage"`
	Choices []struct {
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// relayStream passes a provider's event stream to the client line by line.
func (s *Server) relayStream(
	w http.ResponseWriter, r *http.Request, req chatRequest, resp *http.Response, record *Usage,
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

	// A scanner blocks in Read and takes no deadline, so a silent provider is
	// ended by closing its body, as 9router-go and Bifrost do.
	idle := watchStreamIdle(resp.Body, s.idleTimeout)
	defer idle.timer.Stop()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)

	var (
		done, finished, clientGone bool
		sent                       int
		failure                    string
	)

	for scanner.Scan() {
		idle.reset()

		// Only data lines are passed on; comments and event names are not, as
		// in new-api and Bifrost.
		payload, isData := bytes.CutPrefix(scanner.Bytes(), []byte("data:"))
		if !isData {
			continue
		}

		payload = bytes.TrimSpace(payload)
		noteStreamFailure(&failure, payload)
		if string(payload) == "[DONE]" {
			done = true

			break
		}

		var chunk streamChunk

		usageOnly := false

		if json.Unmarshal(payload, &chunk) == nil {
			hasUsage := len(chunk.Usage) > 0 && string(chunk.Usage) != "null"
			if hasUsage {
				record.Tokens.merge(chunk.Usage)
			}

			usageOnly = hasUsage && len(chunk.Choices) == 0
			finished = finished || slices.ContainsFunc(chunk.Choices, func(c struct {
				FinishReason *string `json:"finish_reason"`
			}) bool {
				return c.FinishReason != nil
			})
		}

		// The usage frame was asked for by the gateway; a client that did not
		// ask does not get it.
		if usageOnly && !req.wantsUsage {
			continue
		}

		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			clientGone = true

			break
		}

		sent += len(payload)

		flush()
		record.firstChunk(s.svc.clock.Now())
	}

	switch {
	case failure != "":
		record.Outcome, record.Error = OutcomeTruncated, failure
	case clientGone || r.Context().Err() != nil:
		record.Outcome = OutcomeClientGone
	case done || finished:
		// A provider that closes after its finish reason has finished
		// (Bifrost); the end mark is the gateway's to send.
		record.Outcome = OutcomeOK

		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	default:
		// A provider that died looks like a clean end of the body, so only an
		// end mark or a finish reason says the answer is whole (Bifrost).
		record.Outcome, record.Error = OutcomeTruncated, idle.endReason(scanner.Err(), "a finish reason or [DONE]")

		_, _ = io.WriteString(w, `data: {"error":{"message":"the provider's answer ended early",`+
			`"type":"server_error","code":"upstream_truncated"}}`+"\n\n")
	}

	flush()
	record.Tokens.estimate(len(req.raw), sent)
}
