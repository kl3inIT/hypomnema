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
	"github.com/kl3inIT/hypomnema/internal/controlplane/machines"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
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
	// machines creates and removes provider machines; nil when the control plane wired none.
	machines Machines
	// terminals reaches a node for a terminal; nil when the control plane wired none.
	terminals Terminals
	nodeFiles NodeFiles
	botFiles  BotFiles
	tickets   terminalTickets
	// browser is the browser every agent shares; nil when the control plane has none.
	browser Browser
	views   viewTickets
	// web searches the web and reads pages (issue #354); nil when the control plane wired none.
	web Web
	// logins counts wrong passwords per address; now is the clock it reads.
	logins *loginLimiter
	now    func() time.Time
	// hostTokens keeps git hosts' tokens; nil when the control plane wired none.
	hostTokens HostTokens
	// secrets is how the owner reaches the vault; nil when the control plane wired none.
	secrets Secrets
	// integrations signs accounts in; nil when the control plane wired none.
	integrations Integrations
	approvals    Approvals
	// relay sends requests to a Connection that has its own address; nil when the control plane wired none.
	relay        Relay
	larkChannels LarkChannels
	// accounts shows the accounts with the state of their sign-ins and removes one; nil when the control plane
	// wired none, and then an account shows what orchestration knows of it.
	accounts Accounts
	// gatewayKeys manages the gateway's keys; nil when the control plane wired none.
	gatewayKeys  GatewayKeys
	gatewayUsage GatewayUsage
	// tokenAccounts keeps an account from a pasted token; nil when the vault has no key.
	tokenAccounts TokenAccounts
	// gatewayModels lists what the model gateway serves; nil when the control plane wired none.
	gatewayModels GatewayModels
	// accountSignIns are the sign-ins of subscription accounts begun and not completed.
	accountSignIns accountSignIns
	// skillSources fetches a skill from a repository; nil when the control plane wired none.
	skillSources SkillSources
	// skillDirectory searches skills.sh; nil when the control plane wired none.
	skillDirectory SkillDirectory
	// builtinSkills lists the skills the binary ships; nil when the control plane wired none.
	builtinSkills func() ([]BuiltinSkill, error)
	// routines starts the runs of Routines the owner asks for; nil when the control plane wired none.
	routines Routines
}

// Accounts is what the API asks of the service that signs subscription
// accounts in (issue #199).
type Accounts interface {
	// List returns every account with the state of its sign-in.
	List(ctx context.Context) ([]subscription.Account, error)
	// Remove deletes an account that was signed in here, and its tokens.
	Remove(ctx context.Context, id string) error
	// Limits says how much of an account's windows is used; fresh asks the
	// vendor now.
	Limits(ctx context.Context, id string, fresh bool) (subscription.Limits, error)
	// UseReset uses one of an account's limit resets and returns its limits
	// after it; requestID makes a repeated call the same use.
	UseReset(ctx context.Context, id, requestID string) (subscription.Limits, error)
	// BeginInBrowser starts a sign-in at the vendor of a runtime whose last
	// address the owner pastes back.
	BeginInBrowser(ctx context.Context, runtime string) (*subscription.Attempt, error)
	// Complete finishes a sign-in and stores it: a new account, or new tokens
	// for the account the vendor's id already has.
	Complete(ctx context.Context, a *subscription.Attempt, pasted, label string) (subscription.Account, error)
}

// GatewayModels lists the models the model gateway serves.
type GatewayModels interface {
	ListModels(ctx context.Context) ([]modelgateway.Model, error)
}

// GatewayKeys makes, lists and revokes the keys clients give the gateway
// (modelgateway.Service).
type GatewayKeys interface {
	ListKeys(ctx context.Context) ([]modelgateway.Key, error)
	AddKey(ctx context.Context, name string, models []string) (modelgateway.Key, string, error)
	RevokeKey(ctx context.Context, id string) error
}

// WithGatewayKeys lets the owner manage the gateway's keys.
func (s *Server) WithGatewayKeys(k GatewayKeys) *Server {
	s.gatewayKeys = k

	return s
}

// TokenAccounts keeps an account from a token the owner pastes; the control
// plane's wiring implements it, as the vault may not be imported here.
type TokenAccounts interface {
	// Add keeps the account and its token and returns the account's id.
	Add(ctx context.Context, runtime, label, token string) (string, error)
}

// WithTokenAccounts lets the owner add an account by pasting its token.
func (s *Server) WithTokenAccounts(t TokenAccounts) *Server {
	s.tokenAccounts = t

	return s
}

// WithAccounts lets the API show the sign-in of an account and remove one.
func (s *Server) WithAccounts(a Accounts) *Server {
	s.accounts = a

	return s
}

// WithGatewayModels lets the API list what the model gateway serves.
func (s *Server) WithGatewayModels(m GatewayModels) *Server {
	s.gatewayModels = m

	return s
}

// HostTokens is how the API has a Connection's token kept (issue #103). The
// API hands a token over and never gets one back: a secret's value is opened
// only by the control plane's own wiring (issue #66).
type HostTokens interface {
	// Connect keeps a token as a Connection of a git host, replacing the one it had.
	Connect(ctx context.Context, integration, label, token string) error
	Disconnect(ctx context.Context, integration, label string) error
	// RemoveHost forgets a declared host and its Connections.
	RemoveHost(ctx context.Context, host string) error
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
	// Key returns the model gateway's key of the chat's running pi.
	Key(chatID string) (string, bool)
}

// WithCoordinator lets chats report whether pi is working on them.
func (s *Server) WithCoordinator(c Coordinator) *Server {
	s.coordinator = c

	return s
}

// New returns a Server. Long requests end when stop is closed, so a shutdown
// does not wait for them.
func New(svc *orchestration.Service, logger *slog.Logger, stop <-chan struct{}) *Server {
	return &Server{
		svc: svc, logger: logger, stop: stop, heartbeat: heartbeatEvery, logins: newLoginLimiter(), now: time.Now,
	}
}

// Machines is what the API asks of the manager of provider machines.
type Machines interface {
	// Create asks a provider for a machine. A node returned with an error is
	// listed as failed, with the reason.
	Create(ctx context.Context, n machines.NewMachine) (orchestration.Node, error)
	// Remove removes any node; a provider's machine is destroyed with it.
	Remove(ctx context.Context, nodeID string) error
	// Stop and Start turn a provider's machine off and on. A node returned
	// with an error is still between the two, and the step is asked again.
	Stop(ctx context.Context, nodeID string) (orchestration.Node, error)
	Start(ctx context.Context, nodeID string) (orchestration.Node, error)
}

// WithMachines sets the manager of provider machines.
func (s *Server) WithMachines(m Machines) *Server {
	s.machines = m

	return s
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

	// A WebSocket is not in the contract's request and response shapes; its ticket is its permission.
	mux.HandleFunc("GET "+BasePath+"/terminals/{ticket}", s.terminalSocket)
	mux.HandleFunc("GET "+BasePath+"/browser/views/{ticket}", s.browserViewSocket)

	mux.Handle(BasePath+"/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, bodyLimit(r))
		validate(api).ServeHTTP(w, r)
	}))

	return nil
}

// bodyLimit is what a request's body may hold: a skill's archive is the one
// body over the default (skillpack.MaxArchive).
func bodyLimit(r *http.Request) int64 {
	if r.Method == http.MethodPost && r.URL.Path == BasePath+"/skills/archive" {
		return skillpack.MaxArchive
	}

	return maxBodyBytes
}

// authenticate returns who makes a request: the user of its session cookie,
// acting as the owner, or else the token it bears.
func (s *Server) authenticate(ctx context.Context, r *http.Request) (orchestration.Token, string, error) {
	if secret := sessionSecret(r); secret != "" {
		tok, session, err := s.svc.AuthenticateSession(ctx, secret)
		if err == nil {
			return tok, session.ID, nil
		}

		if !errors.Is(err, orchestration.ErrNotFound) {
			return orchestration.Token{}, "", err
		}
	}

	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" {
		return orchestration.Token{}, "", errUnauthorized
	}

	tok, err := s.svc.AuthenticateToken(ctx, secret)
	if errors.Is(err, orchestration.ErrNotFound) {
		return orchestration.Token{}, "", errUnauthorized
	}

	return tok, "", err
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

// authorize authenticates the request, by its session cookie or else its
// bearer token, and applies the operation's policy. Every operation must
// have one (TestEveryOperationHasAPolicy).
func (s *Server) authorize(next openapi.StrictHandlerFunc, operationID string) openapi.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		p, ok := policies[operationID]
		if !ok {
			return nil, fmt.Errorf("operation %s has no policy", operationID)
		}

		c := newCaller(r)

		if p.public {
			ctx = context.WithValue(ctx, callerKey{}, c)

			return next(ctx, w, r.WithContext(ctx), request)
		}

		tok, sessionID, err := s.authenticate(ctx, r)
		if err != nil {
			return nil, err
		}

		c.sessionID = sessionID
		ctx = context.WithValue(context.WithValue(ctx, principalKey{}, tok), callerKey{}, c)

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
