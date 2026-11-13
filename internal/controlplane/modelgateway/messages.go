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
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/claude"
)

// POST /v1/messages and /v1/messages/count_tokens (issue #273): for a client
// that speaks Anthropic's Messages format, Claude Code above all. The request
// is passed to Anthropic under a Claude subscription account logged in on the
// control plane, and the answer goes back as it comes, as /v1/responses does
// for Codex. It follows CLIProxyAPI: a client that is Claude Code is passed
// unchanged with its own headers (claude_client_detection.go,
// claude_executor_request.go), any other is disguised as the chat route
// disguises; the gateway's own errors in Anthropic's shape (code_handlers.go);
// count_tokens asked of Anthropic (claude_executor_tokens.go). The usage is
// read from message_start and message_delta (9router usageTracking.js).
//
// Left out, with where a reference has it: a model served by another kind of
// provider, which CLIProxyAPI, 9router-go and 9router translate to chat and
// back; renaming a client's tools, which 9router-go and 9router do always.

// claudeCodeHeaders are the headers of a Claude Code client passed on to
// Anthropic: by prefix, then by name (CLIProxyAPI claude_executor_request.go).
var (
	claudeCodeHeaderPrefixes = []string{"Anthropic-", "X-Stainless-", "X-Claude-Code-"}
	claudeCodeHeaderNames    = []string{"User-Agent", "X-App", "X-Client-Request-Id", "Accept"}
)

// claudeOAuthBeta is the beta that lets a subscription token stand in a Bearer
// header; a client that did not send it gets it added.
const claudeOAuthBeta = "oauth-2025-04-20"

type messagesRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Metadata struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`
}

// messagesCall is one request on its way to Anthropic.
type messagesCall struct {
	path       string
	raw        []byte
	req        messagesRequest
	claudeCode bool
	session    string
	header     http.Header
	// generates says the call makes an answer: it is disguised for a client
	// that is not Claude Code, and its usage is recorded. count_tokens does not.
	generates bool
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	s.serveMessages(w, r, "/v1/messages", true)
}

func (s *Server) countTokens(w http.ResponseWriter, r *http.Request) {
	s.serveMessages(w, r, "/v1/messages/count_tokens", false)
}

func (s *Server) serveMessages(w http.ResponseWriter, r *http.Request, path string, generates bool) {
	started := s.svc.clock.Now()

	key, ok := s.authenticateAs(w, r, writeAnthropicError)
	if !ok {
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAnthropicError(w, http.StatusRequestEntityTooLarge, typeInvalidRequest, "",
				fmt.Sprintf("the request is over %d bytes", maxRequestBytes))

			return
		}

		writeAnthropicError(w, http.StatusBadRequest, typeInvalidRequest, "", "the request body could not be read")

		return
	}

	call := messagesCall{path: path, raw: raw, header: r.Header, generates: generates}
	if err := json.Unmarshal(raw, &call.req); err != nil || call.req.Model == "" {
		writeAnthropicError(w, http.StatusBadRequest, typeInvalidRequest, "",
			"the request is a JSON object that names a model")

		return
	}

	if !key.Allows(call.req.Model) {
		writeAnthropicError(w, http.StatusForbidden, typePermission, "",
			fmt.Sprintf("this key may not use model %q", call.req.Model))

		return
	}

	model, err := s.svc.GetModel(r.Context(), call.req.Model)
	if errors.Is(err, ErrNotFound) || (err == nil && model.Capability != CapabilityChat) {
		writeAnthropicError(w, http.StatusNotFound, "not_found_error", "",
			fmt.Sprintf("the gateway does not serve a chat model named %q", call.req.Model))

		return
	}

	if err != nil {
		s.logger.ErrorContext(r.Context(), "the model gateway failed",
			slog.String("event", "modelgateway.model_failed"), slog.String("error", err.Error()))
		writeAnthropicError(w, http.StatusInternalServerError, typeServer, "", "internal error")

		return
	}

	call.claudeCode = isClaudeCode(r.Header, call.req.Metadata.UserID)

	call.session = r.Header.Get("X-Claude-Code-Session-Id")
	if call.session == "" {
		call.session = claudeSession(key.ID)
	}

	record := Usage{KeyID: key.ID, Model: model.Name, Stream: call.req.Stream}
	s.passMessages(w, r, call, model, &record)

	if !generates {
		return
	}

	record.Duration = s.svc.clock.Now().Sub(started)

	// The client may be gone by now; the record is kept all the same.
	if err := s.svc.recordUsage(context.WithoutCancel(r.Context()), record); err != nil {
		s.logger.ErrorContext(r.Context(), "recording gateway usage",
			slog.String("event", "modelgateway.usage_failed"), slog.String("error", err.Error()))
	}
}

// isClaudeCode reports whether a client is Claude Code itself, as CLIProxyAPI
// recognizes it (claude_client_detection.go): it names itself the CLI app with
// a claude-cli User-Agent, asks for the Claude Code beta and sends the
// metadata user id Claude Code sends.
func isClaudeCode(h http.Header, userID string) bool {
	return h.Get("X-App") == "cli" &&
		strings.HasPrefix(h.Get("User-Agent"), "claude-cli/") &&
		strings.Contains(h.Get("Anthropic-Beta"), "claude-code-") &&
		userID != ""
}

// passMessages tries the model's Claude targets in order, as serveResponses
// tries its Codex targets, and fills the record.
func (s *Server) passMessages(w http.ResponseWriter, r *http.Request, call messagesCall, model Model, record *Usage) {
	ctx := r.Context()

	var targets []upstream

	for _, target := range model.Targets {
		u, _, err := s.svc.upstreamFor(ctx, target)
		if err == nil && u.Kind == "claude_subscription" {
			targets = append(targets, u)
		}
	}

	if len(targets) == 0 || s.accounts == nil {
		writeAnthropicError(w, http.StatusBadRequest, typeInvalidRequest, "",
			fmt.Sprintf("model %q is not served from a Claude subscription, the one kind /v1/messages passes on to",
				model.Name))

		return
	}

	for i, target := range targets {
		last := i == len(targets)-1
		record.Attempts, record.Provider, record.UpstreamModel = i+1, target.Provider, target.Model
		record.Status = 0

		resp, err := s.underAccounts(ctx, claudeAccountRuntime, call.session, nil,
			func(token AccountToken) (*http.Response, error) {
				resp, err := s.messagesCall(ctx, &token, call, target.Model)
				if err != nil {
					return nil, err
				}

				if resp.StatusCode != http.StatusOK {
					return s.claudeRefused(ctx, token, resp), nil
				}

				s.accountState.served(token.AccountID)

				return resp, nil
			})
		if err != nil {
			if ctx.Err() != nil {
				record.Outcome = OutcomeClientGone

				return
			}

			var bad *badMessagesError
			if errors.As(err, &bad) {
				writeAnthropicError(w, http.StatusBadRequest, typeInvalidRequest, "", bad.Error())

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
			record.Outcome = OutcomeUpstreamError

			passMessagesRefusal(w, resp)
			_ = resp.Body.Close()

			return
		}

		if call.req.Stream && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			s.relayMessages(w, r, len(call.raw), resp, record)
		} else {
			passMessagesAnswer(w, len(call.raw), resp, record)
		}

		_ = resp.Body.Close()

		return
	}

	record.Outcome = OutcomeTransportError

	writeAnthropicError(w, http.StatusBadGateway, typeServer, "",
		fmt.Sprintf("no provider of model %q could be reached", model.Name))
}

// badMessagesError is a body that cannot be passed on: the client's to fix.
type badMessagesError struct{ err error }

func (e *badMessagesError) Error() string { return e.err.Error() }

// messagesCall sends a request under an account. A token Anthropic refuses is
// refreshed once and the request sent again; token is then the new one.
func (s *Server) messagesCall(
	ctx context.Context, token *AccountToken, call messagesCall, upstreamModel string,
) (*http.Response, error) {
	send := func(token AccountToken) (*http.Response, error) {
		body, err := claude.PassThrough(call.raw, claude.PassOptions{
			Model: upstreamModel, Token: token.AccessToken, Session: call.session,
			Disguise: call.generates && !call.claudeCode,
		})
		if err != nil {
			return nil, &badMessagesError{err: err}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.claudeURL+call.path+"?beta=true",
			bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("building the request to Claude: %w", err)
		}

		s.messagesHeaders(req.Header, call)
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Content-Type", "application/json")

		return s.claudeDo(token, req)
	}

	resp, err := send(*token)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}

	drain(resp)

	if *token, err = s.accounts.TokenAfterRefusal(ctx, *token); err != nil {
		return nil, err
	}

	return send(*token)
}

// messagesHeaders sets what Anthropic is told of the client. Claude Code's own
// headers go as it sent them; any other client is presented as Claude Code,
// with the betas it asked for kept (9router-go fallback.go, 9router
// default.js). The subscription's beta is always there.
func (s *Server) messagesHeaders(h http.Header, call messagesCall) {
	asked := call.header.Get("Anthropic-Beta")

	if call.claudeCode {
		for name, values := range call.header {
			if passesOn(name) {
				h[name] = append([]string(nil), values...)
			}
		}

		if h.Get("Anthropic-Version") == "" {
			h.Set("Anthropic-Version", claudeAnthropicVersion)
		}

		h.Set("Anthropic-Beta", mergeBetas(asked, claudeOAuthBeta))

		return
	}

	h.Set("User-Agent", claudeUserAgent)
	h.Set("Anthropic-Version", claudeAnthropicVersion)
	h.Set("Anthropic-Beta", mergeBetas(claudeBeta, asked))
	h.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
	h.Set("X-App", claudeApp)
	h.Set("X-Stainless-Retry-Count", "0")

	if call.req.Stream {
		h.Set("Accept", "text/event-stream")
		h.Set("X-Stainless-Helper-Method", "stream")
	} else {
		h.Set("Accept", "application/json")
	}
}

func passesOn(name string) bool {
	name = http.CanonicalHeaderKey(name)

	for _, prefix := range claudeCodeHeaderPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	for _, n := range claudeCodeHeaderNames {
		if name == n {
			return true
		}
	}

	return false
}

// mergeBetas joins comma-separated beta lists, each beta once, in order.
func mergeBetas(lists ...string) string {
	var out []string

	seen := map[string]bool{}

	for _, list := range lists {
		for beta := range strings.SplitSeq(list, ",") {
			beta = strings.TrimSpace(beta)
			if beta != "" && !seen[beta] {
				seen[beta] = true
				out = append(out, beta)
			}
		}
	}

	return strings.Join(out, ",")
}

// relayMessages copies Anthropic's event stream to the client line for line,
// and reads the usage on the way.
func (s *Server) relayMessages(w http.ResponseWriter, r *http.Request, asked int, resp *http.Response, record *Usage) {
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
	// ended by closing its body, as in the other routes.
	idle := time.AfterFunc(s.idleTimeout, func() { _ = resp.Body.Close() })
	defer idle.Stop()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxEventBytes)

	var (
		usage                 claude.PassUsage
		completed, clientGone bool
		sent                  int
	)

	for scanner.Scan() {
		idle.Reset(s.idleTimeout)

		line := scanner.Bytes()

		if payload, isData := bytes.CutPrefix(line, []byte("data:")); isData {
			switch {
			case bytes.Contains(payload, []byte(`"message_start"`)), bytes.Contains(payload, []byte(`"message_delta"`)):
				usage.Note(payload)
			case bytes.Contains(payload, []byte(`"message_stop"`)):
				completed = true
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
		// The answer ended without message_stop: Anthropic said it failed
		// inside the stream, or went away. The client has what was sent.
		record.Outcome = OutcomeTruncated
	}

	record.Tokens = messagesTokens(usage)
	record.Tokens.estimate(asked, sent)
}

// passMessagesAnswer copies an answer that is not a stream, and reads its usage.
func passMessagesAnswer(w http.ResponseWriter, asked int, resp *http.Response, record *Usage) {
	body := copyBody(w, resp, resp.Header.Get("Content-Type"), maxResponseBytes)

	var usage claude.PassUsage
	usage.Note(body)

	record.Outcome = OutcomeOK
	record.Tokens = messagesTokens(usage)
	record.Tokens.estimate(asked, len(body))
}

// messagesTokens counts as the chat route counts a Claude answer: the prompt
// is the input with what was read from and written to the cache.
func messagesTokens(u claude.PassUsage) Tokens {
	return Tokens{
		Prompt: u.Input + u.CacheRead + u.CacheCreation, Completion: u.Output, Cached: u.CacheRead,
	}
}

// passMessagesRefusal passes a refusal on with its status, Retry-After and
// body. A refusal the gateway made itself in OpenAI's shape (every account
// resting) is put in Anthropic's.
func passMessagesRefusal(w http.ResponseWriter, resp *http.Response) {
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		w.Header().Set("Retry-After", retry)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))

	var shape struct {
		Type  string `json:"type"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if json.Unmarshal(body, &shape) == nil && shape.Type == "" && shape.Error != nil && shape.Error.Message != "" {
		writeAnthropicError(w, resp.StatusCode, shape.Error.Type, "", shape.Error.Message)

		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body) // The client went away; nothing is left to tell it.
}

// writeAnthropicError answers with an error in Anthropic's shape, which its
// clients read (CLIProxyAPI code_handlers.go). OpenAI's server_error is
// Anthropic's api_error; the other types have the same names.
func writeAnthropicError(w http.ResponseWriter, status int, kind, _, message string) {
	if kind == "" || kind == typeServer {
		kind = "api_error"
	}

	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]string{"type": kind, "message": message},
	})
}
