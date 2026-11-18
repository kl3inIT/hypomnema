package connproxy_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/connproxy"
)

// Scenario: the owner types where a service is dialed while its name does not resolve (issue #850), as curl's
// --resolve. A host or an IP, with a port or not, is kept; a URL, a user or a path is refused. Empty is off.
func TestADialAddressIsAHostOrAnIP(t *testing.T) {
	t.Parallel()

	for given, kept := range map[string]string{
		"":                  "",
		" 100.64.0.7 ":      "100.64.0.7",
		"100.64.0.7:443":    "100.64.0.7:443",
		"superset.internal": "superset.internal",
		"[fd7a::7]:443":     "[fd7a::7]:443",
		"fd7a::7":           "[fd7a::7]",
	} {
		got, err := connproxy.ParseDial(given)
		require.NoError(t, err, given)
		assert.Equal(t, kept, got, given)
	}

	for _, refused := range []string{"https://100.64.0.7", "me@100.64.0.7", "100.64.0.7:0", "100.64.0.7:99999",
		"100.64.0.7/path", "a b"} {
		_, err := connproxy.ParseDial(refused)
		require.ErrorIs(t, err, connproxy.ErrInvalidDial, refused)
	}
}

// servedAs is a TLS server whose certificate names example.com, as httptest's does, and that records the Host it
// was asked for.
func servedAs(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()

	var (
		mu   sync.Mutex
		host string
	)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		host = r.Host
		mu.Unlock()

		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)

	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()

		return host
	}
}

// Scenario: a service's name does not resolve where the control plane runs, and the owner gives the address to
// dial (issue #850). The request is dialed there, while the Host header is the name and the certificate is checked
// against the name: a name the certificate does not hold is refused, so the dial address cannot stand in for
// another service.
func TestADialAddressKeepsTheNameAndItsCertificate(t *testing.T) {
	t.Parallel()

	srv, asked := servedAs(t)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)

	ways := &connproxy.Transports{
		Direct: http.DefaultTransport,
		New: func() *http.Transport {
			return srv.Client().Transport.(*http.Transport).Clone() //nolint:forcetypeassert // httptest's own.
		},
	}

	way, err := ways.For(connproxy.Route{Dial: "127.0.0.1"})
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, get(t, way, "https://example.com:"+port+"/health"))
	assert.Equal(t, "example.com:"+port, asked(), "the Host header is the name, not the address dialed")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://not-the-service.test:"+port+"/", nil)
	require.NoError(t, err)

	_, err = way.RoundTrip(req) //nolint:bodyclose // It fails.
	require.Error(t, err, "the certificate is checked against the name, whatever address was dialed")
}

// Scenario: the same, through a proxy (issue #850): the proxy is asked for the dial address, never the name it
// could not resolve either, and the service still sees the name and shows its certificate for it.
func TestADialAddressGoesThroughTheProxyAsItsTarget(t *testing.T) {
	t.Parallel()

	srv, asked := servedAs(t)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)

	proxy, targets := connectProxy(t)

	ways := &connproxy.Transports{
		Direct: http.DefaultTransport,
		New: func() *http.Transport {
			return srv.Client().Transport.(*http.Transport).Clone() //nolint:forcetypeassert // httptest's own.
		},
	}

	way, err := ways.For(connproxy.Route{Proxy: proxy, Dial: "127.0.0.1:" + port})
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, get(t, way, "https://example.com/health"))
	assert.Equal(t, []string{"127.0.0.1:" + port}, targets())
	assert.Equal(t, "example.com", asked())
}

// get sends a GET through way and returns its status.
func get(t *testing.T, way http.RoundTripper, address string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
	require.NoError(t, err)

	resp, err := way.RoundTrip(req)
	require.NoError(t, err)

	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())

	return resp.StatusCode
}

// connectProxy is an HTTP proxy that tunnels CONNECT, as a local proxy to a private network does, and records the
// target each tunnel was asked for.
func connectProxy(t *testing.T) (string, func() []string) {
	t.Helper()

	var (
		mu      sync.Mutex
		targets []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)

			return
		}

		mu.Lock()
		targets = append(targets, r.Host)
		mu.Unlock()

		upstream, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)

			return
		}

		w.WriteHeader(http.StatusOK)

		client, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = upstream.Close()

			return
		}

		go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
		go func() { _, _ = io.Copy(client, upstream); _ = client.Close() }()
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	return "http://" + u.Host, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string(nil), targets...)
	}
}
