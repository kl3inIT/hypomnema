package agenttool

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
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
	"time"

	"github.com/coder/websocket"
)

// The bridge (issue #154) is how a tool whose Integration goes through the
// service proxy reaches it. For the life of one run of the tool, the
// launcher listens on loopback as an HTTP proxy; each connection the tool
// opens is carried, as it is, over a websocket to the control plane, which
// reads the requests in it. A node reaches the control plane this way
// already, for its socket and its terminal, so no other route is needed, and
// the coordinator's launcher is the same code. Desktop exec unwraps the
// child's TLS and re-wraps it for the control plane's service proxy instead.

// proxyVariables are what the launcher sets for a proxied tool, and removes
// first: whatever the agent's environment said of proxies and certificates
// does not apply to the tool.
var proxyVariables = []string{
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "AWS_CA_BUNDLE",
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

// bridge starts the loopback proxy for the account integration:label and returns the tool's variables. stop
// ends it and removes the certificate file; refused holds why the control
// plane opened no stream, if it did not. user says the account is the
// owner's own sign-in: the tool is given the placeholder as a person's token
// and held to acting as one.
func (l Launcher) bridge(ctx context.Context, def Def, integration, label string, user bool, local *execAuthority,
) (env []string, refused *refusal, stop func(), err error) {
	raw, err := l.certificate(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	var secure *bridgeTLS
	if local != nil {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(raw) {
			return nil, nil, nil, errors.New("the control plane gave an invalid service proxy certificate")
		}
		secure = &bridgeTLS{local: local, upstream: roots}
		raw = local.rootPEM()
	}
	ca, err := writeCACert(raw)
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

			go l.carry(bridgeCtx, conn, target, integration, label, refused, secure)
		}
	}()

	address := "http://" + listener.Addr().String()

	token, identity := def.TokenEnv, def.AppIdentity
	if user && def.UserTokenEnv != "" {
		token, identity = def.UserTokenEnv, def.UserIdentity
	}

	env = []string{
		token + "=" + Placeholder, "HTTPS_PROXY=" + address, "https_proxy=" + address, "SSL_CERT_FILE=" + ca,
	}
	if def.IdentityEnv != "" {
		env = append(env, def.IdentityEnv+"="+identity)
	}
	if def.CAEnv != "" {
		env = append(env, def.CAEnv+"="+ca)
	}
	stop = func() {
		cancel()

		_ = listener.Close()
		_ = os.Remove(ca)
	}

	return env, refused, stop, nil
}

// certificate reads the control plane's proxy certificate on every run: the proxy makes a new one when it starts.
func (l Launcher) certificate(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(l.URL, "/")+PathCA, nil)
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
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

		return nil, fmt.Errorf("the control plane could not be reached: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed; closing adds nothing.

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil || resp.StatusCode != http.StatusOK || len(raw) == 0 {
		return nil, errors.New("the control plane gave no certificate for its service proxy; it may be older than " +
			"this tool's launcher")
	}

	return raw, nil
}

func writeCACert(raw []byte) (string, error) {
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
func (l Launcher) carry(ctx context.Context, conn net.Conn, target, integration, label string,
	refused *refusal, secure *bridgeTLS,
) {
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

	header := http.Header{"Authorization": {"Bearer " + l.Key}}
	if l.RunKey != "" {
		header.Set(HeaderRoutineRun, l.RunKey)
	}

	//nolint:bodyclose // The library closes the handshake's body.
	ws, resp, err := websocket.Dial(ctx, target+"?"+query.Encode(), &websocket.DialOptions{
		HTTPHeader: header, HTTPClient: l.Client,
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

	if secure != nil {
		if err := secure.relay(ctx, conn, reader, stream, req.Host); err != nil {
			refused.keep(err.Error())
		}
		return
	}
	if _, err := io.WriteString(conn, connected); err != nil {
		return
	}

	copyTunnel(conn, reader, stream)
}

const connected = "HTTP/1.1 200 Connection Established\r\n\r\n"

func copyTunnel(childWriter io.Writer, childReader io.Reader, upstream io.ReadWriteCloser) {
	go func() {
		_, _ = io.Copy(upstream, childReader)
		_ = upstream.Close()
	}()

	_, _ = io.Copy(childWriter, upstream)
}

type bridgeTLS struct {
	local    *execAuthority
	upstream *x509.CertPool
}

func (b *bridgeTLS) relay(ctx context.Context, conn net.Conn, reader *bufio.Reader, stream net.Conn,
	address string,
) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return errors.New("the local service proxy reaches a service over HTTPS only")
	}
	leaf, err := b.local.leaf(host)
	if err != nil {
		return err
	}

	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	handshake, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	if deadline, ok := handshake.Deadline(); ok {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
	}

	upstream := tls.Client(stream, &tls.Config{
		ServerName: host, RootCAs: b.upstream, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
	})
	if err := upstream.HandshakeContext(handshake); err != nil {
		return fmt.Errorf("verifying the control plane's service proxy: %w", err)
	}

	if _, err := io.WriteString(conn, connected); err != nil {
		return err
	}
	child := tls.Server(bufferedConn{Conn: conn, reader: reader}, &tls.Config{
		Certificates: []tls.Certificate{*leaf}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
	})
	if err := child.HandshakeContext(handshake); err != nil {
		return fmt.Errorf("establishing TLS with the service command: %w", err)
	}
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		return err
	}

	cancel()
	copyTunnel(child, child, upstream)

	return nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c bufferedConn) Read(buf []byte) (int, error) { return c.reader.Read(buf) }

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
