package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Scenario: a database from when a git host had one token. After the
// migration each such host has a Connection "default" that names the same
// vault secret, so work on it goes on with the same token; a host without a
// token has no Connection; and the way back restores what was there.
func TestMigrationTurnsAHostsTokenIntoItsDefaultConnection(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	p, err := provider(db)
	require.NoError(t, err)
	_, err = p.UpTo(ctx, 27)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO git_hosts (host, kind, token_secret, created_at) VALUES
		('github.com', 'github', 'git-host-github-com', 100),
		('git.company.example', 'gitlab', 'git-host-git-company-example', 200),
		('gitlab.other.example', 'gitlab', NULL, 300)`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO repos (name, host, path, base, created_at)
		VALUES ('app', 'git.company.example', 'team/app', 'develop', 400)`)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckSchema(ctx, db))

	s := New(db, &fakeClock{now: t0}, func(prefix string) string { return prefix + "_new" })

	conns, err := s.ListConnections(ctx)
	require.NoError(t, err)
	require.Len(t, conns, 2)
	assert.Equal(t, [3]string{"git.company.example", "default", "git-host-git-company-example"},
		[3]string{conns[0].Integration, conns[0].Label, conns[0].Secret})
	assert.Equal(t, [3]string{"github.com", "default", "git-host-github-com"},
		[3]string{conns[1].Integration, conns[1].Label, conns[1].Secret})

	hosts, err := s.ListGitHosts(ctx)
	require.NoError(t, err)
	require.Len(t, hosts, 4)

	has := map[string]bool{}
	for _, h := range hosts {
		has[h.Host] = h.HasToken()
	}

	assert.Equal(t, map[string]bool{
		"github.com": true, "gitlab.com": false, "git.company.example": true, "gitlab.other.example": false,
	}, has)

	conn, ok, err := s.ConnectionFor(ctx, "git.company.example", "team/app")
	require.NoError(t, err)
	require.True(t, ok, "the registered repository goes on with the host's token")
	assert.Equal(t, "git-host-git-company-example", conn.Secret)

	repos, err := s.ListRepos(ctx)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, [3]string{"app", "develop", ""}, [3]string{repos[0].Name, repos[0].Base, repos[0].Connection})

	// The way back: the host names its token's secret again.
	_, err = p.DownTo(ctx, 27)
	require.NoError(t, err)

	var secret string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT token_secret FROM git_hosts WHERE host = 'git.company.example'`).Scan(&secret))
	assert.Equal(t, "git-host-git-company-example", secret)
}

// Scenario: a Connection is recorded only for a host a Run can be on and
// under a label the owner can type; recording it again changes its secret
// and keeps when it was first made.
func TestAConnectionIsRecordedForAKnownHost(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.SetConnection(ctx, "unknown.example", "work", "", "s1")
	require.ErrorIs(t, err, ErrInvalid)

	_, err = w.s.SetConnection(ctx, "github.com", "Work", "", "s1")
	require.ErrorIs(t, err, ErrInvalid)

	_, err = w.s.SetConnection(ctx, "github.com", "work", "", "")
	require.ErrorIs(t, err, ErrInvalid)

	first, err := w.s.SetConnection(ctx, "github.com", "work", "", "s1")
	require.NoError(t, err)

	w.advance(UnplaceableAfter)

	again, err := w.s.SetConnection(ctx, "github.com", "work", "octocat", "s2")
	require.NoError(t, err)
	assert.Equal(t, [2]string{"s2", "octocat"}, [2]string{again.Secret, again.Account})
	assert.Equal(t, first.CreatedAt, again.CreatedAt)
	assert.True(t, again.UpdatedAt.After(first.UpdatedAt))

	_, err = w.s.ConnectionOf(ctx, "github.com", "other")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = w.s.RemoveConnection(ctx, "github.com", "other")
	require.ErrorIs(t, err, ErrNotFound)

	err = w.s.RemoveGitHost(ctx, "github.com")
	require.ErrorIs(t, err, ErrInvalid, "a known host is never removed")
}
