package modelgateway

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/codex"
)

// A Codex stream passed through on /v1/responses (issue #1135) is held at its
// opening: the frames that say nothing has happened yet (codex.Opening) are
// kept back until the first one that is output, so a refusal the backend sends
// inside its 200 before any output (server_is_overloaded, CLIProxyAPI #6856)
// can still send the request to the next account instead of reaching the
// client. It is CLIProxyAPI's stream bootstrap buffering
// (internal/runtime/executor/codex_executor_stream.go, the buffering loop of
// ExecuteStream), with its bounds. The chat route needs no hold: it translates
// the stream and sends nothing before the first chunk (translated.go).

const (
	// openingMaxLines and openingMaxBytes bound what is held: CLIProxyAPI's
	// codexBootstrapMaxBufferedFrames (lines of the stream) and
	// codexBootstrapMaxBufferedBytes.
	openingMaxLines = 48
	openingMaxBytes = 1 << 20
	// openingMaxWait bounds how long the client waits for the stream's first
	// frame because of the hold, checked as frames arrive: CLIProxyAPI's
	// stream-bootstrap-timeout at the value its config.example.yaml advises
	// behind a reverse proxy with a read timeout, as the gateway is.
	openingMaxWait = 20 * time.Second
)

// errOpeningLineTooLong says one line alone is past what may be held.
var errOpeningLineTooLong = errors.New("an opening line is past the hold's bound")

// heldBody is a stream whose opening was held: the held bytes, then the rest.
type heldBody struct {
	io.Reader
	io.Closer
}

// holdCodexOpening reads the opening of a 200 stream. It returns the stream
// to pass on, whole, once output starts, the stream ends, or a bound is
// reached; or, when the backend refuses for lack of capacity before any
// output, that failure, with the stream closed.
func (s *Server) holdCodexOpening(resp *http.Response) (*http.Response, *codex.Failure) {
	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	started := s.svc.clock.Now()

	// The hold has the stream's idle bound, as the relay that follows does.
	idle := watchStreamIdle(resp.Body, s.idleTimeout)
	defer idle.timer.Stop()

	var (
		held       []byte
		lines      int
		moderation string
	)

	release := func() *http.Response {
		out := *resp
		out.Body = &heldBody{Reader: io.MultiReader(bytes.NewReader(held), reader), Closer: resp.Body}

		return &out
	}

	for lines < openingMaxLines && len(held) < openingMaxBytes && s.svc.clock.Now().Sub(started) < openingMaxWait {
		line, err := readOpeningLine(reader, openingMaxBytes-len(held))
		held = append(held, line...)

		if err != nil {
			// The end of the stream, an error reading it, or a line too long
			// to hold: what was read goes on as it came.
			return release(), nil
		}

		idle.reset()
		lines++

		payload, isData := bytes.CutPrefix(bytes.TrimRight(line, "\r\n"), []byte("data:"))
		if !isData {
			// An event name, a comment or the blank line between events.
			continue
		}

		payload = bytes.TrimSpace(payload)
		if codex.Opening(payload) {
			if flags := codex.ModerationFlags(payload); flags != "" {
				moderation = flags
			}

			continue
		}

		failure := codex.EventFailure(payload, s.svc.clock.Now())
		if failure == nil || !failure.Overloaded() {
			// Output, or a failure that is the request's and reaches the
			// client in its stream as before.
			return release(), nil
		}

		_ = resp.Body.Close()

		failure.Moderation = moderation
		if failure.Status != http.StatusTooManyRequests {
			// CLIProxyAPI newCodexBootstrapOverloadErr: an overload refused
			// inside a 200 is a 503.
			failure.Status = http.StatusServiceUnavailable
		}

		return nil, failure
	}

	return release(), nil
}

// readOpeningLine reads one line with its end, up to limit bytes.
func readOpeningLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte

	for {
		part, err := reader.ReadSlice('\n')
		line = append(line, part...)

		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err //nolint:wrapcheck // io.EOF is compared, not wrapped.
		}

		if len(line) > limit {
			return line, errOpeningLineTooLong
		}
	}
}
