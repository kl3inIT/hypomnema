// Package serviceproxy is the proxy between an agent's tool and a service
// the owner connected (issue #154). An agent never holds an account's token:
// its tool is given a placeholder and sends every request here, where the
// request is decrypted, matched to an action of the Integration, answered by
// that action's Policy (ALLOW, ASK or DENY) and only then given the token.
// ASK holds the request for the owner's Approval.
//
// The design is Onyx's sandbox proxy (backend/onyx/sandbox_proxy): one
// service, an action catalog as data, ALWAYS, ASK and DENY an action, a
// request that matches no action asks, every error refuses. It is in the
// control plane and not on each node, so that a token never leaves the
// control plane's machine and policy, approvals and the record are in one
// place (docs/decisions, 2026-11-11). A tool reaches it through a stream its
// launcher opens to the control plane's address, as a node's terminal does.
package serviceproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kl3inIT/hypomnema/internal/controlplane/connproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

const (
	// DefaultHold is how long a request waits for the owner before its tool
	// is told it waits. Onyx holds 180 s; this is under the two minutes
	// after which an agent's shell gives a command up (Claude Code's
	// default), so that the tool hears the proxy's answer and not a timeout.
	DefaultHold = 100 * time.Second
	// maxBody bounds a request's body: it is read whole, to be decoded and
	// matched. Gmail takes a message of 35 MB at most.
	maxBody = 48 << 20
	// DefaultRunHold is how long a request of a Routine's run waits for the
	// owner, in the call that made it: nobody is there to be told that it
	// waits and to ask again. Then it is refused (OpenClaw holds an exec
	// approval in place for DEFAULT_EXEC_APPROVAL_TIMEOUT_MS, 30 minutes:
	// src/infra/exec-approvals-core.ts L266).
	DefaultRunHold = 30 * time.Minute
	// handshakeTimeout bounds the TLS handshake with a tool.
	handshakeTimeout = 20 * time.Second
)

// Caller is the agent whose tool asks.
type Caller struct {
	// Name is what the log calls it: "dispatch dsp_…".
	Name string
	// DispatchID is set for a worker.
	DispatchID string
	// Coordinator says the caller is a chat's coordinator.
	Coordinator bool
	// ChatID is the chat a pi's own token is bound to (issue #260): its
	// Approvals are shown there, whatever chat the request names.
	ChatID  string
	ReadRun bool
	// Bot is the Bot of a pi's own token, by whose attempt at an Inbox item a request may be made (issue #1004).
	Bot string
	// OwnerReadOnly says the caller's Bot only reads the owner's own accounts (orchestration.Bot.OwnerAccounts,
	// issue #1023): through his Lark sign-in, his Google, his Zalo and his WhatsApp, only reads run, whatever the
	// owner's answers or grants say.
	OwnerReadOnly bool
}

// Agents says who a request's credential belongs to and what it may use: the
// agent API's own rules (issue #103).
type Agents interface {
	Caller(r *http.Request) (Caller, error)
	May(ctx context.Context, r *http.Request, integration, label string) (bool, error)
}

// Store is what the proxy needs of orchestration.
type Store interface {
	AskApproval(ctx context.Context, n orchestration.NewApproval) (orchestration.Approval, bool, error)
	StandingGrant(ctx context.Context, n orchestration.NewApproval) (bool, error)
	GetApproval(ctx context.Context, id string) (orchestration.Approval, error)
	DecideApproval(ctx context.Context, id string, approve bool, scope orchestration.ApprovalScope,
	) (orchestration.Approval, error)
	UseApproval(ctx context.Context, id string) (bool, error)
	ExpireApproval(ctx context.Context, id string) (orchestration.Approval, error)
	TellAgentOfApproval(ctx context.Context, a orchestration.Approval) (bool, error)
	ActionPolicies(ctx context.Context, integration, label string) (map[string]string, error)
	// ConnectionOf gives a Connection with what its account can do (issue #567).
	ConnectionOf(ctx context.Context, integration, label string) (orchestration.Connection, error)
	// HostKinds says the kind of a git host, whose catalog is its kind's (GitLab's, issue #740).
	HostKinds
	// InboxAttempt is a Bot's attempt at the Inbox item it names, which the Approvals it asks record (issue #1004).
	InboxAttempt(ctx context.Context, todoID, bot, chatID string) (string, error)
}

// Recheck reads again what a Connection's account can do and keeps it (issue #567): the control plane's own
// wiring of the service that signs accounts in and Assess.
type Recheck func(ctx context.Context, integration, label string) error

// Token gives a Connection's access token, fresh: the control plane's own
// wiring over the vault.
type Token func(ctx context.Context, integration, label string) (string, error)

// Config is what a Proxy is made of.
type Config struct {
	Agents Agents
	Store  Store
	Token  Token
	// RepoToken gives the owner's GitHub App's token for a request to GitHub that names a repository the App is
	// installed on, in place of the Connection's (issue #1262); nil: never.
	RepoToken RepoToken
	// AppReviews ties each review the App's token makes on a pull request to its Inbox attempt (issue #1232); nil:
	// nobody.
	AppReviews AppReviews
	Logger     *slog.Logger
	// Upstream sends a request on to the service; nil is a plain transport.
	Upstream http.RoundTripper
	// Hold is how long a request waits for the owner; zero is DefaultHold.
	Hold time.Duration
	// RunHold is how long a request of a Routine's run waits for the owner
	// before it is refused; zero is DefaultRunHold.
	RunHold time.Duration
	// Send sends a relayed request to the address of a Connection that has
	// its own, with its token (Relay); nil when no Integration has one.
	Send Sender
	// AWS gives the session of a role a request is signed again with (issue
	// #331); nil when the AWS Integration is not wired.
	AWS AWSCredentials
	// Recheck reads a Connection's access again when the service refused a
	// request with 403, or Lark with its code for a missing scope (issue
	// #567); nil leaves the access as it was read.
	Recheck Recheck
}

// Proxy serves the tunnel a tool's requests come through.
type Proxy struct {
	cfg Config
	ca  *authority

	mu      sync.Mutex
	waiting map[string][]chan orchestration.Approval
	// rechecked is when each Connection's access was last read again after a 403, by "integration:label".
	rechecked map[string]time.Time
	// repositories are the owner/name of GitHub repositories' ids, by Connection (issue #802).
	repositories repositoryNames
	// ways are the transports to the services: Upstream, or one through a Connection's proxy (issue #850).
	ways *connproxy.Transports
}

// New returns a Proxy with a certificate authority of its own, made now and
// kept in memory: a launcher reads its certificate on every call, so nothing
// has to outlive the process.
func New(cfg Config) (*Proxy, error) {
	ca, err := newAuthority()
	if err != nil {
		return nil, fmt.Errorf("making the proxy's certificate authority: %w", err)
	}

	if cfg.Upstream == nil {
		cfg.Upstream = connproxy.NewTransport(handshakeTimeout)
	}

	if cfg.Hold == 0 {
		cfg.Hold = DefaultHold
	}

	if cfg.RunHold == 0 {
		cfg.RunHold = DefaultRunHold
	}

	return &Proxy{
		cfg: cfg, ca: ca, waiting: map[string][]chan orchestration.Approval{}, rechecked: map[string]time.Time{},
		ways: &connproxy.Transports{
			Direct: cfg.Upstream, New: func() *http.Transport { return connproxy.NewTransport(handshakeTimeout) },
		},
	}, nil
}

// Register adds the proxy's routes to mux.
func (p *Proxy) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+agenttool.PathTunnel, p.tunnel)
	mux.HandleFunc("GET "+agenttool.PathCA, p.certificate)
}

// certificate gives the authority's certificate, which a tool must trust to
// talk to the proxy. It is public: it proves nothing to hold it.
func (p *Proxy) certificate(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(p.ca.pem)
}

// session is one tunnel: who asks, for which Connection, to which host.
type session struct {
	// opened is the request that opened the tunnel; its credential is asked about again for every request
	// that comes through.
	opened      *http.Request
	caller      Caller
	integration string
	label       string
	chatID      string
	// runKey is the key of the Routine's run whose script asks (issue #211); a prompt run is known by its chat.
	runKey string
	// attempt is the attempt at an Inbox item the requests are made in, from the item a Bot's pi named (issue
	// #1004).
	attempt string
	host    string
	catalog Catalog
	// ownerReadOnly says the session is of a Bot that only reads the owner's accounts, on one of them (readsOnly).
	ownerReadOnly bool
}

func (s session) connection() string { return orchestration.Use(s.integration, s.label) }

// again reads who the agent is and what its Bot may do with the owner's accounts again, from the request that opened
// the session (review of #1037): at each request on an open connection, and when an Approval a request waited for is
// answered (issue #1071). A refusal is what the agent is told when its credential no longer works. A session that
// no request opened keeps what it was made with.
func (p *Proxy) again(ctx context.Context, s *session) (refusal string, err error) {
	if s.opened == nil {
		return "", nil
	}

	caller, err := p.cfg.Agents.Caller(s.opened)
	if err != nil {
		//nolint:nilerr // A credential that no longer works refuses the request; it is no failure of the proxy.
		return "Refused: this agent's credential no longer works. The request was not run", nil
	}

	s.caller.ReadRun, s.caller.OwnerReadOnly = caller.ReadRun, caller.OwnerReadOnly
	s.ownerReadOnly = false

	return "", p.readsOnly(ctx, s)
}

// writesOwners is the first of a request's actions that is not a read, when the session is of a Bot that only reads
// the owner's accounts, on one of them (readsOnly): it refuses the request whatever the owner answered.
func writesOwners(s session, all []Action) (Action, bool) {
	if !s.ownerReadOnly {
		return Action{}, false
	}

	for _, named := range all {
		if named.Kind != Reads && !named.Plumbing {
			return named, true
		}
	}

	return Action{}, false
}

// readsOnly marks a session whose caller's Bot only reads the owner's own accounts when its Connection is one of
// them: a user Connection (his Lark sign-in, his Zalo, his WhatsApp) or his Google (issue #1023, the owner's choice
// of 2026-11-21 on #1037). decide then refuses every action that is not a read, at once, never asking.
func (p *Proxy) readsOnly(ctx context.Context, s *session) error {
	if !s.caller.OwnerReadOnly {
		return nil
	}

	if s.integration == "google" {
		s.ownerReadOnly = true

		return nil
	}

	conn, err := p.cfg.Store.ConnectionOf(ctx, s.integration, s.label)
	if err != nil {
		return err
	}

	s.ownerReadOnly = conn.User()

	return nil
}

// tunnel carries one connection of a tool to one host of an Integration, for an agent or the owner.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	caller, err := p.cfg.Agents.Caller(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	query := r.URL.Query()
	s := session{
		opened: r, caller: caller, integration: query.Get(agenttool.QueryIntegration), label: query.Get(agenttool.QueryLabel),
	}

	host, port, err := net.SplitHostPort(query.Get(agenttool.QueryHost))
	if err != nil || port != "443" {
		refuse(w, http.StatusForbidden, "the proxy reaches a service over HTTPS only")

		return
	}

	s.host = host

	catalog, ok := CatalogIn(r.Context(), p.cfg.Store, s.integration)
	if !ok || !catalog.Has(host) {
		// Deny by default: only the hosts of an Integration's actions are reached.
		refuse(w, http.StatusForbidden, fmt.Sprintf("%s is not a host of the %s integration", host, s.integration))

		return
	}

	s.catalog = catalog

	if err := p.readsOnly(r.Context(), &s); err != nil && !errors.Is(err, orchestration.ErrNotFound) {
		p.cfg.Logger.ErrorContext(r.Context(), "the proxy could not read a connection",
			slog.String("event", "serviceproxy.failed"), slog.String("error", err.Error()))
		refuse(w, http.StatusInternalServerError, "the control plane could not answer; try again")

		return
	}

	may, err := p.cfg.Agents.May(r.Context(), r, s.integration, s.label)
	if err != nil {
		p.cfg.Logger.ErrorContext(r.Context(), "the proxy could not read what an agent may use",
			slog.String("event", "serviceproxy.failed"), slog.String("error", err.Error()))
		refuse(w, http.StatusInternalServerError, "the control plane could not answer; try again")

		return
	}

	if !may {
		refuse(w, http.StatusForbidden, fmt.Sprintf("this agent was not given %s", s.connection()))

		return
	}

	if caller.Coordinator {
		s.chatID = query.Get(agenttool.QueryChat)
		s.runKey = r.Header.Get(agenttool.HeaderRoutineRun)
	}

	// A pi's token works in its own chat: naming another is refused (issue #260).
	if caller.ChatID != "" {
		if s.chatID != "" && s.chatID != caller.ChatID {
			refuse(w, http.StatusForbidden, "this agent's token is of another chat")

			return
		}

		s.chatID = caller.ChatID

		// The Inbox item a Bot works on, named by its tool (agenttool.HeaderTodo): what it asks waits as the item's.
		if todo := r.Header.Get(agenttool.HeaderTodo); todo != "" {
			attempt, err := p.cfg.Store.InboxAttempt(r.Context(), todo, caller.Bot, s.chatID)

			switch {
			case errors.Is(err, orchestration.ErrConflict) || errors.Is(err, orchestration.ErrNotFound):
				refuse(w, http.StatusConflict, err.Error())

				return
			case err != nil:
				p.cfg.Logger.ErrorContext(r.Context(), "the proxy could not read the Inbox item an agent named",
					slog.String("event", "serviceproxy.failed"), slog.String("error", err.Error()))
				refuse(w, http.StatusInternalServerError, "the control plane could not answer; try again")

				return
			}

			s.attempt = attempt
		}
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	conn.SetReadLimit(-1)

	// The stream outlives the request that opened it; it ends when either side closes.
	ctx := context.WithoutCancel(r.Context())
	stream := websocket.NetConn(ctx, conn, websocket.MessageBinary)

	defer stream.Close() //nolint:errcheck // Closing a stream that already ended adds nothing.

	p.serve(ctx, stream, s)
}

// serve decrypts the tool's connection and answers its requests one by one.
func (p *Proxy) serve(ctx context.Context, stream net.Conn, s session) {
	secure := tls.Server(stream, &tls.Config{
		MinVersion: tls.VersionTLS12,
		// HTTP/1.1 only: one request at a time, each read whole.
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return p.ca.leaf(s.host)
		},
	})

	handshake, cancel := context.WithTimeout(ctx, handshakeTimeout)
	err := secure.HandshakeContext(handshake)

	cancel()

	if err != nil {
		return
	}

	reader := bufio.NewReader(secure)

	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			return
		}

		if !p.answer(ctx, secure, req, s) {
			return
		}
	}
}

// answer handles one request and reports whether the connection goes on.
func (p *Proxy) answer(ctx context.Context, out io.Writer, req *http.Request, s session) bool {
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBody+1))
	if err != nil {
		return false
	}

	if len(body) > maxBody {
		return p.reply(out, http.StatusRequestEntityTooLarge, "the request is larger than the proxy passes on") == nil
	}

	// A tunnel stays open for many requests. What the agent may use is asked for each of them, so that an
	// account taken back, or a Dispatch that ended, stops at its next request and not when the tool closes.
	may, err := p.cfg.Agents.May(ctx, s.opened, s.integration, s.label)
	if err != nil || !may {
		p.cfg.Logger.InfoContext(ctx, "a request through the service proxy",
			slog.String("event", "serviceproxy.request"), slog.String("agent", s.caller.Name),
			slog.String("connection", s.connection()), slog.String("verdict", "no longer given"))
		_ = p.reply(out, http.StatusForbidden, "this agent may no longer use "+s.connection()+"; the request was not run")

		return false
	}

	action, message := p.judge(ctx, req, body, s)
	if message != "" {
		return p.reply(out, http.StatusForbidden, message) == nil
	}

	return p.forward(ctx, out, req, body, action, s) == nil
}

// judge answers one request, whichever way it came: through a tunnel, or
// relayed for a Connection that has its own address (Relay). It refuses a
// request that could be read two ways, matches the request to its action,
// applies the action's Policy and writes the log line. An empty message
// means the request runs; otherwise it is what the caller is told.
func (p *Proxy) judge(ctx context.Context, req *http.Request, body []byte, s session) (Action, string) {
	// A request is what it says it is in one way only. One written so that the proxy and the service could
	// read it differently is refused: another host in its address, a path that does not stay where it
	// starts, a header or a parameter that asks the service to treat it as another method.
	if reason := malformed(req, slices.Contains(s.catalog.SlashInSegment, s.host)); reason != "" {
		p.cfg.Logger.InfoContext(ctx, "a request through the service proxy",
			slog.String("event", "serviceproxy.request"), slog.String("agent", s.caller.Name),
			slog.String("connection", s.connection()), slog.String("verdict", "malformed: "+reason))

		return Action{}, "Refused: " + reason + ". The request was not run"
	}

	// On an open connection, who the agent is and what its Bot may do with the owner's accounts are read again at
	// each request, never kept from when the connection opened (review of #1037): a token that ended refuses, and
	// a Bot the owner made read his accounts only reads them from the next request on.
	if refusal, err := p.again(ctx, &s); err != nil {
		return Action{}, "the control plane could not decide this request; it was not run. Try again"
	} else if refusal != "" {
		return Action{}, refusal
	}

	action, verdict, message, err := p.decideRequest(ctx, req, body, s)
	if err != nil {
		// Every error refuses.
		p.cfg.Logger.ErrorContext(ctx, "the proxy could not decide a request", slog.String("event", "serviceproxy.failed"),
			slog.String("agent", s.caller.Name), slog.String("action", action.ID), slog.String("error", err.Error()))

		verdict, message = "error", "the control plane could not decide this request; it was not run. Try again"
	}

	p.cfg.Logger.InfoContext(ctx, "a request through the service proxy",
		slog.String("event", "serviceproxy.request"), slog.String("agent", s.caller.Name),
		slog.String("connection", s.connection()), slog.String("action", action.ID), slog.String("verdict", verdict))

	return action, message
}

// decide applies the action's Policy. An empty message means the request
// runs; otherwise it is what the tool is told. describe says what the owner is
// asked, when he is. Of an action and those the request names beside it
// (With), the strictest answer governs (Onyx's AllMatchedActions): one refused
// or set to never refuses the request, and when the strictest is ask, each
// action that asks needs its own answer, so that a grant of one never lets
// another run (review of #746). The action returned is the one that decided.
func (p *Proxy) decide(ctx context.Context, action Action, s session, describe func(Action) asking,
) (governing Action, verdict, message string, err error) {
	all := append([]Action{action}, action.With...)
	if named, ok := writesOwners(s, all); ok {
		return named, "owner-read-only", agenttool.OwnerReadOnlyRefusal, nil
	}
	if s.caller.ReadRun {
		if !readRunIntegration(s.integration) && s.integration != SupersetID {
			return action, "read-only", agenttool.ReadRunRefusal, nil
		}

		for _, named := range all {
			if named.Kind != Reads || named.Plumbing || named.ID == "lark.other.read" ||
				(s.integration == "google" && named.Service != "Gmail" && named.Service != "Calendar") {
				return named, "read-only", agenttool.ReadRunRefusal, nil
			}
		}
	}

	if action.Plumbing {
		return action, "plumbing", "", nil
	}

	answers := make([]answered, len(all))

	strictest := 0

	for i, a := range all {
		if answers[i], err = p.answerOf(ctx, a, s); err != nil {
			return action, "", "", err
		}

		if answers[i].severity() > answers[strictest].severity() {
			strictest = i
		}
	}

	action, got := all[strictest], answers[strictest]

	if got.refusal != "" {
		return action, got.verdict, got.refusal, nil
	}

	switch got.policy {
	case orchestration.PolicyAllow:
		return action, "allowed", "", nil
	case orchestration.PolicyNever:
		return action, "never", fmt.Sprintf("Refused: the owner set %q to never for %s. It was not run; do not try "+
			"another way. Say so in your answer.", action.Name, s.connection()), nil
	}
	if s.caller.ReadRun {
		return action, "read-only", agenttool.ReadRunRefusal, nil
	}

	asks := make([]Action, 0, len(all))

	for i, a := range all {
		if answers[i].policy == orchestration.PolicyAsk {
			asks = append(asks, a)
		}
	}

	if len(asks) == 1 {
		verdict, message, _, err = p.ask(ctx, action, all, s, describe(action))

		return action, verdict, message, err
	}

	// Several actions ask, and each needs its own answer (review of #746). Those the owner allowed for the session
	// or the Routine are set aside, and the request is asked for as the first of the others, the owner shown all
	// of them: approved, once or for the session, the request runs, since he saw what it does; for the session,
	// only that action is allowed from then on. When the ask finds the first allowed by a standing grant instead
	// (one made since the check, by another request), that grant says nothing of the others: they are decided
	// again, until one is refused or waits, or the owner has answered for all.
	for {
		var waiting []Action

		for _, a := range asks {
			granted, err := p.cfg.Store.StandingGrant(ctx, orchestration.NewApproval{
				Integration: s.integration, Label: s.label, Action: a.ID, DispatchID: s.caller.DispatchID,
				ChatID: s.chatID, RunKey: s.runKey,
			})
			if err != nil {
				return a, "", "", err
			}

			if !granted {
				waiting = append(waiting, a)
			}
		}

		if len(waiting) == 0 {
			return asks[0], "approved before", "", nil
		}

		action = waiting[0]
		q := describe(action)

		if len(waiting) > 1 {
			names := make([]string, 0, len(waiting)-1)
			for _, a := range waiting[1:] {
				names = append(names, a.Name)
			}

			q.fields = append([]orchestration.Field{{Label: "Also", Value: strings.Join(names, "; ")}}, q.fields...)
		}

		verdict, message, standing, err := p.ask(ctx, action, all, s, q)
		if err != nil || message != "" || !standing {
			return action, verdict, message, err
		}
	}
}

// answered is what an action is for a Connection: the owner's Policy, or what refuses it whatever he set.
type answered struct {
	policy string
	// verdict and refusal refuse an action that is never run, or that the account cannot do at the service.
	verdict, refusal string
}

// severity orders answers from the strictest: refused, never, ask, allow (Onyx's POLICY_SEVERITY).
func (a answered) severity() int {
	switch {
	case a.refusal != "":
		return 3
	case a.policy == orchestration.PolicyNever:
		return 2
	case a.policy == orchestration.PolicyAllow:
		return 0
	}

	return 1
}

// answerOf reads an action's answer for the session's Connection. An action the catalog does not list asks.
func (p *Proxy) answerOf(ctx context.Context, action Action, s session) (answered, error) {
	if action.Never {
		return answered{verdict: "never", refusal: fmt.Sprintf("Refused: %q is never run through %s: %s. Do not try "+
			"another way; say so in your answer.", action.Name, s.connection(), action.Text)}, nil
	}

	if _, listed := s.catalog.Action(action.ID); !listed {
		return answered{policy: orchestration.PolicyAsk}, nil
	}

	// What the account cannot do at the service is refused before the owner's answer is read, as lark-cli
	// refuses a call whose scope is missing before it makes it (shortcuts/common/runner.go,
	// checkShortcutScopes). What is granted or unknown goes on.
	conn, err := p.cfg.Store.ConnectionOf(ctx, s.integration, s.label)
	if err != nil {
		return answered{}, err
	}

	if got := conn.Access.Of(action.ID); got.Access == orchestration.AccessMissing {
		return answered{verdict: "missing", refusal: cannot(conn, action, s, got.Reason)}, nil
	}

	set, err := p.cfg.Store.ActionPolicies(ctx, s.integration, s.label)
	if err != nil {
		return answered{}, err
	}

	if policy, ok := set[action.ID]; ok {
		return answered{policy: policy}, nil
	}

	return answered{policy: action.Default()}, nil
}

// asking is what the owner is asked to approve: its title, what it shows him, and the digest that tells the same
// request asked again.
type asking struct {
	title  string
	fields []orchestration.Field
	hash   string
}

// httpAsking is what the owner is asked about an HTTP request, decoded.
func httpAsking(req *http.Request, body []byte, action Action, s session) asking {
	d := decode(req.Method, s.host, req.URL.Path, req.URL.RawQuery, req.Header.Get("Content-Type"), body)

	asked := req.Method + " " + req.URL.Path

	title := action.Name
	if d.Subject != "" {
		title += ": " + clipTitle(d.Subject)
	}

	fields := d.Fields

	// A request that is no action of the catalog has no name of its own to go by: the owner is told which
	// request it is, in the title and as the first line. So is one with nothing to show. A relayed request
	// (it has no host) was written by its caller, and its action is a whole part of the service: which chart
	// is changed is in its path, so the owner is told the request too.
	if _, listed := s.catalog.Action(action.ID); !listed || s.host == "" {
		title = action.Name + ": " + clipTitle(asked)
		fields = append([]orchestration.Field{{Label: "Request", Value: asked}}, fields...)
	} else if len(fields) == 0 {
		fields = []orchestration.Field{{Label: "Request", Value: asked}}
	}

	return asking{title: title, fields: fields, hash: d.Hash}
}

// ask holds a request for the owner's Approval.
//
// standing is true when the action was allowed by a grant for the session or the Routine, not by an answer to this
// request. all are the actions the request names, which the agent's Bot is asked about again once the owner answers.
func (p *Proxy) ask(ctx context.Context, action Action, all []Action, s session, q asking,
) (verdict, message string, standing bool, err error) {

	askedBy := "worker"
	if s.caller.Coordinator {
		askedBy = "pi"
	} else if s.caller.DispatchID == "" {
		askedBy = "owner"
	}

	approval, granted, err := p.cfg.Store.AskApproval(ctx, orchestration.NewApproval{
		Integration: s.integration, Label: s.label, Action: action.ID, ActionName: action.Name,
		DispatchID: s.caller.DispatchID, ChatID: s.chatID, RunKey: s.runKey, AttemptID: s.attempt, AskedBy: askedBy,
		Title:     q.title,
		Payload:   q.fields,
		MatchHash: q.hash,
	})
	if errors.Is(err, orchestration.ErrConflict) {
		// The Inbox item the request is for already waits for the owner's answer to another (issue #1004).
		return "conflict", "Refused: " + err.Error() + ". It was not run", false, nil
	}

	if err != nil {
		return "", "", false, err
	}

	if granted {
		return "approved before", "", approval.ID == "", nil
	}

	// A request of a Routine's run waits where it is, for as long as a run waits, and is then refused: nobody is
	// in a run to be told that it waits and to ask again.
	hold := p.cfg.Hold
	if approval.RoutineRunID != "" {
		hold = p.cfg.RunHold
	}

	decided, answered, err := p.wait(ctx, approval.ID, hold)
	if err != nil {
		return "", "", false, err
	}

	if !answered && approval.RoutineRunID != "" {
		decided, err = p.cfg.Store.ExpireApproval(ctx, approval.ID)
		if err != nil {
			return "", "", false, err
		}

		if decided.Expired {
			return "expired", fmt.Sprintf("Refused: nobody answered the approval of %q in %s. It was not run and "+
				"this run fails; do not try another way.", approval.Title, hold), false, nil
		}

		// He answered as the wait ran out.
		answered = true
	}

	if answered && decided.Status != orchestration.ApprovalRejected {
		// The wait may have been long: what the agent's Bot may do is read again, as at each request, and not kept
		// from before it (issue #1071). A Bot the owner made read his accounts meanwhile does not write with them,
		// whatever he approved.
		refusal, err := p.again(ctx, &s)
		if err != nil {
			return "", "", false, err
		}

		if refusal != "" {
			return "no longer given", refusal, false, nil
		}

		if _, ok := writesOwners(s, all); ok {
			return "owner-read-only", agenttool.OwnerReadOnlyRefusal, false, nil
		}
	}

	switch {
	case !answered:
		return "waiting", p.waitingMessage(approval, s), false, nil
	case decided.Status == orchestration.ApprovalRejected:
		return "rejected", fmt.Sprintf("Rejected by the owner: %q was not run. Do not try another way; say so in "+
			"your answer.", approval.Title), false, nil
	case decided.Scope == orchestration.ScopeSession:
		return "approved for the session", "", false, nil
	case decided.Scope == orchestration.ScopeRoutine:
		return "approved for the routine", "", false, nil
	}

	// An approval for one request runs once, whoever waited for it.
	used, err := p.cfg.Store.UseApproval(ctx, approval.ID)
	if err != nil {
		return "", "", false, err
	}

	if !used {
		return "used", fmt.Sprintf("%q was approved once and has run. Run the command again to ask for it again.",
			approval.Title), false, nil
	}

	return "approved", "", false, nil
}

// waitingMessage tells an agent what a held request needs of it.
func (p *Proxy) waitingMessage(a orchestration.Approval, s session) string {
	next := "Tell the owner it waits for his approval."

	switch {
	case s.caller.Coordinator:
		next = "Tell the owner in one line that it waits for his approval in this chat, then end your turn. You " +
			"are woken when he answers; then run the same command again, unchanged."
	case s.caller.DispatchID != "":
		next = "Ask the owner to approve it with your ask tool, naming what it is, and wait for the answer. When " +
			"it is approved, run the same command again, unchanged."
	}

	return fmt.Sprintf("Waiting for the owner's approval (%s): %q was NOT run. %s Changing the command asks again.",
		a.ID, a.Title, next)
}

// wait blocks until the Approval is answered or the hold ends. answered is
// false when the hold ended first: the Approval stays open.
func (p *Proxy) wait(ctx context.Context, id string, hold time.Duration,
) (decided orchestration.Approval, answered bool, err error) {
	notified := make(chan orchestration.Approval, 1)

	p.mu.Lock()
	p.waiting[id] = append(p.waiting[id], notified)
	p.mu.Unlock()

	defer p.forget(id, notified)

	// It may have been answered between the asking and the waiting.
	now, err := p.cfg.Store.GetApproval(ctx, id)
	if err != nil {
		return orchestration.Approval{}, false, err
	}

	if now.Status != orchestration.ApprovalPending {
		return now, true, nil
	}

	timer := time.NewTimer(hold)
	defer timer.Stop()

	select {
	case decided = <-notified:
		return decided, true, nil
	case <-timer.C:
		return orchestration.Approval{}, false, nil
	case <-ctx.Done():
		return orchestration.Approval{}, false, ctx.Err()
	}
}

func (p *Proxy) forget(id string, notified chan orchestration.Approval) {
	p.mu.Lock()
	defer p.mu.Unlock()

	kept := p.waiting[id][:0]

	for _, c := range p.waiting[id] {
		if c != notified {
			kept = append(kept, c)
		}
	}

	if len(kept) == 0 {
		delete(p.waiting, id)

		return
	}

	p.waiting[id] = kept
}

// Decide records the owner's answer to an Approval and lets it be known: a
// request still held goes on at once; an agent whose request ended is told
// to run it again (owner, 2026-11-11).
func (p *Proxy) Decide(ctx context.Context, id string, approve bool, scope orchestration.ApprovalScope,
) (orchestration.Approval, error) {
	decided, err := p.cfg.Store.DecideApproval(ctx, id, approve, scope)
	if err != nil {
		return orchestration.Approval{}, err
	}

	p.mu.Lock()
	held := append([]chan orchestration.Approval(nil), p.waiting[id]...)
	p.mu.Unlock()

	for _, c := range held {
		select {
		case c <- decided:
		default:
		}
	}

	if len(held) > 0 {
		return decided, nil
	}

	if _, err := p.cfg.Store.TellAgentOfApproval(ctx, decided); err != nil {
		// The answer is kept; the agent learns of it when it asks again.
		p.cfg.Logger.ErrorContext(ctx, "an agent could not be told of an approval",
			slog.String("event", "serviceproxy.tell_failed"), slog.String("approval", id),
			slog.String("error", err.Error()))
	}

	return decided, nil
}

// way is the transport a session's requests are sent with: through its Connection's proxy, at its dial address,
// when it names them (issue #850), the proxy's own Upstream otherwise.
func (p *Proxy) way(ctx context.Context, s session) (http.RoundTripper, string, error) {
	conn, err := p.cfg.Store.ConnectionOf(ctx, s.integration, s.label)
	if err != nil {
		return nil, "", err
	}

	way, err := p.ways.For(connproxy.Route{Proxy: conn.Proxy, Dial: conn.DialAddress})

	return way, conn.Proxy, err
}

// forward sends the request on with the Connection's token and writes the
// service's answer back.
func (p *Proxy) forward(ctx context.Context, out io.Writer, req *http.Request, body []byte, action Action, s session,
) error {
	resp, status, message := p.upstream(ctx, req, body, action, s)
	if resp == nil {
		return p.reply(out, status, message)
	}

	defer resp.Body.Close() //nolint:errcheck // The answer was written or the write failed; closing adds nothing.
	resp.Proto, resp.ProtoMajor, resp.ProtoMinor = "HTTP/1.1", 1, 1
	if resp.ContentLength < 0 {
		resp.TransferEncoding = []string{"chunked"}
	}

	return resp.Write(out)
}

func (p *Proxy) upstream(ctx context.Context, req *http.Request, body []byte, action Action, s session,
) (*http.Response, int, string) {
	if s.catalog.Rewrite != nil {
		body = s.catalog.Rewrite(req, body, action)
	}

	// The address is built from its parts: the host is the tunnel's, one of the Integration's own, whatever
	// the request named; only the path and the query are the tool's, and malformed has checked them.
	target := url.URL{
		Scheme: "https", Host: s.host, Path: req.URL.Path, RawPath: req.URL.RawPath, RawQuery: req.URL.RawQuery,
	}

	upstream, err := http.NewRequestWithContext(ctx, req.Method, target.String(), bytes.NewReader(body))
	if err != nil || upstream.URL.Host != s.host {
		return nil, http.StatusBadGateway, "the request could not be sent on"
	}

	upstream.Header = req.Header.Clone()
	upstream.Host = s.host
	// What the tool sent as a credential is never passed on: it holds a placeholder. glab sends its token as
	// Private-Token, and a CI job's as Job-Token (client-go gitlab.go AccessTokenHeaderName, JobTokenHeaderName).
	for _, name := range credentialHeaders {
		upstream.Header.Del(name)
	}

	for _, name := range overrideHeaders {
		upstream.Header.Del(name)
	}

	upstream.Header.Del("Connection")
	upstream.ContentLength = int64(len(body))

	asApp := false

	if s.catalog.Signed {
		if refused := p.signAgain(ctx, req, upstream, body, s); refused != "" {
			return nil, http.StatusForbidden, refused
		}
	} else if token, ok := p.appToken(ctx, req, body, s); !action.Plumbing && ok {
		upstream.Header.Set("Authorization", "Bearer "+token)

		asApp = true
	} else if !action.Plumbing {
		token, err := p.cfg.Token(ctx, s.integration, s.label)
		if err != nil {
			p.cfg.Logger.ErrorContext(ctx, "the proxy could not get an account's token",
				slog.String("event", "serviceproxy.failed"), slog.String("connection", s.connection()),
				slog.String("error", err.Error()))

			message := "the account's token could not be read; the request was not run"
			if errors.Is(err, orchestration.ErrConflict) || errors.Is(err, orchestration.ErrNotFound) {
				message = err.Error()
			}

			return nil, http.StatusForbidden, message
		}

		upstream.Header.Set("Authorization", "Bearer "+token)
	}

	way, proxy, err := p.way(ctx, s)
	if err != nil {
		return nil, http.StatusBadGateway, "the request could not be sent on: " + err.Error()
	}

	// A review the App makes is its attempt's work: the attempt is read before the review is made.
	review, reviews := AppReview{}, false
	if asApp {
		review, reviews = p.reviewAttempt(ctx, req, s)
	}

	resp, err := way.RoundTrip(upstream)
	if err != nil {
		if proxy != "" {
			return nil, http.StatusBadGateway, connproxy.Unreachable(s.host, proxy, errors.New(rootCause(err))).Error()
		}

		return nil, http.StatusBadGateway, "the service could not be reached: " + rootCause(err)
	}

	// A refusal is looked at before it is passed on (issue #567): its first bytes are read and put back.
	var head []byte
	if resp.StatusCode >= http.StatusBadRequest {
		head, _ = io.ReadAll(io.LimitReader(resp.Body, maxErrorPeek))
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
	}

	p.noticed(ctx, s, action, resp.StatusCode, head)

	if reviews {
		p.appReviewed(ctx, resp, review)
	}

	return resp, resp.StatusCode, ""
}

// credentialHeaders are the headers a tool sends a credential in: the proxy adds the Connection's own.
var credentialHeaders = []string{"Authorization", "Proxy-Authorization", "Private-Token", "Job-Token"}

// overrides are the headers and parameters by which a service is asked to
// treat a request as another method than the one it was sent with. Google's
// APIs honor them; an action is recognized by the method, so they are not
// passed on.
var (
	overrideHeaders = []string{"X-Http-Method-Override", "X-Http-Method", "X-Method-Override"}
	overrideQueries = []string{"$httpmethod", "$method", "_method", "httpmethod"}
)

// malformed says why a request is not one the proxy sends on, or "" for one
// that is. It leaves the request's query written the one way it was read, so
// that the service is sent exactly what was checked. slashInSegment allows an
// encoded slash, on a host whose paths carry a URL as one segment.
func malformed(req *http.Request, slashInSegment bool) string {
	u := req.URL

	switch {
	case u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "":
		return "a request names its path only, not another address"
	case !strings.HasPrefix(u.Path, "/"):
		return "a request's path starts at the service's root"
	}

	for _, segment := range strings.Split(u.Path, "/")[1:] {
		if segment == "." || segment == ".." || strings.ContainsAny(segment, "\\\x00") {
			return "a request's path may not step out of where it starts"
		}
	}

	// An encoded slash is one segment to the proxy and may be two to the service.
	raw := strings.ToLower(u.RawPath)
	if (strings.Contains(raw, "%2f") && !slashInSegment) || strings.Contains(raw, "%5c") {
		return "a request's path may not hide a slash"
	}

	// The header's presence is what counts, whatever its value.
	for _, name := range overrideHeaders {
		if _, present := req.Header[http.CanonicalHeaderKey(name)]; present {
			return "a request may not ask to be treated as another method (" + name + ")"
		}
	}

	// A query that does not parse strictly, such as one with a semicolon, may be split another way by the
	// service: it is refused, not guessed at.
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "a request's query must be plain name=value pairs"
	}

	for name := range query {
		if slices.Contains(overrideQueries, strings.ToLower(name)) {
			return "a request may not ask to be treated as another method (" + name + ")"
		}
	}

	u.RawQuery = query.Encode()

	return ""
}

func rootCause(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}

		err = next
	}
}

func clipTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= 80 {
		return s
	}

	cut := 80
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}

	return s[:cut] + "…"
}

// reply answers the tool itself, in a shape the tools print as the message:
// the "error" object of a Google API error (tried with gws on 2026-11-11),
// and beside it the "code" and "msg" of a Lark one.
func (p *Proxy) reply(out io.Writer, status int, message string) error {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{"code": status, "message": message, "status": "PERMISSION_DENIED"},
		"code":  status, "msg": message,
	})
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(out, "HTTP/1.1 %d %s\r\nContent-Type: application/json; charset=utf-8\r\n"+
		"Content-Length: %d\r\n\r\n%s", status, http.StatusText(status), len(body), body)

	return err
}

func refuse(w http.ResponseWriter, status int, message string) {
	raw, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		status, raw = http.StatusInternalServerError, []byte(`{"message":"the answer could not be written"}`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
