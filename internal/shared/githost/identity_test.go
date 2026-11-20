package githost

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// answering is a host that answers every request with body, and keeps the
// last request it was asked.
type answering struct {
	body  string
	asked *http.Request
}

func (a *answering) RoundTrip(r *http.Request) (*http.Response, error) {
	a.asked = r

	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(a.body)), Request: r,
	}, nil
}

// Scenario (issue #991): the account a token belongs to is read at the host's
// /user. GitHub's gives its login and noreply address; GitLab's its username
// and commit address, else its public one. What git cannot take, an account
// that shows no address, and a GitHub Enterprise host, whose noreply address
// is not known, give none.
func TestReadIdentity(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name, host, body string
		kind             Kind
		url              string
		want             Identity
	}{
		{
			name: "github.com", kind: GitHub, host: GitHubHost, body: `{"login":"octo","id":1}`,
			url: "https://api.github.com/user", want: Identity{"octo", "1+octo@users.noreply.github.com"},
		},
		{
			name: "gitlab commit address", kind: GitLab, host: "gitlab.company.example:8443",
			body: `{"username":"dat","commit_email":"dat@company.example","public_email":"public@company.example"}`,
			url:  "https://gitlab.company.example:8443/api/v4/user", want: Identity{"dat", "dat@company.example"},
		},
		{
			name: "gitlab public address", kind: GitLab, host: GitLabHost,
			body: `{"username":"dat","public_email":"public@example.com"}`,
			url:  "https://gitlab.com/api/v4/user", want: Identity{"dat", "public@example.com"},
		},
		{name: "gitlab no address", kind: GitLab, host: GitLabHost, body: `{"username":"dat"}`},
		{name: "github no id", kind: GitHub, host: GitHubHost, body: `{"login":"octo"}`},
		{name: "a name on two lines", kind: GitLab, host: GitLabHost,
			body: `{"username":"dat\nGIT_AUTHOR_NAME=x","commit_email":"dat@example.com"}`},
		{name: "an address in brackets", kind: GitLab, host: GitLabHost,
			body: `{"username":"dat","commit_email":"<dat@example.com>"}`},
		{name: "github enterprise", kind: GitHub, host: "github.company.example", body: `{"login":"octo","id":1}`},
	} {
		host := &answering{body: c.body}
		got, err := ReadIdentity(t.Context(), &http.Client{Transport: host}, c.kind, c.host, "the-token")

		if !c.want.Known() {
			require.ErrorIs(t, err, ErrNoIdentity, c.name)
			assert.Empty(t, got.Env(), c.name)

			continue
		}

		require.NoError(t, err, c.name)
		assert.Equal(t, c.want, got, c.name)
		assert.Equal(t, c.url, host.asked.URL.String(), c.name)
		assert.Equal(t, "Bearer the-token", host.asked.Header.Get("Authorization"), c.name)
	}
}
