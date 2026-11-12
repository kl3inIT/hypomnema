package egress_test

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser/egress"
)

// The replies of RFC 1928 a client of the gate sees.
const (
	replySucceeded         = 0
	replyNotAllowed        = 2
	replyConnectionRefused = 5
	replyUnsupported       = 7
)

// listen is a server on loopback, as a service on the control plane's machine
// is: it answers each connection with what it was sent. It returns its port
// and how many connections reached it.
func listen(t *testing.T) (uint16, func() int) {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	var (
		mu      sync.Mutex
		reached int
	)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			mu.Lock()
			reached++
			mu.Unlock()

			go func() {
				defer conn.Close() //nolint:errcheck // A test's connection.

				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	//nolint:forcetypeassert,gosec // A TCP listener's port.
	return uint16(listener.Addr().(*net.TCPAddr).Port), func() int {
		mu.Lock()
		defer mu.Unlock()

		return reached
	}
}

type refusal struct {
	host   string
	port   uint16
	reason egress.Refusal
}

// open is a gate with the owner's list, and what it refused.
func open(t *testing.T, allow ...string) (*egress.Gate, func() []refusal) {
	t.Helper()

	list, err := egress.ParseAllow(allow)
	require.NoError(t, err)

	var (
		mu      sync.Mutex
		refused []refusal
	)

	gate, err := egress.Open(egress.Rules{Allow: list}, func(host string, port uint16, reason egress.Refusal) {
		mu.Lock()
		defer mu.Unlock()

		refused = append(refused, refusal{host, port, reason})
	})
	require.NoError(t, err)
	t.Cleanup(gate.Close)

	return gate, func() []refusal {
		mu.Lock()
		defer mu.Unlock()

		return refused
	}
}

// greet connects to the gate as Chrome does and offers the methods.
func greet(t *testing.T, gate *egress.Gate, methods ...byte) (net.Conn, []byte) {
	t.Helper()

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", "127.0.0.1:"+strconv.Itoa(int(gate.Port())))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	_, err = conn.Write(append([]byte{5, byte(len(methods))}, methods...)) //nolint:gosec // A few methods.
	require.NoError(t, err)

	answer := make([]byte, 2)
	_, err = io.ReadFull(conn, answer)
	require.NoError(t, err)

	return conn, answer
}

// ask sends a request for a name and returns the connection and the gate's
// reply code.
func ask(t *testing.T, gate *egress.Gate, command byte, host string, port uint16) (net.Conn, byte) {
	t.Helper()

	conn, answer := greet(t, gate, 0)
	require.Equal(t, []byte{5, 0}, answer)

	request := append([]byte{5, command, 0, 3, byte(len(host))}, host...) //nolint:gosec // A short name.
	request = binary.BigEndian.AppendUint16(request, port)

	_, err := conn.Write(request)
	require.NoError(t, err)

	reply := make([]byte, 10)
	_, err = io.ReadFull(conn, reply)
	require.NoError(t, err)
	require.Equal(t, byte(5), reply[0])

	return conn, reply[1]
}

// A destination the owner listed is connected to, by name or by address, and
// bytes pass both ways.
func TestGateCarriesAnAllowedConnection(t *testing.T) {
	port, reached := listen(t)
	gate, refused := open(t, "127.0.0.1:"+strconv.Itoa(int(port)))

	for _, host := range []string{"localhost", "127.0.0.1", "app.localhost"} {
		conn, code := ask(t, gate, 1, host, port)
		require.Equal(t, byte(replySucceeded), code, host)

		_, err := conn.Write([]byte("ping"))
		require.NoError(t, err)

		echoed := make([]byte, 4)
		_, err = io.ReadFull(conn, echoed)
		require.NoError(t, err)
		assert.Equal(t, "ping", string(echoed))
	}

	assert.Equal(t, 3, reached())
	assert.Empty(t, refused())
}

// A service on the browser's own machine that the owner did not list is never
// connected to, and the refusal is reported with its reason.
func TestGateRefusesAServiceOnItsOwnMachine(t *testing.T) {
	port, reached := listen(t)
	gate, refused := open(t)

	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		_, code := ask(t, gate, 1, host, port)
		assert.Equal(t, byte(replyNotAllowed), code, host)
	}

	_, code := ask(t, gate, 1, "169.254.169.254", 80)
	assert.Equal(t, byte(replyNotAllowed), code)

	assert.Zero(t, reached())
	assert.Equal(t, []refusal{
		{"localhost", port, egress.ThisComputer},
		{"127.0.0.1", port, egress.ThisComputer},
		{"::1", port, egress.ThisComputer},
		{"169.254.169.254", 80, egress.Metadata},
	}, refused())
}

// The gate only opens connections: a client asking it to listen is refused,
// and so is one that will not go without a password.
func TestGateTakesOnlyConnect(t *testing.T) {
	port, reached := listen(t)
	gate, _ := open(t, "127.0.0.1")

	const bind = 2

	_, code := ask(t, gate, bind, "127.0.0.1", port)
	assert.Equal(t, byte(replyUnsupported), code)
	assert.Zero(t, reached())

	const password = 2

	_, answer := greet(t, gate, password)
	assert.Equal(t, []byte{5, 0xff}, answer)
}

// An allowed destination with nothing listening is told so, as a connection
// without the gate would be.
func TestGateReportsADestinationThatDoesNotAnswer(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := uint16(listener.Addr().(*net.TCPAddr).Port) //nolint:forcetypeassert,gosec // A TCP listener's port.
	require.NoError(t, listener.Close())

	gate, _ := open(t, "127.0.0.1")

	_, code := ask(t, gate, 1, "127.0.0.1", port)
	assert.Equal(t, byte(replyConnectionRefused), code)
}

// A closed gate ends the connections through it and takes no more: a browser
// whose gate stopped connects nowhere.
func TestClosedGateConnectsNowhere(t *testing.T) {
	port, _ := listen(t)
	gate, _ := open(t, "127.0.0.1")

	conn, code := ask(t, gate, 1, "127.0.0.1", port)
	require.Equal(t, byte(replySucceeded), code)

	gate.Close()

	_, err := io.ReadFull(conn, make([]byte, 1))
	require.Error(t, err, "the connection through the gate ended with it")

	_, err = (&net.Dialer{Timeout: 2 * time.Second}).DialContext(t.Context(), "tcp",
		"127.0.0.1:"+strconv.Itoa(int(gate.Port())))
	require.Error(t, err)
}
