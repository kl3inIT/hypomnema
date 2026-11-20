package clientapi

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Scenario (issue #996): reading a share link fails inside the server. The internal error is logged with the
// request's path, without the link's token, which opens its artifact to whoever reads the log.
func TestAnInternalErrorOnAShareLinkLogsNoToken(t *testing.T) {
	t.Parallel()

	const token = "VhTZQ8CLAtyTkW3wXTkvYSumsVJSxKbvBf5SJjHownY"

	for _, path := range []string{sharesPath + token, sharesPath + token + "/files/2/index.html"} {
		var logged bytes.Buffer

		s := &Server{logger: slog.New(slog.NewTextHandler(&logged, nil))}
		s.writeError(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil), errors.New("disk failed"))

		assert.NotContains(t, logged.String(), token, path)
		assert.Contains(t, logged.String(), sharesPath+"<token>", path)
	}

	assert.Equal(t, BasePath+"/runs/run_1", loggedPath(BasePath+"/runs/run_1"), "another path is logged as it is")
}
