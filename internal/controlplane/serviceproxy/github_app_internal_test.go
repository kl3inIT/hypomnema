package serviceproxy

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario (issue #1262): a Bot's gh on a repository the owner's GitHub App is installed on goes with the App's
// token. The repository is read from what gh sends: a REST path under /repos/{owner}/{repo}, or a GraphQL body whose
// variables are owner with repo or name. A request by a repository's id, or one that names none, names none, and
// goes with the Connection's token.
func TestTheRepositoryAGitHubRequestNamesIsReadFromWhatGhSends(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		method, path, body, want string
	}{
		{http.MethodGet, "/repos/kl3inIT/Hypomnema/pulls/1262", "", "kl3init/hypomnema"},
		{http.MethodGet, "/repos/kl3inIT/hypomnema", "", "kl3init/hypomnema"},
		{http.MethodPost, "/graphql", `{"query":"q","variables":{"owner":"kl3inIT","repo":"hypomnema","pr":1}}`,
			"kl3init/hypomnema"},
		{http.MethodPost, "/graphql", `{"query":"q","variables":{"owner":"kl3inIT","name":"MemoryOS"}}`,
			"kl3init/memoryos"},
		{http.MethodPost, "/graphql", `{"query":"q","variables":{"login":"kl3inIT"}}`, ""},
		{http.MethodGet, "/repositories/123/pulls", "", ""},
		{http.MethodGet, "/user", "", ""},
		{http.MethodGet, "/repos/kl3inIT/..", "", ""},
	} {
		req := httptest.NewRequestWithContext(t.Context(), c.method, "https://api.github.com"+c.path,
			strings.NewReader(c.body))

		got, ok := githubRepositoryOf(req, []byte(c.body))
		assert.Equal(t, c.want, got, c.path+" "+c.body)
		assert.Equal(t, c.want != "", ok, c.path+" "+c.body)
	}
}

// Scenario (review round 3 of PR #1323): GitHub answers gh's request for gzip with a gzipped body; the review the App
// made is read from it unpacked, by its Content-Encoding, deflate too, and a coding not known is not read.
func TestAnAnswerIsReadByItsContentEncoding(t *testing.T) {
	t.Parallel()

	plain := []byte(`{"id":880,"html_url":"https://github.com/o/r/pull/1#pullrequestreview-880"}`)

	var gz, zl bytes.Buffer

	gw := gzip.NewWriter(&gz)
	_, _ = gw.Write(plain)
	_ = gw.Close()

	zw := zlib.NewWriter(&zl)
	_, _ = zw.Write(plain)
	_ = zw.Close()

	for coding, body := range map[string][]byte{"": plain, "identity": plain, "gzip": gz.Bytes(), "deflate": zl.Bytes()} {
		got, err := unpacked(body, coding)
		require.NoError(t, err, coding)
		assert.Equal(t, string(plain), string(got), coding)
	}

	_, err := unpacked(plain, "br")
	assert.Error(t, err)
}
