package serviceproxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHub's /repositories/{repository_id}/… (issue #802). GitHub writes the links of a paged list with the
// repository's id in place of its owner and name (`Link: <https://api.github.com/repositories/1447911125/pulls/786/
// files?page=2>; rel="next"`, read on 2026-11-18), and gh's `api --paginate` requests that link as it is
// (cli/cli pkg/cmd/api/pagination.go findNextPage, api.go's loop over pages). The REST description names no such
// path, so the catalog has none. A request to /repositories/{id}/<rest> on api.github.com that the catalog does not
// name is matched as /repos/{owner}/{repo}/<rest>, with the same method, once the id is resolved to its owner and
// name through the same Connection: GitHub's GET /repositories/{id}, which answers only for a repository the
// account can read. The request is sent on unchanged. An id that cannot be resolved stays github.unlisted and asks
// (fail closed). A GitHub Connection names no repositories: what the Bot is given is what its token reaches.
//
// The answer is kept per Connection for a while, as github-mcp-server keeps what it learned of a repository per
// identity (pkg/lockdown/lockdown.go RepoAccessCache, WithIdentity, defaultRepoAccessTTL of 20 minutes).

const (
	githubRepositoryTTL     = 20 * time.Minute
	githubRepositoriesKept  = 1024
	githubLookupTimeout     = 10 * time.Second
	githubLookupMaxResponse = 1 << 20
)

var (
	// githubRepositoryPath is /repositories/{id} and what follows it; an id is a positive integer.
	githubRepositoryPath = regexp.MustCompile(`^/repositories/([1-9][0-9]{0,18})(/.*)?$`)
	// githubNamePart is an owner's or a repository's name as GitHub allows them.
	githubNamePart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// githubRepositoryID reads a path of the form /repositories/{id}[/rest]: the id, and the rest as it was escaped.
func githubRepositoryID(escapedPath string) (id, rest string, ok bool) {
	m := githubRepositoryPath.FindStringSubmatch(escapedPath)
	if m == nil {
		return "", "", false
	}

	return m[1], m[2], true
}

// repositoryNames are the owner/name of repository ids, learned through a Connection, each kept until a time.
type repositoryNames struct {
	mu    sync.Mutex
	names map[string]repositoryName
}

type repositoryName struct {
	fullName string
	until    time.Time
}

func (r *repositoryNames) get(key string, now time.Time) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	got, ok := r.names[key]
	if !ok || !now.Before(got.until) {
		return "", false
	}

	return got.fullName, true
}

// put keeps a name; when as many are kept as allowed, the expired go, and if none has, all.
func (r *repositoryNames) put(key, fullName string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.names == nil {
		r.names = map[string]repositoryName{}
	}

	if len(r.names) >= githubRepositoriesKept {
		for k, v := range r.names {
			if !now.Before(v.until) {
				delete(r.names, k)
			}
		}

		if len(r.names) >= githubRepositoriesKept {
			clear(r.names)
		}
	}

	r.names[key] = repositoryName{fullName: fullName, until: now.Add(githubRepositoryTTL)}
}

// actionOf is the action a request is, and for a GitHub request by a repository's id, the repository it was
// resolved to ("" otherwise).
func (p *Proxy) actionOf(ctx context.Context, req *http.Request, body []byte, s session) (Action, string) {
	action := s.catalog.Of(req, body, s.host)

	if s.catalog.Integration != "github" || s.host != githubAPI || action.ID != unlisted(s.catalog.Integration).ID {
		return action, ""
	}

	id, rest, ok := githubRepositoryID(req.URL.EscapedPath())
	if !ok {
		return action, ""
	}

	fullName, ok := p.githubRepository(ctx, s, id)
	if !ok {
		return action, ""
	}

	named, err := url.Parse("/repos/" + fullName + rest)
	if err != nil {
		return action, ""
	}

	named.RawQuery = req.URL.RawQuery
	as := req.Clone(ctx)
	as.URL = named

	return s.catalog.Of(as, body, s.host), fullName
}

// githubRepository resolves a repository's id to its owner/name with GitHub's GET /repositories/{id}, read through
// the session's Connection; false when GitHub does not answer it as a repository the account can read.
func (p *Proxy) githubRepository(ctx context.Context, s session, id string) (string, bool) {
	key := s.connection() + " " + id
	if fullName, ok := p.repositories.get(key, time.Now()); ok {
		return fullName, true
	}

	fullName, reason := p.lookUpGitHubRepository(ctx, s, id)
	if reason != "" {
		p.cfg.Logger.InfoContext(ctx, "a repository's id was not resolved",
			slog.String("event", "serviceproxy.repository_unresolved"), slog.String("connection", s.connection()),
			slog.String("repository_id", id), slog.String("reason", reason))

		return "", false
	}

	p.repositories.put(key, fullName, time.Now())

	return fullName, true
}

// lookUpGitHubRepository asks GitHub for a repository by its id; reason says why it gave no owner/name.
func (p *Proxy) lookUpGitHubRepository(ctx context.Context, s session, id string) (fullName, reason string) {
	ctx, cancel := context.WithTimeout(ctx, githubLookupTimeout)
	defer cancel()

	token, err := p.cfg.Token(ctx, s.integration, s.label)
	if err != nil {
		return "", "the account's token could not be read"
	}

	// The address is GitHub's own, with the id read as the number githubRepositoryPath let through.
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return "", "the id is not a number"
	}

	target := url.URL{Scheme: "https", Host: githubAPI, Path: "/repositories/" + strconv.FormatUint(n, 10)}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), http.NoBody)
	if err != nil {
		return "", err.Error()
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", "2022-11-28")

	resp, err := p.cfg.Upstream.RoundTrip(req)
	if err != nil {
		return "", "GitHub could not be reached: " + rootCause(err)
	}

	defer resp.Body.Close() //nolint:errcheck // Only the body was read; closing adds nothing.

	if resp.StatusCode != http.StatusOK {
		return "", "GitHub answered " + strconv.Itoa(resp.StatusCode)
	}

	var got struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, githubLookupMaxResponse)).Decode(&got); err != nil {
		return "", "GitHub's answer is not a repository"
	}

	owner, name, found := strings.Cut(got.FullName, "/")

	switch {
	case strconv.FormatInt(got.ID, 10) != id:
		return "", "GitHub answered another repository"
	case !found || !githubNamePart.MatchString(owner) || !githubNamePart.MatchString(name) ||
		name == "." || name == "..":
		return "", "GitHub's answer names no owner and repository"
	}

	return got.FullName, ""
}
