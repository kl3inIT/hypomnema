package githost

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The owner names a repository as before (owner/name), by the URL copied from
// the browser, or by the address git prints; each is the same repository.
func TestARepositoryIsReadFromWhatTheOwnerTypes(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]Repo{
		"kl3inIT/hypomnema":                                  {GitHubHost, "kl3inIT/hypomnema"},
		" https://github.com/kl3inIT/hypomnema.git ":         {GitHubHost, "kl3inIT/hypomnema"},
		"git@github.com:kl3inIT/hypomnema.git":               {GitHubHost, "kl3inIT/hypomnema"},
		"https://gitlab.com/group/sub/project":               {GitLabHost, "group/sub/project"},
		"https://GitLab.Company.Example/team/app/":           {"gitlab.company.example", "team/app"},
		"https://gitlab.company.example:8443/team/app.git":   {"gitlab.company.example:8443", "team/app"},
		"git@gitlab.company.example:team/platform/api.git":   {"gitlab.company.example", "team/platform/api"},
		"https://gitlab.com/group/project_with.dots-and_all": {GitLabHost, "group/project_with.dots-and_all"},
	} {
		got, err := Parse(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}

// What cannot be a repository is refused: no host to trust by accident, no
// path that reads as an option or leaves its directory, no credential in a URL.
func TestWhatIsNotARepositoryIsRefused(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"", "hypomnema", "owner/", "/name", "owner/name/extra",
		"http://gitlab.com/group/project",
		"https://user:pass@gitlab.com/group/project",
		"https://gitlab.com/group/project?ref=main",
		"https://gitlab.com/group",
		"https://localhost/group/project",
		"https://gitlab.com/../etc/passwd",
		"https://gitlab.com/group/-project",
		"https://gitlab.com/group/.hidden",
		"https://gitlab.com/group/project.",
		"owner/--upload-pack=x",
		"ssh://git@gitlab.com/group/project",
		"https://evil..com/group/project",
	} {
		_, err := Parse(in)
		assert.ErrorIs(t, err, ErrInvalid, in)
	}
}

func TestARepositoryPrintsAsTheOwnerWroteIt(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "kl3inIT/hypomnema", Repo{GitHubHost, "kl3inIT/hypomnema"}.String())
	assert.Equal(t, "https://gitlab.company.example/team/app", Repo{"gitlab.company.example", "team/app"}.String())
}

// Each kind of host has its clone address, its command to open a pull request
// and the page of a request.
func TestEachKindOfHostHasItsOwnWays(t *testing.T) {
	t.Parallel()

	gh, ok := For(GitHub)
	require.True(t, ok)

	repo := Repo{GitHubHost, "kl3inIT/hypomnema"}
	assert.Equal(t, "https://github.com/kl3inIT/hypomnema.git", gh.CloneURL(repo))
	assert.Equal(t, "gh pr create --base main --head hyp/tsk_1", gh.OpenCommand("main", "hyp/tsk_1"))
	assert.Equal(t, "https://github.com/kl3inIT/hypomnema/pull/78", gh.RequestURL(repo, 78))

	gl, ok := For(GitLab)
	require.True(t, ok)

	own := Repo{"gitlab.company.example", "team/platform/api"}
	assert.Equal(t, "https://gitlab.company.example/team/platform/api.git", gl.CloneURL(own))
	assert.Equal(t, "glab mr create --target-branch develop --source-branch hyp/tsk_1 --yes",
		gl.OpenCommand("develop", "hyp/tsk_1"))
	assert.Equal(t, "https://gitlab.company.example/team/platform/api/-/merge_requests/12", gl.RequestURL(own, 12))

	_, ok = For("bitbucket")
	assert.False(t, ok)

	kind, ok := KnownKind(GitLabHost)
	assert.True(t, ok)
	assert.Equal(t, GitLab, kind)

	_, ok = KnownKind("gitlab.company.example")
	assert.False(t, ok, "a company's server is declared by the owner")
}

// A worker reports its pull request as a URL, or as owner/name#N as workers
// did before hosts existed. Only a request on a host that is known is read.
func TestAPullRequestIsReadFromAWorkersEvidence(t *testing.T) {
	t.Parallel()

	kinds := func(host string) (Kind, bool) {
		if host == "gitlab.company.example" {
			return GitLab, true
		}

		return KnownKind(host)
	}

	for ref, want := range map[string]Request{
		"kl3inIT/hypomnema#78":                              {Repo{GitHubHost, "kl3inIT/hypomnema"}, 78},
		"https://github.com/kl3inIT/hypomnema/pull/78":      {Repo{GitHubHost, "kl3inIT/hypomnema"}, 78},
		"https://github.com/o/r/pull/7/files":               {Repo{GitHubHost, "o/r"}, 7},
		"https://gitlab.com/group/sub/p/-/merge_requests/3": {Repo{GitLabHost, "group/sub/p"}, 3},
		"https://gitlab.company.example/team/app/-/merge_requests/12/diffs": {
			Repo{"gitlab.company.example", "team/app"}, 12,
		},
	} {
		got, ok := ParseRequest(ref, kinds)
		require.True(t, ok, ref)
		assert.Equal(t, want, got, ref)
	}

	for _, ref := range []string{
		"", "not a url", "kl3inIT/hypomnema#0", "kl3inIT/hypomnema#x",
		"javascript:alert(1)//pull/1",
		"http://github.com/o/r/pull/1",
		"https://unknown.example/o/r/pull/1",
		"https://github.com/o/r/-/merge_requests/1",
		"https://gitlab.com/group/p/pull/1",
		"https://github.com/o/r/issues/1",
		"https://user@github.com/o/r/pull/1",
	} {
		_, ok := ParseRequest(ref, kinds)
		assert.False(t, ok, ref)
	}
}

// With a token, git and the host's own tool act as the token's owner on that
// host and no other; the token is in the environment, never in an argument.
// Without one nothing is added, and the node user's logins are used.
func TestAHostsTokenReachesGitAndItsTool(t *testing.T) {
	t.Parallel()

	gh, _ := For(GitHub)
	gl, _ := For(GitLab)

	assert.Equal(t, []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic eC1hY2Nlc3MtdG9rZW46Z2hwX3g=",
		"GH_TOKEN=ghp_x",
	}, gh.Env(GitHubHost, "ghp_x"))

	assert.Equal(t, []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.company.example/.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic eC1hY2Nlc3MtdG9rZW46Z2hwX3g=",
		"GH_ENTERPRISE_TOKEN=ghp_x",
		"GH_HOST=github.company.example",
	}, gh.Env("github.company.example", "ghp_x"), "a company's GitHub server takes the enterprise token and its host")

	assert.Equal(t, []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://gitlab.company.example:8443/.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic b2F1dGgyOmdscGF0LXg=",
		"GITLAB_TOKEN=glpat-x",
		"GITLAB_HOST=gitlab.company.example:8443",
	}, gl.Env("gitlab.company.example:8443", "glpat-x"))

	assert.Empty(t, gh.Env(GitHubHost, ""))
	assert.Empty(t, gl.Env(GitLabHost, ""))
}

const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB8TL1wZn5vEveB+nHTVCX490uF9kH8g6jcBT6BgFep8"

// The owner gives the SSH host's key with its address (issue #625), as
// ssh-keyscan prints it or as one key line; it is kept as "<type> <base64>",
// and its fingerprint is shown for him to check. Nothing, or what is not a
// key, is refused: a host is never trusted on first use.
func TestAnSSHHostKeyIsReadAsSSHKeyscanPrintsIt(t *testing.T) {
	t.Parallel()

	for _, given := range []string{
		"# 167.254.68.172:222 SSH-2.0-OpenSSH_9.6\n[167.254.68.172]:222 " + hostKey + "\n",
		hostKey + " admin@host",
		"git-vmaps-devops.vtii.vn " + hostKey,
	} {
		got, err := ParseHostKeys(given)
		require.NoError(t, err, given)
		assert.Equal(t, hostKey, got)
	}

	fingerprints := HostKeyFingerprints(hostKey)
	require.Len(t, fingerprints, 1)
	assert.Regexp(t, `^ssh-ed25519 SHA256:[A-Za-z0-9+/]{43}$`, fingerprints[0])

	for _, bad := range []string{"", "# only a comment", "ssh-ed25519 notbase64!", "hello"} {
		_, err := ParseHostKeys(bad)
		require.ErrorIs(t, err, ErrInvalid, bad)
	}
}

// The owner gives a repository's SSH address (issue #625) as the host shows
// it: with a port in the URL form, or in git's short form. One with a
// password, of another scheme, or whose user, host or path could be read as
// an option is refused.
func TestAnSSHAddressIsCheckedBeforeGitSeesIt(t *testing.T) {
	t.Parallel()

	for _, good := range []string{
		"ssh://git@167.254.68.172:222/mobility/platform.git",
		"ssh://git@gitlab.company.example/team/app",
		"git@github.com:kl3inIT/hypomnema.git",
	} {
		assert.True(t, ValidSSHURL(good), good)
	}

	for _, bad := range []string{
		"",
		"https://gitlab.company.example/team/app.git",
		"ssh://gitlab.company.example/team/app.git",
		"ssh://git:secret@gitlab.company.example/team/app.git",
		"ssh://git@-oProxyCommand=x/team/app.git",
		"ssh://-oProxyCommand=x@gitlab.company.example/team/app.git",
		"ssh://git@gitlab.company.example/-team/app.git",
		"ssh://git@gitlab.company.example/app.git",
		"ssh://git@Gitlab.Company.example/team/app.git",
		"git@gitlab.company.example",
		"gitlab.company.example:team/app.git",
	} {
		assert.False(t, ValidSSHURL(bad), bad)
	}
}

// Each kind of host has its own way to have a pull request merged by itself
// (issue #714): GitHub's checks read both a ruleset and branch protection, and
// the merge is pinned to the pushed commit; GitLab's project path is one
// escaped segment of the API's path, and the merge request needs a pipeline.
func TestEachKindOfHostTurnsOnItsOwnAutoMerge(t *testing.T) {
	t.Parallel()

	gh, _ := For(GitHub)
	m := gh.AutoMerge(Repo{GitHubHost, "kl3inIT/hypomnema"}, "main")

	require.Len(t, m.Ready, 2)
	assert.Equal(t, []string{"gh api repos/kl3inIT/hypomnema --jq .allow_auto_merge"}, m.Ready[0].Commands)
	assert.Equal(t, "auto-merge is off in the repository's settings", m.Ready[0].Lacks)
	assert.Equal(t, []string{
		"gh api repos/kl3inIT/hypomnema/rules/branches/main",
		"gh api repos/kl3inIT/hypomnema/branches/main/protection/required_status_checks",
	}, m.Ready[1].Commands)
	assert.Equal(t, "no required check on main", m.Ready[1].Lacks)
	assert.Equal(t, "gh pr view <url> --json isDraft,mergeStateStatus", m.View)
	assert.Equal(t, "gh pr merge <url> --auto --merge --match-head-commit <sha>", m.Enable)
	assert.Equal(t, "gh pr checks <url> --required --watch", m.Watch)

	gl, _ := For(GitLab)
	m = gl.AutoMerge(Repo{"gitlab.company.example", "team/platform/api"}, "develop")

	require.Len(t, m.Ready, 1)
	assert.Equal(t, []string{"glab api projects/team%2Fplatform%2Fapi"}, m.Ready[0].Commands)
	assert.Contains(t, m.Ready[0].Shows, "only_allow_merge_if_pipeline_succeeds: true")
	assert.Equal(t, "glab mr merge <iid> --auto-merge --yes --sha <sha>", m.Enable)
	assert.Contains(t, m.Before, "pipeline is not null", "glab merges at once without a pipeline")
}
