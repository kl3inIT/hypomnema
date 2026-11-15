package device

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// One scrcpy session on the phone, as Orca's ScrcpyStreamSession
// (src/main/emulator/android/scrcpy-stream-session.ts, MIT) runs it: the
// server pushed, an adb forward from a local port to its abstract socket,
// the server started, then the video socket, retried until the server's
// dummy byte comes, and the control socket after it. Ending it ends the
// server's adb, and removes the forward.

const (
	// dialTries and dialEvery bound the wait for the server's socket (Orca: 100 tries 100 ms apart).
	dialTries = 100
	dialEvery = 100 * time.Millisecond
	// readyWithin is how long a connection may stay silent before it is tried again (Orca: 2 s).
	readyWithin = 2 * time.Second
	// preambleWithin is how long the server has to say the device and the codec once both sockets are open: it
	// starts the phone's encoder first, slow on an emulator without a GPU (inference: no reference bounds it).
	preambleWithin = 10 * time.Second
	scrcpyLog      = "scrcpy.log"
)

// screenStream is a running stream of the phone's screen.
type screenStream interface {
	// Size is the video's size at its start.
	Size() screenSize
	// Read returns the next packet.
	Read() (packet, error)
	// Control writes a control message.
	Control(message []byte) error
	// Close ends the stream.
	Close()
}

// scrcpyStream is the stream of a scrcpy server on the phone.
type scrcpyStream struct {
	m       *Manager
	port    int
	server  Process
	video   net.Conn
	reader  *bufio.Reader
	control net.Conn
	size    screenSize

	writing sync.Mutex
	closing sync.Once
}

// openScrcpy starts a scrcpy server on the running phone and connects to it.
func (m *Manager) openScrcpy(ctx context.Context) (screenStream, error) {
	out, err := m.adb(ctx, m.opts.EndWithin, "-s", serial, "push", m.path(ScrcpyServerPath), scrcpyJar)
	if err != nil || out.Code != 0 {
		return nil, fmt.Errorf("pushing scrcpy-server to the phone: %s", why(err, out))
	}

	scid := newScid()

	out, err = m.adb(ctx, m.opts.EndWithin, "-s", serial, "forward", "tcp:0", "localabstract:scrcpy_"+scid)
	if err != nil || out.Code != 0 {
		return nil, fmt.Errorf("forwarding scrcpy's socket: %s", why(err, out))
	}

	port, err := strconv.Atoi(strings.TrimSpace(out.Stdout))
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("adb gave no port for scrcpy's socket: %q", out.Stdout)
	}

	s := &scrcpyStream{m: m, port: port}

	s.server, err = m.opts.System.Start(Command{
		Path: m.path(adbPath), Args: scrcpyServerArgs(scid), Env: os.Environ(), Log: m.path(runDir, scrcpyLog),
	})
	if err != nil {
		s.shut(ctx)

		return nil, fmt.Errorf("starting scrcpy-server: %w", err)
	}

	if err := s.connect(ctx); err != nil {
		s.shut(ctx)

		return nil, fmt.Errorf("%w: %s", err, m.tail(scrcpyLog))
	}

	return s, nil
}

// connect opens the video socket once the server took it, reads its start,
// and opens the control socket.
func (s *scrcpyStream) connect(ctx context.Context) error {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(s.port))

	var dialer net.Dialer

	for try := 0; ; try++ {
		if try >= dialTries {
			return errors.New("scrcpy's video stream did not start")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.server.Done():
			return errors.New("scrcpy-server ended before its video stream started")
		default:
		}

		if try > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(dialEvery):
			}
		}

		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			continue
		}

		// adb takes the connection before the server is there; the server's dummy byte says it is.
		_ = conn.SetReadDeadline(time.Now().Add(readyWithin))

		reader := bufio.NewReaderSize(conn, 1<<20)
		if _, err := reader.ReadByte(); err != nil {
			_ = conn.Close()

			continue
		}

		s.video, s.reader = conn, reader

		break
	}

	control, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connecting scrcpy's control socket: %w", err)
	}

	// Touches are small; Nagle would hold them back (Orca's setNoDelay).
	if tcp, ok := control.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}

	s.control = control

	// What the phone sends back on the control socket (its clipboard) is not used; it is drained (serve-emu).
	go func() { _, _ = io.Copy(io.Discard, control) }()

	// The start comes once both sockets are open; a server that says nothing is given up, and so is the wait when
	// ctx ends. The deadline is lifted after it: a phone whose screen does not change sends no frames.
	_ = s.video.SetReadDeadline(time.Now().Add(preambleWithin))

	stop := context.AfterFunc(ctx, func() { _ = s.video.SetReadDeadline(time.Now()) })
	defer stop()

	if s.size, err = readPreamble(s.reader); err != nil {
		return err
	}

	if !stop() {
		return ctx.Err()
	}

	return s.video.SetReadDeadline(time.Time{})
}

func (s *scrcpyStream) Size() screenSize { return s.size }

func (s *scrcpyStream) Read() (packet, error) { return readPacket(s.reader) }

func (s *scrcpyStream) Control(message []byte) error {
	s.writing.Lock()
	defer s.writing.Unlock()

	if s.control == nil {
		return errors.New("scrcpy's control socket is not open")
	}

	_ = s.control.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := s.control.Write(message)

	return err
}

func (s *scrcpyStream) Close() { s.shut(context.Background()) }

// shut ends the stream: its sockets, the server's adb, and the forward.
func (s *scrcpyStream) shut(ctx context.Context) {
	s.closing.Do(func() {
		if s.video != nil {
			_ = s.video.Close()
		}

		if s.control != nil {
			_ = s.control.Close()
		}

		if s.server != nil {
			_ = s.m.opts.System.Signal(s.server.Pid(), Terminate)
		}

		forward := "tcp:" + strconv.Itoa(s.port)
		_, _ = s.m.adb(context.WithoutCancel(ctx), s.m.opts.EndWithin, "-s", serial, "forward", "--remove", forward)
	})
}
