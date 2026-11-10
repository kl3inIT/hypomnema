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
// git hosts' tokens are kept; keys is nil for a vault that was never set up.
func tokensWorld(t *testing.T, withKey bool) (hostTokens, *orchestration.Service, string) {
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

	return hostTokens{svc: svc, vault: secrets}, svc, path
}

// Scenario: the owner gives the company's GitLab server a token, replaces it,
// and takes it away. A Dispatch on that host gets the current token; the token
// is sealed in the database, never in the clear; the host's record only says
// that it has one.
func TestAGitHostsTokenIsKeptInTheVault(t *testing.T) {
	t.Parallel()

	tokens, svc, path := tokensWorld(t, true)
	ctx := t.Context()

	const host = "gitlab.company.example:8443"

	_, err := svc.AddGitHost(ctx, host, githost.GitLab)
	require.NoError(t, err)

	_, ok := tokens.Token(ctx, host)
	assert.False(t, ok, "a host starts without a token")

	require.NoError(t, tokens.Set(ctx, host, "  "+gitlabToken+"\n"))

	got, ok := tokens.Token(ctx, host)
	require.True(t, ok)
	assert.Equal(t, gitlabToken, got, "the token is kept without the spaces around it")

	known, err := svc.GitHostOf(ctx, host)
	require.NoError(t, err)
	assert.True(t, known.HasToken())
	assert.Equal(t, "git-host-gitlab-company-example-8443", known.TokenSecret)

	raw, err := os.ReadFile(path) //nolint:gosec // the test's own database
	require.NoError(t, err)
	assert.NotContains(t, string(raw), gitlabToken, "the database holds the token sealed, not in the clear")

	require.NoError(t, tokens.Set(ctx, host, "glpat-the-second-one"))

	got, _ = tokens.Token(ctx, host)
	assert.Equal(t, "glpat-the-second-one", got, "a new token replaces the old one")

	// github.com is known without a declaration and takes a token as well.
	require.NoError(t, tokens.Set(ctx, githost.GitHubHost, "github_pat_x"))

	hosts, err := svc.ListGitHosts(ctx)
	require.NoError(t, err)
	require.Len(t, hosts, 3)
	assert.Equal(t, []bool{true, false, true}, []bool{hosts[0].HasToken(), hosts[1].HasToken(), hosts[2].HasToken()})

	require.NoError(t, tokens.Clear(ctx, host))
	require.NoError(t, tokens.Clear(ctx, host), "clearing twice changes nothing")

	_, ok = tokens.Token(ctx, host)
	assert.False(t, ok)

	// A token given again after it was taken away is kept as a new secret.
	require.NoError(t, tokens.Set(ctx, host, gitlabToken))
	require.NoError(t, tokens.Remove(ctx, host), "removing a host removes its token with it")

	_, err = svc.GitHostOf(ctx, host)
	require.ErrorIs(t, err, orchestration.ErrInvalid)

	_, ok = tokens.Token(ctx, host)
	assert.False(t, ok)
}

// Scenario: what is not a token, a host that is not known, and a vault without
// a key are each refused, and the error never repeats what was given.
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
		err := tokens.Set(ctx, githost.GitLabHost, bad)
		require.ErrorIs(t, err, orchestration.ErrInvalid, name)
		assert.NotContains(t, err.Error(), "glpat", name)
	}

	err := tokens.Set(ctx, "unknown.example", gitlabToken)
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	assert.NotContains(t, err.Error(), gitlabToken)

	unset, svc, _ := tokensWorld(t, false)

	err = unset.Set(ctx, githost.GitLabHost, gitlabToken)
	require.ErrorIs(t, err, orchestration.ErrUnavailable)
	require.ErrorContains(t, err, "HYPOMNEMA_VAULT_KEYS")
	assert.NotContains(t, err.Error(), gitlabToken)

	known, err := svc.GitHostOf(ctx, githost.GitLabHost)
	require.NoError(t, err)
	assert.False(t, known.HasToken(), "a token that could not be kept leaves the host without one")
}
