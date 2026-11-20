package modelgateway_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// sendInPieces sends a request as a proxy that passes a body on as it comes does (nginx's proxy_request_buffering
// off) over a slow link: its head at once, then its body in four pieces, a pause before each, and reads the answer.
func sendInPieces(t *testing.T, addr, head string, body []byte, pause time.Duration) (*http.Response, error) {
	t.Helper()

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := fmt.Fprintf(conn, "%sContent-Length: %d\r\n\r\n", head, len(body)); err != nil {
		return nil, err
	}

	piece := len(body)/4 + 1
	for len(body) > 0 {
		time.Sleep(pause)

		n := min(piece, len(body))
		if _, err := conn.Write(body[:n]); err != nil {
			return nil, err
		}

		body = body[n:]
	}

	return http.ReadResponse(bufio.NewReader(conn), nil)
}

// Scenario (issue #977): the gateway is served with the control plane's read timeout, shortened here, behind a
// proxy that passes the body on as it comes; a long conversation whose body takes longer than that timeout to
// arrive is read whole on each route a model is asked on, not answered "the request body could not be read".
func TestAConversationSlowerThanTheReadTimeoutIsRead(t *testing.T) {
	t.Parallel()

	upstream, _ := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, upstream)

	hs := httptest.NewUnstartedServer(g.mux)
	hs.Config.ReadTimeout = 300 * time.Millisecond
	hs.Start()
	t.Cleanup(hs.Close)

	addr := strings.TrimPrefix(hs.URL, "http://")
	conversation := strings.Repeat("a long conversation ", 1000)

	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens"} {
		body, err := json.Marshal(map[string]any{
			"model": "gpt", "max_tokens": 10,
			"messages": []map[string]string{{"role": "user", "content": conversation}},
			"input":    conversation,
		})
		require.NoError(t, err)

		head := "POST " + path + " HTTP/1.1\r\nHost: " + addr + "\r\nAuthorization: Bearer " + g.key +
			"\r\nContent-Type: application/json\r\n"

		// Four pieces, 200 ms apart: 800 ms, past the read timeout.
		resp, err := sendInPieces(t, addr, head, body, 200*time.Millisecond)
		require.NoError(t, err, path)

		answered, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		assert.NotContains(t, string(answered), "could not be read", path)

		if path == "/v1/chat/completions" {
			assert.Equal(t, http.StatusOK, resp.StatusCode, string(answered))
		}
	}
}

// failingBody gives some bytes, then the error a connection's read gives.
type failingBody struct {
	given bool
}

func (b *failingBody) Read(p []byte) (int, error) {
	if b.given {
		return 0, errors.New("read tcp 127.0.0.1:18991->127.0.0.1:54804: i/o timeout")
	}

	b.given = true

	return copy(p, `{"model":"gpt","messages":[`), nil
}

// Scenario (issue #977): a request's body that cannot be read is still answered 400, and the gateway logs why,
// with what tells a proxy's failure from a client's: the key, how the body was framed, how much of it came.
func TestABodyThatCannotBeReadIsLoggedWithWhatCame(t *testing.T) {
	t.Parallel()

	upstream, _ := provider(t, plain(http.StatusOK, answer))
	g := newChatGateway(t, modelgateway.Config{}, upstream)

	var logs bytes.Buffer

	mux := http.NewServeMux()
	modelgateway.NewServer(g.svc, platform.NewLogger(&logs, platform.LogConfig{}, "test"), modelgateway.Config{}).
		Register(mux)

	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		logs.Reset()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, &failingBody{})
		req.Header.Set("Authorization", "Bearer "+g.key)
		req.Header.Set("Content-Length", "2000078")
		req.Header.Set("Content-Encoding", "zstd")

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code, path)
		assert.Contains(t, rec.Body.String(), "the request body could not be read", path)

		var line map[string]any
		require.NoError(t, json.Unmarshal(logs.Bytes(), &line), logs.String())
		assert.Equal(t, "modelgateway.body_unread", line["event"], path)
		assert.Equal(t, path, line["path"])
		assert.Equal(t, "pi", line["key_name"], path)
		assert.NotEmpty(t, line["key_id"], path)
		assert.Equal(t, "2000078", line["content_length"], path)
		assert.Equal(t, "zstd", line["content_encoding"], path)
		assert.InDelta(t, len(`{"model":"gpt","messages":[`), line["bytes_read"], 0, path)
		assert.Contains(t, line["error"], "i/o timeout", path)
		assert.Contains(t, line, "elapsed_ms", path)
	}
}
