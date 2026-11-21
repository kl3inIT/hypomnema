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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	shippedrelease "github.com/kl3inIT/hypomnema/internal/shared/release"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

const writeTimeout = 10 * time.Second

// Server serves the node API.
type Server struct {
	svc    *orchestration.Service
	logger *slog.Logger
	// hostToken returns the token of a git host for a Dispatch, with the
	// account it belongs to; nil when the control plane wired none. The node
	// API may not open a secret itself (issue #66).
	hostToken func(ctx context.Context, host, path string) (HostCredential, bool)
	// modelKey makes the model gateway's key for a Dispatch; nil when the
	// control plane wired none.
	modelKey func(ctx context.Context, dispatchID, model string) (string, bool)
	// agentKey makes a Dispatch's own key to the agent API, for the
	// Connections its Run may use; nil when the control plane wired none.
	agentKey func(ctx context.Context, dispatchID string, uses []string) (string, bool)
	// toolNames are the tools a Dispatch's worker gets, read with its key, for its "Tools" block (issues #723,
	// #789).
	toolNames func(ctx context.Context, dispatchID, key string) []string
	// tools names the tools a node of a system should have; nil when the
	// control plane wired none.
	tools func(ctx context.Context, os, arch string) []nodeproto.Tool
	// runtimeTools are the agent CLIs a node of a system is told to have (issue #107).
	runtimeTools func(os, arch string) []nodeproto.Tool

	// phoneHeld says whether a node holds a phone allowed workers are given; nil when the control
	// plane wired none.
	phoneHeld func(ctx context.Context) bool

	// diskAlert tells the owner a node is still out of disk after trimming, or has recovered (issue #1017); nil
	// when the control plane wired none.
	diskAlert func(ctx context.Context, alert orchestration.DiskAlert)
	// diskReports orders a node's disk reports with the alerts they make, and diskAlerts holds, by node, the alerts
	// still to tell, the first being told now: one sender a node, so a recovery is never told before its drop.
	diskReports sync.Mutex
	diskAlerts  map[string][]orchestration.DiskAlert

	// releaseURL is where a node finds its releases, sent at hello.
	releaseURL string

	mu       sync.Mutex
	sessions map[string]*session
	// streams are the terminal connections nodes have been asked to open, by stream id.
	streams map[string]*pendingStream
	// answers are the files requests nodes have been sent, by request id.
	answers map[string]*pendingAnswer
	// devices are the states of their phones nodes said last, by node id (device.go).
	devices map[string]nodeproto.DeviceState
	// storeGuides are the nodes that said at their last hello that they write no guides of their own
	// (HelloRequest.StoreGuides), kept in memory as devices are: a node says hello before each socket.
	storeGuides map[string]bool
	// deviceCalls are the phone tools' calls nodes have been sent, by request id (device.go).
	deviceCalls map[string]*pendingDevice
	// takeFiles takes the takes' files nodes upload, and takesHeld settles those a node holds at hello (takes.go).
	takeFiles TakeFiles
	takesHeld TakesHeld
	// execs are the agents' commands nodes have been sent, by request id (exec.go).
	execs map[string]*pendingExec
}

// New returns a Server over the orchestration service.
func New(svc *orchestration.Service, logger *slog.Logger) *Server {
	return &Server{svc: svc, logger: logger, sessions: map[string]*session{}, streams: map[string]*pendingStream{},
		answers: map[string]*pendingAnswer{}, devices: map[string]nodeproto.DeviceState{},
		storeGuides: map[string]bool{}, deviceCalls: map[string]*pendingDevice{}, execs: map[string]*pendingExec{}}
}

// HostCredential is what an offer is given to act on its repository's host:
// the token, and the account it belongs to when that is known (issue #991),
// which the worker's commits then name.
//
// Pending is set while that account is being read: it is closed when the read
// ends, and the offer is held until then, or until identityWait.
type HostCredential struct {
	Token    string
	Identity githost.Identity
	Pending  <-chan struct{}
}

// identityWait bounds how long an offer is held for its token's account to be read (issue #991); the control plane's
// read itself gives up sooner.
const identityWait = 15 * time.Second

// WithHostTokens sets where an offer's git host token comes from.
func (s *Server) WithHostTokens(token func(ctx context.Context, host, path string) (HostCredential, bool)) *Server {
	s.hostToken = token

	return s
}

// WithTools sets which tools a node is told to have.
func (s *Server) WithTools(tools func(ctx context.Context, os, arch string) []nodeproto.Tool) *Server {
	s.tools = tools

	return s
}

// WithRuntimeTools sets which agent CLIs a node is told to have.
func (s *Server) WithRuntimeTools(tools func(os, arch string) []nodeproto.Tool) *Server {
	s.runtimeTools = tools

	return s
}

// runtimeToolsSince is the first node release that installs the agent CLIs (issue #107): one before it cannot
// install a release file that is a bare binary, nor one of omp's size, and would fetch it again every five minutes.
const runtimeToolsSince = "v0.9.17"

// toolsFor are the tools a node of this system and this version is told to have: the agent CLIs when it can
// install them, then the Integrations' tools.
func (s *Server) toolsFor(ctx context.Context, os, arch, version string) []nodeproto.Tool {
	var out []nodeproto.Tool

	if s.runtimeTools != nil && !shippedrelease.Newer(runtimeToolsSince, version) {
		out = append(out, s.runtimeTools(os, arch)...)
	}

	if s.tools != nil {
		out = append(out, s.tools(ctx, os, arch)...)
	}

	return out
}

// guidesSaid records whether a node writes no guides of its own.
func (s *Server) guidesSaid(nodeID string, store bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !store {
		delete(s.storeGuides, nodeID)

		return
	}

	s.storeGuides[nodeID] = true
}

// skillsFor are the skills of an offer as a node is sent them. A node of a
// release before issue #536 writes the guides of its Run's Integrations and
// of the phone itself and names them in the worker's first message, so it is
// sent none of the skills that require something, which are those guides:
// its worker gets each guide once, from the node, until the node takes the
// release that leaves them to the store.
func (s *Server) skillsFor(nodeID string, skills []nodeproto.Skill) []nodeproto.Skill {
	s.mu.Lock()
	store := s.storeGuides[nodeID]
	s.mu.Unlock()

	if store {
		return skills
	}

	return slices.DeleteFunc(slices.Clone(skills), func(sk nodeproto.Skill) bool { return len(sk.Requires) > 0 })
}

// WithDiskAlerts sets how the owner is told of a node's out-of-disk episode and its recovery.
func (s *Server) WithDiskAlerts(tell func(ctx context.Context, alert orchestration.DiskAlert)) *Server {
	s.diskAlert = tell

	return s
}

// reportDisk keeps a heartbeat's disk sample and queues the alert it makes, in the order the samples were kept.
func (s *Server) reportDisk(ctx context.Context, nodeID string, disk nodeproto.DiskSpace) error {
	s.diskReports.Lock()
	defer s.diskReports.Unlock()

	alert, err := s.svc.ReportNodeDisk(ctx, nodeID, disk)
	if alert == nil || s.diskAlert == nil {
		return err
	}

	if s.diskAlerts == nil {
		s.diskAlerts = map[string][]orchestration.DiskAlert{}
	}

	queued := s.diskAlerts[nodeID]
	s.diskAlerts[nodeID] = append(queued, *alert)

	if len(queued) == 0 {
		// Told apart from the session, as sending to a channel may take seconds the node's messages do not wait.
		go s.tellDiskAlerts(context.WithoutCancel(ctx), nodeID)
	}

	return err
}

// tellDiskAlerts tells a node's queued alerts one after another, and ends when none is left.
func (s *Server) tellDiskAlerts(ctx context.Context, nodeID string) {
	for {
		s.diskReports.Lock()
		queued := s.diskAlerts[nodeID]
		if len(queued) == 0 {
			delete(s.diskAlerts, nodeID)
			s.diskReports.Unlock()

			return
		}
		s.diskReports.Unlock()

		s.diskAlert(ctx, queued[0])

		s.diskReports.Lock()
		s.diskAlerts[nodeID] = s.diskAlerts[nodeID][1:]
		s.diskReports.Unlock()
	}
}

// WithPhone sets how an offer learns whether workers are given a phone.
func (s *Server) WithPhone(held func(ctx context.Context) bool) *Server {
	s.phoneHeld = held

	return s
}

// WithToolNames sets where an offer's tool names come from: the tools the
// Dispatch's worker gets, read with its key (agentapi.Server.WorkerTools).
func (s *Server) WithToolNames(names func(ctx context.Context, dispatchID, key string) []string) *Server {
	s.toolNames = names

	return s
}

// WithAgentKeys sets where an offer's key to the agent API comes from.
func (s *Server) WithAgentKeys(key func(ctx context.Context, dispatchID string, uses []string) (string, bool),
) *Server {
	s.agentKey = key

	return s
}

// WithModelKeys sets where an offer's model gateway key comes from.
func (s *Server) WithModelKeys(key func(ctx context.Context, dispatchID, model string) (string, bool)) *Server {
	s.modelKey = key

	return s
}

// WithReleaseURL sets where nodes find their releases; "" means DefaultReleaseURL.
func (s *Server) WithReleaseURL(url string) *Server {
	if url == "" {
		url = DefaultReleaseURL
	}

	s.releaseURL = strings.TrimSuffix(url, "/")

	return s
}

// Register adds the node routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+nodeproto.PathHello, s.hello)
	mux.HandleFunc("GET "+nodeproto.PathSocket, s.socket)
	mux.HandleFunc("GET "+nodeproto.PathTerminal, s.terminal)
	mux.HandleFunc("POST "+nodeproto.PathFiles, s.filesAnswer)
	mux.HandleFunc("POST "+nodeproto.PathDevice, s.deviceAnswer)
	mux.HandleFunc("GET "+nodeproto.PathDevice, s.deviceView)
	mux.HandleFunc("POST "+nodeproto.PathExec, s.execAnswer)
	mux.HandleFunc("PUT "+nodeproto.PathTakes+"{take}/files/{name}", s.takeUpload)
	mux.HandleFunc("GET "+nodeproto.PathSkills+"{digest}", s.skill)
}

// skill serves a skill's archive to a node that holds an offer naming it
// (issue #21), as Omnara's daemon downloads a revision its offer names.
func (s *Server) skill(w http.ResponseWriter, r *http.Request) {
	n, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	archive, err := s.svc.SkillArchiveForNode(r.Context(), n.ID, r.PathValue("digest"))

	switch {
	case errors.Is(err, orchestration.ErrNotFound):
		http.Error(w, "no offer of this node names that skill", http.StatusNotFound)

		return
	case err != nil:
		s.logger.ErrorContext(r.Context(), "reading a skill for a node",
			slog.String("event", "nodeapi.skill_failed"), slog.String("node_id", n.ID), slog.String("error", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
	_, _ = w.Write(archive) //nolint:gosec // a gzipped tar served as application/gzip, not a page
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

	hello := orchestration.Hello{
		DiskPolicy: req.DiskPolicy, TaskBranches: req.TaskBranches,
		WorktreeRoot: req.WorktreeRoot, Terminal: req.Terminal, Device: req.Device, KVM: req.KVM,
		Held: req.Held, ListsHeld: req.ListsHeld,
		System: orchestration.SystemInfo{
			DaemonVersion: req.DaemonVersion, OS: req.System.OS, Arch: req.System.Arch, CPUs: req.System.CPUs,
			MemoryBytes: req.System.MemoryBytes, DiskBytes: req.System.DiskBytes,
		},
	}
	if t := req.TempIsolation; t != nil {
		hello.TempIsolation = &orchestration.TempIsolation{On: t.On, Reason: t.Reason}
	}
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

	s.deviceSaid(n.ID, req.DeviceState)
	s.guidesSaid(n.ID, req.StoreGuides)
	endTakes, dropTakes := s.settleTakes(r.Context(), n.ID, req)

	resp := nodeproto.HelloResponse{
		NodeID: n.ID, Dispositions: make([]nodeproto.Disposition, 0, len(dispositions)),
		HeartbeatAfterMs: nodeproto.HeartbeatEvery.Milliseconds(),
		GitHosts:         s.gitHosts(r.Context()),
		Tools:            s.toolsFor(r.Context(), req.System.OS, req.System.Arch, req.DaemonVersion),
		ReleaseURL:       s.releaseURL,
		// The node looks for its release at once when this is newer than its own (issue #783).
		ControlPlaneVersion: platform.Version,
		EndTakes:            endTakes, DropTakes: dropTakes,
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
		srv: s, conn: conn, nodeID: n.ID, tokenID: tokenID,
		os: n.System.OS, arch: n.System.Arch, version: n.System.DaemonVersion,
		cancel:  cancel,
		out:     make(chan nodeproto.Message, 64),
		offered: map[string]bool{}, stopped: map[string]bool{}, answered: map[string]bool{},
		identityWaits: map[string]<-chan struct{}{}, identityDeadline: map[string]time.Time{},
		identityRead: make(chan identityEnded),
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

	s.loseDeviceCalls(sess)
	s.loseExecs(sess)
}

// session is one node's WebSocket.
type session struct {
	srv    *Server
	conn   *websocket.Conn
	nodeID string
	// tokenID is the token the node connected with; a heartbeat is refused once it is revoked.
	tokenID string
	// os, arch and version are the node's system and release, for the tools it is told to have.
	os, arch, version string
	cancel            context.CancelFunc
	out               chan nodeproto.Message

	// What this connection already sent; a new connection sends all again.
	offered  map[string]bool
	stopped  map[string]bool
	answered map[string]bool

	// The offers held while their token's account is read (issue #991), by Dispatch: identityWaits the read each
	// waits for, identityDeadline when its hold ends (identityWait after it was first held). identityRead says a
	// wait ended: the offer is looked at again. Only the session's loop reads and writes the maps.
	identityWaits    map[string]<-chan struct{}
	identityDeadline map[string]time.Time
	identityRead     chan identityEnded
}

// identityEnded is the end of an offer's wait for one read of its token's account.
type identityEnded struct {
	dispatchID string
	read       <-chan struct{}
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
		case ended := <-c.identityRead:
			// The end of a read the offer no longer waits for (its token was replaced) changes nothing.
			if c.identityWaits[ended.dispatchID] == ended.read {
				delete(c.identityWaits, ended.dispatchID)
			}
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

// holdForIdentity says whether an offer is held while its token's account is read. It is held for the read its
// credential names now, beside the loop, until that read ends or identityWait has passed since the offer was first
// held; then identityRead says so and the offer is looked at again. A token replaced meanwhile names another read:
// the offer then waits for that one, within the same limit, so it never goes out with the new token and without its
// account because the old token's read ended. An offer whose limit has passed goes now, without the account, which
// is said.
func (c *session) holdForIdentity(ctx context.Context, dispatchID string, pending <-chan struct{}) bool {
	deadline, held := c.identityDeadline[dispatchID]
	if !held {
		deadline = time.Now().Add(identityWait)
		c.identityDeadline[dispatchID] = deadline
	}

	left := time.Until(deadline)
	if left <= 0 {
		c.srv.logger.WarnContext(ctx, "the account of a git host's token was not read in time; this Dispatch goes "+
			"without it and its worker commits as its node's git configuration says",
			slog.String("dispatch", dispatchID))

		return false
	}

	if c.identityWaits[dispatchID] == pending {
		return true
	}

	c.identityWaits[dispatchID] = pending

	go func() {
		timer := time.NewTimer(left)
		defer timer.Stop()

		select {
		case <-pending:
		case <-timer.C:
		case <-ctx.Done():
			return
		}

		select {
		case c.identityRead <- identityEnded{dispatchID: dispatchID, read: pending}:
		case <-ctx.Done():
		}
	}()

	return true
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

		// The token is read when the offer is sent and is kept nowhere in the control plane but the vault.
		if c.srv.hostToken != nil && o.RepoHost != "" {
			if cred, ok := c.srv.hostToken(ctx, o.RepoHost, o.Repo); ok {
				// The account it belongs to goes with it, so the worker commits as that account (issue #991): an
				// offer whose account is being read waits for it, beside this loop.
				if cred.Pending != nil && c.holdForIdentity(ctx, o.DispatchID, cred.Pending) {
					continue
				}

				o.RepoToken = cred.Token
				if cred.Identity.Known() {
					o.GitName, o.GitEmail = cred.Identity.Name, cred.Identity.Email
				}
			}
		}

		c.offered[o.DispatchID] = true
		delete(c.identityWaits, o.DispatchID)
		delete(c.identityDeadline, o.DispatchID)

		// So is the gateway's key: made now, for this Dispatch and its model only.
		if c.srv.modelKey != nil && o.AccountRef == orchestration.GatewayAccountRef {
			if key, ok := c.srv.modelKey(ctx, o.DispatchID, o.Model); ok {
				o.ModelKey = key
			}
		}

		// And the worker's key to the agent API: its tools reach the accounts its Run was given with it, and the
		// shared browser (issue #212), which a Run with no account has too.
		if c.srv.agentKey != nil {
			if key, ok := c.srv.agentKey(ctx, o.DispatchID, o.Uses); ok {
				o.AgentKey = key
			}
		}

		if c.srv.phoneHeld != nil && o.AgentKey != "" && c.srv.phoneHeld(ctx) {
			switches, err := c.srv.svc.ToolSwitchesOfDispatch(ctx, o.DispatchID)
			if err != nil {
				c.srv.logger.WarnContext(ctx, "reading phone access",
					slog.String("dispatch", o.DispatchID), slog.String("error", err.Error()))
			} else if switches.AllowsFamily("phone") {
				o.Device = nodeproto.DeviceAndroid
			}
		}

		// And the names of the tools it gets, read with that key as its tools server will be, for the "Tools" block
		// of its rules (issues #723, #789).
		if c.srv.toolNames != nil && o.AgentKey != "" {
			o.Tools = c.srv.toolNames(ctx, o.DispatchID, o.AgentKey)
		}

		o.Skills = c.srv.skillsFor(c.nodeID, o.Skills)
		if o.Device == "" {
			android := skillpack.Requirement(skillpack.RequiresDevice, nodeproto.DeviceAndroid)
			o.Skills = slices.DeleteFunc(slices.Clone(o.Skills), func(sk nodeproto.Skill) bool {
				return slices.Contains(sk.Requires, android)
			})
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
	if err == nil && hb.Device != nil {
		c.srv.deviceSaid(c.nodeID, hb.Device)
	}

	if err == nil && hb.Reaches != nil {
		err = c.srv.svc.NodeReaches(ctx, c.nodeID, *hb.Reaches)
	}

	if err == nil && hb.Runtimes != nil {
		runtimes := make([]orchestration.RuntimeInfo, 0, len(*hb.Runtimes))
		for _, rt := range *hb.Runtimes {
			runtimes = append(runtimes, orchestration.RuntimeInfo{Runtime: rt.Runtime, Path: rt.Path, Version: rt.Version})
		}

		err = c.srv.svc.NodeRuntimes(ctx, c.nodeID, runtimes)
	}

	if err == nil && hb.Tools != nil {
		tools := make([]orchestration.NodeTool, 0, len(*hb.Tools))
		for _, t := range *hb.Tools {
			tools = append(tools, orchestration.NodeTool{Name: t.Name, Version: t.Version, Error: t.Error})
		}

		err = c.srv.svc.NodeTools(ctx, c.nodeID, tools)
	}

	if hb.Disk != nil {
		if diskErr := c.srv.reportDisk(ctx, c.nodeID, *hb.Disk); diskErr != nil {
			err = errors.Join(err, diskErr)
		}
	}

	if err == nil && hb.Stats != nil {
		err = c.srv.svc.NodeStats(ctx, c.nodeID, orchestration.SystemStats{
			CPUPercent: hb.Stats.CPUPercent, MemoryUsedBytes: hb.Stats.MemoryUsedBytes,
			DiskUsedBytes: hb.Stats.DiskUsedBytes, Load1: hb.Stats.Load1, GPUs: gpusOf(hb.Stats.GPUs),
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
			Tools: c.srv.toolsFor(ctx, c.os, c.arch, c.version),
		})
	case nodeproto.TypeReport:
		var r nodeproto.Report
		if err := json.Unmarshal(msg.Payload, &r); err != nil || r.ReportID == "" {
			return c.send(ctx, nodeproto.TypeError, nodeproto.Error{Code: nodeproto.ErrorInvalid, Message: "invalid report"})
		}

		return c.send(ctx, nodeproto.TypeReportAck, c.srv.apply(ctx, c.nodeID, r))
	case nodeproto.TypeCleanupCheck:
		if err := c.srv.svc.CheckNodeToken(ctx, c.tokenID); err != nil {
			return err
		}

		var check nodeproto.CleanupCheck
		err := json.Unmarshal(msg.Payload, &check)
		if err != nil || check.ID == "" || len(check.Paths) > nodeproto.CleanupBatchSize {
			return c.send(ctx, nodeproto.TypeError, nodeproto.Error{
				Code: nodeproto.ErrorInvalid, Message: "invalid cleanup check",
			})
		}

		worktrees, protected, err := c.srv.svc.CheckWorktreeCleanupWithProtection(ctx, c.nodeID, check.Paths)
		if err != nil {
			c.srv.logger.WarnContext(ctx, "could not check worktree cleanup", slog.Any("error", err))
			return c.send(ctx, nodeproto.TypeCleanupAck, nodeproto.CleanupAck{ID: check.ID})
		}
		return c.send(ctx, nodeproto.TypeCleanupAck, cleanupReply(check.ID, worktrees, protected))
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

func cleanupReply(id string, worktrees []nodeproto.CleanupWorktree, protected []string) nodeproto.CleanupAck {
	ack := nodeproto.CleanupAck{ID: id, Worktrees: worktrees, Protected: &protected}
	msg, err := nodeproto.Encode(nodeproto.TypeCleanupAck, ack)
	if err != nil {
		return nodeproto.CleanupAck{ID: id}
	}
	if len(msg.Payload) >= nodeproto.MaxMessageBytes-1024 {
		return nodeproto.CleanupAck{ID: id, Split: true}
	}
	return ack
}

// gpusOf is a heartbeat's GPUs as the registry keeps them.
func gpusOf(gpus []nodeproto.GPUStats) []orchestration.GPUStats {
	out := make([]orchestration.GPUStats, 0, len(gpus))
	for _, g := range gpus {
		out = append(out, orchestration.GPUStats{
			Index: g.Index, Name: g.Name, UsagePercent: g.UsagePercent, MemoryUsedBytes: g.MemoryUsedBytes,
			MemoryTotalBytes: g.MemoryTotalBytes, TemperatureCelsius: g.TemperatureCelsius, PowerWatts: g.PowerWatts,
		})
	}

	return out
}
