package integration_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset/supersettest"
)

// forwardProxy is a plain HTTP proxy, as a local proxy to a private network is: it sends each request on and
// counts them.
func forwardProxy(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var relayed atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayed.Add(1)

		// A proxy sends on to what it is asked for: here only the test's own Superset.
		out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body) //nolint:gosec // Above.
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)

			return
		}

		out.Header = r.Header.Clone()

		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)

			return
		}
		defer resp.Body.Close() //nolint:errcheck // A test's proxy.

		for k, v := range resp.Header {
			w.Header()[k] = v
		}

		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(server.Close)

	return server, &relayed
}

// closedAddress is an address nothing listens on.
func closedAddress(t *testing.T) string {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	address := l.Addr().String()
	require.NoError(t, l.Close())

	return address
}

// Scenario: the owner's Superset is in a private network, reached through a local proxy (issue #850). He
// connects it with the proxy: the sign-in and every later request go through it. When the proxy is down, a
// request says the service cannot be reached through it, and the Connection is not marked as needing a new
// sign-in: the password was not refused. Cleared, the Connection goes direct again. A proxy with a user, a path or
// another scheme is refused.
func TestASupersetInAPrivateNetworkIsReachedThroughItsProxy(t *testing.T) {
	t.Parallel()

	w, s := supersetWorld(t)
	proxy, relayed := forwardProxy(t)

	conn, err := w.svc.ConnectAccount(t.Context(), integration.SupersetID, "vmaps", integration.Account{
		Address: s.URL(), User: supersettest.User, Password: supersettest.Password, Proxy: proxy.URL,
	})
	require.NoError(t, err)
	assert.Equal(t, proxy.URL, conn.Proxy)
	assert.Positive(t, relayed.Load(), "the sign-in goes through the proxy")

	before := relayed.Load()
	answer, err := w.get("/api/v1/dashboard/9")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, answer.Status)
	assert.Greater(t, relayed.Load(), before, "a request goes through the proxy")

	down := "http://" + closedAddress(t)
	conn, err = w.svc.SetProxy(t.Context(), integration.SupersetID, "vmaps", down)
	require.NoError(t, err)
	assert.Equal(t, down, conn.Proxy)

	_, err = w.get("/api/v1/dashboard/9")
	require.ErrorIs(t, err, orchestration.ErrUnavailable)
	assert.Contains(t, err.Error(), "through "+down)
	assert.NotContains(t, err.Error(), supersettest.Password)

	kept, err := w.orch.ConnectionOf(t.Context(), integration.SupersetID, "vmaps")
	require.NoError(t, err)
	assert.False(t, kept.SignInFailed, "a proxy that is down is not a refused password")

	before = relayed.Load()
	_, err = w.svc.SetProxy(t.Context(), integration.SupersetID, "vmaps", "")
	require.NoError(t, err)

	answer, err = w.get("/api/v1/dashboard/9")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, answer.Status)
	assert.Equal(t, before, relayed.Load(), "cleared, the Connection goes direct")

	for _, bad := range []string{"http://user:secret@127.0.0.1:1080", "ftp://127.0.0.1:21", "socks5://127.0.0.1",
		"http://127.0.0.1:1080/path"} {
		_, err := w.svc.SetProxy(t.Context(), integration.SupersetID, "vmaps", bad)
		require.ErrorIs(t, err, orchestration.ErrInvalid, bad)
	}
}
