package zalo

// The bridge process (issue #657): the control plane's side of bridges/zalo/src/protocol.mjs, JSON lines on the
// bridge's stdin and stdout, as Hermes's WhatsApp adapter and MemoryOS's bridge speak (MemoryOS ADR 0031). A call
// waits for the answer with its id; everything else the bridge writes is an event. The bridge's stderr is the
// control plane's log. Closing stdin ends the bridge (bridge.mjs, lines.on("close")).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// maxLine bounds one line of the bridge: a credentials event carries Zalo's cookies, a message a long text.
const maxLine = 4 << 20

// stopWait is how long a bridge has to end after its stdin is closed.
const stopWait = 5 * time.Second

// errBridgeEnded is a call to a bridge that ended.
var errBridgeEnded = errors.New("zalo: the bridge ended")

// errRejected is a login Zalo refused: the session is no longer valid, and no retry mends it (bridge.mjs, the
// code "rejected").
var errRejected = errors.New("zalo: Zalo refused the session")

// bridgeEvent is one event of the bridge.
type bridgeEvent struct {
	Name string          `json:"event"`
	Data json.RawMessage `json:"data"`
}

type line struct {
	ID     *int64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Code   string          `json:"code"`
	bridgeEvent
}

// bridge is one running bridge process.
type bridge struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan bridgeEvent
	log    *slog.Logger

	mu      sync.Mutex
	next    int64
	waiting map[int64]chan line
	ended   chan struct{}
	// stopping is closed by stop, so that an event nobody reads any more does not hold the reader.
	stopping chan struct{}
	stopOnce sync.Once
}

// startBridge runs node on the bundle at script and reads it until it ends, or until ctx ends, which kills it. Its
// events are given on events, which is closed when it ends.
func startBridge(ctx context.Context, node, script string, logger *slog.Logger) (*bridge, error) {
	//nolint:gosec // G204: the machine's Node and the bundle the control plane wrote.
	cmd := exec.CommandContext(ctx, node, script)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("zalo: the bridge's stdin: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("zalo: the bridge's stdout: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("zalo: the bridge's stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("zalo: starting the bridge with %s: %w", node, err)
	}

	b := &bridge{
		cmd: cmd, stdin: stdin, events: make(chan bridgeEvent, 256), log: logger,
		waiting: map[int64]chan line{}, ended: make(chan struct{}), stopping: make(chan struct{}),
	}

	go b.logStderr(stderr)
	go b.read(stdout)

	return b, nil
}

func (b *bridge) logStderr(r io.Reader) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), maxLine)

	for s.Scan() {
		b.log.Warn("zalo bridge: "+s.Text(), slog.String("event", "zalo.bridge"))
	}
}

func (b *bridge) read(r io.Reader) {
	defer func() {
		_ = b.cmd.Wait()

		b.mu.Lock()
		for id, ch := range b.waiting {
			close(ch)
			delete(b.waiting, id)
		}
		b.mu.Unlock()

		close(b.ended)
		close(b.events)
	}()

	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), maxLine)

	for s.Scan() {
		var l line
		if err := json.Unmarshal(s.Bytes(), &l); err != nil {
			b.log.Warn("zalo bridge: a line that is not JSON", slog.String("event", "zalo.bridge"))

			continue
		}

		if l.ID == nil {
			select {
			case b.events <- l.bridgeEvent:
			case <-b.stopping:
			}

			continue
		}

		b.mu.Lock()
		ch := b.waiting[*l.ID]
		delete(b.waiting, *l.ID)
		b.mu.Unlock()

		if ch != nil {
			ch <- l
		}
	}
}

// call asks the bridge to run a method and waits for its answer, decoded into out unless it is nil.
func (b *bridge) call(ctx context.Context, method string, params, out any) error {
	answered := make(chan line, 1)

	b.mu.Lock()
	b.next++
	id := b.next
	b.waiting[id] = answered
	b.mu.Unlock()

	raw, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return err
	}

	b.mu.Lock()
	_, err = b.stdin.Write(append(raw, '\n'))
	b.mu.Unlock()

	if err != nil {
		return fmt.Errorf("%w: %w", errBridgeEnded, err)
	}

	select {
	case l, ok := <-answered:
		switch {
		case !ok:
			return errBridgeEnded
		case l.Code == "rejected":
			return fmt.Errorf("%w: %s", errRejected, l.Error)
		case l.Error != "":
			return fmt.Errorf("zalo: %s: %s", method, l.Error)
		case out != nil:
			return json.Unmarshal(l.Result, out)
		}

		return nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.waiting, id)
		b.mu.Unlock()

		return ctx.Err()
	}
}

// stop ends the bridge: its stdin closed, then killed if it has not ended in a while. Events not read yet are
// dropped.
func (b *bridge) stop() {
	b.stopOnce.Do(func() { close(b.stopping) })

	_ = b.stdin.Close()

	select {
	case <-b.ended:
	case <-time.After(stopWait):
		_ = b.cmd.Process.Kill()
		<-b.ended
	}
}
