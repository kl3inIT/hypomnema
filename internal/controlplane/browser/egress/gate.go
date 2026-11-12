package egress

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// SOCKS5 (RFC 1928) replies.
const (
	succeeded         = 0
	notAllowed        = 2
	hostUnreachable   = 4
	connectionRefused = 5
	unsupported       = 7
)

const (
	socksVersion  = 5
	noAuth        = 0
	noMethod      = 0xff
	cmdConnect    = 1
	typeIPv4      = 1
	typeName      = 3
	typeIPv6      = 4
	handshakeTime = 30 * time.Second
	dialTime      = 30 * time.Second
	// stagger is how long one address is tried before the next one is tried beside it, so that a family that
	// cannot route does not hold up one that can.
	stagger = 250 * time.Millisecond
)

// Gate is a SOCKS5 server on loopback that the browser sends every connection
// through. It resolves names itself and connects only to addresses its rules
// allow, so neither redirects nor DNS rebinding reach anything else, and it
// carries bytes only: TLS, HTTP/2 and WebSockets pass through unchanged.
type Gate struct {
	rules     Rules
	onRefusal func(host string, port uint16, reason Refusal)
	listener  net.Listener
	stop      context.CancelFunc

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	done  sync.WaitGroup
}

// Open starts a gate on a loopback port the system picks. onRefusal is told
// each connection the rules refuse.
func Open(rules Rules, onRefusal func(host string, port uint16, reason Refusal)) (*Gate, error) {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("the browser's network gate: %w", err)
	}

	ctx, stop := context.WithCancel(context.Background())
	g := &Gate{rules: rules, onRefusal: onRefusal, listener: listener, stop: stop, conns: map[net.Conn]struct{}{}}

	g.done.Go(func() { g.accept(ctx) })

	return g, nil
}

// Port is the loopback port the gate listens on.
func (g *Gate) Port() uint16 {
	//nolint:forcetypeassert // A TCP listener's address.
	return uint16(g.listener.Addr().(*net.TCPAddr).Port) //nolint:gosec // A port.
}

// ChromeSettings is how a whole Chrome goes through the gate: its proxy, what
// bypasses it, and the arguments that go with them.
func (g *Gate) ChromeSettings() (proxy, bypass string, args []string) {
	return "socks5://127.0.0.1:" + strconv.Itoa(int(g.Port())),
		// Chrome otherwise connects to loopback directly.
		"<-loopback>",
		[]string{
			// WebRTC would otherwise send UDP around the proxy. Chrome reads the first switch and
			// chrome-headless-shell the second; each ignores the other.
			"--webrtc-ip-handling-policy=disable_non_proxied_udp",
			"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
			// Chrome never asks the desktop keyring.
			"--password-store=basic",
		}
}

// Close stops the gate and every connection through it. A closed gate fails
// every connection.
func (g *Gate) Close() {
	g.stop()
	_ = g.listener.Close()

	g.mu.Lock()
	for conn := range g.conns {
		_ = conn.Close()
	}

	g.conns = nil
	g.mu.Unlock()

	g.done.Wait()
}

func (g *Gate) accept(ctx context.Context) {
	for {
		client, err := g.listener.Accept()
		if err != nil {
			return
		}

		if !g.track(client) {
			return
		}

		g.done.Go(func() {
			defer g.drop(client)

			g.serve(ctx, client)
		})
	}
}

// track remembers a connection so that Close ends it; it reports false, with
// the connection closed, once the gate is closing.
func (g *Gate) track(conn net.Conn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.conns == nil {
		_ = conn.Close()

		return false
	}

	g.conns[conn] = struct{}{}

	return true
}

func (g *Gate) drop(conn net.Conn) {
	_ = conn.Close()

	g.mu.Lock()
	delete(g.conns, conn)
	g.mu.Unlock()
}

func reply(client net.Conn, code byte) {
	_, _ = client.Write([]byte{socksVersion, code, 0, typeIPv4, 0, 0, 0, 0, 0, 0})
}

// serve carries one connection of the browser's: it reads where to, checks
// it, connects, and then passes bytes both ways until either side ends.
func (g *Gate) serve(ctx context.Context, client net.Conn) {
	_ = client.SetDeadline(time.Now().Add(handshakeTime))

	host, port, code := handshake(client)
	if code != succeeded {
		if code != noMethod {
			reply(client, code)
		}

		return
	}

	addresses := resolve(ctx, host)
	if len(addresses) == 0 {
		reply(client, hostUnreachable)

		return
	}

	if reason := g.rules.Refusal(port, addresses, OwnAddresses()); reason != Allowed {
		if g.onRefusal != nil {
			g.onRefusal(host, port, reason)
		}

		reply(client, notAllowed)

		return
	}

	// Exactly the checked addresses, so a name that now resolves elsewhere changes nothing.
	upstream, err := dial(ctx, addresses, port)
	if err != nil {
		reply(client, connectionRefused)

		return
	}

	if !g.track(upstream) {
		return
	}

	defer g.drop(upstream)

	_ = client.SetDeadline(time.Time{})

	reply(client, succeeded)

	// Either side ending ends both.
	ended := make(chan struct{}, 2)

	for _, pipe := range [][2]net.Conn{{upstream, client}, {client, upstream}} {
		go func() {
			_, _ = io.Copy(pipe[0], pipe[1])
			ended <- struct{}{}
		}()
	}

	<-ended

	_ = client.Close()
	_ = upstream.Close()

	<-ended
}

// handshake reads a client's greeting and its request, and returns the
// CONNECT target, or the reply the client is refused with; noMethod has been
// answered already.
func handshake(client net.Conn) (string, uint16, byte) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(client, head); err != nil {
		return "", 0, noMethod
	}

	methods := make([]byte, head[1])
	if _, err := io.ReadFull(client, methods); err != nil {
		return "", 0, noMethod
	}

	// Version 5, offering "no authentication".
	offered := false
	for _, method := range methods {
		offered = offered || (head[0] == socksVersion && method == noAuth)
	}

	if !offered {
		_, _ = client.Write([]byte{socksVersion, noMethod})

		return "", 0, noMethod
	}

	if _, err := client.Write([]byte{socksVersion, noAuth}); err != nil {
		return "", 0, noMethod
	}

	// Version, command, a reserved zero, and the address type.
	request := make([]byte, 4)
	if _, err := io.ReadFull(client, request); err != nil {
		return "", 0, noMethod
	}

	if request[0] != socksVersion || request[2] != 0 {
		return "", 0, unsupported
	}

	var size int

	switch request[3] {
	case typeIPv4:
		size = 4
	case typeIPv6:
		size = 16
	case typeName:
		length := make([]byte, 1)
		if _, err := io.ReadFull(client, length); err != nil {
			return "", 0, noMethod
		}

		size = int(length[0])
	default:
		return "", 0, unsupported
	}

	target := make([]byte, size+2)
	if _, err := io.ReadFull(client, target); err != nil {
		return "", 0, noMethod
	}

	port := binary.BigEndian.Uint16(target[size:])
	if request[1] != cmdConnect || port == 0 {
		return "", 0, unsupported
	}

	if request[3] == typeName {
		return string(target[:size]), port, succeeded
	}

	address, _ := netip.AddrFromSlice(target[:size])

	return address.String(), port, succeeded
}

// resolve is the addresses of a host. Chrome sends names, address literals
// included, for the proxy to resolve.
func resolve(ctx context.Context, host string) []netip.Addr {
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address.WithZone("")}
	}

	if namesThisMachine(host) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()}
	}

	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil
	}

	return addresses
}

// dial connects to the first of addresses that answers: each gets its own
// attempt, attempts start staggered, and a quick failure moves straight on to
// the next. It fails with the last attempt's error when every one fails.
func dial(ctx context.Context, addresses []netip.Addr, port uint16) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTime)
	defer cancel()

	type attempt struct {
		conn net.Conn
		err  error
	}

	attempts := make(chan attempt, len(addresses))
	start := func(address netip.Addr) {
		go func() {
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", netip.AddrPortFrom(address, port).String())
			attempts <- attempt{conn: conn, err: err}
		}()
	}

	start(addresses[0])

	next, pending := 1, 1
	last := errors.New("no address")

	timer := time.NewTimer(stagger)
	defer timer.Stop()

	for pending > 0 {
		select {
		case <-timer.C:
			if next < len(addresses) {
				start(addresses[next])
				next++
				pending++

				timer.Reset(stagger)
			}
		case a := <-attempts:
			pending--

			if a.err == nil {
				// The attempts still dialing are canceled; one that connected meanwhile is closed.
				cancel()

				go func() {
					for ; pending > 0; pending-- {
						if other := <-attempts; other.conn != nil {
							_ = other.conn.Close()
						}
					}
				}()

				return a.conn, nil
			}

			last = a.err

			if next < len(addresses) {
				start(addresses[next])
				next++
				pending++

				timer.Reset(stagger)
			}
		}
	}

	return nil, last
}
