package modelgateway

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/cloudcode"
)

// The gateway's HTTP API speaks OpenAI's shapes, so any OpenAI client works
// with a base URL and a key: a model list as {"object":"list","data":[…]} and
// errors as {"error":{message,type,code}}, as 9router-go
// (internal/handlerutil/response.go) and new-api (middleware/utils.go) do.

// Config tunes the gateway's HTTP API; the zero value is the default.
type Config struct {
	// StreamIdleTimeout ends a stream whose provider sends nothing for this
	// long; zero is DefaultStreamIdleTimeout.
	StreamIdleTimeout time.Duration
	// Subscriptions gives the built-in providers the accounts logged in on
	// the control plane; nil: those providers answer that they have none.
	Subscriptions Subscriptions
	// CodexBaseURL is the Codex backend; empty: ChatGPT's. Tests name their own.
	CodexBaseURL string
	// AntigravityBaseURL is the Cloud Code host Antigravity accounts are
	// served from, and AntigravityManifestURL the IDE's update manifest;
	// empty: Google's. Tests name their own.
	AntigravityBaseURL     string
	AntigravityManifestURL string
}

// Server serves the gateway's HTTP API.
type Server struct {
	svc         *Service
	logger      *slog.Logger
	client      *http.Client
	idleTimeout time.Duration
	accounts    Subscriptions
	codexURL    string
	// antigravityURL is the Cloud Code host; antigravity, what is remembered
	// of Antigravity accounts while the gateway runs.
	antigravityURL string
	antigravity    *antigravityState
	// accountState is what is remembered of every built-in provider's accounts.
	accountState *accountState
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

	if cfg.CodexBaseURL == "" {
		cfg.CodexBaseURL = codexBaseURL
	}

	if cfg.AntigravityBaseURL == "" {
		cfg.AntigravityBaseURL = cloudcode.DailyHost
	}

	return &Server{
		svc: svc, logger: logger, client: &http.Client{Transport: transport}, idleTimeout: cfg.StreamIdleTimeout,
		accounts: cfg.Subscriptions, codexURL: strings.TrimRight(cfg.CodexBaseURL, "/"),
		antigravityURL: strings.TrimRight(cfg.AntigravityBaseURL, "/"),
		antigravity:    newAntigravityState(cfg.AntigravityManifestURL, svc.clock.Now),
		accountState:   newAccountState(),
	}
}

// Register adds the gateway's routes to a mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models", s.listModels)
	mux.HandleFunc("POST /v1/chat/completions", s.chatCompletions)
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

// authenticate returns the key a request carries in `Authorization: Bearer`.
// It answers the request itself when there is none.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (Key, bool) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" {
		writeError(w, http.StatusUnauthorized, typeAuthentication, "missing_api_key",
			"a gateway key is required: Authorization: Bearer <key>")

		return Key{}, false
	}

	k, err := s.svc.Authenticate(r.Context(), secret)
	if errors.Is(err, ErrUnauthorized) {
		writeError(w, http.StatusUnauthorized, typeAuthentication, "invalid_api_key", "unknown or revoked gateway key")

		return Key{}, false
	}

	if err != nil {
		s.internal(w, r, "modelgateway.auth_failed", err)

		return Key{}, false
	}

	return k, true
}

func (s *Server) internal(w http.ResponseWriter, r *http.Request, event string, err error) {
	s.logger.ErrorContext(r.Context(), "the model gateway failed",
		slog.String("event", event), slog.String("error", err.Error()))
	writeError(w, http.StatusInternalServerError, typeServer, "internal_error", "internal error")
}

type modelList struct {
	Object string      `json:"object"`
	Data   []modelItem `json:"data"`
}

type modelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// listModels answers GET /v1/models with the models the key may use.
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
	for _, m := range models {
		out.Data = append(out.Data, modelItem{
			ID: m.Name, Object: "model", Created: m.CreatedAt.Unix(), OwnedBy: "hypomnema",
		})
	}

	writeJSON(w, http.StatusOK, out)
}
