// Package githost is what Hypomnema knows about a git host (issue #44): how a
// repository is named, where it is cloned from, which command opens a pull
// request and where that request is read on the web. Both binaries use it, so
// nothing else names a host. There is one Adapter a kind of host, as Orca has
// a ForgeProvider (src/main/source-control/forge-provider.ts), t3code a
// SourceControlProvider and Omnigent a GitProvider a host; the kinds are
// GitHub and GitLab, and a company's GitLab server is the GitLab kind at
// another host.
//
// A request to merge is called a pull request everywhere in Hypomnema
// (docs/glossary.md); GitLab's own word for it, merge request, appears only
// in what a worker is told to run.
package githost

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Kind is a kind of git host.
type Kind string

// The kinds of host.
const (
	GitHub Kind = "github"
	GitLab Kind = "gitlab"
)

// The hosts whose kind is known without the owner saying so.
const (
	GitHubHost = "github.com"
	GitLabHost = "gitlab.com"
)

// ErrInvalid marks a repository or a host that cannot be one.
var ErrInvalid = errors.New("invalid repository")

// Repo is a repository on a host. Path is owner/name on GitHub and
// group/[subgroup/...]project on GitLab, without ".git".
type Repo struct {
	Host string
	Path string
}

// String is the repository as the owner writes it: owner/name on github.com,
// its https URL elsewhere.
func (r Repo) String() string {
	if r.Host == GitHubHost {
		return r.Path
	}

	return "https://" + r.Host + "/" + r.Path
}

var (
	hostPattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
	segmentPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	scpPattern     = regexp.MustCompile(`^[\w.-]+@([\w.-]+):(.+)$`)
)

// ValidHost reports whether s can be a host: a lower-case name, optionally
// with a port.
func ValidHost(s string) bool {
	return hostPattern.MatchString(s) && strings.Contains(s, ".") && !strings.Contains(s, "..")
}

// Parse reads a repository as the owner or a coordinator gives it: owner/name
// (GitHub, as before hosts existed), an https URL, or git's user@host:path
// form. A trailing ".git" and "/" are dropped. It does not say whether the
// host is known.
func Parse(s string) (Repo, error) {
	s = strings.TrimSpace(s)

	var host, path string

	switch m := scpPattern.FindStringSubmatch(s); {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Repo{}, fmt.Errorf("%w: %q is not an https URL of a repository", ErrInvalid, s)
		}

		host, path = strings.ToLower(u.Host), u.Path
	case m != nil:
		host, path = strings.ToLower(m[1]), m[2]
	default:
		host, path = GitHubHost, s
	}

	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	r := Repo{Host: host, Path: path}

	if !ValidHost(host) || !validPath(path) {
		return Repo{}, fmt.Errorf("%w: %q is not owner/name or the URL of a repository", ErrInvalid, s)
	}

	if host == GitHubHost && strings.Count(path, "/") != 1 {
		return Repo{}, fmt.Errorf("%w: a GitHub repository is owner/name, not %q", ErrInvalid, path)
	}

	return r, nil
}

// validPath reports whether p is two or more names that can be part of a
// repository's path. A name never starts with a dash or a dot, so a path can
// not be read as an option or climb out of a directory.
func validPath(p string) bool {
	segments := strings.Split(p, "/")
	if len(segments) < 2 {
		return false
	}

	for _, seg := range segments {
		if !segmentPattern.MatchString(seg) || strings.HasSuffix(seg, ".") {
			return false
		}
	}

	return true
}

// KnownKind is the kind of a host everyone knows; other hosts are declared by
// the owner.
func KnownKind(host string) (Kind, bool) {
	switch host {
	case GitHubHost:
		return GitHub, true
	case GitLabHost:
		return GitLab, true
	default:
		return "", false
	}
}

// ValidKind reports whether k is a kind of host.
func ValidKind(k Kind) bool { return k == GitHub || k == GitLab }

// Adapter is what differs between kinds of host.
type Adapter interface {
	// CloneURL is where git fetches the repository from.
	CloneURL(r Repo) string
	// OpenCommand is the command a worker runs to open the pull request of
	// branch into base, without its title and body.
	OpenCommand(base, branch string) string
	// RequestURL is the page of the repository's pull request number n.
	RequestURL(r Repo, n int) string
	// Env is the environment that lets git and the host's CLI act on host
	// with token: nothing is written to disk, and nothing is in a command's
	// arguments. It is empty for an empty token.
	Env(host, token string) []string
	// splitRequest reads the repository's path and the pull request's
	// number from the path of a request's URL.
	splitRequest(path string) (repo string, n int, ok bool)
}

// For returns the Adapter of a kind; ok is false for an unknown kind.
func For(k Kind) (Adapter, bool) {
	switch k {
	case GitHub:
		return github{}, true
	case GitLab:
		return gitlab{}, true
	default:
		return nil, false
	}
}

type github struct{}

func (github) CloneURL(r Repo) string { return "https://" + r.Host + "/" + r.Path + ".git" }

// gh does not find the branch's remote in a worktree of the node's mirror, so
// --head names it (found with the first real Task, issue #79).
func (github) OpenCommand(base, branch string) string {
	return "gh pr create --base " + base + " --head " + branch
}

func (github) RequestURL(r Repo, n int) string {
	return "https://" + r.Host + "/" + r.Path + "/pull/" + strconv.Itoa(n)
}

// Env gives gh its token (GH_TOKEN on github.com, GH_ENTERPRISE_TOKEN with
// GH_HOST elsewhere, as `gh help environment` says) and git the same token
// over HTTPS, as the user x-access-token.
func (github) Env(host, token string) []string {
	if token == "" {
		return nil
	}

	env := gitAuth(host, "x-access-token", token)
	if host == GitHubHost {
		return append(env, "GH_TOKEN="+token)
	}

	return append(env, "GH_ENTERPRISE_TOKEN="+token, "GH_HOST="+host)
}

var githubRequest = regexp.MustCompile(`^(.+)/pull/([0-9]+)(/.*)?$`)

func (github) splitRequest(path string) (string, int, bool) { return split(githubRequest, path) }

type gitlab struct{}

func (gitlab) CloneURL(r Repo) string { return "https://" + r.Host + "/" + r.Path + ".git" }

func (gitlab) OpenCommand(base, branch string) string {
	return "glab mr create --target-branch " + base + " --source-branch " + branch + " --yes"
}

func (gitlab) RequestURL(r Repo, n int) string {
	return "https://" + r.Host + "/" + r.Path + "/-/merge_requests/" + strconv.Itoa(n)
}

// Env gives glab its token and host (GITLAB_TOKEN, GITLAB_HOST) and git the
// same token over HTTPS, as the user oauth2.
func (gitlab) Env(host, token string) []string {
	if token == "" {
		return nil
	}

	return append(gitAuth(host, "oauth2", token), "GITLAB_TOKEN="+token, "GITLAB_HOST="+host)
}

var gitlabRequest = regexp.MustCompile(`^(.+)/-/merge_requests/([0-9]+)(/.*)?$`)

func (gitlab) splitRequest(path string) (string, int, bool) { return split(gitlabRequest, path) }

// gitAuth makes git send user and token to host and to no other, through
// configuration given in the environment (GIT_CONFIG_COUNT, git 2.31): an
// http.<url>.extraheader for https://host/ only.
func gitAuth(host, user, token string) []string {
	basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))

	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://" + host + "/.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + basic,
	}
}

func split(re *regexp.Regexp, path string) (string, int, bool) {
	m := re.FindStringSubmatch(strings.Trim(path, "/"))
	if m == nil {
		return "", 0, false
	}

	n, err := strconv.Atoi(m[2])
	if err != nil || n <= 0 {
		return "", 0, false
	}

	return m[1], n, true
}

// Request is a pull request named by a worker's evidence.
type Request struct {
	Repo   Repo
	Number int
}

var shortRequest = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9._-]*/[A-Za-z0-9_][A-Za-z0-9._-]*)#([0-9]+)$`)

// ParseRequest reads a pull request from a worker's evidence: its https URL
// on a host of the given kind, or owner/name#N, which workers reported before
// hosts existed and which means github.com. kindOf says the kind of a host,
// and false for a host that is not known.
func ParseRequest(ref string, kindOf func(host string) (Kind, bool)) (Request, bool) {
	ref = strings.TrimSpace(ref)

	if m := shortRequest.FindStringSubmatch(ref); m != nil {
		n, err := strconv.Atoi(m[2])

		return Request{Repo: Repo{Host: GitHubHost, Path: m[1]}, Number: n}, err == nil && n > 0
	}

	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return Request{}, false
	}

	host := strings.ToLower(u.Host)

	kind, ok := kindOf(host)
	if !ok {
		return Request{}, false
	}

	adapter, ok := For(kind)
	if !ok {
		return Request{}, false
	}

	path, n, ok := adapter.splitRequest(u.Path)
	if !ok || !validPath(path) {
		return Request{}, false
	}

	return Request{Repo: Repo{Host: host, Path: path}, Number: n}, true
}
