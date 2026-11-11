package agenttool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// The bridge (issue #154) is how a tool whose Integration goes through the
// service proxy reaches it. For the life of one run of the tool, the
// launcher listens on loopback as an HTTP proxy; each connection the tool
// opens is carried, as it is, over a websocket to the control plane, which
// reads the requests in it. A node reaches the control plane this way
// already, for its socket and its terminal, so no other route is needed, and
// the coordinator's launcher is the same code. The bridge holds nothing: no
// token, and not the key of the proxy's certificate.

// proxyVariables are what the launcher sets for a proxied tool, and removes
// first: whatever the agent's environment said of proxies and certificates
// does not apply to the tool.
var proxyVariables = []string{
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
}

// refusal keeps the first reason the control plane gave for not opening a
// stream. It is said after the tool ends, not while it runs: the tool's own
// output is being copied to the same place.
type refusal struct {
	mu     sync.Mutex
	reason string
}

func (r *refusal) keep(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.reason == "" {
		r.reason = reason
	}
}

func (r *refusal) said() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.reason
}

// bridge starts the loopback proxy and returns the tool's variables. stop
// ends it and removes the certificate file; refused holds why the control
// plane opened no stream, if it did not.
func (l Launcher) bridge(ctx context.Context, def Def, label string,
) (env []string, refused *refusal, stop func(), err error) {
	ca, err := l.certificate(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	//nolint:noctx // A listener on loopback; it is closed by stop.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = os.Remove(ca)

		return nil, nil, nil, fmt.Errorf("listening for %s: %w", def.Name, err)
	}

	target := "ws" + strings.TrimPrefix(strings.TrimSuffix(l.URL, "/"), "http") + PathTunnel
	refused = &refusal{}

	bridgeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go l.carry(bridgeCtx, conn, target, def.Integration, label, refused)
		}
	}()

	address := "http://" + listener.Addr().String()

	env = []string{
		def.TokenEnv + "=" + Placeholder, "HTTPS_PROXY=" + address, "https_proxy=" + address, "SSL_CERT_FILE=" + ca,
	}
	stop = func() {
		cancel()

		_ = listener.Close()
		_ = os.Remove(ca)
	}

	return env, refused, stop, nil
}

// certificate reads the proxy's certificate into a file the tool is told to
// trust. It is read on every run: the proxy makes a new one when it starts.
func (l Launcher) certificate(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(l.URL, "/")+PathCA, nil)
	if err != nil {
		return "", fmt.Errorf("building the request: %w", err)
	}

	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: launchTimeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}

		return "", fmt.Errorf("the control plane could not be reached: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed; closing adds nothing.

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil || resp.StatusCode != http.StatusOK || len(raw) == 0 {
		return "", errors.New("the control plane gave no certificate for its service proxy; it may be older than " +
			"this tool's launcher")
	}

	file, err := os.CreateTemp("", "hypomnema-proxy-ca-*.pem")
	if err != nil {
		return "", fmt.Errorf("keeping the proxy's certificate: %w", err)
	}

	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())

		return "", fmt.Errorf("keeping the proxy's certificate: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())

		return "", fmt.Errorf("keeping the proxy's certificate: %w", err)
	}

	return file.Name(), nil
}

// carry serves one connection of the tool: it reads the CONNECT, opens the
// stream to the control plane for that host, and copies bytes both ways.
func (l Launcher) carry(ctx context.Context, conn net.Conn, target, integration, label string, refused *refusal) {
	defer conn.Close() //nolint:errcheck // Closing a connection that already ended adds nothing.

	reader := bufio.NewReader(conn)

	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}

	if req.Method != http.MethodConnect {
		_, _ = io.WriteString(conn, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")

		return
	}

	query := url.Values{QueryIntegration: {integration}, QueryLabel: {label}, QueryHost: {req.Host}}
	if l.Chat != "" {
		query.Set(QueryChat, l.Chat)
	}

	//nolint:bodyclose // The library closes the handshake's body.
	ws, resp, err := websocket.Dial(ctx, target+"?"+query.Encode(), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + l.Key}}, HTTPClient: l.Client,
	})
	if err != nil {
		// The tool only sees that its proxy refused; the reason is kept for the agent.
		refused.keep(refusalOf(resp, err))

		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")

		return
	}

	ws.SetReadLimit(-1)

	stream := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer stream.Close() //nolint:errcheck // Closing a stream that already ended adds nothing.

	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	go func() {
		_, _ = io.Copy(stream, reader)
		_ = stream.Close()
	}()

	_, _ = io.Copy(conn, stream)
}

// refusalOf is why the control plane did not open a stream, in its own words.
func refusalOf(resp *http.Response, err error) string {
	if resp != nil && resp.Body != nil {
		var refused struct {
			Message string `json:"message"`
		}

		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(raw, &refused) == nil && refused.Message != "" {
			return refused.Message
		}

		return fmt.Sprintf("the control plane's service proxy answered %d", resp.StatusCode)
	}

	return "the control plane's service proxy could not be reached: " + err.Error()
}
