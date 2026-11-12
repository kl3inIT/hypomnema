// Package agentapi is what an agent's tool launcher asks the control plane
// (issue #103, step 3): which accounts of an Integration the agent may use,
// and a short-lived token for one of them. It is one API for every agent.
// Who the agent is decides what it gets:
//
//   - a worker presents its Dispatch's own key and gets the Connections its
//     Run was given; when a coordinator created the Run, only while the owner
//     still allows them for chats;
//   - the coordinator presents its coordinator token and gets the Connections
//     the owner switched on for chats;
//   - the owner's own token gets every Connection, as it can already read the
//     vault.
//
// A refresh token and a client secret never pass through here: the token it
// returns is the access token, good for about an hour. It is handed only for
// an Integration that does not go through the service proxy; Google and Lark
// do (issues #154, #175), so an agent gets none for them, and the proxy asks
// this package who the agent is and what it may use.
package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Store is what the API needs of orchestration.
type Store interface {
	AuthenticateToken(ctx context.Context, secret string) (orchestration.Token, error)
	AgentSessionOf(ctx context.Context, secret string) (orchestration.AgentSession, error)
	ConnectionsOf(ctx context.Context, integration string) ([]orchestration.Connection, error)
	IntegrationClientOf(ctx context.Context, integration string) (orchestration.IntegrationClient, error)
}

// Token is a Connection's access token and when it ends.
type Token struct {
	AccessToken string
	ExpiresAt   time.Time
	// ClientID is the id of the Integration's client when its tool needs it
	// beside the token: a Lark app's id. Not a secret.
	ClientID string
}

// Tokens gives a Connection's access token, fresh. It is the control plane's
// own wiring over the vault: this package never opens a secret.
type Tokens func(ctx context.Context, integration, label string) (Token, error)

// Browser is the browser every agent shares (issue #193): its tools, and one
// call of a tool in the tab of the agent named.
type Browser interface {
	Tools(ctx context.Context) ([]browser.Tool, error)
	Call(ctx context.Context, agent, tool string, arguments json.RawMessage) (json.RawMessage, error)
}

// Server serves the agent API.
type Server struct {
	store   Store
	tokens  Tokens
	browser Browser
	logger  *slog.Logger
}

// New returns a Server.
func New(store Store, tokens Tokens, logger *slog.Logger) *Server {
	return &Server{store: store, tokens: tokens, logger: logger}
}

// WithBrowser gives agents the shared browser. Without it the browser's
// routes answer that there is none.
func (s *Server) WithBrowser(b Browser) *Server {
	s.browser = b

	return s
}

// Register adds the API's routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+agenttool.PathConnections, s.connections)
	mux.HandleFunc("POST "+agenttool.PathToken, s.token)
	mux.HandleFunc("GET "+agenttool.PathBrowserTools, s.browserTools)
	mux.HandleFunc("POST "+agenttool.PathBrowserCall, s.browserCall)
}

// agent is who asks: what it is called in the log, and which Connections of
// an Integration it may use.
type agent struct {
	name string
	may  func(c orchestration.Connection) bool
	// dispatchID is set for a worker; coordinator and owner say which token
	// it was otherwise.
	dispatchID  string
	coordinator bool
	owner       bool
	// key names the agent's own tab in the shared browser: a worker's is its
	// Dispatch's, so a new Dispatch starts on a new tab.
	key string
}

var errNoCredential = errors.New("the agent's credential is missing or no longer works")

// authenticate reads the bearer token and says who it is.
func (s *Server) authenticate(r *http.Request) (agent, error) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" {
		return agent{}, errNoCredential
	}

	kind, err := bearertoken.Parse(secret)
	if err != nil {
		return agent{}, errNoCredential
	}

	if kind != bearertoken.Session && kind != bearertoken.Owner && kind != bearertoken.Coordinator {
		return agent{}, errNoCredential
	}

	if kind == bearertoken.Session {
		session, err := s.store.AgentSessionOf(r.Context(), secret)
		if err != nil {
			return agent{}, errNoCredential
		}

		return agent{
			name: "dispatch " + session.DispatchID, dispatchID: session.DispatchID, key: "d-" + session.DispatchID,
			// A Run a coordinator created was given only what the coordinator may use, and keeps it only while
			// the coordinator may: the owner's switch is read now, not when the Run was made.
			may: func(c orchestration.Connection) bool {
				return slices.Contains(session.Uses, orchestration.Use(c.Integration, c.Label)) &&
					(!session.ByCoordinator || c.Chats)
			},
		}, nil
	}

	token, err := s.store.AuthenticateToken(r.Context(), secret)
	if err != nil {
		return agent{}, errNoCredential
	}

	// Each kind that may ask is named; a kind added later gets nothing until it is added here.
	switch token.Kind {
	case bearertoken.Owner:
		return agent{
			name: "owner token " + token.ID, owner: true, key: "o-" + token.ID,
			may: func(orchestration.Connection) bool { return true },
		}, nil
	case bearertoken.Coordinator:
		return agent{
			name: "coordinator token " + token.ID, coordinator: true, key: "c-" + token.ID,
			may: func(c orchestration.Connection) bool { return c.Chats },
		}, nil
	default:
		return agent{}, errNoCredential
	}
}

// Caller says who a request's credential belongs to, for the service proxy
// (issue #154), which serves the same agents by the same rules.
func (s *Server) Caller(r *http.Request) (serviceproxy.Caller, error) {
	a, err := s.authenticate(r)
	if err != nil {
		return serviceproxy.Caller{}, err
	}

	return serviceproxy.Caller{Name: a.name, DispatchID: a.dispatchID, Coordinator: a.coordinator}, nil
}

// May reports whether the request's agent may use a Connection.
func (s *Server) May(ctx context.Context, r *http.Request, integration, label string) (bool, error) {
	a, err := s.authenticate(r)
	if err != nil {
		return false, nil //nolint:nilerr // No credential, no Connection: the caller is refused, not failed.
	}

	allowed, err := s.allowed(ctx, a, integration)
	if err != nil {
		return false, err
	}

	return slices.ContainsFunc(allowed, func(c orchestration.Connection) bool { return c.Label == label }), nil
}

func (s *Server) allowed(ctx context.Context, a agent, integration string) ([]orchestration.Connection, error) {
	all, err := s.store.ConnectionsOf(ctx, integration)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(all, func(c orchestration.Connection) bool { return !a.may(c) }), nil
}

func (s *Server) connections(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	integration := r.URL.Query().Get("integration")

	allowed, err := s.allowed(r.Context(), a, integration)
	if err != nil {
		s.fail(w, r, err)

		return
	}

	out := agenttool.ConnectionList{Data: make([]agenttool.Connection, 0, len(allowed))}
	for _, c := range allowed {
		out.Data = append(out.Data, agenttool.Connection{Label: c.Label, Account: c.Account})
	}

	// A tool that will not run without the id of the Integration's client is told it with the accounts: a Lark
	// app's id, which is not a secret. An agent that was given no account is told nothing.
	if def, ok := agenttool.Of(integration); ok && def.ClientEnv != "" && len(allowed) > 0 {
		client, err := s.store.IntegrationClientOf(r.Context(), integration)
		if err != nil && !errors.Is(err, orchestration.ErrNotFound) {
			s.fail(w, r, err)

			return
		}

		out.ClientID = client.ClientID
	}

	answer(w, out)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	var asked agenttool.TokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&asked); err != nil ||
		asked.Integration == "" || asked.Label == "" {
		refuse(w, http.StatusBadRequest, "name the account as an integration and a label")

		return
	}

	allowed, err := s.allowed(r.Context(), a, asked.Integration)
	if err != nil {
		s.fail(w, r, err)

		return
	}

	if !slices.ContainsFunc(allowed, func(c orchestration.Connection) bool { return c.Label == asked.Label }) {
		// The same answer whether the account does not exist or was not given: an agent learns nothing of
		// accounts it may not use.
		refuse(w, http.StatusForbidden, fmt.Sprintf("this agent was not given %s:%s", asked.Integration, asked.Label))

		return
	}

	// An Integration that goes through the service proxy hands no token to an agent: its tool is given a
	// placeholder and the proxy adds the token (issue #154). The owner's own token still gets one, as it can
	// read the vault.
	if _, proxied := serviceproxy.CatalogOf(asked.Integration); proxied && !a.owner {
		refuse(w, http.StatusForbidden, fmt.Sprintf("no token of %s is handed to an agent: its tool goes through "+
			"the service proxy. Run the tool as the guide says", asked.Integration))

		return
	}

	token, err := s.tokens(r.Context(), asked.Integration, asked.Label)
	if err != nil {
		s.fail(w, r, err)

		return
	}

	// What was handed out, to whom, and never the token.
	s.logger.InfoContext(r.Context(), "an agent got an account's token",
		slog.String("event", "agentapi.token"), slog.String("agent", a.name),
		slog.String("connection", orchestration.Use(asked.Integration, asked.Label)))

	w.Header().Set("Cache-Control", "no-store")
	answer(w, agenttool.TokenResponse{
		AccessToken: token.AccessToken, ExpiresAt: token.ExpiresAt, ClientID: token.ClientID,
	})
}

// maxBrowserCallBytes bounds a browser call's body: the text a tool types is
// the largest part of one.
const maxBrowserCallBytes = 1 << 20

func (s *Server) browserTools(w http.ResponseWriter, r *http.Request) {
	if _, err := s.authenticate(r); err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	if s.browser == nil {
		refuse(w, http.StatusServiceUnavailable, "this control plane has no browser")

		return
	}

	tools, err := s.browser.Tools(r.Context())
	if err != nil {
		s.failBrowser(w, r, err)

		return
	}

	out := agenttool.BrowserToolList{Data: make([]agenttool.BrowserTool, 0, len(tools))}
	for _, t := range tools {
		out.Data = append(out.Data, agenttool.BrowserTool{
			Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
		})
	}

	answer(w, out)
}

func (s *Server) browserCall(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	if s.browser == nil {
		refuse(w, http.StatusServiceUnavailable, "this control plane has no browser")

		return
	}

	var asked agenttool.BrowserCall
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBrowserCallBytes)).Decode(&asked); err != nil ||
		asked.Tool == "" {
		refuse(w, http.StatusBadRequest, "name the tool and give its arguments as a JSON object")

		return
	}

	result, err := s.browser.Call(r.Context(), a.key, asked.Tool, asked.Arguments)
	if err != nil {
		s.failBrowser(w, r, err)

		return
	}

	// Which agent used which tool, and never what it typed or read.
	s.logger.InfoContext(r.Context(), "an agent used the browser",
		slog.String("event", "agentapi.browser_call"), slog.String("agent", a.name),
		slog.String("tool", asked.Tool))

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

// failBrowser answers an error of the browser: a call the agent made wrongly
// says how, a browser that cannot be used says why, anything else is logged
// and not told.
func (s *Server) failBrowser(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, browser.ErrNoSuchTool):
		refuse(w, http.StatusNotFound, err.Error())
	case errors.Is(err, browser.ErrBadArguments):
		refuse(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, browser.ErrUnavailable):
		s.logger.WarnContext(r.Context(), "the browser was not available",
			slog.String("event", "agentapi.browser_unavailable"), slog.String("error", err.Error()))
		refuse(w, http.StatusServiceUnavailable, err.Error())
	default:
		s.fail(w, r, err)
	}
}

// fail answers an error of the store or the vault's wiring. A Connection
// that must sign in again says so; anything else is logged and not told.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, orchestration.ErrConflict) || errors.Is(err, orchestration.ErrNotFound) {
		refuse(w, http.StatusConflict, err.Error())

		return
	}

	s.logger.ErrorContext(r.Context(), "the agent API failed", slog.String("event", "agentapi.failed"),
		slog.String("error", err.Error()))
	refuse(w, http.StatusInternalServerError, "the control plane could not answer; try again")
}

func answer(w http.ResponseWriter, body any) { write(w, http.StatusOK, body) }

func refuse(w http.ResponseWriter, status int, message string) {
	write(w, status, map[string]string{"message": message})
}

// write sends one JSON answer. A body that cannot be encoded is this
// package's own mistake and answers 500; a client that stopped reading is
// not something to act on.
func write(w http.ResponseWriter, status int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		status, raw = http.StatusInternalServerError, []byte(`{"message":"the answer could not be written"}`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
