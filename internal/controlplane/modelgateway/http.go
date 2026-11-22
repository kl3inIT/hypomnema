package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/cloudcode"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
	"github.com/kl3inIT/hypomnema/internal/shared/agentconfig"
)

// The gateway's HTTP API speaks OpenAI's shapes, so any OpenAI client works
// with a base URL and a key: a model list as {"object":"list","data":[…]} and
// errors as {"error":{message,type,code}}, as 9router-go
// (internal/handlerutil/response.go) and new-api (middleware/utils.go) do.

// Config tunes the gateway's HTTP API; the zero value is the default.
type Config struct {
	ChatFallbacks func(ctx context.Context, chatID, primary string) ([]string, error)
	// StreamIdleTimeout ends a stream whose provider sends nothing for this
	// long; zero is DefaultStreamIdleTimeout.
	StreamIdleTimeout time.Duration
	// Subscriptions gives the built-in providers the accounts logged in on
	// the control plane; nil: those providers answer that they have none.
	Subscriptions Subscriptions
	// CodexBaseURL is the Codex backend; empty: ChatGPT's. Tests name their own.
	CodexBaseURL string
	// ClaudeBaseURL is the Anthropic Messages backend; empty: Anthropic's.
	// Tests name their own.
	ClaudeBaseURL string
	// AntigravityBaseURL is the Cloud Code host Antigravity accounts are
	// served from, and AntigravityManifestURL the IDE's update manifest;
	// empty: Google's. Tests name their own.
	AntigravityBaseURL     string
	AntigravityManifestURL string
	// OpenCodeGoBaseURL is OpenCode Go's inference root; empty: opencode.ai's.
	// Tests name their own.
	OpenCodeGoBaseURL string
	// OpenCodeZenBaseURL is OpenCode's inference root, where Zen's models are
	// served; empty: opencode.ai's. Tests name their own.
	OpenCodeZenBaseURL string
	// Web searches the web and reads pages for /v1/search and /v1/web/fetch
	// (issue #354); nil: those routes answer that there is none.
	Web *websearch.Service
	// Speech hears and makes speech for /v1/audio/* (issue #694); nil: those
	// routes answer that there is none.
	Speech *speech.Service
}

// Server serves the gateway's HTTP API.
type Server struct {
	chatFallbacks func(ctx context.Context, chatID, primary string) ([]string, error)
	svc           *Service
	logger        *slog.Logger
	client        *http.Client
	idleTimeout   time.Duration
	accounts      Subscriptions
	codexURL      string
	claudeURL     string
	// antigravityURL is the Cloud Code host; antigravity, what is remembered
	// of Antigravity accounts while the gateway runs.
	antigravityURL string
	antigravity    *antigravityState
	// goURL is OpenCode Go's inference root (issue #854).
	goURL string
	// zenURL is OpenCode's inference root, for OpenCode Zen (issue #965).
	zenURL string
	// accountState is what is remembered of every built-in provider's accounts.
	accountState *accountState
	// web serves the web's search and pages; nil when the control plane gave none.
	web *websearch.Service
	// imageClient waits longer for an answer than chat's client: an image
	// answers once it is made (images.go); imageRests are the image models
	// of accounts that rest.
	imageClient *http.Client
	imageRests  *imageRests
	// speech serves /v1/audio/*; nil when the control plane gave none.
	speech *speech.Service
	// overflows are the keys' runs of prompts that did not fit their model (issue #1052).
	overflows *overflows
}

// NewServer returns the gateway's HTTP API over a Service.
func NewServer(svc *Service, logger *slog.Logger, cfg Config) *Server {
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = DefaultStreamIdleTimeout
	}

	// No total timeout: a stream lasts as long as it sends. The wait for the
	// provider's first answer is bounded, and the idle timeout covers the rest.
	//nolint:forcetypeassert // the standard library's own type
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = responseHeaderTimeout

	imageTransport := transport.Clone()
	imageTransport.ResponseHeaderTimeout = imageHeaderTimeout

	if cfg.CodexBaseURL == "" {
		cfg.CodexBaseURL = codexBaseURL
	}

	if cfg.ClaudeBaseURL == "" {
		cfg.ClaudeBaseURL = claudeBaseURL
	}

	if cfg.OpenCodeGoBaseURL == "" {
		cfg.OpenCodeGoBaseURL = openCodeGoBaseURL
	}

	if cfg.OpenCodeZenBaseURL == "" {
		cfg.OpenCodeZenBaseURL = openCodeZenBaseURL
	}

	if cfg.AntigravityBaseURL == "" {
		cfg.AntigravityBaseURL = cloudcode.DailyHost
	}

	return &Server{
		chatFallbacks: cfg.ChatFallbacks,
		svc:           svc, logger: logger, client: &http.Client{Transport: transport}, idleTimeout: cfg.StreamIdleTimeout,
		accounts: cfg.Subscriptions, codexURL: strings.TrimRight(cfg.CodexBaseURL, "/"),
		claudeURL:      strings.TrimRight(cfg.ClaudeBaseURL, "/"),
		antigravityURL: strings.TrimRight(cfg.AntigravityBaseURL, "/"),
		antigravity:    newAntigravityState(cfg.AntigravityManifestURL, svc.clock.Now),
		goURL:          strings.TrimRight(cfg.OpenCodeGoBaseURL, "/"),
		zenURL:         strings.TrimRight(cfg.OpenCodeZenBaseURL, "/"),
		accountState:   newAccountState(),
		web:            cfg.Web,
		imageClient:    &http.Client{Transport: imageTransport},
		imageRests:     newImageRests(),
		overflows:      newOverflows(),
		speech:         cfg.Speech,
	}
}

// Register adds the gateway's routes to a mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models", s.listModels)
	// Codex's catalog of the key's models, at its provider's model_catalog_url (codex_catalog.go, issue #907).
	mux.HandleFunc("GET "+agentconfig.CodexCatalogPath, s.codexModels)
	// A conversation is sent whole with each request, megabytes for a long one: it may take longer to arrive than
	// the server's read timeout (issue #977), as a file does.
	mux.HandleFunc("POST /v1/chat/completions", longBody(s.chatCompletions))
	mux.HandleFunc("POST /v1/responses", longBody(s.responses))
	mux.HandleFunc("POST /v1/messages", longBody(s.messages))
	mux.HandleFunc("POST /v1/messages/count_tokens", longBody(s.countTokens))
	// A search tries each provider in turn, and a page may be read twice (directly, then by Jina): longer than
	// the server's write timeout at worst, within their own bounds.
	mux.HandleFunc("POST /v1/search", longAnswer(s.webSearch))
	mux.HandleFunc("POST /v1/web/fetch", longAnswer(s.webFetch))
	// Codex's standalone search, at <base URL>/alpha/search (alpha_search.go).
	mux.HandleFunc("POST /v1/alpha/search", longAnswer(s.alphaSearch))
	// Images (issue #431): one is made in a minute or more, longer than the write timeout.
	mux.HandleFunc("POST /v1/images/generations", longAnswer(s.imageGenerations))
	mux.HandleFunc("POST /v1/images/edits", longBody(s.imageEdits))
	// Audio (issue #694): a Soniox transcription is one request that polls its job up to 30 minutes.
	mux.HandleFunc("POST /v1/audio/transcriptions", longBody(s.audioTranscriptions))
	mux.HandleFunc("POST /v1/audio/speech", longAnswer(s.audioSpeech))
	mux.HandleFunc("GET /v1/audio/voices", s.audioVoices)
}

// longAnswer lifts the server's write timeout for a model's answer, which streams for as long as the model works:
// the client API does the same for its streams (clientapi/server.go). A provider that stops sending is ended by the
// stream idle timeout instead.
func longAnswer(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		h(w, withAccountWait(r))
	}
}

// accountWait is how long a request may wait, in all, for the accounts' lists of models being read before it is told
// to try again (issue #814): one bound for the whole request, through every pick and fallback.
const accountWait = 30 * time.Second

type accountWaitKey struct{}

// withAccountWait gives a request the moment its wait for the accounts' lists ends.
func withAccountWait(r *http.Request) *http.Request {
	return r.WithContext(WithAccountWait(r.Context(), time.Now().Add(accountWait)))
}

// WithAccountWait gives ctx the moment its request stops waiting for the accounts' lists of models (AccountWaitEnds).
func WithAccountWait(ctx context.Context, ends time.Time) context.Context {
	return context.WithValue(ctx, accountWaitKey{}, ends)
}

// AccountWaitEnds is when the request of ctx stops waiting for the accounts' lists of models; ok is false outside
// one.
func AccountWaitEnds(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(accountWaitKey{}).(time.Time)

	return t, ok
}

// longBody is longAnswer for a request whose body may take longer to arrive than the server's read timeout (15 s,
// counted from the request's start), so the read deadline is moved too, by an hour (issue #779, as clientapi's
// uploads): a file (an image to edit, a recording to transcribe), or a model's conversation sent through a proxy
// that passes the body on as it comes (nginx's proxy_request_buffering off), which was cut at 15 s and answered 400
// (issue #977). None of 9router-go, CLIProxyAPI and LiteLLM's uvicorn bounds the time a body takes, only its
// headers (9router-go internal/app/server.go, CLIProxyAPI internal/api/server.go: ReadHeaderTimeout alone). The
// deadline bounds the body's reading only: net/http clears it once the body is read (connReader.startBackgroundRead).
func longBody(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(time.Hour))
		_ = rc.SetWriteDeadline(time.Time{})
		h(w, withAccountWait(r))
	}
}

type errorBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// Error types, as OpenAI names them.
const (
	typeInvalidRequest = "invalid_request_error"
	typeAuthentication = "authentication_error"
	typePermission     = "permission_error"
	typeServer         = "server_error"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		status, encoded = http.StatusInternalServerError, []byte(`{"error":{"message":"internal error"}}`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded) // The client went away; nothing is left to tell it.
}

func writeError(w http.ResponseWriter, status int, kind, code, message string) {
	var body errorBody

	body.Error.Message, body.Error.Type, body.Error.Code = message, kind, code
	writeJSON(w, status, body)
}

// authenticate returns the key a request carries, in `Authorization: Bearer`
// or in `x-api-key`, where Anthropic's clients put it (CLIProxyAPI, 9router-go
// and 9router read both). It answers the request itself when there is none.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (Key, bool) {
	return s.authenticateAs(w, r, writeError)
}

// authenticateFor returns the key of a request of a kind other than chat, and refuses a key not given that kind
// (issue #664) before anything is asked of a provider, as OmniRoute's enforceApiKeyPolicy answers 403 from
// validateEndpointAccess.
func (s *Server) authenticateFor(w http.ResponseWriter, r *http.Request, e Endpoint) (Key, bool) {
	k, ok := s.authenticate(w, r)
	if !ok {
		return Key{}, false
	}

	if !k.Reaches(e) {
		writeError(w, http.StatusForbidden, typePermission, "endpoint_not_allowed",
			fmt.Sprintf("this gateway key may not use %s; make a key that may", e))

		return Key{}, false
	}

	return k, true
}

// errorWriter answers a request with an error in one API's shape.
type errorWriter func(w http.ResponseWriter, status int, kind, code, message string)

func (s *Server) authenticateAs(w http.ResponseWriter, r *http.Request, fail errorWriter) (Key, bool) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" {
		secret = r.Header.Get("X-Api-Key")
	}

	if secret == "" {
		fail(w, http.StatusUnauthorized, typeAuthentication, "missing_api_key",
			"a gateway key is required: Authorization: Bearer <key>, or x-api-key: <key>")

		return Key{}, false
	}

	k, err := s.svc.Authenticate(r.Context(), secret)
	if errors.Is(err, ErrUnauthorized) {
		fail(w, http.StatusUnauthorized, typeAuthentication, "invalid_api_key", "unknown or revoked gateway key")

		return Key{}, false
	}

	if err != nil {
		s.logger.ErrorContext(r.Context(), "the model gateway failed",
			slog.String("event", "modelgateway.auth_failed"), slog.String("error", err.Error()))
		fail(w, http.StatusInternalServerError, typeServer, "internal_error", "internal error")

		return Key{}, false
	}

	return k, true
}

// readRequest reads the body of a request for a model, bounded by maxRequestBytes. A body that cannot be read whole
// for another reason than its size is logged with what is known of it (issue #977): the key, the headers that say
// how the body is framed, how much of it came and how long it took, so a failure names its cause. io.ReadAll gives
// back what it read before the error.
func (s *Server) readRequest(w http.ResponseWriter, r *http.Request, key Key, started time.Time) ([]byte, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))

	var tooLarge *http.MaxBytesError
	if err != nil && !errors.As(err, &tooLarge) {
		s.logger.WarnContext(r.Context(), "a request's body could not be read",
			slog.String("event", "modelgateway.body_unread"),
			slog.String("path", r.URL.Path),
			slog.String("key_id", key.ID),
			slog.String("key_name", key.Name),
			slog.String("proto", r.Proto),
			slog.String("content_length", r.Header.Get("Content-Length")),
			slog.String("transfer_encoding", strings.Join(r.TransferEncoding, ",")),
			slog.String("content_encoding", r.Header.Get("Content-Encoding")),
			slog.Int("bytes_read", len(raw)),
			slog.Int64("elapsed_ms", s.svc.clock.Now().Sub(started).Milliseconds()),
			slog.String("error", err.Error()))
	}

	return raw, err
}

func (s *Server) internal(w http.ResponseWriter, r *http.Request, event string, err error) {
	s.logger.ErrorContext(r.Context(), "the model gateway failed",
		slog.String("event", event), slog.String("error", err.Error()))
	writeError(w, http.StatusInternalServerError, typeServer, "internal_error", "internal error")
}

// modelList is OpenAI's model list, which Anthropic's has the same data of (GET /v1/models, `data`, `has_more`),
// so that Claude Code reads it too (issue #907).
type modelList struct {
	Object  string      `json:"object"`
	Data    []modelItem `json:"data"`
	HasMore bool        `json:"has_more"`
}

// modelItem is a model as OpenAI lists it, with Anthropic's display_name and the description Claude Code adds to
// its /model picker with gateway model discovery (llm-gateway-protocol, "Model discovery": data[].id, optional
// display_name and description), and what omp's proxy discovery reads of a row (omp
// packages/coding-agent/src/config/model-discovery.ts discoverProxyModels, docs/models.md "Proxy discovery"): its
// name, the wires it is served on (supported_endpoint_types, "anthropic" before "openai" for a model whose
// subscription speaks Messages, as agentconfig.Wires gives a worker its wire), its window (context_length) and its
// reasoning and effort levels (reasoning, supported_reasoning_levels). A value the gateway does not know is left out.
type modelItem struct {
	ID                       string   `json:"id"`
	Object                   string   `json:"object"`
	Created                  int64    `json:"created"`
	OwnedBy                  string   `json:"owned_by"`
	Name                     string   `json:"name"`
	DisplayName              string   `json:"display_name"`
	Description              string   `json:"description,omitempty"`
	SupportedEndpointTypes   []string `json:"supported_endpoint_types"`
	ContextLength            *int64   `json:"context_length,omitempty"`
	Reasoning                *bool    `json:"reasoning,omitempty"`
	SupportedReasoningLevels []string `json:"supported_reasoning_levels,omitempty"`
}

// listModels answers GET /v1/models with the models the key may use. A limit, as Claude Code asks
// (`?limit=1000`) and Anthropic's API takes, gives the first ones and says whether there are more.
func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	k, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	models, err := s.svc.ModelsFor(r.Context(), k)
	if err != nil {
		s.internal(w, r, "modelgateway.list_models_failed", err)

		return
	}

	out := modelList{Object: "list", Data: make([]modelItem, 0, len(models))}

	if limit, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && limit > 0 && limit < len(models) {
		models, out.HasMore = models[:limit], true
	}

	for _, m := range models {
		item, err := s.svc.listed(r.Context(), m)
		if err != nil {
			s.internal(w, r, "modelgateway.list_models_failed", err)

			return
		}

		out.Data = append(out.Data, item)
	}

	writeJSON(w, http.StatusOK, out)
}
