package clientapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Scenario (issue #996): reading a share link fails inside the server. The internal error is logged with the
// request's path, without the link's token, which opens its artifact to whoever reads the log.
func TestAnInternalErrorOnAShareLinkLogsNoToken(t *testing.T) {
	t.Parallel()

	token := strings.Repeat("t", 43)

	for _, path := range []string{sharesPath + token, sharesPath + token + "/files/2/index.html"} {
		var logged bytes.Buffer

		s := &Server{logger: slog.New(slog.NewTextHandler(&logged, nil))}
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		s.writeError(httptest.NewRecorder(), r, errors.New("disk failed"))

		assert.NotContains(t, logged.String(), token, path)
		assert.Contains(t, logged.String(), sharesPath+"<token>", path)
	}

	assert.Equal(t, BasePath+"/runs/run_1", loggedPath(BasePath+"/runs/run_1"), "another path is logged as it is")
}

// A request the page abandoned (issue #1113: "reading run review stall: context canceled" on /tasks and /runs) is
// not the server's error: the client left, nobody reads the answer, and nothing is logged, as the model gateway's
// handlers leave it (modelgateway/web.go, webFailed).
func TestARequestThePageAbandonedIsNoInternalError(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer

	s := &Server{logger: slog.New(slog.NewTextHandler(&logged, nil))}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	r := httptest.NewRequestWithContext(ctx, http.MethodGet, BasePath+"/runs", nil)
	s.writeError(httptest.NewRecorder(), r, fmt.Errorf("reading run review stall: %w", context.Canceled))

	assert.Empty(t, logged.String())

	r = httptest.NewRequestWithContext(t.Context(), http.MethodGet, BasePath+"/runs", nil)
	s.writeError(httptest.NewRecorder(), r, fmt.Errorf("reading run review stall: %w", context.Canceled))
	assert.Contains(t, logged.String(), "clientapi.internal_error",
		"a canceled step of a request still open is the server's own")
}
