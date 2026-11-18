// Package speechtest is a fake of Soniox's realtime WebSocket for the tests of the meetings recorded live (issue
// #820): it keeps what each stream was sent and answers what a test scripts.
package speechtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// Stream is what one stream of the fake was sent.
type Stream struct {
	Auth  string
	Start map[string]any
	// Audio is every binary frame's bytes, in order; Texts every text frame after the start ("" is the end).
	Audio []byte
	Texts []string
}

// Realtime is the fake. Answer is asked after each binary frame, with the stream's number from 0, the stream's start
// and the audio bytes it has had; it returns the messages to send and whether to drop the stream at once, as a
// provider that fails. End is asked when a stream is ended, for the messages sent before the finished one.
type Realtime struct {
	Answer func(stream int, start map[string]any, received int) (messages []string, drop bool)
	End    func(stream int, start map[string]any) []string

	mu      sync.Mutex
	streams []*Stream
}

// Streams returns a copy of what each stream was sent.
func (f *Realtime) Streams() []Stream {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]Stream, 0, len(f.streams))
	for _, s := range f.streams {
		c := *s
		c.Audio = append([]byte(nil), s.Audio...)
		c.Texts = append([]string(nil), s.Texts...)
		out = append(out, c)
	}

	return out
}

// Serve starts the fake and returns its WebSocket address.
func (f *Realtime) Serve(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)

	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func (f *Realtime) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	defer func() { _ = conn.CloseNow() }()

	conn.SetReadLimit(1 << 20)

	ctx := r.Context()

	_, data, err := conn.Read(ctx)
	if err != nil {
		return
	}

	stream := &Stream{Auth: r.Header.Get("Authorization")}
	_ = json.Unmarshal(data, &stream.Start)

	f.mu.Lock()
	n := len(f.streams)
	f.streams = append(f.streams, stream)
	f.mu.Unlock()

	send := func(messages []string) bool {
		for _, m := range messages {
			if conn.Write(ctx, websocket.MessageText, []byte(m)) != nil {
				return false
			}
		}

		return true
	}

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}

		if typ == websocket.MessageText {
			f.mu.Lock()
			stream.Texts = append(stream.Texts, string(data))
			f.mu.Unlock()

			if len(data) > 0 {
				continue
			}

			var tail []string
			if f.End != nil {
				tail = f.End(n, stream.Start)
			}

			send(append(tail, `{"tokens":[],"finished":true}`))
			_ = conn.Close(websocket.StatusNormalClosure, "")

			return
		}

		f.mu.Lock()
		stream.Audio = append(stream.Audio, data...)
		received := len(stream.Audio)
		f.mu.Unlock()

		if f.Answer == nil {
			continue
		}

		messages, drop := f.Answer(n, stream.Start, received)
		if !send(messages) {
			return
		}

		if drop {
			return
		}
	}
}

// Tokens is a message of final tokens, each {text, start_ms, end_ms, speaker}, followed by an endpoint when end.
func Tokens(final bool, end bool, tokens ...Token) string {
	out := make([]map[string]any, 0, len(tokens)+1)
	for _, t := range tokens {
		tk := map[string]any{"text": t.Text, "start_ms": t.Start, "end_ms": t.End, "is_final": final}
		if t.Speaker != "" {
			tk["speaker"] = t.Speaker
		}

		out = append(out, tk)
	}

	if end {
		out = append(out, map[string]any{"text": "<end>", "is_final": true})
	}

	body, err := json.Marshal(map[string]any{"tokens": out})
	if err != nil {
		panic(err)
	}

	return string(body)
}

// Token is one token of Tokens, its times in the stream's milliseconds.
type Token struct {
	Text       string
	Start, End int64
	Speaker    string
}
