package controlplane

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// botGitHub is a Bot's gh in one of its chats, reaching GitHub through the service proxy as serve wires it: the git
// host's Connection github.com:default, whose token is "conn-github-token", and the owner's GitHub App's token for a
// request that names a repository the App covers (issue #1262). GitHub is the world's fake.
type botGitHub struct {
	w     *appWorld
	proxy *serviceproxy.Proxy
	conn  *tls.Conn
	read  *bufio.Reader
	// plain asks GitHub for an answer not compressed, so a test reaches what follows the read of the answer.
	plain bool
}

// connToken is the Connection's own token, the owner's, which the proxy uses when no App token is picked.
const connToken = "conn-github-token" //nolint:gosec // a test's made-up token

// ghIn opens a Bot's gh in chat, through a proxy over the world's store, its App and its GitHub.
func (w *appWorld) ghIn(chat string) *botGitHub {
	w.t.Helper()

	ctx := w.t.Context()
	logger := slog.New(slog.DiscardHandler)

	_, err := w.svc.SetConnection(ctx, serviceproxy.GitHubID, "default", "", "conn-github-com-default")
	require.NoError(w.t, err)
	_, err = w.svc.SetConnectionChats(ctx, serviceproxy.GitHubID, "default", true)
	require.NoError(w.t, err)

	_, _, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(w.t, err)

	// The Bot's pi's own token, bound to the chat and its Bot, as the coordinator gives each chat's pi.
	pi, err := w.svc.AddBotToken(ctx, "pi", chat, orchestration.DefaultBotID)
	require.NoError(w.t, err)

	agents := agentapi.New(w.svc, func(context.Context, string, string) (agentapi.Token, error) {
		return agentapi.Token{AccessToken: connToken}, nil
	}, logger)

	proxy, err := serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: w.svc, Logger: logger, Upstream: w.gh, Hold: time.Minute,
		Token: func(context.Context, string, string) (string, error) { return connToken, nil },
		RepoToken: func(ctx context.Context, id, repo string) (string, bool) {
			token, ok, err := w.app.TokenFor(ctx, id, repo)

			return token.Value, err == nil && ok
		},
		AppReviews: appReviews{svc: w.svc, logger: logger},
	})
	require.NoError(w.t, err)

	mux := http.NewServeMux()
	agents.Register(mux)
	proxy.Register(mux)

	srv := httptest.NewServer(mux)
	w.t.Cleanup(srv.Close)

	query := url.Values{
		agenttool.QueryIntegration: {serviceproxy.GitHubID}, agenttool.QueryLabel: {"default"},
		agenttool.QueryHost: {"api.github.com:443"}, agenttool.QueryChat: {chat},
	}

	//nolint:bodyclose // The library closes the handshake's body.
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+agenttool.PathTunnel+"?"+
		query.Encode(), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + pi}}})
	require.NoError(w.t, err)

	ws.SetReadLimit(-1)

	resp, err := http.Get(srv.URL + agenttool.PathCA) //nolint:noctx // A test's own server.
	require.NoError(w.t, err)

	pem, err := io.ReadAll(resp.Body)
	require.NoError(w.t, err)
	require.NoError(w.t, resp.Body.Close())

	roots := x509.NewCertPool()
	require.True(w.t, roots.AppendCertsFromPEM(pem))

	conn := tls.Client(websocket.NetConn(context.WithoutCancel(ctx), ws, websocket.MessageBinary),
		&tls.Config{ServerName: "api.github.com", RootCAs: roots, MinVersion: tls.VersionTLS12})
	require.NoError(w.t, conn.HandshakeContext(ctx))
	w.t.Cleanup(func() { _ = conn.Close() })

	return &botGitHub{w: w, proxy: proxy, conn: conn, read: bufio.NewReader(conn)}
}

// post sends what gh api -X POST path with JSON fields sends, and approves the write when the owner is asked, as he
// does for a Bot's write on GitHub; it returns the status GitHub answered.
func (g *botGitHub) post(path string, body []byte) int {
	g.w.t.Helper()

	status, _ := g.postRead(path, body)

	return status
}

// postRead is post that also returns the answer's body as gh reads it: gh asks for gzip, as gh 2.103 does, and
// unpacks what it gets.
func (g *botGitHub) postRead(path string, body []byte) (int, string) {
	g.w.t.Helper()

	req, err := http.NewRequestWithContext(g.w.t.Context(), http.MethodPost, "https://api.github.com/"+
		strings.TrimPrefix(path, "/"), bytes.NewReader(body))
	require.NoError(g.w.t, err)
	req.Header.Set("Authorization", "Bearer "+agenttool.Placeholder)
	req.Header.Set("Content-Type", "application/json")
	if !g.plain {
		req.Header.Set("Accept-Encoding", "gzip")
	}

	type got struct {
		status int
		body   string
	}

	answered := make(chan got, 1)

	go func() {
		if err := req.Write(g.conn); err != nil {
			answered <- got{}

			return
		}

		resp, err := http.ReadResponse(g.read, req)
		if err != nil {
			answered <- got{}

			return
		}

		defer func() { _ = resp.Body.Close() }()

		var r io.Reader = resp.Body
		if resp.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(resp.Body)
			if err != nil {
				answered <- got{status: resp.StatusCode}

				return
			}

			r = zr
		}

		raw, _ := io.ReadAll(r)
		answered <- got{status: resp.StatusCode, body: string(raw)}
	}()

	for {
		select {
		case a := <-answered:
			return a.status, a.body
		case <-time.After(20 * time.Millisecond):
		}

		pending, err := g.w.svc.ListApprovals(g.w.t.Context(),
			orchestration.ApprovalFilter{Status: orchestration.ApprovalPending})
		require.NoError(g.w.t, err)

		for _, a := range pending {
			_, err := g.proxy.Decide(g.w.t.Context(), a.ID, true, orchestration.ScopeOnce)
			require.NoError(g.w.t, err)
		}
	}
}
