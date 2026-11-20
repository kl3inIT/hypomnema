package controlplane

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/connproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// fakeHosts answers a git host's /user as a host would, by the token asked
// with, and counts what it was asked.
type fakeHosts struct {
	mu    sync.Mutex
	asked []string
	users map[string]string // token → body; a token not here is refused
}

func (f *fakeHosts) RoundTrip(r *http.Request) (*http.Response, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	f.mu.Lock()
	f.asked = append(f.asked, r.URL.Host+r.URL.Path+" "+token)
	body, ok := f.users[token]
	f.mu.Unlock()

	status := http.StatusOK
	if !ok {
		status, body = http.StatusForbidden, `{"message":"403 Forbidden"}`
	}

	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(body)), Request: r,
	}, nil
}

func (f *fakeHosts) times() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.asked)
}

// withIdentities is tokens reading accounts at hosts, a fresh control plane's process each call, and the log
// it writes.
func withIdentities(tokens connections, hosts *fakeHosts) (connections, *bytes.Buffer) {
	var log bytes.Buffer

	tokens.identities = &gitIdentities{
		ways:   &connproxy.Transports{Direct: hosts},
		logger: slog.New(slog.NewTextHandler(&log, nil)),
	}

	return tokens, &log
}

// Scenario (issue #991): the owner connects github.com with a token of the
// account octo, whose id is 1. A Dispatch on GitHub is given the token and
// octo's identity, which the node gives git as the author and committer. The
// account is read at GitHub once and kept with the Connection: a later offer,
// even after the control plane restarts, does not ask again. A token
// replaced, by connecting again or on its vault secret, is read again.
func TestADispatchCommitsAsItsTokensAccount(t *testing.T) {
	t.Parallel()

	plain, svc, _ := tokensWorld(t, true)
	ctx := t.Context()

	hosts := &fakeHosts{users: map[string]string{
		"ghp_octo":  `{"login":"octo","id":1,"name":"The Octocat","email":null}`,
		"ghp_hubot": `{"login":"hubot","id":2}`,
	}}
	tokens, _ := withIdentities(plain, hosts)

	require.NoError(t, tokens.Connect(ctx, githost.GitHubHost, "default", "ghp_octo"))

	cred, ok := tokens.Credential(ctx, githost.GitHubHost, "kl3inIT/hypomnema")
	require.True(t, ok)
	assert.Equal(t, "ghp_octo", cred.Token)
	assert.Equal(t, []string{
		"GIT_AUTHOR_NAME=octo", "GIT_AUTHOR_EMAIL=1+octo@users.noreply.github.com",
		"GIT_COMMITTER_NAME=octo", "GIT_COMMITTER_EMAIL=1+octo@users.noreply.github.com",
	}, cred.Identity.Env())
	assert.Equal(t, []string{"api.github.com/user ghp_octo"}, hosts.asked, "asked at GitHub's API with the token")

	conn, err := svc.ConnectionOf(ctx, githost.GitHubHost, "default")
	require.NoError(t, err)
	assert.Equal(t, [2]string{"octo", "1+octo@users.noreply.github.com"}, [2]string{conn.GitName, conn.GitEmail},
		"kept with the Connection")

	restarted, _ := withIdentities(plain, hosts)
	again, _ := restarted.Credential(ctx, githost.GitHubHost, "kl3inIT/hypomnema")
	assert.Equal(t, cred.Identity, again.Identity)
	assert.Equal(t, 1, hosts.times(), "read once: a later offer takes what was kept")

	require.NoError(t, tokens.Connect(ctx, githost.GitHubHost, "default", "ghp_hubot"))

	cred, _ = tokens.Credential(ctx, githost.GitHubHost, "")
	assert.Equal(t, githost.Identity{Name: "hubot", Email: "2+hubot@users.noreply.github.com"}, cred.Identity,
		"a new token is read again: it may be another account's")

	// The owner replaces the value of the Connection's secret itself, on the Secrets page.
	_, err = tokens.vault.Rotate(ctx, conn.Secret, map[string]string{"value": "ghp_octo"})
	require.NoError(t, err)

	cred, _ = restarted.Credential(ctx, githost.GitHubHost, "")
	assert.Equal(t, "octo", cred.Identity.Name, "so is a token replaced on its secret")
	assert.Equal(t, 3, hosts.times())
}

// Scenario (issue #991): a GitLab token whose account names its commit
// address gives that; a token GitLab will not say the account of gives no
// identity: the offer still carries the token, nothing is set for git, and it
// is said once, not at each offer. Without a token there is nothing.
func TestAnAccountThatCannotBeReadGivesNoIdentity(t *testing.T) {
	t.Parallel()

	plain, svc, _ := tokensWorld(t, true)
	ctx := t.Context()

	const company = "gitlab.company.example"

	_, err := svc.AddGitHost(ctx, company, githost.GitLab)
	require.NoError(t, err)

	hosts := &fakeHosts{users: map[string]string{
		"glpat-reads-user": `{"username":"dat","commit_email":"dat@company.example","public_email":""}`,
	}}
	tokens, log := withIdentities(plain, hosts)

	_, ok := tokens.Credential(ctx, githost.GitLabHost, "team/app")
	assert.False(t, ok, "no token, nothing to give")

	require.NoError(t, tokens.Connect(ctx, githost.GitLabHost, "default", "glpat-reads-user"))
	require.NoError(t, tokens.Connect(ctx, company, "default", "glpat-repository-only"))

	cred, _ := tokens.Credential(ctx, githost.GitLabHost, "team/app")
	assert.Equal(t, githost.Identity{Name: "dat", Email: "dat@company.example"}, cred.Identity)

	for range 2 {
		cred, ok := tokens.Credential(ctx, company, "team/app")
		require.True(t, ok)
		assert.Equal(t, "glpat-repository-only", cred.Token, "the token goes without its account")
		assert.Empty(t, cred.Identity.Env())
	}

	assert.Equal(t, []string{
		"gitlab.com/api/v4/user glpat-reads-user", company + "/api/v4/user glpat-repository-only",
	}, hosts.asked, "a token whose account cannot be read is asked once")
	assert.Equal(t, 1, strings.Count(log.String(), "is not known"), log.String())
	assert.NotContains(t, log.String(), "glpat", "the token is never logged")

	conn, err := svc.ConnectionOf(ctx, company, "default")
	require.NoError(t, err)
	assert.Empty(t, conn.GitName)
}
