// Package agentapi is what an agent's tool launcher asks the control plane
// (issue #103, step 3): which accounts of an Integration the agent may use,
// and a short-lived token for one of them. It is one API for every agent.
// Who the agent is decides what it gets:
//
//   - a worker presents its Dispatch's own key and gets the Connections its
//     Run was given; when a coordinator created the Run, only while the owner
//     still allows them for chats;
//   - the coordinator presents its coordinator token and gets the Connections
//     the owner switched on for chats; a Bot, with its pi's or its Routine's
//     token, only those on its list, if it has one (issue #169);
//   - the owner's own token gets every Connection, as it can already read the
//     vault.
//
// A user Connection, the owner's own sign-in through an app (issue #468),
// acts as him, so it is narrower still: only the owner's token, and the pi of
// a Bot that may use it on the owner's own turn in its chat, may use it
// (issue #618), as a personal OpenMausBot installation gives every bot the
// person's connected apps (docs/composio.md). A worker, a coordinator token
// and a Routine's script never get it, whatever the switches say, as
// OpenMausBot keeps a person's connected app to the requests that are that
// person's own and out of a team (server/cloud-team.ts).
//
// An agent's tools server lists and calls here the control plane's tools
// (tools.go, issue #536), each given to the agent kinds its definition names:
// the memory's (memory.go), show_artifact (artifacts.go), the gateway's, the
// web's (issue #354, web.go) and generate_image (issue #492, images.go),
// Superset's for an agent given a Superset Connection (superset.go), a Bot's
// pi's own Routines (routines.go), and
// the browser every agent shares (issues #212, #536), a worker's and a Bot's
// pi's; and the tools of the phone a node holds (issue #417, device.go).
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
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/memorystore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Store is what the API needs of orchestration.
type Store interface {
	AuthenticateToken(ctx context.Context, secret string) (orchestration.Token, error)
	AgentSessionOf(ctx context.Context, secret string) (orchestration.AgentSession, error)
	ConnectionsOf(ctx context.Context, integration string) ([]orchestration.Connection, error)
	// ListGitHosts and KindOfHost say which git hosts are of a kind: a tool for a kind of host (glab) acts on a
	// Connection of any of them (issue #740).
	ListGitHosts(ctx context.Context) ([]orchestration.GitHost, error)
	serviceproxy.HostKinds
	GetBot(ctx context.Context, name string) (orchestration.Bot, error)
	// OwnerTurn says whether a chat's pi works on the owner's own request now (issue #468).
	OwnerTurn(ctx context.Context, chatID string) (bool, error)
	ReadRun(ctx context.Context, chatID string) (bool, error)
	// ChatTurn names the turn a chat's pi works on now, empty between turns (issue #817).
	ChatTurn(ctx context.Context, chatID string) (string, error)
	// ApprovedOnce says whether the owner approved a chat's request once and it has not run (issue #748).
	ApprovedOnce(ctx context.Context, n orchestration.NewApproval) (bool, error)
	// ShowArtifact and ArtifactMakerOfDispatch show an artifact for the show_artifact tool (issue #536), a
	// worker's as its Run's chat's.
	ShowArtifact(ctx context.Context, by orchestration.ArtifactMaker, in orchestration.ShowArtifact,
	) (orchestration.Artifact, bool, error)
	ArtifactMakerOfDispatch(ctx context.Context, dispatchID string) (orchestration.ArtifactMaker, error)
	// ToolSwitchesOfDispatch are a worker's tool switches, its profile version's (issue #536, step 7).
	ToolSwitchesOfDispatch(ctx context.Context, dispatchID string) (agenttool.ToolSwitches, error)
	Memories() *memorystore.Store
	// Channels says whether a chat is a Lark group's, which gets no memory tool (issue #589).
	Channels() *channelstore.Store
	// Routines keeps the Routines, for a Bot's pi's routines tool (issue #536).
	Routines() *routinestore.Store
	// Inbox keeps the owner's to-dos, for a Bot's pi's inbox tools (issue #1004); InboxWorkOf is where a pi works
	// on them, its chat or the Routine's run the chat was opened for, and InboxAttempt its attempt at the item it
	// names, which an Approval it asks records.
	Inbox() *inboxstore.Store
	InboxWorkOf(ctx context.Context, chatID string) (inboxstore.Work, error)
	InboxAttempt(ctx context.Context, todoID, bot, chatID string) (string, error)
	// SSHOfDispatch says whether a Dispatch's repository is cloned over SSH, and its host's keys, for the SSH key
	// (issue #625).
	SSHOfDispatch(ctx context.Context, dispatchID string) (hostKeys string, overSSH bool, err error)
}

// Token is a Connection's access token and when it ends.
type Token struct {
	AccessToken string
	ExpiresAt   time.Time
	// ClientID is the id of the Connection's app when its tool needs it
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
	// web and images are the gateway's tools a worker is given by its runtime, which runtimeOf reads,
	// and a Bot's pi (issues #354, #492); nil when the control plane gave none.
	web       Web
	images    Images
	audio     Audio
	runtimeOf RuntimeOf
	// muse is the Muse pool's tools (muse.go), given as the web's are.
	muse Muse
	// device is the phone a node holds (issue #417); nil when the control plane gave none.
	device Device
	// exec runs agents' commands on the nodes' machines (issue #543, nodes.go); nil when the control plane gave none.
	exec   *nodeExec
	videos *videoTools
	// relay sends Superset's tools' requests through the service proxy (issue #536); nil when the control
	// plane gave none.
	relay Relay
	// messaging reads the owner's WhatsApp and Zalo for their tools (#638, #680); nil when the control plane gave none.
	messaging Messaging
	// judge decides a send tool as the service proxy decides a request (#645, #680); nil sends nothing.
	judge Judge
	// routines starts a Routine's run for a Bot's pi's routines tool (issue #536); nil when the control plane
	// gave none.
	routines Routines
	// gitSSHKey is the control plane's SSH key for git (issue #625, gitssh.go); nil when the control plane gave
	// none.
	gitSSHKey GitSSHKey
	logger    *slog.Logger
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
	mux.HandleFunc("POST "+agenttool.PathMemory, s.memory)
	mux.HandleFunc("GET "+agenttool.PathDeviceTools, s.deviceTools)
	mux.HandleFunc("POST "+agenttool.PathDeviceCall, s.deviceCall)
	mux.HandleFunc("GET "+agenttool.PathGatewayTools, s.listGateway)
	mux.HandleFunc("POST "+agenttool.PathGatewayCall, s.callGateway)
	mux.HandleFunc("GET "+agenttool.PathTools, s.listTools)
	mux.HandleFunc("POST "+agenttool.PathToolsCall, s.callTool)
	mux.HandleFunc("GET "+agenttool.PathGitSSHKey, s.gitSSHKeyFor)
}

// agent is who asks: what it is called in the log, and which Connections of
// an Integration it may use.
type agent struct {
	name string
	may  func(c orchestration.Connection) bool
	// offered is what the agent's tools are listed for, whoever's turn it is: a Bot's pi keeps the owner's own
	// accounts' tools between his turns, and may decides each call (issue #748). nil is may.
	offered func(c orchestration.Connection) bool
	// dispatchID is set for a worker; coordinator and owner say which token
	// it was otherwise.
	dispatchID  string
	coordinator bool
	owner       bool
	// chatID is the chat a pi's own token is bound to, and bot that pi's Bot's id.
	chatID  string
	bot     string
	readRun bool
	// scope is the dashboards a Bot's agent may read of a Superset
	// Connection (issue #562); nil for every other agent, which reads all.
	scope func(label string) superset.Scope
	// key names the agent's own tab in the shared browser: a worker's is its
	// Dispatch's, so a new Dispatch starts on a new tab.
	key string
	// botSwitches are a Bot's pi's tool switches, its Bot's as read for this request (tools.go).
	botSwitches agenttool.ToolSwitches
}

// who is whose the agent's gateway calls are.
func (a agent) who() Who { return Who{DispatchID: a.dispatchID, ChatID: a.chatID} }

func (a agent) isBotPi() bool { return a.chatID != "" && a.bot != "" }

func (a agent) hasAgentTools() bool { return a.owner || a.dispatchID != "" || a.isBotPi() }

func (a agent) proxyCaller() serviceproxy.Caller {
	return serviceproxy.Caller{
		Name: a.name, DispatchID: a.dispatchID, Coordinator: a.coordinator, ChatID: a.chatID, ReadRun: a.readRun,
		Bot: a.bot,
	}
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

	if kind != bearertoken.Session && kind != bearertoken.Owner && kind != bearertoken.Coordinator &&
		kind != bearertoken.Bot {
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
			// the coordinator may: the owner's switch and its Bot's scopes are read now, not when the Run was
			// made (AgentSession.May). The owner's own sign-in is never a worker's.
			may: session.May,
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
			may: func(c orchestration.Connection) bool { return c.Chats && !c.User() },
		}, nil
	case bearertoken.Bot:
		// A Bot uses what the coordinator does, narrowed to its list, read now as the owner's switch is (issue
		// #169, after OpenMausBot's connector scopes, enforced where the call is relayed).
		bot, err := s.store.GetBot(r.Context(), token.Bot)
		if err != nil {
			return agent{}, errNoCredential
		}

		// A Routine's script's token (issue #211) is bound to its Bot and no chat: its tab is its Routine's.
		// A Routine's run is never the owner's turn: one that should act as him needs a grant of its own.
		if token.Routine != "" {
			return agent{
				name: fmt.Sprintf("bot %s in routine %s", token.Bot, token.Routine), coordinator: true,
				key:   "r-" + token.Routine,
				may:   func(c orchestration.Connection) bool { return !c.User() && bot.MayUse(c) },
				scope: func(label string) superset.Scope { return superset.ScopeOf(bot, label) },
			}, nil
		}

		// A pi's own token (issue #260): what the coordinator gets, and the chat it is bound to. Its ID is its
		// coordinator token's, which every chat's pi shares, so its tab is its chat's: a chat has one Bot and
		// one pi at a time, and the pi that follows goes on in the same tab.
		readRun, err := s.store.ReadRun(r.Context(), token.Chat)
		if err != nil {
			return agent{}, err
		}

		return agent{
			name: fmt.Sprintf("bot %s in chat %s", token.Bot, token.Chat), coordinator: true, chatID: token.Chat,
			bot: token.Bot, key: "b-" + token.Chat, may: s.botMay(r.Context(), bot, token.Chat, readRun), readRun: readRun,
			offered:     bot.MayUse,
			botSwitches: bot.ToolSwitches(),
			scope:       func(label string) superset.Scope { return superset.ScopeOf(bot, label) },
		}, nil
	default:
		return agent{}, errNoCredential
	}
}

// botMay is what a Bot's pi in a chat may use: what its Bot may, and the
// owner's own sign-in on the owner's turn in that chat (issues #468,
// #618), or in an owner-marked read run (#956). The turn is read once a request, when a user Connection is
// asked about. A turn that cannot be read is not his.
func (s *Server) botMay(ctx context.Context, bot orchestration.Bot, chatID string, readRun bool,
) func(orchestration.Connection) bool {
	var asked, ownerTurn bool

	return func(c orchestration.Connection) bool {
		if !bot.MayUse(c) {
			return false
		}

		if readRun || !c.User() {
			return true
		}

		if !asked {
			asked = true

			var err error
			if ownerTurn, err = s.store.OwnerTurn(ctx, chatID); err != nil {
				s.logger.ErrorContext(ctx, "whose turn a chat is could not be read",
					slog.String("event", "agentapi.failed"), slog.String("chat", chatID),
					slog.String("error", err.Error()))

				ownerTurn = false
			}
		}

		return ownerTurn
	}
}

// Caller says who a request's credential belongs to, for the service proxy
// (issue #154), which serves the same agents by the same rules.
func (s *Server) Caller(r *http.Request) (serviceproxy.Caller, error) {
	a, err := s.authenticate(r)
	if err != nil {
		return serviceproxy.Caller{}, err
	}

	return a.proxyCaller(), nil
}

// May reports whether the request's agent may use a Connection through the
// service proxy's tunnel, a request it writes itself. One its Bot reaches only
// part of is refused (issue #562): only the Superset reads, which apply the
// scope, use it.
func (s *Server) May(ctx context.Context, r *http.Request, integration, label string) (bool, error) {
	a, err := s.authenticate(r)
	if err != nil {
		return false, nil //nolint:nilerr // No credential, no Connection: the caller is refused, not failed.
	}

	if integration == serviceproxy.SupersetID && a.scopeOf(label).Limited {
		return false, nil
	}

	allowed, err := s.allowed(ctx, a, integration)
	if err != nil {
		return false, err
	}

	return slices.ContainsFunc(allowed, func(c orchestration.Connection) bool { return c.Label == label }), nil
}

func (s *Server) allowed(ctx context.Context, a agent, integration string) ([]orchestration.Connection, error) {
	return s.connectionsBy(ctx, integration, a.may)
}

// offeredTo are the Connections an agent's tools are listed for, whoever's turn it is (issue #748).
func (s *Server) offeredTo(ctx context.Context, a agent, integration string) ([]orchestration.Connection, error) {
	if a.offered == nil {
		return s.allowed(ctx, a, integration)
	}

	return s.connectionsBy(ctx, integration, a.offered)
}

func (s *Server) connectionsBy(ctx context.Context, integration string, keep func(orchestration.Connection) bool,
) ([]orchestration.Connection, error) {
	all, err := s.store.ConnectionsOf(ctx, integration)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(all, func(c orchestration.Connection) bool { return !keep(c) }), nil
}

// allowedOf is what an agent may use for a tool: the Connections of its Integration, or, for a tool of a kind of
// git host (glab, issue #740), those of every host of that kind.
func (s *Server) allowedOf(ctx context.Context, a agent, integration string, def agenttool.Def,
) ([]orchestration.Connection, error) {
	if def.HostKind == "" {
		return s.allowed(ctx, a, integration)
	}

	hosts, err := s.store.ListGitHosts(ctx)
	if err != nil {
		return nil, err
	}

	var out []orchestration.Connection

	for _, h := range hosts {
		if string(h.Kind) != def.HostKind {
			continue
		}

		allowed, err := s.allowed(ctx, a, h.Host)
		if err != nil {
			return nil, err
		}

		out = append(out, allowed...)
	}

	return out, nil
}

func (s *Server) connections(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	integration := r.URL.Query().Get("integration")

	// A tool that will not run without the id of its app is told it with each account: a Lark app's id, which
	// is not a secret. Each Connection is an app of its own (issue #311), as OpenClaw's tool takes the app of
	// the account it acts as (extensions/feishu/src/tool-account.ts).
	def, _ := agenttool.Of(integration)

	allowed, err := s.allowedOf(r.Context(), a, integration, def)
	if err != nil {
		s.fail(w, r, err)

		return
	}

	out := agenttool.ConnectionList{Data: make([]agenttool.Connection, 0, len(allowed))}
	for _, c := range allowed {
		conn := agenttool.Connection{Label: c.Label, Account: c.Account, User: c.User()}
		if def.HostKind != "" {
			conn.Host = c.Integration
		}

		if def.ClientEnv != "" {
			conn.ClientID = c.AppID
		}

		// What the account cannot do at the service, so that the agent is told before it tries (issue #567).
		for _, no := range serviceproxy.CannotOf(c.Integration, c.Access) {
			conn.Cannot = append(conn.Cannot, agenttool.Cannot{
				Action: no.Action.ID, Service: no.Action.Service, Name: no.Action.Name, Reason: no.Reason,
			})
		}

		out.Data = append(out.Data, conn)
	}

	answer(w, out)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}
	if a.readRun {
		refuse(w, http.StatusForbidden, agenttool.ReadRunRefusal)

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
	if _, proxied := serviceproxy.CatalogIn(r.Context(), s.store, asked.Integration); proxied && !a.owner {
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

// browserTools lists the browser's tools and a worker's gateway tools
// (issues #354, #492), which a browser that cannot be used does not take
// away: for a node of a release before PathTools (issue #536), whose tools
// server lists them here.
func (s *Server) browserTools(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}
	if a.readRun {
		answer(w, agenttool.BrowserToolList{Data: []agenttool.BrowserTool{}})

		return
	}

	web := s.gatewayToolList(r.Context(), a)

	if s.browser == nil && len(web) == 0 {
		refuse(w, http.StatusServiceUnavailable, "this control plane has no browser")

		return
	}

	out := agenttool.BrowserToolList{Data: []agenttool.BrowserTool{}}

	if s.browser != nil {
		tools, err := s.browser.Tools(r.Context())

		switch {
		case err != nil && len(web) == 0:
			s.failBrowser(w, r, err)

			return
		case err != nil:
			s.logger.WarnContext(r.Context(), "the browser's tools could not be listed",
				slog.String("event", "agentapi.browser_tools_failed"), slog.String("error", err.Error()))
		}

		for _, t := range tools {
			out.Data = append(out.Data, agenttool.BrowserTool{
				Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
			})
		}
	}

	listed, err := s.narrowed(r.Context(), a, append(out.Data, web...))
	if err != nil {
		s.fail(w, r, err)

		return
	}

	answer(w, agenttool.BrowserToolList{Data: listed})
}

func (s *Server) browserCall(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}
	if a.readRun {
		answerTool(w, agenttool.ReadRunRefusal, true)

		return
	}

	var asked agenttool.BrowserCall
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, agenttool.MaxCallBytes)).Decode(&asked); err != nil ||
		asked.Tool == "" {
		refuse(w, http.StatusBadRequest, "name the tool and give its arguments as a JSON object")

		return
	}

	if s.switchedOff(w, r, a, asked.Tool) {
		return
	}

	// A gateway tool is the agent's when it is given it (a worker's by its runtime); for any other agent it is
	// no tool of the browser.
	if slices.Contains(s.gatewayTools(r.Context(), a), asked.Tool) {
		s.gatewayCall(w, r, a, asked)

		return
	}

	if s.browser == nil {
		refuse(w, http.StatusServiceUnavailable, "this control plane has no browser")

		return
	}

	s.runBrowser(w, r, a, asked)
}

// runBrowser runs one of the browser's tools in the agent's own tab.
func (s *Server) runBrowser(w http.ResponseWriter, r *http.Request, a agent, asked agenttool.BrowserCall) {
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
