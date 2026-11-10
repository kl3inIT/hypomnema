package node

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: a git host that answers is reached, whatever it answers and even
// when this machine does not trust its certificate; a host nothing listens on
// is not.
func TestReachHost(t *testing.T) {
	t.Parallel()

	// httptest's certificate is signed by no authority this machine trusts.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	assert.True(t, ReachHost(t.Context(), strings.TrimPrefix(srv.URL, "https://")),
		"the host answered: git says why it fails there")

	// A port that was just closed: nothing listens on it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	gone := l.Addr().String()
	require.NoError(t, l.Close())

	assert.False(t, ReachHost(t.Context(), gone))
	assert.False(t, ReachHost(t.Context(), "not a host"))
}
