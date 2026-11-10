// Package nodeapi is the control plane's side of the node protocol (issue
// #32, internal/shared/nodeproto): registration over HTTP, then one WebSocket
// per node on which the control plane offers Dispatches, asks for stops and
// passes answers, and the node sends its heartbeats and reports. It follows
// Omnara's daemon socket (internal/httpapi/daemon_socket_route.go,
// daemon_socket_hub.go at 0ef1711). Every report is a node call of the
// orchestration core, whose receipts make a resent report harmless.
package nodeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

const writeTimeout = 10 * time.Second

// Server serves the node API.
type Server struct {
	svc    *orchestration.Service
	logger *slog.Logger
	// hostToken returns the token of a git host for a Dispatch; nil when the
	// control plane wired none. The node API may not open a secret itself
	// (issue #66).
	hostToken func(ctx context.Context, host, path string) (string, bool)
	// modelKey makes the model gateway's key for a Dispatch; nil when the
	// control plane wired none.
	modelKey func(ctx context.Context, dispatchID, model string) (string, bool)
	// tools names the tools a node of a system should have; nil when the
	// control plane wired none.
	tools func(ctx context.Context, os, arch string) []nodeproto.Tool

	mu       sync.Mutex
	sessions map[string]*session
}

// New returns a Server over the orchestration service.
func New(svc *orchestration.Service, logger *slog.Logger) *Server {
	return &Server{svc: svc, logger: logger, sessions: map[string]*session{}}
}

// WithHostTokens sets where an offer's git host token comes from.
func (s *Server) WithHostTokens(token func(ctx context.Context, host, path string) (string, bool)) *Server {
	s.hostToken = token

	return s
}

// WithTools sets which tools a node is told to have.
func (s *Server) WithTools(tools func(ctx context.Context, os, arch string) []nodeproto.Tool) *Server {
	s.tools = tools

	return s
}

// toolsFor are the tools a node of this system is told to have.
func (s *Server) toolsFor(ctx context.Context, os, arch string) []nodeproto.Tool {
	if s.tools == nil {
		return nil
	}

	return s.tools(ctx, os, arch)
}

// WithModelKeys sets where an offer's model gateway key comes from.
func (s *Server) WithModelKeys(key func(ctx context.Context, dispatchID, model string) (string, bool)) *Server {
	s.modelKey = key

	return s
}

// Register adds the node routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+nodeproto.PathHello, s.hello)
	mux.HandleFunc("GET "+nodeproto.PathSocket, s.socket)
}

// authenticate returns the node a request's bearer token belongs to, and the token's id.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (orchestration.Node, string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(w, "a node token is required", http.StatusUnauthorized)

		return orchestration.Node{}, "", false
	}

	n, tokenID, err := s.svc.AuthenticateNodeToken(r.Context(), token)
	if errors.Is(err, orchestration.ErrNotFound) {
		http.Error(w, "unknown node token", http.StatusUnauthorized)

		return orchestration.Node{}, "", false
	}

	if err != nil {
		s.logger.ErrorContext(r.Context(), "authenticating a node",
			slog.String("event", "nodeapi.auth_failed"), slog.String("error", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)

		return orchestration.Node{}, "", false
	}

	return n, tokenID, true
}

func (s *Server) hello(w http.ResponseWriter, r *http.Request) {
	n, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	var req nodeproto.HelloRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, nodeproto.MaxMessageBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid hello: "+err.Error(), http.StatusBadRequest)

		return
	}

	hello := orchestration.Hello{WorktreeRoot: req.WorktreeRoot, System: orchestration.SystemInfo{
		DaemonVersion: req.DaemonVersion, OS: req.System.OS, Arch: req.System.Arch, CPUs: req.System.CPUs,
		MemoryBytes: req.System.MemoryBytes, DiskBytes: req.System.DiskBytes,
	}}
	for _, rt := range req.Runtimes {
		hello.Runtimes = append(hello.Runtimes, orchestration.RuntimeInfo{
			Runtime: rt.Runtime, Path: rt.Path, Version: rt.Version,
		})
	}

	claims := make([]orchestration.Claim, 0, len(req.Claims))
	for _, c := range req.Claims {
		claims = append(claims, orchestration.Claim{DispatchID: c.DispatchID, Epoch: c.Epoch})
	}

	dispositions, err := s.svc.NodeConnect(r.Context(), n.ID, hello, claims)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, orchestration.ErrInvalid) {
			status = http.StatusBadRequest
		}

		http.Error(w, err.Error(), status)

		return
	}

	resp := nodeproto.HelloResponse{
		NodeID: n.ID, Dispositions: make([]nodeproto.Disposition, 0, len(dispositions)),
		HeartbeatAfterMs: nodeproto.HeartbeatEvery.Milliseconds(),
		GitHosts:         s.gitHosts(r.Context()),
		Tools:            s.toolsFor(r.Context(), req.System.OS, req.System.Arch),
	}
	for _, d := range dispositions {
		resp.Dispositions = append(resp.Dispositions, nodeproto.Disposition{DispatchID: d.DispatchID, Kind: string(d.Kind)})
	}

	s.logger.InfoContext(r.Context(), "node connected",
		slog.String("event", "nodeapi.hello"), slog.String("node_id", n.ID),
		slog.String("instance_id", req.InstanceID), slog.String("daemon_version", req.DaemonVersion),
		slog.Int("claims", len(claims)))

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.WarnContext(r.Context(), "writing hello response",
			slog.String("event", "nodeapi.hello_write_failed"), slog.String("error", err.Error()))
	}
}

func (s *Server) socket(w http.ResponseWriter, r *http.Request) {
	n, tokenID, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	// The HTTP server's read and write timeouts must not end a long-lived socket.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	conn.SetReadLimit(nodeproto.MaxMessageBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sess := &session{
		srv: s, conn: conn, nodeID: n.ID, tokenID: tokenID, os: n.System.OS, arch: n.System.Arch, cancel: cancel,
		out:     make(chan nodeproto.Message, 64),
		offered: map[string]bool{}, stopped: map[string]bool{}, answered: map[string]bool{},
	}
	s.take(ctx, sess)
	defer s.drop(sess)

	err = sess.run(ctx)
	status, reason := websocket.StatusNormalClosure, ""

	if err != nil && ctx.Err() == nil {
		status, reason = websocket.StatusInternalError, "error"
		s.logger.WarnContext(r.Context(), "node socket ended",
			slog.String("event", "nodeapi.socket_ended"), slog.String("node_id", n.ID), slog.String("error", err.Error()))
	}

	_ = conn.Close(status, reason)
}

// take makes sess the node's connection; an older one is told it was
// replaced and closed (Omnara presence_replaced).
func (s *Server) take(ctx context.Context, sess *session) {
	s.mu.Lock()
	old := s.sessions[sess.nodeID]
	s.sessions[sess.nodeID] = sess
	s.mu.Unlock()

	if old != nil {
		old.replaced(ctx)
	}
}

func (s *Server) drop(sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sessions[sess.nodeID] == sess {
		delete(s.sessions, sess.nodeID)
	}
}

// session is one node's WebSocket.
type session struct {
	srv    *Server
	conn   *websocket.Conn
	nodeID string
	// tokenID is the token the node connected with; a heartbeat is refused once it is revoked.
	tokenID string
	// os and arch are the node's system, for the tools it is told to have.
	os, arch string
	cancel   context.CancelFunc
	out      chan nodeproto.Message

	// What this connection already sent; a new connection sends all again.
	offered  map[string]bool
	stopped  map[string]bool
	answered map[string]bool
}

func (c *session) replaced(ctx context.Context) {
	msg, err := nodeproto.Encode(nodeproto.TypeError, nodeproto.Error{
		Code: nodeproto.ErrorReplaced, Message: "another connection of this node took over",
	})
	if err == nil {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		_ = wsjson.Write(writeCtx, c.conn, msg)

		cancel()
	}

	c.cancel()
}

func (c *session) run(ctx context.Context) error {
	wake := c.srv.svc.Subscribe(ctx)
	drain := time.NewTicker(nodeproto.DrainEvery)

	defer drain.Stop()

	in := make(chan nodeproto.Message)
	readErr := make(chan error, 1)

	go func() { readErr <- c.read(ctx, in) }()

	writeErr := make(chan error, 1)

	go func() { writeErr <- c.write(ctx) }()

	if err := c.push(ctx); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case err := <-writeErr:
			return err
		case msg := <-in:
			if err := c.handle(ctx, msg); err != nil {
				return err
			}
		case <-wake:
		case <-drain.C:
		}

		if err := c.push(ctx); err != nil {
			return err
		}
	}
}

func (c *session) read(ctx context.Context, in chan<- nodeproto.Message) error {
	for {
		readCtx, cancel := context.WithTimeout(ctx, nodeproto.ReadTimeout)

		var msg nodeproto.Message

		err := wsjson.Read(readCtx, c.conn, &msg)

		cancel()

		if err != nil {
			if ctx.Err() != nil || websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil //nolint:nilerr // the session ended or the node closed it; neither is a failure
			}

			return fmt.Errorf("reading from node %s: %w", c.nodeID, err)
		}

		select {
		case in <- msg:
		case <-ctx.Done():
			return nil
		}
	}
}

func (c *session) write(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-c.out:
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := wsjson.Write(writeCtx, c.conn, msg)

			cancel()

			if err != nil {
				if ctx.Err() != nil {
					return nil //nolint:nilerr // the session ended while writing; not a failure
				}

				return fmt.Errorf("writing to node %s: %w", c.nodeID, err)
			}
		}
	}
}

func (c *session) send(ctx context.Context, t nodeproto.Type, payload any) error {
	msg, err := nodeproto.Encode(t, payload)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", t, err)
	}

	select {
	case c.out <- msg:
		return nil
	case <-ctx.Done():
		return nil
	}
}

// push sends what the node has not been told on this connection yet.
func (c *session) push(ctx context.Context) error {
	p, err := c.srv.svc.NodePending(ctx, c.nodeID)
	if err != nil {
		return err
	}

	for _, o := range p.Offers {
		if c.offered[o.DispatchID] {
			continue
		}

		c.offered[o.DispatchID] = true

		// The token is read when the offer is sent and is kept nowhere in the control plane but the vault.
		if c.srv.hostToken != nil && o.RepoHost != "" {
			if token, ok := c.srv.hostToken(ctx, o.RepoHost, o.Repo); ok {
				o.RepoToken = token
			}
		}

		// So is the gateway's key: made now, for this Dispatch and its model only.
		if c.srv.modelKey != nil && o.AccountRef == orchestration.GatewayAccountRef {
			if key, ok := c.srv.modelKey(ctx, o.DispatchID, o.Model); ok {
				o.ModelKey = key
			}
		}

		if err := c.send(ctx, nodeproto.TypeOffer, nodeproto.Offer(o)); err != nil {
			return err
		}
	}

	for _, st := range p.Stops {
		key := fmt.Sprintf("%s@%d", st.DispatchID, st.Epoch)
		if c.stopped[key] {
			continue
		}

		c.stopped[key] = true

		if err := c.send(ctx, nodeproto.TypeStop, nodeproto.Stop(st)); err != nil {
			return err
		}
	}

	for _, a := range p.Answers {
		if c.answered[a.ID] {
			continue
		}

		c.answered[a.ID] = true

		if err := c.send(ctx, nodeproto.TypeAnswer, a); err != nil {
			return err
		}
	}

	return nil
}

// gitHosts are the hosts a node is asked to try. A list that cannot be read is
// left out: the node keeps the one it has.
func (s *Server) gitHosts(ctx context.Context) []string {
	hosts, err := s.svc.ListGitHosts(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "listing git hosts for a node", slog.String("event", "nodeapi.git_hosts_failed"),
			slog.String("error", err.Error()))

		return nil
	}

	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, h.Host)
	}

	return out
}

// stats records the measurement and the reached git hosts a heartbeat carries. One that cannot be read
// or stored is logged and dropped: the heartbeat itself still counts.
func (c *session) stats(ctx context.Context, payload json.RawMessage) {
	if len(payload) == 0 {
		return
	}

	var hb nodeproto.Heartbeat

	err := json.Unmarshal(payload, &hb)
	if err == nil && hb.Reaches != nil {
		err = c.srv.svc.NodeReaches(ctx, c.nodeID, *hb.Reaches)
	}

	if err == nil && hb.Tools != nil {
		tools := make([]orchestration.NodeTool, 0, len(*hb.Tools))
		for _, t := range *hb.Tools {
			tools = append(tools, orchestration.NodeTool{Name: t.Name, Version: t.Version, Error: t.Error})
		}

		err = c.srv.svc.NodeTools(ctx, c.nodeID, tools)
	}

	if err == nil && hb.Stats != nil {
		err = c.srv.svc.NodeStats(ctx, c.nodeID, orchestration.SystemStats{
			CPUPercent: hb.Stats.CPUPercent, MemoryUsedBytes: hb.Stats.MemoryUsedBytes,
			DiskUsedBytes: hb.Stats.DiskUsedBytes, Load1: hb.Stats.Load1,
		})
	}

	if err != nil {
		c.srv.logger.WarnContext(ctx, "dropping a node's measurement", slog.String("event", "nodeapi.stats_dropped"),
			slog.String("node_id", c.nodeID), slog.String("error", err.Error()))
	}
}

func (c *session) handle(ctx context.Context, msg nodeproto.Message) error {
	switch msg.Type {
	case nodeproto.TypeHeartbeat:
		if err := c.srv.svc.CheckNodeToken(ctx, c.tokenID); err != nil {
			return err
		}

		if err := c.srv.svc.NodeHeartbeat(ctx, c.nodeID); err != nil {
			return err
		}

		c.stats(ctx, msg.Payload)

		return c.send(ctx, nodeproto.TypeHeartbeatAck, nodeproto.HeartbeatAck{
			HeartbeatAfterMs: nodeproto.HeartbeatEvery.Milliseconds(), GitHosts: c.srv.gitHosts(ctx),
			Tools: c.srv.toolsFor(ctx, c.os, c.arch),
		})
	case nodeproto.TypeReport:
		var r nodeproto.Report
		if err := json.Unmarshal(msg.Payload, &r); err != nil || r.ReportID == "" {
			return c.send(ctx, nodeproto.TypeError, nodeproto.Error{Code: nodeproto.ErrorInvalid, Message: "invalid report"})
		}

		return c.send(ctx, nodeproto.TypeReportAck, c.srv.apply(ctx, c.nodeID, r))
	case nodeproto.TypeItems:
		var b nodeproto.Items
		if err := json.Unmarshal(msg.Payload, &b); err != nil || b.DispatchID == "" {
			return c.send(ctx, nodeproto.TypeError,
				nodeproto.Error{Code: nodeproto.ErrorInvalid, Message: "invalid transcript items"})
		}

		return c.send(ctx, nodeproto.TypeItemsAck, c.srv.items(ctx, c.nodeID, b))
	case nodeproto.TypeHeartbeatAck, nodeproto.TypeReportAck, nodeproto.TypeOffer, nodeproto.TypeStop,
		nodeproto.TypeAnswer, nodeproto.TypeItemsAck, nodeproto.TypeError:
		fallthrough
	default:
		return c.send(ctx, nodeproto.TypeError,
			nodeproto.Error{Code: nodeproto.ErrorInvalid, Message: "unexpected message " + string(msg.Type)})
	}
}
