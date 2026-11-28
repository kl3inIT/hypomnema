package listening_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// Review round 4 of PR #1322 (issue #1290): the reviewer's probe, failing on the reviewed code.

// P1: the web's live test reads with no date window (ARCHITECTURE.md: "asked `python`, 2 results, with no date
// window"): no provider is asked a window, and an item its provider gives no date is kept. With the default keyless
// chain, Exa answering 500 and Parallel one valid undated item, the web works and the owner is not told.
func TestReviewLiveWebDoesNotUseAWindow(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		bodies []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		bodies = append(bodies, r.URL.String()+" "+string(body))
		mu.Unlock()

		if !strings.HasPrefix(r.URL.Path, "/parallel") {
			w.WriteHeader(http.StatusInternalServerError)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"…"}],` +
			`"structuredContent":{"search_id":"s1","results":[{"url":"https://python.example/news",` +
			`"title":"Python news","excerpts":["Python 3.15 is out."]}]}}}`))
	}))
	t.Cleanup(srv.Close)

	web := websearch.New(websearch.Config{
		ExaMCPURL: srv.URL + "/exa", ParallelMCPURL: srv.URL + "/parallel", TavilyURL: srv.URL + "/tavily",
		FirecrawlURL: srv.URL + "/firecrawl", YouMCPURL: srv.URL + "/you",
	})
	ask := func(ctx context.Context, query string, limit int) (websearch.Answer, error) {
		return web.Search(ctx, query, limit, "")
	}

	tell := &told{}
	s := kept(t, newClock(), tell, webOnly())

	require.NoError(t, s.Test(t.Context(), "web", ask, true))

	st := stateOf(t, s, "web")
	assert.Equal(t, listening.GroupWorking, st.Group, "Parallel's undated item is a read: code=%s", st.Code)
	assert.Equal(t, 0, tell.Count())

	mu.Lock()
	defer mu.Unlock()

	for _, b := range bodies {
		assert.NotContains(t, b, "after:", "no window hint in a query")
		assert.NotContains(t, b, "PublishedDate", "no date bounds asked")
		assert.NotContains(t, b, "web_search_advanced_exa", "Exa is asked its plain search")
	}
}
