package githost

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// answering is a host that answers every request with body, and keeps the
// last request it was asked.
type answering struct {
	body   string
	status int
	err    error
	asked  *http.Request
}

func (a *answering) RoundTrip(r *http.Request) (*http.Response, error) {
	a.asked = r
	if a.err != nil {
		return nil, a.err
	}

	status := a.status
	if status == 0 {
		status = http.StatusOK
	}

	return &http.Response{
		StatusCode: status, Status: strconv.Itoa(status) + " " + a.body, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(a.body)), Request: r,
	}, nil
}

// Scenario (issue #991): the account a token belongs to is read at the host's
// /user. GitHub's gives its login and noreply address; GitLab's its username
// and commit address, else its public one; a GitHub Enterprise host its
// login and its own noreply address. What git cannot take, an account that
// shows no address, and one named with the token give none.
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
		{
			name: "github enterprise", kind: GitHub, host: "github.company.example:8443", body: `{"login":"octo","id":1}`,
			url:  "https://github.company.example:8443/api/v3/user",
			want: Identity{"octo", "1+octo@users.noreply.github.company.example"},
		},
		{name: "named with the token", kind: GitLab, host: GitLabHost,
			body: `{"username":"the-token","commit_email":"dat@example.com"}`},
		{name: "the token in the address", kind: GitHub, host: GitHubHost, body: `{"login":"x-the-token-x","id":7}`},
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

// Scenario (issue #991): a host or a proxy that repeats the token, in its
// status line, its body or a failed connection's error, does not get it into
// the error that is logged.
func TestReadIdentityNeverRepeatsTheToken(t *testing.T) {
	t.Parallel()

	for _, host := range []*answering{
		{status: http.StatusUnauthorized, body: "bad credentials the-token"},
		{err: errors.New("proxyconnect: 407 the-token refused")},
		{body: `{"login": the-token`},
	} {
		_, err := ReadIdentity(t.Context(), &http.Client{Transport: host}, GitHub, GitHubHost, "the-token")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "the-token")
	}
}

// redirecting answers the first request with a redirect to location, and any other with GitHub's user; it keeps
// every request it was asked, with the Authorization header each carried.
type redirecting struct {
	location string
	asked    []string
}

func (r *redirecting) RoundTrip(req *http.Request) (*http.Response, error) {
	r.asked = append(r.asked, req.URL.String()+" "+req.Header.Get("Authorization"))

	if len(r.asked) == 1 {
		return &http.Response{
			StatusCode: http.StatusFound, Status: "302 Found", Header: http.Header{"Location": {r.location}},
			Body: io.NopCloser(strings.NewReader("")), Request: req,
		}, nil
	}

	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(`{"login":"octo","id":1}`)), Request: req,
	}, nil
}

// Scenario (issue #991): the host's /user redirects. A redirect within the same https origin is followed with the
// token; one to http on the same host, or to another host, is not followed: the token never crosses the network in
// the clear nor reaches another host, and no identity comes of it.
func TestReadIdentityFollowsRedirectsOnlyWithinTheHost(t *testing.T) {
	t.Parallel()

	const host = "github.company.example"

	for _, c := range []struct {
		location string
		followed bool
	}{
		{"https://" + host + "/api/v3/users/octo", true},
		{"http://" + host + "/api/v3/user", false},
		{"https://elsewhere.example/api/v3/user", false},
		{"https://" + host + ":8443/api/v3/user", false},
	} {
		r := &redirecting{location: c.location}
		got, err := ReadIdentity(t.Context(), &http.Client{Transport: r}, GitHub, host, "the-token")

		if !c.followed {
			require.Error(t, err, c.location)
			assert.Len(t, r.asked, 1, "%s is not followed", c.location)

			continue
		}

		require.NoError(t, err, c.location)
		assert.Equal(t, "octo", got.Name)
		assert.Equal(t, []string{
			"https://" + host + "/api/v3/user Bearer the-token", c.location + " Bearer the-token",
		}, r.asked)
	}
}
