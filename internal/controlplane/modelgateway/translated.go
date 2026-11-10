package modelgateway

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// A built-in provider answers in its own event stream, which its adapter
// translates. translated is the part every such adapter shares: reading the
// events, deciding when the client can be promised an answer, and giving the
// answer as OpenAI's stream or as one completion.

// translation is one answer being translated; the adapter's package does the
// translating.
type translation struct {
	// event takes one event's data and returns the chat chunks it stands for.
	event func(data []byte) [][]byte
	// finished says the provider ended the answer; failed, that it reported
	// an error inside the stream.
	finished func() bool
	failed   func() bool
	// completion is the whole answer as one chat completion.
	completion func() []byte
	// refusal is the answer for a stream that failed or ended early before
	// anything was shown. It also does what the failure calls for, such as
	// cooling an account down.
	refusal func() *http.Response
	// release is called once the answer is over, however it ends; may be nil.
	release func()
}

// translated turns a provider's event stream into the answer. Nothing is
// promised to the client before the provider has sent something to show: an
// error that comes first becomes an error status, so the model's next target
// can still be tried.
func (s *Server) translated(ctx context.Context, call chatCall, resp *http.Response, t translation) *http.Response {
	release := func() {
		if t.release != nil {
			t.release()
		}
	}

	events := bufio.NewScanner(resp.Body)
	events.Buffer(make([]byte, 0, 64<<10), maxEventBytes)

	// next reads events until one gives chunks, or the stream ends.
	next := func() [][]byte {
		for events.Scan() {
			data, isData := bytes.CutPrefix(events.Bytes(), []byte("data:"))
			if !isData {
				continue
			}

			if chunks := t.event(bytes.TrimSpace(data)); len(chunks) > 0 || t.failed() {
				return chunks
			}
		}

		return nil
	}

	fail := func() *http.Response {
		_ = resp.Body.Close()

		release()

		return t.refusal()
	}

	if !call.req.stream {
		for !t.finished() && !t.failed() {
			if chunks := next(); len(chunks) == 0 && !t.failed() {
				break // The provider closed the stream.
			}
		}

		if !t.finished() {
			return fail()
		}

		_ = resp.Body.Close()

		release()

		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(bytes.NewReader(t.completion())),
		}
	}

	first := next()
	if len(first) == 0 {
		return fail()
	}

	reader, writer := io.Pipe()

	go func() {
		defer release()

		chunks := first

		for len(chunks) > 0 {
			for _, chunk := range chunks {
				if _, err := fmt.Fprintf(writer, "data: %s\n\n", chunk); err != nil {
					return // The reader closed: the client left or the stream sat idle.
				}
			}

			if t.finished() {
				_, _ = io.WriteString(writer, "data: [DONE]\n\n")

				break
			}

			chunks = next()
		}

		if t.failed() {
			// The refusal is not sent, a 200 being on its way already, but
			// what the failure calls for is still done.
			_ = t.refusal().Body.Close()

			s.logger.WarnContext(ctx, "a provider failed inside its answer",
				slog.String("event", "modelgateway.stream_failed"), slog.String("provider", call.upstream.Provider))
		}

		// With no end mark written, the gateway tells the client the answer
		// was cut, as for any provider.
		_ = writer.Close()
	}()

	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: &pipedBody{PipeReader: reader, upstream: resp.Body},
	}
}

// pipedBody is a translated stream: closing it also ends the provider's.
type pipedBody struct {
	*io.PipeReader

	upstream io.Closer
}

func (b *pipedBody) Close() error {
	return errors.Join(b.PipeReader.Close(), b.upstream.Close())
}
