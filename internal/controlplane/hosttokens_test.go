package controlplane

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

const gitlabToken = "glpat-0123456789abcdef-never-printed"

// tokensWorld is a control plane's database with a vault and the place its
// Connections' tokens are kept; withKey false is a vault that was never set up.
func tokensWorld(t *testing.T, withKey bool) (connections, *orchestration.Service, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "hypomnema.db")

	cfg, err := LoadConfig(func(name string) string {
		if name == "HYPOMNEMA_DATABASE_PATH" {
			return path
		}

		return ""
	})
	require.NoError(t, err)

	if withKey {
		cfg = vaultConfig(t, path, "k1", keygen(t, "k1"))
	}

	require.NoError(t, Migrate(t.Context(), cfg, io.Discard))

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	secrets := vault.New(db, platform.SystemClock{}, platform.NewID, cfg.VaultKeys, svc.ProvidersUsingKeyRef)

	return connections{svc: svc, vault: secrets}, svc, path
}

// Scenario: the owner connects the company's GitLab server with a token,
// replaces it, and disconnects. A Dispatch on that host gets the current
// token; the token is sealed in the database, never in the clear; the host's
// record only names its Connections.
func TestAConnectionsTokenIsKeptInTheVault(t *testing.T) {
	t.Parallel()

	tokens, svc, path := tokensWorld(t, true)
	ctx := t.Context()

	const host = "gitlab.company.example:8443"

	_, err := svc.AddGitHost(ctx, host, githost.GitLab)
	require.NoError(t, err)

	_, ok := tokens.Token(ctx, host, "team/app")
	assert.False(t, ok, "a host starts without a connection")

	require.NoError(t, tokens.Connect(ctx, host, orchestration.DefaultConnection, "  "+gitlabToken+"\n"))

	got, ok := tokens.Token(ctx, host, "team/app")
	require.True(t, ok)
	assert.Equal(t, gitlabToken, got, "the token is kept without the spaces around it")

	known, err := svc.GitHostOf(ctx, host)
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, known.Connections)

	conn, err := svc.ConnectionOf(ctx, host, "default")
	require.NoError(t, err)
	assert.Equal(t, "conn-gitlab-company-example-8443-default", conn.Secret)

	raw, err := os.ReadFile(path) //nolint:gosec // the test's own database
	require.NoError(t, err)
	assert.NotContains(t, string(raw), gitlabToken, "the database holds the token sealed, not in the clear")

	require.NoError(t, tokens.Connect(ctx, host, "default", "glpat-the-second-one"))

	got, _ = tokens.Token(ctx, host, "")
	assert.Equal(t, "glpat-the-second-one", got, "a new token replaces the old one, in the same secret")

	// github.com is known without a declaration and takes a connection as well.
	require.NoError(t, tokens.Connect(ctx, githost.GitHubHost, "default", "github_pat_x"))

	hosts, err := svc.ListGitHosts(ctx)
	require.NoError(t, err)
	require.Len(t, hosts, 3)
	assert.Equal(t, []bool{true, false, true}, []bool{hosts[0].HasToken(), hosts[1].HasToken(), hosts[2].HasToken()})

	require.NoError(t, tokens.Disconnect(ctx, host, "default"))
	require.NoError(t, tokens.Disconnect(ctx, host, "default"), "disconnecting twice changes nothing")

	_, ok = tokens.Token(ctx, host, "team/app")
	assert.False(t, ok)

	// A token given again after it was taken away is kept as a new secret.
	require.NoError(t, tokens.Connect(ctx, host, "default", gitlabToken))
	require.NoError(t, tokens.RemoveHost(ctx, host), "removing a host removes its connections with it")

	_, err = svc.GitHostOf(ctx, host)
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	_, ok = tokens.Token(ctx, host, "team/app")
	assert.False(t, ok)
}

// Scenario: the owner has two accounts on github.com, his own and his
// company's. Work on a repository registered with one of them uses that one;
// work on another repository of the host gets no token, because nothing is
// guessed between two; once one account is disconnected the other is the
// host's only one and is used everywhere. An account a repository names
// cannot be disconnected.
func TestAHostWithSeveralConnections(t *testing.T) {
	t.Parallel()

	tokens, svc, _ := tokensWorld(t, true)
	ctx := t.Context()

	require.NoError(t, tokens.Connect(ctx, githost.GitHubHost, "personal", "ghp_personal"))

	got, ok := tokens.Token(ctx, githost.GitHubHost, "acme/shop")
	require.True(t, ok, "the host's only connection is used for every repository")
	assert.Equal(t, "ghp_personal", got)

	require.NoError(t, tokens.Connect(ctx, githost.GitHubHost, "work", "ghp_work"))

	known, err := svc.GitHostOf(ctx, githost.GitHubHost)
	require.NoError(t, err)
	assert.Equal(t, []string{"personal", "work"}, known.Connections, "oldest first")

	_, ok = tokens.Token(ctx, githost.GitHubHost, "acme/shop")
	assert.False(t, ok, "with two connections and none named, none is used")

	_, err = svc.AddRepo(ctx, "shop", "acme/shop", "", "nobody")
	require.ErrorIs(t, err, orchestration.ErrInvalid, "a repository can only name a connection its host has")

	repo, err := svc.AddRepo(ctx, "shop", "acme/shop", "", "work")
	require.NoError(t, err)
	assert.Equal(t, "work", repo.Connection)

	got, ok = tokens.Token(ctx, githost.GitHubHost, "acme/shop")
	require.True(t, ok)
	assert.Equal(t, "ghp_work", got, "the repository's connection")

	_, ok = tokens.Token(ctx, githost.GitHubHost, "someone/else")
	assert.False(t, ok, "another repository of the host names none")

	err = tokens.Disconnect(ctx, githost.GitHubHost, "work")
	require.ErrorIs(t, err, orchestration.ErrConflict, "a repository still uses it")

	got, ok = tokens.Token(ctx, githost.GitHubHost, "acme/shop")
	require.True(t, ok, "a refused disconnect leaves the token where it was")
	assert.Equal(t, "ghp_work", got)

	require.NoError(t, tokens.Disconnect(ctx, githost.GitHubHost, "personal"))

	got, ok = tokens.Token(ctx, githost.GitHubHost, "someone/else")
	require.True(t, ok, "one connection left: it is the host's only one")
	assert.Equal(t, "ghp_work", got)
}

// Scenario: what is not a token, a label that is not one, a host that is not
// known, and a vault without a key are each refused, and the error never
// repeats what was given.
func TestATokenThatCannotBeKeptIsRefused(t *testing.T) {
	t.Parallel()

	tokens, _, _ := tokensWorld(t, true)
	ctx := t.Context()

	for name, bad := range map[string]string{
		"nothing":      "  ",
		"two lines":    "glpat-one\nglpat-two",
		"with a space": "Bearer glpat-secret",
		"too long":     strings.Repeat("x", maxTokenBytes+1),
	} {
		err := tokens.Connect(ctx, githost.GitLabHost, "default", bad)
		require.ErrorIs(t, err, orchestration.ErrInvalid, name)
		assert.NotContains(t, err.Error(), "glpat", name)
	}

	for _, label := range []string{"", "Work", "my account", strings.Repeat("a", 32)} {
		err := tokens.Connect(ctx, githost.GitLabHost, label, gitlabToken)
		require.ErrorIs(t, err, orchestration.ErrInvalid, label)
		assert.NotContains(t, err.Error(), gitlabToken)
	}

	err := tokens.Connect(ctx, "unknown.example", "default", gitlabToken)
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	assert.NotContains(t, err.Error(), gitlabToken)

	unset, svc, _ := tokensWorld(t, false)

	err = unset.Connect(ctx, githost.GitLabHost, "default", gitlabToken)
	require.ErrorIs(t, err, orchestration.ErrUnavailable)
	require.ErrorContains(t, err, "HYPOMNEMA_VAULT_KEYS")
	assert.NotContains(t, err.Error(), gitlabToken)

	known, err := svc.GitHostOf(ctx, githost.GitLabHost)
	require.NoError(t, err)
	assert.False(t, known.HasToken(), "a token that could not be kept leaves the host without a connection")
}

// A long host leaves room for the label in a secret's 63 characters.
func TestSecretNameFitsTheVault(t *testing.T) {
	t.Parallel()

	name := secretName(strings.Repeat("very-long-host.", 6)+"example", "work")
	assert.LessOrEqual(t, len(name), 63)
	assert.True(t, strings.HasPrefix(name, "conn-very-long-host-"))
	assert.True(t, strings.HasSuffix(name, "-work"))
}
