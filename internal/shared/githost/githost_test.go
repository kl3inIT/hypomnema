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
