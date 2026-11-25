package clientapi

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Scenario: a long chat's page of history goes gzipped to a browser that accepts it, five times smaller on a slow
// link (issue #1129), while the chat's stream stays plain and its frames arrive as they are written.
func TestAChatsPageGoesGzippedAndItsStreamStaysPlain(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	secret := bearerOf(t, w, "web")
	ctx := t.Context()

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	for range 20 {
		_, err := w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatSystem,
			"pi ran bash: go test ./internal/controlplane/... "+strings.Repeat("ok  package 0.1s\n", 20))
		require.NoError(t, err)
	}

	get := func(path, encoding string) *http.Response {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url+BasePath+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+secret)
		// Set by hand, so Go's transport neither asks for gzip itself nor undoes it.
		req.Header.Set("Accept-Encoding", encoding)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		return resp
	}

	page := "/chats/" + chat.ID + "/messages?before=9007199254740991&limit=200"

	plain := get(page, "identity")
	defer func() { _ = plain.Body.Close() }()

	assert.Empty(t, plain.Header.Get("Content-Encoding"), "nothing gzipped to a client that did not ask")
	plainBody, err := io.ReadAll(plain.Body)
	require.NoError(t, err)

	zipped := get(page, "gzip, deflate, br")
	defer func() { _ = zipped.Body.Close() }()

	assert.Equal(t, "gzip", zipped.Header.Get("Content-Encoding"))
	assert.Contains(t, zipped.Header.Values("Vary"), "Accept-Encoding")
	wire, err := io.ReadAll(zipped.Body)
	require.NoError(t, err)

	zr, err := gzip.NewReader(strings.NewReader(string(wire)))
	require.NoError(t, err)
	body, err := io.ReadAll(zr)
	require.NoError(t, err)

	var list openapi.ChatMessageList
	require.NoError(t, json.Unmarshal(body, &list))
	assert.Len(t, list.Data, 20)
	assert.JSONEq(t, string(plainBody), string(body), "the same page, only gzipped")
	assert.Less(t, len(wire), len(plainBody)/3, "the page is much smaller on the wire")

	stream := get("/chats/"+chat.ID+"/stream", "gzip")
	defer func() { _ = stream.Body.Close() }()

	assert.Equal(t, "text/event-stream", stream.Header.Get("Content-Type"))
	assert.Empty(t, stream.Header.Get("Content-Encoding"), "a stream is never gzipped")

	frames := make(chan string, 16)

	go func() {
		defer close(frames)

		sc := bufio.NewScanner(stream.Body)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "event: ") {
				frames <- l
			}
		}
	}()

	select {
	case f, ok := <-frames:
		require.True(t, ok, "the stream ended")
		assert.Equal(t, "event: state", f, "the first frame arrives at once, not held back by a compressor")
	case <-time.After(5 * time.Second):
		t.Fatal("no frame in time")
	}
}
