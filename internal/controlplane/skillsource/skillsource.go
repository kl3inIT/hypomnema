// Package skillsource fetches a skill from a public GitHub repository for the
// control plane to keep (issue #21), as Omnigent installs an agent's skills
// from `owner/repo[/path]@ref` and OpenHands records the commit a ref
// resolved to (InstallationInfo.resolved_ref).
package skillsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// MaxTarball bounds what is read of a repository's tarball: the skill is one
// folder of it, and the rest is read past without being kept.
const MaxTarball = 512 << 20

// ErrInvalid is a source that does not name a skill.
var ErrInvalid = errors.New("invalid skill source")

// ErrUnavailable is GitHub not answering, or refusing for its rate limit.
var ErrUnavailable = errors.New("github unavailable")

var (
	partPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Source is a skill's place in a repository: owner/repo[/path][@ref]. An
// empty ref is the repository's default branch.
type Source struct {
	Owner, Repo, Path, Ref string
}

// Parse reads owner/repo[/path][@ref].
func Parse(s string) (Source, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "github:")

	where, ref, _ := strings.Cut(s, "@")
	parts := strings.Split(strings.Trim(where, "/"), "/")

	if len(parts) < 2 {
		return Source{}, fmt.Errorf("%w: %q is not owner/repo[/path][@ref]", ErrInvalid, s)
	}

	for _, p := range parts {
		if !partPattern.MatchString(p) || p == "." || p == ".." {
			return Source{}, fmt.Errorf("%w: %q is not owner/repo[/path][@ref]", ErrInvalid, s)
		}
	}

	if ref != "" && (strings.ContainsAny(ref, " \t\n?#") || strings.Contains(ref, "..")) {
		return Source{}, fmt.Errorf("%w: %q is not a git ref", ErrInvalid, ref)
	}

	return Source{Owner: parts[0], Repo: parts[1], Path: strings.Join(parts[2:], "/"), Ref: ref}, nil
}

// String is the source as kept: github:owner/repo[/path]@ref.
func (s Source) String() string {
	out := "github:" + s.Owner + "/" + s.Repo
	if s.Path != "" {
		out += "/" + s.Path
	}

	ref := s.Ref
	if ref == "" {
		ref = "HEAD"
	}

	return out + "@" + ref
}

// GitHub reads public repositories. API and Codeload are GitHub's addresses
// unless a test sets them.
type GitHub struct {
	Client   *http.Client
	API      string
	Codeload string
}

// NewGitHub reads from github.com with client.
func NewGitHub(client *http.Client) *GitHub {
	return &GitHub{Client: client, API: "https://api.github.com", Codeload: "https://codeload.github.com"}
}

// Fetch resolves the source's ref to a commit and reads the skill in its
// folder at that commit, so that what is kept is the commit that is recorded.
func (g *GitHub) Fetch(ctx context.Context, src Source) (skillpack.Skill, string, error) {
	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}

	commit, err := g.commitOf(ctx, src, ref)
	if err != nil {
		return skillpack.Skill{}, "", err
	}

	resp, err := g.get(ctx, g.Codeload+"/"+src.Owner+"/"+src.Repo+"/tar.gz/"+commit, "")
	if err != nil {
		return skillpack.Skill{}, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	sk, err := skillpack.FromTarball(&limited{r: resp.Body, left: MaxTarball}, src.Path)
	if err != nil {
		return skillpack.Skill{}, "", fmt.Errorf("reading %s at %s: %w", src, commit[:12], err)
	}

	return sk, commit, nil
}

func (g *GitHub) commitOf(ctx context.Context, src Source, ref string) (string, error) {
	resp, err := g.get(ctx, g.API+"/repos/"+src.Owner+"/"+src.Repo+"/commits/"+ref, "application/vnd.github.sha")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", fmt.Errorf("%w: reading the commit of %s: %w", ErrUnavailable, src, err)
	}

	commit := strings.TrimSpace(string(b))
	if !commitPattern.MatchString(commit) {
		return "", fmt.Errorf("%w: GitHub answered no commit for %s", ErrUnavailable, src)
	}

	return commit, nil
}

// get asks GitHub without a token: a public repository needs none.
func (g *GitHub) get(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("asking GitHub: %w", err)
	}

	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	req.Header.Set("User-Agent", "hypomnema")

	resp, err := g.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return resp, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity:
		_ = resp.Body.Close()

		return nil, fmt.Errorf("%w: GitHub has no such public repository or ref (%s)", ErrInvalid, resp.Status)
	default:
		_ = resp.Body.Close()

		return nil, fmt.Errorf("%w: GitHub answered %s", ErrUnavailable, resp.Status)
	}
}

// limited fails, rather than ending early, past its bytes.
type limited struct {
	r    io.Reader
	left int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, fmt.Errorf("%w: the repository's tarball is over %d MB", ErrInvalid, MaxTarball>>20)
	}

	if int64(len(p)) > l.left {
		p = p[:l.left]
	}

	n, err := l.r.Read(p)
	l.left -= int64(n)

	return n, err
}
