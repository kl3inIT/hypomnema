package platform_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

var discard = slog.New(slog.DiscardHandler)

// /readyz must refuse traffic while a dependency is down, and say when to
// retry, so a load balancer stops routing to the process instead of failing
// requests.
func TestReadyzFollowsTheDependency(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		ready     error
		wantCode  int
		wantRetry string
	}{
		{name: "dependency answers", wantCode: http.StatusNoContent},
		{name: "dependency down", ready: errors.New("down"), wantCode: http.StatusServiceUnavailable, wantRetry: "5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			platform.RegisterHealth(mux, func(context.Context) error { return tc.ready }, discard)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, platform.PathReady, nil))

			assert.Equal(t, tc.wantCode, rec.Code)
			assert.Equal(t, tc.wantRetry, rec.Header().Get("Retry-After"))
		})
	}
}

// A request in flight when the process is told to stop must still get its
// answer, and Serve must return without an error.
func TestServeFinishesInFlightRequestsOnShutdown(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)

	go func() { served <- platform.Serve(ctx, ln, mux, discard) }()

	answered := make(chan int, 1)

	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/slow", nil)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			answered <- 0

			return
		}

		_ = resp.Body.Close()
		answered <- resp.StatusCode
	}()

	<-started
	cancel()

	assert.Equal(t, http.StatusOK, <-answered)
	require.NoError(t, <-served)
}
