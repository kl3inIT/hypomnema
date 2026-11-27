package serviceproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
