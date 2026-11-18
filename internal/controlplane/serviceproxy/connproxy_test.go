package serviceproxy_test

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the owner's Connection names a proxy to its service (issue #850), and the proxy is down. A read
// through it is not sent direct: the tool is told the service cannot be reached through that proxy, and nothing
// reaches the service. Cleared, the Connection's reads go direct again.
func TestAConnectionsRequestsGoThroughItsProxyOrNowhere(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	down := "socks5://" + l.Addr().String()
	require.NoError(t, l.Close())

	_, err = w.svc.SetConnectionProxy(t.Context(), "google", "work", down)
	require.NoError(t, err)

	gmail := w.piTool("gmail.googleapis.com")

	got := gmail.do(http.MethodGet, "/gmail/v1/users/me/messages", "", nil)
	require.Equal(t, http.StatusBadGateway, got.Status)
	assert.Contains(t, got.message(), "cannot reach gmail.googleapis.com through "+down)
	assert.Empty(t, w.service.received(), "a request whose proxy is down is never sent direct")

	_, err = w.svc.SetConnectionProxy(t.Context(), "google", "work", "")
	require.NoError(t, err)

	got = w.piTool("gmail.googleapis.com").do(http.MethodGet, "/gmail/v1/users/me/messages", "", nil)
	require.Equal(t, http.StatusOK, got.Status)
	assert.Len(t, w.service.received(), 1)
}
