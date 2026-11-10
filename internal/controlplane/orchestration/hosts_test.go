package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

func (w *world) runOn(repo, base string) (Run, error) {
	return w.s.CreateRun(w.t.Context(), NewRun{Objective: "ship it", CreatedBy: "owner", Repo: repo, Base: base})
}

// Scenario: the owner works on GitHub, on GitLab.com and on the company's own
// GitLab server. GitHub and GitLab.com work at once; the company's server
// works once the owner has declared it, and until then a Run on it is refused
// with the command that declares it.
func TestARunIsOnAKnownGitHost(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	old, err := w.runOn("kl3inIT/hypomnema", "")
	require.NoError(t, err)
	assert.Equal(t, [3]string{"github.com", "kl3inIT/hypomnema", DefaultBase}, [3]string{old.RepoHost, old.Repo, old.Base})

	cloud, err := w.runOn("https://gitlab.com/group/sub/project.git", "develop")
	require.NoError(t, err)
	assert.Equal(t, [3]string{"gitlab.com", "group/sub/project", "develop"},
		[3]string{cloud.RepoHost, cloud.Repo, cloud.Base})

	_, err = w.runOn("https://gitlab.company.example/team/app", "")
	require.ErrorIs(t, err, ErrInvalid)
	require.ErrorContains(t, err, "hypomnema hosts add gitlab.company.example --kind gitlab")

	declared, err := w.s.AddGitHost(ctx, "gitlab.company.example", githost.GitLab)
	require.NoError(t, err)
	assert.Equal(t, GitHost{Host: "gitlab.company.example", Kind: githost.GitLab, CreatedAt: declared.CreatedAt}, declared)

	own, err := w.runOn("git@gitlab.company.example:team/app.git", "")
	require.NoError(t, err)
	assert.Equal(t, [2]string{"gitlab.company.example", "team/app"}, [2]string{own.RepoHost, own.Repo})

	none, err := w.s.CreateRun(ctx, NewRun{Objective: "read my mail", CreatedBy: "owner"})
	require.NoError(t, err)
	assert.Empty(t, none.RepoHost, "a Run without a repository is on no host")

	hosts, err := w.s.ListGitHosts(ctx)
	require.NoError(t, err)
	require.Len(t, hosts, 3)
	assert.Equal(t, []string{"github.com", "gitlab.com", "gitlab.company.example"},
		[]string{hosts[0].Host, hosts[1].Host, hosts[2].Host})
	assert.True(t, hosts[0].Known)
	assert.False(t, hosts[2].Known)

	for name, bad := range map[string]func() error{
		"a host that is not a name":      func() error { _, err := w.s.AddGitHost(ctx, "https://x/y", githost.GitLab); return err },
		"a kind that is not known":       func() error { _, err := w.s.AddGitHost(ctx, "git.company.example", "bitbucket"); return err },
		"github.com declared as GitLab":  func() error { _, err := w.s.AddGitHost(ctx, "github.com", githost.GitLab); return err },
		"a repository that is not one":   func() error { _, err := w.runOn("not a repository", ""); return err },
		"a repository with a credential": func() error { _, err := w.runOn("https://me:secret@gitlab.com/g/p", ""); return err },
	} {
		require.ErrorIs(t, bad(), ErrInvalid, name)
	}
}

// Scenario: a host in use is not forgotten by accident: not while an open Run
// or a registered repository is on it. Afterwards it is, and a Run on it is
// refused again.
func TestAHostInUseIsNotForgotten(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.AddGitHost(ctx, "gitlab.company.example", githost.GitLab)
	require.NoError(t, err)

	run, err := w.runOn("https://gitlab.company.example/team/app", "")
	require.NoError(t, err)

	_, err = w.s.AddRepo(ctx, "app", "https://gitlab.company.example/team/app", "develop", "")
	require.NoError(t, err)

	require.ErrorIs(t, w.s.RemoveGitHost(ctx, "gitlab.company.example"), ErrConflict)

	require.NoError(t, w.s.CancelRun(ctx, run.ID))
	require.ErrorIs(t, w.s.RemoveGitHost(ctx, "gitlab.company.example"), ErrConflict, "a repository is still registered")

	require.NoError(t, w.s.RemoveRepo(ctx, "app"))
	require.NoError(t, w.s.RemoveGitHost(ctx, "gitlab.company.example"))
	require.ErrorIs(t, w.s.RemoveGitHost(ctx, "gitlab.company.example"), ErrNotFound)

	_, err = w.runOn("https://gitlab.company.example/team/app", "")
	require.ErrorIs(t, err, ErrInvalid)
}

// Scenario: the owner registers the repositories worked on most under short
// names. A Run names one and gets its host, its path and its base branch; the
// Run's own base wins; a name that is not registered is read as before.
func TestARunNamesARegisteredRepository(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.AddRepo(ctx, "api", "https://gitlab.company.example/team/platform/api", "develop", "")
	require.ErrorIs(t, err, ErrInvalid, "a repository on a host that is not known")

	_, err = w.s.AddGitHost(ctx, "gitlab.company.example", githost.GitLab)
	require.NoError(t, err)

	api, err := w.s.AddRepo(ctx, "api", "https://gitlab.company.example/team/platform/api", "develop", "")
	require.NoError(t, err)
	assert.Equal(t, [4]string{"api", "gitlab.company.example", "team/platform/api", "develop"},
		[4]string{api.Name, api.Host, api.Path, api.Base})

	_, err = w.s.AddRepo(ctx, "hypomnema", "kl3inIT/hypomnema", "", "")
	require.NoError(t, err)

	run, err := w.runOn("api", "")
	require.NoError(t, err)
	assert.Equal(t, [3]string{"gitlab.company.example", "team/platform/api", "develop"},
		[3]string{run.RepoHost, run.Repo, run.Base})

	hot, err := w.runOn("api", "hotfix")
	require.NoError(t, err)
	assert.Equal(t, "hotfix", hot.Base)

	gh, err := w.runOn("hypomnema", "")
	require.NoError(t, err)
	assert.Equal(t, [3]string{"github.com", "kl3inIT/hypomnema", DefaultBase}, [3]string{gh.RepoHost, gh.Repo, gh.Base})

	_, err = w.runOn("unregistered", "")
	require.ErrorIs(t, err, ErrInvalid)
	require.ErrorContains(t, err, "hypomnema repos add")

	repos, err := w.s.ListRepos(ctx)
	require.NoError(t, err)
	assert.Len(t, repos, 2)

	_, err = w.s.AddRepo(ctx, "Not A Name", "kl3inIT/hypomnema", "", "")
	require.ErrorIs(t, err, ErrInvalid)
	require.ErrorIs(t, w.s.RemoveRepo(ctx, "missing"), ErrNotFound)
}

// Scenario: a node is offered a Task of a Run on the company's GitLab server.
// The offer names the host and its kind, so the node clones from there and
// tells the worker to open its request with GitLab's tool; a Run from before
// hosts existed is offered as GitHub.
func TestAnOfferNamesTheHostAndItsKind(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.s.AddGitHost(ctx, "gitlab.company.example", githost.GitLab)
	require.NoError(t, err)

	run, err := w.runOn("https://gitlab.company.example/team/app", "develop")
	require.NoError(t, err)

	tasks, err := w.s.CreateTasks(ctx, run.ID, []NewTask{{Ref: "a", Profile: "implementer", Spec: "a"}})
	require.NoError(t, err)

	w.dispatch(tasks[0].ID)

	pending, err := w.s.NodePending(ctx, w.node.ID)
	require.NoError(t, err)
	require.Len(t, pending.Offers, 1)

	o := pending.Offers[0]
	assert.Equal(t, [4]string{"gitlab.company.example", "gitlab", "team/app", "develop"},
		[4]string{o.RepoHost, o.RepoKind, o.Repo, o.Base})

	// A Run stored before hosts existed has no host; it is github.com.
	_, err = w.db.ExecContext(ctx, `UPDATE runs SET repo_host = NULL, repo = 'kl3inIT/hypomnema' WHERE id = ?`, run.ID)
	require.NoError(t, err)

	pending, err = w.s.NodePending(ctx, w.node.ID)
	require.NoError(t, err)
	require.Len(t, pending.Offers, 1)
	assert.Equal(t, [3]string{"github.com", "github", "kl3inIT/hypomnema"},
		[3]string{pending.Offers[0].RepoHost, pending.Offers[0].RepoKind, pending.Offers[0].Repo})
}
