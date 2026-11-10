// Package clientapi is the control plane's client API (issue #41): one API at
// /api/v1 that the web client, the hypomnema CLI and coordinators call. The
// contract is api/openapi.yaml; the server types are generated into
// clientapi/openapi by oapi-codegen (make openapi-generate).
//
// It follows Omnara's internal/httpapi (0ef1711): a strict server with one
// middleware that authenticates the bearer token and applies the operation's
// policy before the handler runs (openapi_strict.go, openapi_policies.go),
// request validation against the embedded spec (openapi_request_validator.go),
// errors as {code, message} (apierror), and Server-Sent Events with
// Last-Event-ID and a heartbeat (agent_runtime_routes.go).
package clientapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// BasePath is where the API is served.
const BasePath = "/api/v1"

const (
	// maxBodyBytes bounds a request body.
	maxBodyBytes = 1 << 20
	// heartbeatEvery is the SSE comment interval; Omnara's SDK treats 35 s of
	// silence as a stall.
	heartbeatEvery = 10 * time.Second
)

// Server serves the client API over the orchestration service.
type Server struct {
	svc    *orchestration.Service
	logger *slog.Logger
	// stop ends long requests (event waits and streams) when the server shuts down.
	stop      <-chan struct{}
	heartbeat time.Duration
	// coordinator reports whether pi works on a chat; nil when none runs.
	coordinator Coordinator
	// hostTokens keeps git hosts' tokens; nil when the control plane wired none.
	hostTokens HostTokens
}

// HostTokens is how the API has a git host's token kept. The API hands a token
// over and never gets one back: a secret's value is opened only by the control
// plane's own wiring (issue #66).
type HostTokens interface {
	Set(ctx context.Context, host, token string) error
	Clear(ctx context.Context, host string) error
	// Remove forgets a declared host and its token.
	Remove(ctx context.Context, host string) error
}

// WithHostTokens sets where git hosts' tokens are kept.
func (s *Server) WithHostTokens(h HostTokens) *Server {
	s.hostTokens = h

	return s
}

// Coordinator is what the API asks of the coordinator's supervisor.
type Coordinator interface {
	Enabled() bool
	Working(chatID string) bool
	// Watch returns the chat's live frames until stop is called.
	Watch(chatID string) (frames <-chan coordinator.Frame, stop func())
	// Abort asks pi to stop its current turn in the chat.
	Abort(ctx context.Context, chatID string) error
}

// WithCoordinator lets chats report whether pi is working on them.
func (s *Server) WithCoordinator(c Coordinator) *Server {
	s.coordinator = c

	return s
}

// New returns a Server. Long requests end when stop is closed, so a shutdown
// does not wait for them.
func New(svc *orchestration.Service, logger *slog.Logger, stop <-chan struct{}) *Server {
	return &Server{svc: svc, logger: logger, stop: stop, heartbeat: heartbeatEvery}
}

// Register adds the API under BasePath to mux.
func (s *Server) Register(mux *http.ServeMux) error {
	validate, err := requestValidator()
	if err != nil {
		return err
	}

	api := http.NewServeMux()
	openapi.HandlerWithOptions(
		openapi.NewStrictHandlerWithOptions(s, []openapi.StrictMiddlewareFunc{s.authorize},
			openapi.StrictHTTPServerOptions{
				RequestErrorHandlerFunc:  s.writeRequestError,
				ResponseErrorHandlerFunc: s.writeError,
			}),
		openapi.StdHTTPServerOptions{BaseURL: BasePath, BaseRouter: api, ErrorHandlerFunc: s.writeRequestError},
	)

	mux.Handle(BasePath+"/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		validate(api).ServeHTTP(w, r)
	}))

	return nil
}

// requestValidator checks every request against the spec before it reaches a
// handler; authentication is the strict middleware's.
func requestValidator() (func(http.Handler) http.Handler, error) {
	spec, err := openapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("loading the embedded OpenAPI spec: %w", err)
	}

	return nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc:  openapi3filter.NoopAuthenticationFunc,
			SkipSettingDefaults: true,
		},
		SilenceServersWarning: true,
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request,
			opts nethttpmiddleware.ErrorHandlerOpts,
		) {
			code := openapi.ValidationFailed

			switch {
			case opts.MatchedRoute == nil && opts.StatusCode == http.StatusNotFound:
				code = openapi.NotFound
			case opts.StatusCode == http.StatusMethodNotAllowed:
				writeJSONError(w, http.StatusMethodNotAllowed, openapi.InvalidRequest, "method not allowed")

				return
			}

			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				code = openapi.RequestTooLarge
			}

			writeJSONError(w, statusOf(code), code, validationMessage(err))
		},
	}), nil
}

func validationMessage(err error) string {
	var re *openapi3filter.RequestError
	if errors.As(err, &re) {
		return re.Error()
	}

	return err.Error()
}

// principalKey carries the authenticated token in a request's context.
type principalKey struct{}

func principal(ctx context.Context) orchestration.Token {
	t, _ := ctx.Value(principalKey{}).(orchestration.Token)

	return t
}

// authorize authenticates the bearer token and applies the operation's
// policy. Every operation must have one (TestEveryOperationHasAPolicy).
func (s *Server) authorize(next openapi.StrictHandlerFunc, operationID string) openapi.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		p, ok := policies[operationID]
		if !ok {
			return nil, fmt.Errorf("operation %s has no policy", operationID)
		}

		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || secret == "" {
			return nil, errUnauthorized
		}

		tok, err := s.svc.AuthenticateToken(ctx, secret)
		if errors.Is(err, orchestration.ErrNotFound) {
			return nil, errUnauthorized
		}

		if err != nil {
			return nil, err
		}

		ctx = context.WithValue(ctx, principalKey{}, tok)

		if err := s.checkPolicy(ctx, p, tok, request); err != nil {
			return nil, err
		}

		if p.long {
			// Event waits and streams outlive the server's write timeout.
			_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		}

		return next(ctx, w, r.WithContext(ctx), request)
	}
}
