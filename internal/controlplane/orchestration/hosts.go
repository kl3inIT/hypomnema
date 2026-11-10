package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// Git hosts and repositories (issue #44). github.com and gitlab.com are known;
// the owner declares a company's server once with its kind. A repository can
// be registered under a name, so a Run names it shortly and gets its base
// branch. What a host is and how its kind behaves is in internal/shared/githost.

// GitHost is a host the owner declared, or one everyone knows.
type GitHost struct {
	Host string
	Kind githost.Kind
	// Known is true for github.com and gitlab.com, which need no declaration.
	Known bool
	// Connections are the labels of the host's Connections, oldest first;
	// empty when the owner gave it none.
	Connections []string
	CreatedAt   time.Time
}

// HasToken says whether the owner gave the host a Connection.
func (h GitHost) HasToken() bool { return len(h.Connections) > 0 }

// Repo is a repository registered under a name.
type Repo struct {
	Name string
	Host string
	Path string
	// Base is the branch worktrees start from when a Run names none.
	Base string
	// Connection is the label of the host's Connection work on it uses;
	// empty for the host's only one.
	Connection string
	CreatedAt  time.Time
}

var repoNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// AddGitHost declares a host and its kind, or changes the kind of a declared
// one. github.com and gitlab.com are known and cannot be declared otherwise.
func (s *Service) AddGitHost(ctx context.Context, host string, kind githost.Kind) (GitHost, error) {
	if !githost.ValidHost(host) || !githost.ValidKind(kind) {
		return GitHost{}, fmt.Errorf("%w: a host is a name such as gitlab.company.example, of kind github or gitlab",
			ErrInvalid)
	}

	if known, ok := githost.KnownKind(host); ok && known != kind {
		return GitHost{}, fmt.Errorf("%w: %s is %s", ErrInvalid, host, known)
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.UpsertGitHost(ctx, store.UpsertGitHostParams{Host: host, Kind: string(kind), Now: now})
	})
	if err != nil {
		return GitHost{}, fmt.Errorf("declaring host %s: %w", host, err)
	}

	return s.GitHostOf(ctx, host)
}

// RemoveGitHost forgets a declared host. It is refused while an open Run or a
// registered repository is on it, and while it still has a Connection: the
// vault holds its credential, so whoever removes a host removes those first.
func (s *Service) RemoveGitHost(ctx context.Context, host string) error {
	if _, known := githost.KnownKind(host); known {
		return fmt.Errorf("%w: %s is always known", ErrInvalid, host)
	}

	return s.write(ctx, func(q *store.Queries, _ int64) error {
		if rows, err := q.ListConnectionsOf(ctx, host); err != nil {
			return fmt.Errorf("listing the connections of %s: %w", host, err)
		} else if len(rows) > 0 {
			return fmt.Errorf("%w: %s still has %d connections", ErrConflict, host, len(rows))
		}

		runs, err := q.CountOpenRunsOnHost(ctx, nullString(host))
		if err != nil {
			return fmt.Errorf("counting the runs on %s: %w", host, err)
		}

		repos, err := q.CountReposOnHost(ctx, host)
		if err != nil {
			return fmt.Errorf("counting the repositories on %s: %w", host, err)
		}

		if runs > 0 || repos > 0 {
			return fmt.Errorf("%w: %s has %d open runs and %d registered repositories", ErrConflict, host, runs, repos)
		}

		n, err := q.DeleteGitHost(ctx, host)
		if err != nil {
			return fmt.Errorf("removing host %s: %w", host, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: host %s is not declared", ErrNotFound, host)
		}

		return nil
	})
}

// ListGitHosts returns the hosts a Run can be on: the known ones, then the
// declared ones by name.
func (s *Service) ListGitHosts(ctx context.Context) ([]GitHost, error) {
	rows, err := s.read().ListGitHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing hosts: %w", err)
	}

	conns, err := s.read().ListConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing connections: %w", err)
	}

	out := []GitHost{
		{Host: githost.GitHubHost, Kind: githost.GitHub, Known: true},
		{Host: githost.GitLabHost, Kind: githost.GitLab, Known: true},
	}

	for _, r := range rows {
		// A row of a known host is left from when it named the host's token.
		if _, known := githost.KnownKind(r.Host); known {
			continue
		}

		out = append(out, gitHostFromRow(r))
	}

	for i := range out {
		for _, c := range conns {
			if c.Integration == out[i].Host {
				out[i].Connections = append(out[i].Connections, c.Label)
			}
		}
	}

	return out, nil
}

func gitHostFromRow(r store.GitHost) GitHost {
	return GitHost{Host: r.Host, Kind: githost.Kind(r.Kind), CreatedAt: millis(r.CreatedAt)}
}

// GitHostOf returns a host a Run can be on, with the labels of its
// Connections; ErrInvalid for a host that is not known.
func (s *Service) GitHostOf(ctx context.Context, host string) (GitHost, error) {
	q := s.read()

	h, err := gitHostOf(ctx, q, host)
	if err != nil {
		return GitHost{}, err
	}

	rows, err := q.ListConnectionsOf(ctx, host)
	if err != nil {
		return GitHost{}, fmt.Errorf("listing the connections of %s: %w", host, err)
	}

	for _, r := range rows {
		h.Connections = append(h.Connections, r.Label)
	}

	return h, nil
}

// gitHostOf returns a host a Run can be on, without its Connections;
// ErrInvalid names the command that declares one that is not known.
func gitHostOf(ctx context.Context, q *store.Queries, host string) (GitHost, error) {
	if kind, ok := githost.KnownKind(host); ok {
		return GitHost{Host: host, Kind: kind, Known: true}, nil
	}

	row, err := q.GetGitHost(ctx, host)

	if errors.Is(err, sql.ErrNoRows) {
		return GitHost{}, fmt.Errorf("%w: the host %s is not known; declare it with `hypomnema hosts add %s --kind gitlab`",
			ErrInvalid, host, host)
	}

	if err != nil {
		return GitHost{}, fmt.Errorf("reading host %s: %w", host, err)
	}

	return gitHostFromRow(row), nil
}

// KindOfHost says the kind of a host a Run can be on, and false for another.
func (s *Service) KindOfHost(ctx context.Context, host string) (githost.Kind, bool) {
	h, err := gitHostOf(ctx, s.read(), host)

	return h.Kind, err == nil
}

// AddRepo registers a repository under a name, or changes what the name means.
// The repository is given as a Run takes it: owner/name or a URL on a host
// that is known. connection, when given, is the label of the host's Connection
// work on it uses.
func (s *Service) AddRepo(ctx context.Context, name, repo, base, connection string) (Repo, error) {
	if !repoNamePattern.MatchString(name) {
		return Repo{}, fmt.Errorf("%w: a repository's name is lower-case letters, digits, dots and dashes", ErrInvalid)
	}

	parsed, err := githost.Parse(repo)
	if err != nil {
		return Repo{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := gitHostOf(ctx, q, parsed.Host); err != nil {
			return err
		}

		if connection != "" {
			_, err := q.GetConnection(ctx, store.GetConnectionParams{Integration: parsed.Host, Label: connection})
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s has no connection %s", ErrInvalid, parsed.Host, connection)
			}

			if err != nil {
				return fmt.Errorf("reading connection %s:%s: %w", parsed.Host, connection, err)
			}
		}

		return q.UpsertRepo(ctx, store.UpsertRepoParams{
			Name: name, Host: parsed.Host, Path: parsed.Path, Base: nullString(base),
			Connection: nullString(connection), Now: now,
		})
	})
	if err != nil {
		return Repo{}, fmt.Errorf("registering repository %s: %w", name, err)
	}

	row, err := s.read().GetRepo(ctx, name)
	if err != nil {
		return Repo{}, notFound(err, "repository", name)
	}

	return repoFromRow(row), nil
}

// RemoveRepo forgets a registered repository. Runs that named it keep their
// host and path.
func (s *Service) RemoveRepo(ctx context.Context, name string) error {
	return s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.DeleteRepo(ctx, name)
		if err != nil {
			return fmt.Errorf("removing repository %s: %w", name, err)
		}

		if n == 0 {
			return fmt.Errorf("%w: repository %s is not registered", ErrNotFound, name)
		}

		return nil
	})
}

// ListRepos returns the registered repositories by name.
func (s *Service) ListRepos(ctx context.Context) ([]Repo, error) {
	rows, err := s.read().ListRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing repositories: %w", err)
	}

	out := make([]Repo, 0, len(rows))
	for _, r := range rows {
		out = append(out, repoFromRow(r))
	}

	return out, nil
}

func repoFromRow(r store.Repo) Repo {
	return Repo{
		Name: r.Name, Host: r.Host, Path: r.Path, Base: r.Base.String, Connection: r.Connection.String,
		CreatedAt: millis(r.CreatedAt),
	}
}

// resolveRepo turns what a Run was given into a host, a path and the base its
// registered repository names: a registered name first, else owner/name or a
// URL. The host must be known.
func (s *Service) resolveRepo(ctx context.Context, q *store.Queries, given string) (githost.Repo, string, error) {
	if repoNamePattern.MatchString(given) {
		row, err := q.GetRepo(ctx, given)
		if err == nil {
			return githost.Repo{Host: row.Host, Path: row.Path}, row.Base.String, nil
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return githost.Repo{}, "", fmt.Errorf("reading repository %s: %w", given, err)
		}
	}

	parsed, err := githost.Parse(given)
	if err != nil {
		return githost.Repo{}, "", fmt.Errorf("%w: %w; or name a repository registered with `hypomnema repos add`",
			ErrInvalid, err)
	}

	if _, err := gitHostOf(ctx, q, parsed.Host); err != nil {
		return githost.Repo{}, "", err
	}

	return parsed, "", nil
}
