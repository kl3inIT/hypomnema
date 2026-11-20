package controlplane

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/connproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// fakeHosts answers a git host's /user as a host would, by the token asked
// with, and counts what it was asked. A token it does not know is refused with
// a status line that repeats it, as a careless host or proxy might.
type fakeHosts struct {
	mu    sync.Mutex
	asked []string
	users map[string]string // token → body
}

func (f *fakeHosts) RoundTrip(r *http.Request) (*http.Response, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

	f.mu.Lock()
	f.asked = append(f.asked, r.URL.Host+r.URL.Path+" "+token)
	body, ok := f.users[token]
	f.mu.Unlock()

	status, line := http.StatusOK, "200 OK"
	if !ok {
		status, line, body = http.StatusForbidden, "403 Forbidden for "+token, `{"message":"403 Forbidden"}`
	}

	return &http.Response{
		StatusCode: status, Status: line, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(body)), Request: r,
	}, nil
}

func (f *fakeHosts) askedSoFar() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.asked)
}

// lockedLog is a log written beside the test.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.String()
}

// withIdentities is tokens reading accounts at hosts, a fresh control plane's process each call, and the log
// it writes.
func withIdentities(tokens connections, hosts *fakeHosts) (connections, *lockedLog) {
	log := &lockedLog{}

	tokens.identities = &gitIdentities{
		ways:   &connproxy.Transports{Direct: hosts},
		logger: slog.New(slog.NewTextHandler(log, nil)),
	}

	return tokens, log
}

// identityOf is the account an offer on host is given now.
func identityOf(t *testing.T, tokens connections, host string) githost.Identity {
	t.Helper()

	cred, _ := tokens.Credential(t.Context(), host, "")

	return cred.Identity
}

// eventually waits until an offer on host is given want: the account is read beside the offers.
func eventually(t *testing.T, tokens connections, host string, want githost.Identity, msg string) {
	t.Helper()

	require.Eventually(t, func() bool { return identityOf(t, tokens, host) == want }, 5*time.Second,
		10*time.Millisecond, msg)
}

// Scenario (issue #991): the owner connects github.com with a token of the
// account octo, whose id is 1. The account is read at GitHub beside the
// offers, once, and kept with the Connection; a Dispatch on GitHub is then
// given the token and octo's identity, which the node gives git as the author
// and committer. A later offer, even after the control plane restarts, does not
// ask again. A token replaced is read again, as it may be another account's:
// after a disconnect and a connect again under the same label (a new secret,
// whose version is numbered 1 again), and on its vault secret.
func TestADispatchCommitsAsItsTokensAccount(t *testing.T) {
	t.Parallel()

	plain, svc, _ := tokensWorld(t, true)
	ctx := t.Context()

	hosts := &fakeHosts{users: map[string]string{
		"ghp_octo":  `{"login":"octo","id":1,"name":"The Octocat","email":null}`,
		"ghp_hubot": `{"login":"hubot","id":2}`,
	}}
	tokens, _ := withIdentities(plain, hosts)
	octo := githost.Identity{Name: "octo", Email: "1+octo@users.noreply.github.com"}
	hubot := githost.Identity{Name: "hubot", Email: "2+hubot@users.noreply.github.com"}

	require.NoError(t, tokens.Connect(ctx, githost.GitHubHost, "default", "ghp_octo"))
	eventually(t, tokens, githost.GitHubHost, octo, "the account is read as the token is given")

	cred, ok := tokens.Credential(ctx, githost.GitHubHost, "kl3inIT/hypomnema")
	require.True(t, ok)
	assert.Equal(t, "ghp_octo", cred.Token)
	assert.Equal(t, []string{
		"GIT_AUTHOR_NAME=octo", "GIT_AUTHOR_EMAIL=1+octo@users.noreply.github.com",
		"GIT_COMMITTER_NAME=octo", "GIT_COMMITTER_EMAIL=1+octo@users.noreply.github.com",
	}, cred.Identity.Env())
	assert.Equal(t, []string{"api.github.com/user ghp_octo"}, hosts.askedSoFar(),
		"asked at GitHub's API with the token, once")

	conn, err := svc.ConnectionOf(ctx, githost.GitHubHost, "default")
	require.NoError(t, err)
	assert.Equal(t, [2]string{octo.Name, octo.Email}, [2]string{conn.GitName, conn.GitEmail}, "kept with the Connection")

	restarted, _ := withIdentities(plain, hosts)
	assert.Equal(t, octo, identityOf(t, restarted, githost.GitHubHost), "a restart takes what was kept")
	assert.Len(t, hosts.askedSoFar(), 1)

	require.NoError(t, tokens.Disconnect(ctx, githost.GitHubHost, "default"))
	require.NoError(t, tokens.Connect(ctx, githost.GitHubHost, "default", "ghp_hubot"))
	eventually(t, tokens, githost.GitHubHost, hubot, "a token connected again is read again")

	// The owner replaces the value of the Connection's secret itself, on the Secrets page: no Connect reads it, so
	// the first offer starts the read and the next ones have it.
	conn, err = svc.ConnectionOf(ctx, githost.GitHubHost, "default")
	require.NoError(t, err)
	_, err = tokens.vault.Rotate(ctx, conn.Secret, map[string]string{"value": "ghp_octo"})
	require.NoError(t, err)
	eventually(t, restarted, githost.GitHubHost, octo, "so is a token replaced on its secret")
	assert.Len(t, hosts.askedSoFar(), 3)
}

// Scenario (issue #991): a Dispatch on a host without a token gets nothing,
// said once. A GitLab token whose account names its commit address gives
// that; a token GitLab will not say the account of gives no identity: the
// offer still carries the token, nothing is set for git, it is asked once and
// said once, not at each offer, and the token, though the host repeats it,
// never reaches the log.
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

	for range 2 {
		_, ok := tokens.Credential(ctx, githost.GitLabHost, "team/app")
		assert.False(t, ok, "no token, nothing to give")
	}

	assert.Equal(t, 1, strings.Count(log.String(), "it has no token"), log.String())

	require.NoError(t, tokens.Connect(ctx, githost.GitLabHost, "default", "glpat-reads-user"))
	require.NoError(t, tokens.Connect(ctx, company, "default", "glpat-repository-only"))

	eventually(t, tokens, githost.GitLabHost, githost.Identity{Name: "dat", Email: "dat@company.example"},
		"GitLab's username and commit address")
	require.Eventually(t, func() bool { return strings.Contains(log.String(), "could not be read") }, 5*time.Second,
		10*time.Millisecond)

	for range 2 {
		cred, ok := tokens.Credential(ctx, company, "team/app")
		require.True(t, ok)
		assert.Equal(t, "glpat-repository-only", cred.Token, "the token goes without its account")
		assert.Empty(t, cred.Identity.Env())
	}

	assert.ElementsMatch(t, []string{
		"gitlab.com/api/v4/user glpat-reads-user", company + "/api/v4/user glpat-repository-only",
	}, hosts.askedSoFar(), "a token whose account cannot be read is asked once")
	assert.Equal(t, 1, strings.Count(log.String(), "could not be read"), log.String())
	assert.Equal(t, 1, strings.Count(log.String(), "on=\"connection "+company), log.String())
	assert.Contains(t, log.String(), "HTTP 403")
	assert.NotContains(t, log.String(), "glpat", "the token is never logged")

	conn, err := svc.ConnectionOf(ctx, company, "default")
	require.NoError(t, err)
	assert.Empty(t, conn.GitName)
}
