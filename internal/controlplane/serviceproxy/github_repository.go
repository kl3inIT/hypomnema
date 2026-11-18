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

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
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
	// githubOwner is an account's login as GitHub allows it: letters, digits and hyphens, and the underscore of a
	// managed user's suffix; never a dot, so never "." or "..", which the /repos form refuses as a step out of the path.
	githubOwner = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	// githubNamePart is a repository's name as GitHub allows it; "." and ".." are refused beside it.
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

// byID is the repository a GitHub request named by its id was resolved to: its id, and its owner/name as it was
// kept or just read (Cached). Empty for any other request.
type byID struct {
	ID, FullName string
	Cached       bool
}

// actionOf is the action a request is, and for a GitHub request by a repository's id, the repository it was
// resolved to.
//
// The lookup is itself a read of the repository, the one GET /repos/{owner}/{repo} is: it is decided as that read
// is before it is sent (review of #813). Only when the Connection allows that read, by the owner's answer or a grant
// for the session or the Routine, is the id looked up, or its kept name used. Otherwise nothing is sent: the request
// stays unlisted, with the read beside it (With), so that a read set to never refuses it and one that asks is shown
// to the owner with it.
func (p *Proxy) actionOf(ctx context.Context, req *http.Request, body []byte, s session) (Action, byID) {
	action := s.catalog.Of(req, body, s.host)

	if s.catalog.Integration != "github" || s.host != githubAPI || action.ID != unlisted(s.catalog.Integration).ID {
		return action, byID{}
	}

	id, rest, ok := githubRepositoryID(req.URL.EscapedPath())
	if !ok {
		return action, byID{}
	}

	lookup := s.catalog.Match(http.MethodGet, githubAPI, "/repos/owner/repo")

	if allowed, err := p.allows(ctx, lookup, s); err != nil || !allowed {
		action.With = []Action{lookup}

		return action, byID{}
	}

	repo, ok := p.githubRepository(ctx, s, id)
	if !ok {
		return action, byID{}
	}

	named, err := url.Parse("/repos/" + repo.FullName + rest)
	if err != nil {
		return action, byID{}
	}

	named.RawQuery = req.URL.RawQuery
	as := req.Clone(ctx)
	as.URL = named

	return s.catalog.Of(as, body, s.host), repo
}

// allows is whether an action runs for the session's Connection without asking anyone: the owner allowed it, or a
// grant for the session or the Routine stands.
func (p *Proxy) allows(ctx context.Context, action Action, s session) (bool, error) {
	got, err := p.answerOf(ctx, action, s)

	switch {
	case err != nil:
		return false, err
	case got.refusal != "" || got.policy == orchestration.PolicyNever:
		return false, nil
	case got.policy == orchestration.PolicyAllow:
		return true, nil
	}

	return p.cfg.Store.StandingGrant(ctx, orchestration.NewApproval{
		Integration: s.integration, Label: s.label, Action: action.ID, DispatchID: s.caller.DispatchID,
		ChatID: s.chatID, RunKey: s.runKey,
	})
}

// githubRepository resolves a repository's id to its owner/name: the name kept for the Connection, or GitHub's
// GET /repositories/{id} read through it. false when GitHub does not answer it as a repository the account can
// read.
func (p *Proxy) githubRepository(ctx context.Context, s session, id string) (byID, bool) {
	if fullName, ok := p.repositories.get(s.connection()+" "+id, time.Now()); ok {
		return byID{ID: id, FullName: fullName, Cached: true}, true
	}

	fullName, ok := p.freshGitHubRepository(ctx, s, id)

	return byID{ID: id, FullName: fullName}, ok
}

// freshGitHubRepository reads a repository's owner/name from GitHub now, and keeps it.
func (p *Proxy) freshGitHubRepository(ctx context.Context, s session, id string) (string, bool) {
	fullName, reason := p.lookUpGitHubRepository(ctx, s, id)
	if reason != "" {
		p.cfg.Logger.InfoContext(ctx, "a repository's id was not resolved",
			slog.String("event", "serviceproxy.repository_unresolved"), slog.String("connection", s.connection()),
			slog.String("repository_id", id), slog.String("reason", reason))

		return "", false
	}

	p.repositories.put(s.connection()+" "+id, fullName, time.Now())

	return fullName, true
}

// shown is how the owner is told which repository a request by its id is, when it asks (review of #813): its name
// read from GitHub now, not a kept one, which may be from before a rename or a transfer; when it cannot be read now,
// only its id.
func (p *Proxy) shown(ctx context.Context, s session, repo byID) string {
	fullName, ok := repo.FullName, !repo.Cached
	if !ok {
		fullName, ok = p.freshGitHubRepository(ctx, s, repo.ID)
	}

	if !ok {
		return "the repository with id " + repo.ID + " (its name could not be read now)"
	}

	return fullName + " (id " + repo.ID + ")"
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
	case !found || !githubOwner.MatchString(owner) || !githubNamePart.MatchString(name) ||
		name == "." || name == "..":
		return "", "GitHub's answer names no owner and repository"
	}

	return got.FullName, ""
}
