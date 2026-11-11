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
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

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
	GetApproval(ctx context.Context, id string) (orchestration.Approval, error)
	DecideApproval(ctx context.Context, id string, approve bool, scope orchestration.ApprovalScope,
	) (orchestration.Approval, error)
	UseApproval(ctx context.Context, id string) (bool, error)
	TellAgentOfApproval(ctx context.Context, a orchestration.Approval) (bool, error)
	ActionPolicies(ctx context.Context, integration, label string) (map[string]string, error)
}

// Token gives a Connection's access token, fresh: the control plane's own
// wiring over the vault.
type Token func(ctx context.Context, integration, label string) (string, error)

// Config is what a Proxy is made of.
type Config struct {
	Agents Agents
	Store  Store
	Token  Token
	Logger *slog.Logger
	// Upstream sends a request on to the service; nil is a plain transport.
	Upstream http.RoundTripper
	// Hold is how long a request waits for the owner; zero is DefaultHold.
	Hold time.Duration
}

// Proxy serves the tunnel a tool's requests come through.
type Proxy struct {
	cfg Config
	ca  *authority

	mu      sync.Mutex
	waiting map[string][]chan orchestration.Approval
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
		cfg.Upstream = &http.Transport{
			Proxy: nil, ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second,
			TLSHandshakeTimeout: handshakeTimeout,
		}
	}

	if cfg.Hold == 0 {
		cfg.Hold = DefaultHold
	}

	return &Proxy{cfg: cfg, ca: ca, waiting: map[string][]chan orchestration.Approval{}}, nil
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
	caller      Caller
	integration string
	label       string
	chatID      string
	host        string
	catalog     Catalog
}

func (s session) connection() string { return orchestration.Use(s.integration, s.label) }

// tunnel carries one connection of a tool to one host of an Integration.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	caller, err := p.cfg.Agents.Caller(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	query := r.URL.Query()
	s := session{
		caller: caller, integration: query.Get(agenttool.QueryIntegration), label: query.Get(agenttool.QueryLabel),
	}

	host, port, err := net.SplitHostPort(query.Get(agenttool.QueryHost))
	if err != nil || port != "443" {
		refuse(w, http.StatusForbidden, "the proxy reaches a service over HTTPS only")

		return
	}

	s.host = host

	catalog, ok := CatalogOf(s.integration)
	if !ok || !catalog.Has(host) {
		// Deny by default: only the hosts of an Integration's actions are reached.
		refuse(w, http.StatusForbidden, fmt.Sprintf("%s is not a host of the %s integration", host, s.integration))

		return
	}

	s.catalog = catalog

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

	action := s.catalog.Match(req.Method, s.host, req.URL.Path)

	verdict, message, err := p.decide(ctx, req, body, action, s)
	if err != nil {
		// Every error refuses.
		p.cfg.Logger.ErrorContext(ctx, "the proxy could not decide a request", slog.String("event", "serviceproxy.failed"),
			slog.String("agent", s.caller.Name), slog.String("action", action.ID), slog.String("error", err.Error()))

		verdict, message = "error", "the control plane could not decide this request; it was not run. Try again"
	}

	p.cfg.Logger.InfoContext(ctx, "a request through the service proxy",
		slog.String("event", "serviceproxy.request"), slog.String("agent", s.caller.Name),
		slog.String("connection", s.connection()), slog.String("action", action.ID), slog.String("verdict", verdict))

	if message != "" {
		return p.reply(out, http.StatusForbidden, message) == nil
	}

	return p.forward(ctx, out, req, body, action, s) == nil
}

// decide applies the action's Policy. An empty message means the request
// runs; otherwise it is what the tool is told.
func (p *Proxy) decide(ctx context.Context, req *http.Request, body []byte, action Action, s session,
) (verdict, message string, err error) {
	if action.Plumbing {
		return "plumbing", "", nil
	}

	policy := orchestration.PolicyAsk

	if _, listed := s.catalog.Action(action.ID); listed {
		policy = action.Default()

		set, err := p.cfg.Store.ActionPolicies(ctx, s.integration, s.label)
		if err != nil {
			return "", "", err
		}

		if answer, ok := set[action.ID]; ok {
			policy = answer
		}
	}

	switch policy {
	case orchestration.PolicyAllow:
		return "allowed", "", nil
	case orchestration.PolicyNever:
		return "never", fmt.Sprintf("Refused: the owner set %q to never for %s. It was not run; do not try another "+
			"way. Say so in your answer.", action.Name, s.connection()), nil
	}

	return p.ask(ctx, req, body, action, s)
}

// ask holds a request for the owner's Approval.
func (p *Proxy) ask(ctx context.Context, req *http.Request, body []byte, action Action, s session,
) (verdict, message string, err error) {
	d := decode(req.Method, s.host, req.URL.Path, req.URL.RawQuery, req.Header.Get("Content-Type"), body)

	title := action.Name
	if d.Subject != "" {
		title += ": " + clipTitle(d.Subject)
	}

	fields := d.Fields
	if len(fields) == 0 {
		fields = []orchestration.Field{{Label: "Request", Value: req.Method + " " + req.URL.Path}}
	}

	askedBy := "worker"
	if s.caller.Coordinator {
		askedBy = "pi"
	} else if s.caller.DispatchID == "" {
		askedBy = "owner"
	}

	approval, granted, err := p.cfg.Store.AskApproval(ctx, orchestration.NewApproval{
		Integration: s.integration, Label: s.label, Action: action.ID, ActionName: action.Name,
		DispatchID: s.caller.DispatchID, ChatID: s.chatID, AskedBy: askedBy, Title: title, Payload: fields,
		MatchHash: d.Hash,
	})
	if err != nil {
		return "", "", err
	}

	if granted {
		return "approved before", "", nil
	}

	decided, answered, err := p.wait(ctx, approval.ID)
	if err != nil {
		return "", "", err
	}

	switch {
	case !answered:
		return "waiting", p.waitingMessage(approval, s), nil
	case decided.Status == orchestration.ApprovalRejected:
		return "rejected", fmt.Sprintf("Rejected by the owner: %q was not run. Do not try another way; say so in "+
			"your answer.", approval.Title), nil
	case decided.Scope == orchestration.ScopeSession:
		return "approved for the session", "", nil
	}

	// An approval for one request runs once, whoever waited for it.
	used, err := p.cfg.Store.UseApproval(ctx, approval.ID)
	if err != nil {
		return "", "", err
	}

	if !used {
		return "used", fmt.Sprintf("%q was approved once and has run. Run the command again to ask for it again.",
			approval.Title), nil
	}

	return "approved", "", nil
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
func (p *Proxy) wait(ctx context.Context, id string) (decided orchestration.Approval, answered bool, err error) {
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

	timer := time.NewTimer(p.cfg.Hold)
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

// forward sends the request on with the Connection's token and writes the
// service's answer back.
func (p *Proxy) forward(ctx context.Context, out io.Writer, req *http.Request, body []byte, action Action, s session,
) error {
	// The host is one of the Integration's own, checked when the tunnel was opened; the path is the tool's.
	//nolint:gosec // G704: the proxy's purpose is to send a tool's request on, to a host from a fixed catalog.
	upstream, err := http.NewRequestWithContext(ctx, req.Method, "https://"+s.host+req.URL.RequestURI(),
		bytes.NewReader(body))
	if err != nil {
		return p.reply(out, http.StatusBadGateway, "the request could not be sent on")
	}

	upstream.Header = req.Header.Clone()
	// What the tool sent as a credential is never passed on: it holds a placeholder.
	upstream.Header.Del("Authorization")
	upstream.Header.Del("Proxy-Authorization")
	upstream.Header.Del("Connection")
	upstream.ContentLength = int64(len(body))

	if !action.Plumbing {
		token, err := p.cfg.Token(ctx, s.integration, s.label)
		if err != nil {
			p.cfg.Logger.ErrorContext(ctx, "the proxy could not get an account's token",
				slog.String("event", "serviceproxy.failed"), slog.String("connection", s.connection()),
				slog.String("error", err.Error()))

			message := "the account's token could not be read; the request was not run"
			if errors.Is(err, orchestration.ErrConflict) || errors.Is(err, orchestration.ErrNotFound) {
				message = err.Error()
			}

			return p.reply(out, http.StatusForbidden, message)
		}

		upstream.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := p.cfg.Upstream.RoundTrip(upstream)
	if err != nil {
		return p.reply(out, http.StatusBadGateway, "the service could not be reached: "+rootCause(err))
	}

	defer resp.Body.Close() //nolint:errcheck // The answer was written or the write failed; closing adds nothing.

	// The tool speaks HTTP/1.1 whatever the service answered in.
	resp.Proto, resp.ProtoMajor, resp.ProtoMinor = "HTTP/1.1", 1, 1
	if resp.ContentLength < 0 {
		resp.TransferEncoding = []string{"chunked"}
	}

	return resp.Write(out)
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

// reply answers the tool itself, in the shape of a Google API error, which
// its tools print as the message (tried with gws on 2026-11-11).
func (p *Proxy) reply(out io.Writer, status int, message string) error {
	body, err := json.Marshal(map[string]any{"error": map[string]any{
		"code": status, "message": message, "status": "PERMISSION_DENIED",
	}})
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
