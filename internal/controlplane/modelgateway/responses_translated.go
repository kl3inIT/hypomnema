package modelgateway

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway/responseschat"
)

// responsesAsChat asks a target served on chat with a Responses request (issue #1144): the request translated to
// chat (package responseschat) is sent through the target's chat adapter, as the chat route sends it, and a 200's
// body becomes the Responses event stream relayResponses passes on. A refusal comes back as the provider sent it. A
// body that cannot be translated, the client's to fix, is a *badMessagesError.
func (s *Server) responsesAsChat(
	ctx context.Context, raw []byte, a responsesAttempt, call chatCall,
) (*http.Response, error) {
	built, err := responseschat.ChatFromResponses(raw)
	if err != nil {
		return nil, &badMessagesError{err: err}
	}

	req, err := parseChatRequest(built.Body)
	if err != nil {
		return nil, &badMessagesError{err: err}
	}

	call.req, call.upstream = req, a.upstream

	resp, err := a.adapter.do(ctx, s, call)
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}

	stream := responseschat.NewStream(s.svc.newID("resp"), s.svc.clock.Now().Unix(), call.model, built.Tools)
	resp.Body = newChatAsResponses(resp, stream)
	resp.Header = resp.Header.Clone()
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1

	return resp, nil
}

// chatAsResponses reads a chat answer as the Responses events it translates to: a stream event by event, a whole
// answer at once. A read error of the provider's stream (an idle stream closed by watchStreamIdle) is the reader's,
// so the relay records why it ended.
type chatAsResponses struct {
	src     io.ReadCloser
	scanner *bufio.Scanner
	stream  *responseschat.Stream
	whole   bool
	pending []byte
	err     error
}

func newChatAsResponses(resp *http.Response, stream *responseschat.Stream) *chatAsResponses {
	r := &chatAsResponses{
		src: resp.Body, stream: stream,
		whole: !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"),
	}

	r.scanner = bufio.NewScanner(resp.Body)
	r.scanner.Buffer(make([]byte, 0, 64<<10), maxEventBytes)

	return r
}

func (r *chatAsResponses) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}

		r.fill()
	}

	n := copy(p, r.pending)
	r.pending = r.pending[n:]

	return n, nil
}

func (r *chatAsResponses) fill() {
	defer func() {
		if r.err == nil && r.stream.Ended() {
			// The answer's last event is out; nothing after it is passed on.
			r.err = io.EOF
		}
	}()

	if r.whole {
		body, err := io.ReadAll(io.LimitReader(r.src, maxResponseBytes))
		if err != nil {
			r.err = err

			return
		}

		r.pending, r.err = r.stream.Completion(body), io.EOF

		return
	}

	if r.scanner.Scan() {
		if payload, isData := bytes.CutPrefix(r.scanner.Bytes(), []byte("data:")); isData {
			r.pending = r.stream.Chunk(payload)
		}

		return
	}

	if err := r.scanner.Err(); err != nil {
		r.err = err

		return
	}

	r.pending, r.err = r.stream.Close(), io.EOF
}

func (r *chatAsResponses) Close() error { return r.src.Close() }
