package connproxy_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/connproxy"
)

// Scenario: the owner types a proxy as he would anywhere (issue #850). A scheme of SOCKS5 or HTTP with a host and
// a port is kept as written; spaces and a trailing slash go; a user, a password, a path, a query, another scheme
// or no port are refused, so that no credential is kept with it. Empty is no proxy.
func TestAProxyIsAHostAndAPortOnly(t *testing.T) {
	t.Parallel()

	for given, kept := range map[string]string{
		"":                           "",
		" socks5://127.0.0.1:1055 ":  "socks5://127.0.0.1:1055",
		"socks5h://localhost:1055/":  "socks5h://localhost:1055",
		"http://[::1]:8080":          "http://[::1]:8080",
		"https://proxy.internal:443": "https://proxy.internal:443",
	} {
		got, err := connproxy.Parse(given)
		require.NoError(t, err, given)
		assert.Equal(t, kept, got, given)
	}

	for _, refused := range []string{
		"socks5://user:secret@127.0.0.1:1055", "socks5://127.0.0.1", "ftp://127.0.0.1:21", "127.0.0.1:1055",
		"http://127.0.0.1:8080/path", "http://127.0.0.1:8080?x=1", "http://127.0.0.1:8080#f",
	} {
		_, err := connproxy.Parse(refused)
		require.ErrorIs(t, err, connproxy.ErrInvalid, refused)
	}
}

// Scenario: two Connections name the same proxy and a third none. The two share one transport, so its connections
// are reused; the third gets the direct one.
func TestOneTransportServesEachProxy(t *testing.T) {
	t.Parallel()

	direct := http.DefaultTransport
	ways := &connproxy.Transports{Direct: direct, New: func() *http.Transport { return &http.Transport{} }}

	first, err := ways.For(connproxy.Route{Proxy: "socks5://127.0.0.1:1055"})
	require.NoError(t, err)

	second, err := ways.For(connproxy.Route{Proxy: "socks5://127.0.0.1:1055/"})
	require.NoError(t, err)
	assert.Same(t, first, second)

	other, err := ways.For(connproxy.Route{Proxy: ""})
	require.NoError(t, err)
	assert.Same(t, direct, other)

	_, err = ways.For(connproxy.Route{Proxy: "socks5://user:pw@127.0.0.1:1055"})
	require.ErrorIs(t, err, connproxy.ErrInvalid)
}
