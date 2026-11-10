// Package daemon is the node's connection to the control plane (issue #32):
// it says hello with the Dispatches it holds, keeps one WebSocket open,
// accepts offered Dispatches, hands them to a Runner, and turns what the
// Runner reports into reports. Durable reports go through the outbox in
// statedb and are resent after every reconnect until acknowledged. It follows
// Omnara's machine daemon (internal/machinedaemon/client.go,
// client_socket.go, report_outbox.go at 0ef1711).
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Reconnect backoff and timers (Omnara client.go L154-159, L507-533).
const (
	backoffMin   = time.Second
	backoffMax   = 30 * time.Second
	writeTimeout = 10 * time.Second
	// LeaseEvery is how often a running Dispatch's lease is renewed; the
	// control plane's lease is 90 s.
	LeaseEvery = 30 * time.Second
	// retryTransient: a report answered transient_error is sent again after this.
	retryTransient = 5 * time.Second
)

// ErrUnauthorized is returned when the control plane refuses the node's token; the daemon stops.
var ErrUnauthorized = errors.New("the control plane refused the node token")

var errReplaced = errors.New("another connection of this node took over")

// Reporter is how a Runner reports what one worker does; *Worker is the
// daemon's.
type Reporter interface {
	ID() string
	Started(ctx context.Context, processRef, cliSessionRef string) error
	Progress(ctx context.Context) error
	Ask(ctx context.Context, a nodeproto.Ask) (string, error)
	AnswerReceived(ctx context.Context, questionID string) error
	Finished(ctx context.Context, f nodeproto.Finished) error
	Exited(ctx context.Context) error
	Stopped(ctx context.Context) error
}

// Runner runs workers. Its methods return quickly; what a worker does comes
// back through its Reporter.
type Runner interface {
	// Start runs the worker of an accepted Dispatch.
	Start(ctx context.Context, d statedb.Dispatch, r Reporter) error
	// Adopt takes back a Dispatch whose worker may still run, after the
	// daemon restarted or the outcome was marked unknown.
	Adopt(ctx context.Context, d statedb.Dispatch, r Reporter) error
	// Stop asks the worker to stop; the Runner reports Stopped when it has.
	Stop(id string)
	// Release kills the worker and cleans up, reporting nothing: the
	// Dispatch is no longer the node's.
	Release(id string)
	// Answer passes an answer to the worker that asked.
	Answer(id string, q nodeproto.Question)
	// Asked tells the worker the id of the question it asked with reportID.
	Asked(id, reportID, questionID string)
	// ItemsAcked passes on the control plane's answer to a transcript batch sent with SendItems.
	ItemsAcked(ack nodeproto.ItemsAck)
}

// Config is what the daemon needs to reach its control plane.
type Config struct {
	// URL is the control plane's base URL, such as https://hypomnema.example.
	URL          string
	Token        string
	WorktreeRoot string
	Version      string
	Runtimes     []nodeproto.RuntimeInfo
	System       nodeproto.SystemInfo
}

// Daemon is one node's connection.
type Daemon struct {
	cfg    Config
	db     *statedb.DB
	runner Runner
	logger *slog.Logger
	http   *http.Client
	newID  func(prefix string) string
	// instance identifies this process to the control plane.
	instance string

	mu        sync.Mutex
	out       chan nodeproto.Message // the current session's send queue; nil while disconnected
	ephemeral map[string]nodeproto.Report
	known     map[string]bool // Dispatches the Runner has this process
}

// New returns a Daemon.
func New(cfg Config, db *statedb.DB, runner Runner, logger *slog.Logger) *Daemon {
	return &Daemon{
		cfg: cfg, db: db, runner: runner, logger: logger, http: &http.Client{Timeout: 30 * time.Second},
		newID: platform.NewID, instance: platform.NewID("ins"),
		ephemeral: map[string]nodeproto.Report{}, known: map[string]bool{},
	}
}

// Run keeps the node connected until ctx ends or the token is refused.
func (d *Daemon) Run(ctx context.Context) error {
	wait := backoffMin

	for {
		healthy, err := d.connectOnce(ctx)

		switch {
		case ctx.Err() != nil:
			return nil //nolint:nilerr // ending with ctx is how the daemon stops
		case errors.Is(err, ErrUnauthorized):
			return err
		case healthy:
			wait = backoffMin
		}

		if err != nil {
			d.logger.WarnContext(ctx, "disconnected from the control plane",
				slog.String("event", "node.disconnected"), slog.String("error", err.Error()),
				slog.Duration("retry_in", wait))
		}

		// Jitter between half and the full delay (Omnara).
		delay := wait/2 + time.Duration(rand.Int64N(int64(wait/2)+1)) //nolint:gosec // jitter, not security

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}

		wait = min(wait*2, backoffMax)
	}
}

// connectOnce says hello, applies the dispositions and holds one session. It
// reports whether the session got as far as a heartbeat acknowledgement.
func (d *Daemon) connectOnce(ctx context.Context) (bool, error) {
	held, err := d.db.List(ctx)
	if err != nil {
		return false, err
	}

	claims := []nodeproto.Claim{}
	for _, h := range held {
		if h.State == statedb.StateAccepted && h.Epoch > 0 {
			claims = append(claims, nodeproto.Claim{DispatchID: h.ID, Epoch: h.Epoch})
		}
	}

	resp, err := d.hello(ctx, claims)
	if err != nil {
		return false, err
	}

	if err := d.dispose(ctx, held, resp.Dispositions); err != nil {
		return false, err
	}

	return d.session(ctx, time.Duration(resp.HeartbeatAfterMs)*time.Millisecond)
}

// Hello says hello once, claiming nothing, and returns the node's id. It
// checks an install.
func (d *Daemon) Hello(ctx context.Context) (string, error) {
	resp, err := d.hello(ctx, []nodeproto.Claim{})
	if err != nil {
		return "", err
	}

	return resp.NodeID, nil
}

func (d *Daemon) hello(ctx context.Context, claims []nodeproto.Claim) (nodeproto.HelloResponse, error) {
	body, err := json.Marshal(nodeproto.HelloRequest{
		InstanceID: d.instance, DaemonVersion: d.cfg.Version, WorktreeRoot: d.cfg.WorktreeRoot,
		Runtimes: d.cfg.Runtimes, System: d.cfg.System, Claims: claims,
	})
	if err != nil {
		return nodeproto.HelloResponse{}, fmt.Errorf("encoding hello: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(d.cfg.URL, "/")+nodeproto.PathHello,
		bytes.NewReader(body))
	if err != nil {
		return nodeproto.HelloResponse{}, fmt.Errorf("building hello: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.http.Do(req)
	if err != nil {
		return nodeproto.HelloResponse{}, fmt.Errorf("saying hello: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nodeproto.HelloResponse{}, ErrUnauthorized
	case resp.StatusCode != http.StatusOK:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

		return nodeproto.HelloResponse{}, fmt.Errorf("hello answered %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	var out nodeproto.HelloResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, nodeproto.MaxMessageBytes)).Decode(&out); err != nil {
		return nodeproto.HelloResponse{}, fmt.Errorf("decoding hello: %w", err)
	}

	return out, nil
}

// dispose applies the control plane's answer for each claimed Dispatch, and
// takes back the ones retained that this process does not run yet.
func (d *Daemon) dispose(ctx context.Context, held []statedb.Dispatch, ds []nodeproto.Disposition) error {
	kinds := map[string]string{}
	for _, x := range ds {
		kinds[x.DispatchID] = x.Kind
	}

	for _, h := range held {
		switch kinds[h.ID] {
		case nodeproto.DispositionRelease:
			d.runner.Release(h.ID)
			d.forget(h.ID)

			if err := d.db.Remove(ctx, h.ID); err != nil {
				return err
			}
		case nodeproto.DispositionReattach:
			if err := d.submit(ctx, nodeproto.Report{
				Kind: nodeproto.ReportReattach, DispatchID: h.ID, Epoch: h.Epoch,
				Payload: mustJSON(nodeproto.Reattach{ProcessRef: h.ProcessRef}),
			}); err != nil {
				return err
			}

			if err := d.adopt(ctx, h); err != nil {
				return err
			}
		case nodeproto.DispositionRetain:
			if err := d.adopt(ctx, h); err != nil {
				return err
			}
		}
	}

	return nil
}

func (d *Daemon) adopt(ctx context.Context, h statedb.Dispatch) error {
	if !d.remember(h.ID) {
		return nil
	}

	if err := d.runner.Adopt(ctx, h, d.worker(h.ID)); err != nil {
		return fmt.Errorf("adopting %s: %w", h.ID, err)
	}

	return nil
}

// remember records that this process runs id; it reports false if it already did.
func (d *Daemon) remember(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.known[id] {
		return false
	}

	d.known[id] = true

	return true
}

func (d *Daemon) forget(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.known, id)
}

func (d *Daemon) session(ctx context.Context, heartbeat time.Duration) (bool, error) {
	url := "ws" + strings.TrimPrefix(strings.TrimSuffix(d.cfg.URL, "/"), "http") + nodeproto.PathSocket

	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: d.http, HTTPHeader: http.Header{"Authorization": {"Bearer " + d.cfg.Token}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return false, ErrUnauthorized
		}

		return false, fmt.Errorf("opening the socket: %w", err)
	}

	conn.SetReadLimit(nodeproto.MaxMessageBytes)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := make(chan nodeproto.Message, 256)
	d.setOut(out)

	defer d.setOut(nil)

	errs := make(chan error, 2)
	in := make(chan nodeproto.Message)

	go func() { errs <- readLoop(ctx, conn, in) }()
	go func() { errs <- writeLoop(ctx, conn, out) }()

	healthy, err := d.loop(ctx, in, errs, heartbeat)

	status := websocket.StatusNormalClosure
	if err != nil {
		status = websocket.StatusGoingAway
	}

	_ = conn.Close(status, "")

	return healthy, err
}

func (d *Daemon) loop(ctx context.Context, in <-chan nodeproto.Message, errs <-chan error, heartbeat time.Duration,
) (bool, error) {
	if heartbeat <= 0 {
		heartbeat = nodeproto.HeartbeatEvery
	}

	beat := time.NewTimer(0)
	defer beat.Stop()

	lease := time.NewTicker(LeaseEvery)
	defer lease.Stop()

	if err := d.replay(ctx); err != nil {
		return false, err
	}

	healthy := false

	for {
		select {
		case <-ctx.Done():
			return healthy, nil
		case err := <-errs:
			return healthy, err
		case <-beat.C:
			d.send(nodeproto.Message{Type: nodeproto.TypeHeartbeat})
			beat.Reset(heartbeat)
		case <-lease.C:
			if err := d.renewLeases(ctx); err != nil {
				return healthy, err
			}
		case msg := <-in:
			if msg.Type == nodeproto.TypeHeartbeatAck {
				healthy = true

				var ack nodeproto.HeartbeatAck
				if json.Unmarshal(msg.Payload, &ack) == nil && ack.HeartbeatAfterMs > 0 {
					heartbeat = time.Duration(ack.HeartbeatAfterMs) * time.Millisecond
				}

				continue
			}

			if err := d.handle(ctx, msg); err != nil {
				return healthy, err
			}
		}
	}
}

// replay sends every queued report again, in order (Omnara report_outbox.go).
func (d *Daemon) replay(ctx context.Context) error {
	pending, err := d.db.Pending(ctx)
	if err != nil {
		return err
	}

	for _, r := range pending {
		d.sendReport(r)
	}

	return nil
}

func (d *Daemon) renewLeases(ctx context.Context) error {
	held, err := d.db.List(ctx)
	if err != nil {
		return err
	}

	for _, h := range held {
		if h.State == statedb.StateAccepted {
			d.ephemeralReport(nodeproto.Report{Kind: nodeproto.ReportHeartbeat, DispatchID: h.ID, Epoch: h.Epoch})
		}
	}

	return nil
}

func (d *Daemon) setOut(out chan nodeproto.Message) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.out = out
}

// send queues a message on the current session and reports whether it did;
// while disconnected it is dropped (durable reports are in the outbox and
// replayed).
func (d *Daemon) send(msg nodeproto.Message) bool {
	d.mu.Lock()
	out := d.out
	d.mu.Unlock()

	if out == nil {
		return false
	}

	select {
	case out <- msg:
		return true
	default:
		d.logger.Warn("send queue full; dropping a message",
			slog.String("event", "node.send_dropped"), slog.String("type", string(msg.Type)))

		return false
	}
}

// SendItems sends a batch of a worker's transcript (issue #43) and reports
// whether it was queued. Nothing keeps it: the Runner sends it again until
// ItemsAcked says the control plane has it.
func (d *Daemon) SendItems(b nodeproto.Items) bool {
	msg, err := nodeproto.Encode(nodeproto.TypeItems, b)
	if err != nil || len(msg.Payload) > nodeproto.MaxMessageBytes-1024 {
		d.logger.Warn("a transcript batch cannot be sent",
			slog.String("event", "node.items_unsendable"), slog.String("dispatch_id", b.DispatchID))

		return false
	}

	return d.send(msg)
}

func (d *Daemon) sendReport(r nodeproto.Report) {
	msg, err := nodeproto.Encode(nodeproto.TypeReport, r)
	if err == nil {
		d.send(msg)
	}
}

// submit queues a durable report in the outbox and sends it.
func (d *Daemon) submit(ctx context.Context, r nodeproto.Report) error {
	if r.ReportID == "" {
		r.ReportID = d.newID("rep")
	}

	if err := d.db.Enqueue(ctx, r); err != nil {
		return err
	}

	d.sendReport(r)

	return nil
}

// ephemeralReport sends a report that is not worth resending (a heartbeat,
// progress); its answer is still handled.
func (d *Daemon) ephemeralReport(r nodeproto.Report) {
	r.ReportID = d.newID("rep")

	d.mu.Lock()
	d.ephemeral[r.ReportID] = r
	d.mu.Unlock()

	d.sendReport(r)
}

func readLoop(ctx context.Context, conn *websocket.Conn, in chan<- nodeproto.Message) error {
	for {
		readCtx, cancel := context.WithTimeout(ctx, nodeproto.ReadTimeout)

		var msg nodeproto.Message

		err := wsjson.Read(readCtx, conn, &msg)

		cancel()

		if err != nil {
			if ctx.Err() != nil {
				return nil //nolint:nilerr // the session ended; not a failure
			}

			return fmt.Errorf("reading from the control plane: %w", err)
		}

		select {
		case in <- msg:
		case <-ctx.Done():
			return nil
		}
	}
}

func writeLoop(ctx context.Context, conn *websocket.Conn, out <-chan nodeproto.Message) error {
	ping := time.NewTicker(nodeproto.PingEvery)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ping.C:
			pingCtx, cancel := context.WithTimeout(ctx, nodeproto.PingTimeout)
			err := conn.Ping(pingCtx)

			cancel()

			if err != nil && ctx.Err() == nil {
				return fmt.Errorf("ping: %w", err)
			}
		case msg := <-out:
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := wsjson.Write(writeCtx, conn, msg)

			cancel()

			if err != nil {
				if ctx.Err() != nil {
					return nil
				}

				return fmt.Errorf("writing to the control plane: %w", err)
			}
		}
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("encoding %T: %v", v, err))
	}

	return raw
}
