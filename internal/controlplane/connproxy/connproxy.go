// Package connproxy is the way a Connection's requests reach its service when the Connection names a proxy
// (issue #850): a service in a private network, reached through a local SOCKS5 or HTTP proxy such as a userspace
// Tailscale's. As Metabase's database details carry their own tunnel (src/metabase/driver.clj, :tunnel-enabled)
// and Grafana's data sources turn on its secure SOCKS proxy one by one (pkg/setting/setting_secure_socks_proxy.go),
// the proxy is a setting of one Connection, never of the server: a Connection without one goes direct.
package connproxy

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxProxyBytes bounds a proxy's address.
const maxProxyBytes = 256

// ErrInvalid is a proxy address that is not one.
var ErrInvalid = errors.New("a proxy is socks5://, socks5h://, http:// or https:// with a host and a port, " +
	"and nothing else")

// Parse reads a proxy's address as the owner types it: socks5, socks5h, http or https, a host and a port, with no
// user, path, query or fragment, so that no credential is ever kept in it. Empty is no proxy.
func Parse(given string) (string, error) {
	given = strings.TrimSpace(given)
	if given == "" {
		return "", nil
	}

	u, err := url.Parse(given)
	if err != nil || len(given) > maxProxyBytes || u.User != nil || u.Opaque != "" || u.RawQuery != "" ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Hostname() == "" || u.Port() == "" {
		return "", ErrInvalid
	}

	switch u.Scheme {
	case "socks5", "socks5h", "http", "https":
	default:
		return "", ErrInvalid
	}

	return u.Scheme + "://" + u.Host, nil
}

// maxDialBytes bounds a dial address.
const maxDialBytes = 256

// ErrInvalidDial is a dial address that is not one.
var ErrInvalidDial = errors.New("a dial address is a host or an IP, with a port or not, and nothing else")

// ParseDial reads the address a Connection's requests are dialed at instead of their host's (issue #850): a host
// name or an IP, with a port or without, as curl's --resolve names one. It is a temporary way round a name that
// does not resolve where the control plane runs; no reference BI tool has it. Empty is off.
func ParseDial(given string) (string, error) {
	given = strings.TrimSpace(given)
	if given == "" {
		return "", nil
	}

	if len(given) > maxDialBytes || strings.ContainsAny(given, "/@?# \t") {
		return "", ErrInvalidDial
	}

	host, port, err := net.SplitHostPort(given)
	if err != nil {
		// No port: a host or an IP alone, an IPv6 one in brackets or not.
		host, port = strings.TrimSuffix(strings.TrimPrefix(given, "["), "]"), ""
	}

	if host == "" || strings.Contains(host, "]") || strings.Contains(host, "[") ||
		(net.ParseIP(host) == nil && strings.Contains(host, ":")) {
		return "", ErrInvalidDial
	}

	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", ErrInvalidDial
		}

		return net.JoinHostPort(host, port), nil
	}

	if net.ParseIP(host) != nil && strings.Contains(host, ":") {
		return "[" + host + "]", nil
	}

	return host, nil
}

// Route is how a Connection's requests reach its service: through a proxy, at a dial address, both or neither.
type Route struct {
	// Proxy is read by Parse; empty goes direct.
	Proxy string
	// Dial is read by ParseDial; empty dials the request's own host.
	Dial string
}

// Transports are the transports of the routes Connections name, made when first asked for and kept, so that the
// connections they hold are reused.
type Transports struct {
	// Direct is the transport of a Connection without a route.
	Direct http.RoundTripper
	// New makes a transport like Direct; its Proxy and its TLS server name are set to the route's.
	New func() *http.Transport

	mu sync.Mutex
	by map[string]*http.Transport
}

// For is the transport a request of a Connection with this route is sent with: Direct without one. A route was
// read by Parse and ParseDial when it was kept; one that no longer reads is refused.
func (t *Transports) For(r Route) (http.RoundTripper, error) {
	proxy, err := Parse(r.Proxy)
	if err != nil {
		return nil, err
	}

	dial, err := ParseDial(r.Dial)
	if err != nil {
		return nil, err
	}

	switch {
	case proxy == "" && dial == "":
		return t.Direct, nil
	case dial == "":
		return t.transport(proxy, "")
	default:
		return redial{ways: t, proxy: proxy, dial: dial}, nil
	}
}

// transport is the kept transport through proxy (none when empty) whose TLS checks the certificate against name
// (the request's own host when empty).
func (t *Transports) transport(proxy, name string) (*http.Transport, error) {
	key := proxy + "|" + name

	t.mu.Lock()
	defer t.mu.Unlock()

	if made, ok := t.by[key]; ok {
		return made, nil
	}

	made := t.New()

	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, ErrInvalid
		}

		made.Proxy = http.ProxyURL(u)
	}

	if name != "" {
		// The certificate is checked against the name, whatever address was dialed; the roots stay New's.
		checked := &tls.Config{MinVersion: tls.VersionTLS12}
		if made.TLSClientConfig != nil {
			checked = made.TLSClientConfig.Clone()
		}

		checked.ServerName = name
		made.TLSClientConfig = checked
	}

	if t.by == nil {
		t.by = map[string]*http.Transport{}
	}

	t.by[key] = made

	return made, nil
}

// redial sends a request to its dial address, through its proxy when it has one, as curl's --resolve does: the
// connection (and a proxy's CONNECT or SOCKS5 target) is the dial address, while the Host header and the TLS
// server name stay the request's host, so the certificate is still checked against the real name.
type redial struct {
	ways  *Transports
	proxy string
	dial  string
}

func (d redial) RoundTrip(req *http.Request) (*http.Response, error) {
	name := req.URL.Hostname()

	way, err := d.ways.transport(d.proxy, name)
	if err != nil {
		return nil, err
	}

	port := req.URL.Port()
	if port == "" {
		port = "80"
		if req.URL.Scheme == "https" {
			port = "443"
		}
	}

	target := d.dial
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(strings.TrimSuffix(strings.TrimPrefix(target, "["), "]"), port)
	}

	out := req.Clone(req.Context())
	out.URL.Host = target

	if out.Host == "" {
		out.Host = req.URL.Host
	}

	return way.RoundTrip(out)
}

// Unreachable says why a service could not be reached through a proxy, apart from what its account may do: the
// host and the proxy are named, never the request.
func Unreachable(host, proxy string, err error) error {
	return fmt.Errorf("cannot reach %s through %s: %w", host, proxy, err)
}

// NewTransport is a plain transport as the control plane's clients make theirs.
func NewTransport(handshake time.Duration) *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: handshake,
	}
}
