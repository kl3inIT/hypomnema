//go:build unix

package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func (o Options) binary() string   { return filepath.Join(o.Home, "bin", "hypomnema") }
func (o Options) database() string { return filepath.Join(o.Home, "data", "hypomnema.db") }
func (o Options) control() string  { return filepath.Join(o.Home, "data", ControlSocket) }

func (o Options) manifestURL() string {
	return strings.TrimSuffix(o.ReleaseURL, "/") + "/controlplane-" + runtime.GOOS + "-" + runtime.GOARCH + ".txt"
}

func (o Options) printf(format string, args ...any) {
	if o.Out != nil {
		_, _ = fmt.Fprintf(o.Out, format, args...)
	}
}

// waitReady polls Ready until it answers 2xx or ReadyWithin passes.
func (o Options) waitReady(ctx context.Context) error {
	within := o.ReadyWithin
	if within <= 0 {
		within = 60 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()

	client := o.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.Ready, nil)
		if err != nil {
			return fmt.Errorf("building the request for %s: %w", o.Ready, err)
		}

		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()

			// /readyz answers 204 (platform.PathReady); any 2xx is ready.
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("the control plane did not become ready at %s within %s", o.Ready, within)
		case <-time.After(time.Second):
		}
	}
}

// waitTurns waits until no Bot's turn and no agent's tool call runs in the control plane, asking its control socket
// every TurnsEvery, for at most TurnsWithin; then the update goes on. The tool calls are waited for as OpenClaw's
// update drains a Gateway's requests in flight before it restarts, for at most 5 minutes, then restarts anyway
// (src/infra/restart-budget.ts DEFAULT_RESTART_DEFERRAL_TIMEOUT_MS, src/gateway/server-reload-active-work.ts
// "gateway request(s)"; issue #1060). A control plane that does not answer, one from before the socket or one
// that is down, is not waited for, as Hermes's updater takes a gateway whose control socket does not answer as one
// it cannot drain (gateway/control_socket.py, query_gateway_control).
func (o Options) waitTurns(ctx context.Context) {
	within, every := o.TurnsWithin, o.TurnsEvery
	if within <= 0 {
		within = DefaultTurnsWithin
	}

	if every <= 0 {
		every = time.Second
	}

	deadline := time.Now().Add(within)
	said := false

	var last Turns

	for {
		// Each question gets what is left of the wait at most: the last one does not run past the bound. None is
		// asked once the bound has passed (a client's timeout of zero is none).
		left := min(turnsAskWithin, time.Until(deadline))

		var (
			running Turns
			err     error
		)

		if left > 0 {
			running, err = o.turns(ctx, left)
		} else {
			running, err = last, context.DeadlineExceeded
		}

		switch {
		case err != nil && said && !time.Now().Before(deadline):
			o.printf("Bots' turns or tool calls still run after %s, %s; a turn is cut, and continues after the "+
				"update\n", within, last)

			return
		case err != nil:
			o.printf("not waiting for Bots' turns: the control plane did not say which run: %v\n", err)

			return
		case !running.busy():
			if said {
				o.printf("no Bot's turn nor tool call runs now\n")
			}

			return
		case !time.Now().Before(deadline):
			o.printf("Bots' turns or tool calls still run after %s, %s; a turn is cut, and continues after the "+
				"update\n", within, running)

			return
		case !said:
			o.printf("waiting up to %s for Bots' turns and tool calls to end, %s\n", within, running)

			said = true
		}

		last = running

		select {
		case <-ctx.Done():
			return
		case <-time.After(min(every, time.Until(deadline))):
		}
	}
}

// turnsAskWithin bounds one question on the control socket.
const turnsAskWithin = 5 * time.Second

// turns asks the control socket which chats' turns run, and how many tool calls, within the time given.
func (o Options) turns(ctx context.Context, within time.Duration) (Turns, error) {
	resp, err := o.ask(ctx, http.MethodGet, PathTurns, nil, within)
	if err != nil {
		return Turns{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // read only

	var t Turns
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return Turns{}, fmt.Errorf("reading the answer of %s: %w", o.control(), err)
	}

	return t, nil
}

// begin has the control plane mark the update (PathUpdate). It answers once no pi start is under way, which takes
// at most as long as pi takes to start.
func (o Options) begin(ctx context.Context, b Begin) error {
	body, err := json.Marshal(b)
	if err != nil {
		return err
	}

	resp, err := o.ask(ctx, http.MethodPost, PathUpdate, body, 2*time.Minute)
	if err != nil {
		return err
	}

	return resp.Body.Close()
}

// ask sends one request to the control socket and returns a 2xx answer.
func (o Options) ask(ctx context.Context, method, path string, body []byte, within time.Duration,
) (*http.Response, error) {
	socket := o.control()
	client := &http.Client{
		Timeout: within,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}},
	}
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, method, "http://control"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = resp.Body.Close()

		return nil, fmt.Errorf("%s answered %s", socket, resp.Status)
	}

	return resp, nil
}
