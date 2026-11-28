package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/connproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// followPullRequestsEvery is how often the pull requests found on the branches of succeeded Tasks are read:
// pstack's watch-pr polls every 60 s by default (skills/poteto-mode/scripts/watch-pr/cli.ts:110) (issue #1053).
// A branch without one yet is looked up every orchestration.LookUpEvery for a short window (issue #1168).
const followPullRequestsEvery = time.Minute

// pullRequestTimeout bounds one read of a pull request, as identityTimeout bounds a read of a token's account.
const pullRequestTimeout = 10 * time.Second

// followPullRequests follows, until ctx ends, the pull requests on the branches of succeeded Tasks
// (orchestration.FollowPullRequests): every lookUp the branches without one found yet are looked up, and every
// interval all are read. A conflict or a failed required check with auto-merge armed is sent to the author as a
// follow-up, and a merge or a close wakes the coordinator (issues #1053, #1168). Every interval, after the
// reads, the queued `review` statuses are posted (orchestration.PostReviewStatuses, issue #1279), each linking its
// Task under publicURL.
func followPullRequests(ctx context.Context, svc *orchestration.Service, tokens connections, logger *slog.Logger,
	publicURL string, lookUp, every time.Duration,
) {
	statuses := tokens.reviewStatusHost(publicURL)

	looking := time.NewTicker(lookUp)
	defer looking.Stop()

	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		found := false

		select {
		case <-ctx.Done():
			return
		case <-looking.C:
		case <-tick.C:
			found = true
		}

		if err := svc.FollowPullRequests(ctx, tokens.readPullRequest, found); err != nil && ctx.Err() == nil {
			logger.WarnContext(ctx, "following the pull requests of succeeded tasks",
				slog.String("event", "controlplane.pull_requests_failed"), slog.String("error", err.Error()))
		}

		if !found {
			continue
		}

		if err := svc.PostReviewStatuses(ctx, statuses); err != nil && ctx.Err() == nil {
			logger.WarnContext(ctx, "posting the review statuses of pull requests",
				slog.String("event", "controlplane.review_statuses_failed"), slog.String("error", err.Error()))
		}
	}
}

// readPullRequest finds and reads a followed pull request with the token of its repository's Connection, the one
// its worker was given, by the Connection's route, as the account of a token is read (gitIdentities.lookup). A
// repository without a token is not read: orchestration.ErrCannotFollow, as its worker used the node's own login
// and was told to wait for the checks itself (node/runner's prompt).
func (c connections) readPullRequest(ctx context.Context, f orchestration.FollowedRequest,
) (githost.Request, githost.RequestState, error) {
	// A repository the owner's GitHub App covers is read with the App's token, which counts against the App's own
	// limit (issue #1262).
	if r, st, ok, err := c.app.FindRequest(ctx, f.Kind, f.Repo, f.Branch, f.Hint); ok {
		return r, st, err
	}

	client, token, err := c.repoClient(ctx, f.Repo)
	if err != nil {
		return githost.Request{}, githost.RequestState{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, pullRequestTimeout)
	defer cancel()

	r, st, err := githost.FindRequest(ctx, client, f.Kind, f.Repo, f.Branch, f.Hint, token)
	if errors.Is(err, githost.ErrNoRequest) {
		return githost.Request{}, githost.RequestState{}, err
	}

	if err != nil {
		return githost.Request{}, githost.RequestState{}, hideToken(err, token)
	}

	return r, st, nil
}

// reviewStatusHost reads pull requests and posts the `review` status with the token of each repository's
// Connection (issue #1279); a branch's pull request is found as readPullRequest finds it, with the GitHub App's
// token where the App covers the repository. The App asks only to read statuses (githubapp.Permissions), so the
// status is posted with the Connection's token; a repository without one gets none. A status links its Task on
// the control plane's Team page when publicURL is set.
func (c connections) reviewStatusHost(publicURL string) orchestration.ReviewStatusHost {
	return orchestration.ReviewStatusHost{
		Find: c.readPullRequest,
		Read: func(ctx context.Context, kind githost.Kind, r githost.Request) (githost.RequestState, error) {
			client, token, err := c.repoClient(ctx, r.Repo)
			if err != nil {
				return githost.RequestState{}, err
			}

			ctx, cancel := context.WithTimeout(ctx, pullRequestTimeout)
			defer cancel()

			st, err := githost.ReadRequest(ctx, client, kind, r, token)

			return st, hideToken(err, token)
		},
		Post: func(ctx context.Context, s orchestration.ReviewStatus) error {
			client, token, err := c.repoClient(ctx, s.Request.Repo)
			if err != nil {
				return err
			}

			target := ""
			if publicURL != "" {
				target = publicURL + "/team?" + url.Values{"task": {s.TaskID}}.Encode()
			}

			ctx, cancel := context.WithTimeout(ctx, pullRequestTimeout)
			defer cancel()

			return hideToken(githost.PostStatus(ctx, client, s.Kind, s.Request.Repo, s.Head, githost.Status{
				Context: orchestration.ReviewStatusContext, State: s.State, Description: s.Description,
				TargetURL: target,
			}, token), token)
		},
	}
}

// repoClient is the client and token to reach repo's host with: its Connection's token and route. A repository
// without a token is orchestration.ErrCannotFollow.
func (c connections) repoClient(ctx context.Context, repo githost.Repo) (*http.Client, string, error) {
	conn, ok, err := c.svc.ConnectionFor(ctx, repo.Host, repo.Path)
	if err != nil {
		return nil, "", err
	}

	if !ok {
		return nil, "", fmt.Errorf("%s/%s: %w", repo.Host, repo.Path, orchestration.ErrCannotFollow)
	}

	opened, err := c.vault.Open(ctx, conn.Secret)
	if err != nil {
		return nil, "", vaultError(err)
	}

	token := opened.Payload[vault.KeyValue]
	if token == "" {
		return nil, "", fmt.Errorf("%s/%s: %w", repo.Host, repo.Path, orchestration.ErrCannotFollow)
	}

	if c.identities == nil {
		return nil, "", fmt.Errorf("no route to %s", repo.Host)
	}

	way, err := c.identities.ways.For(connproxy.Route{Proxy: conn.Proxy, Dial: conn.DialAddress})
	if err != nil {
		return nil, "", err
	}

	return &http.Client{Transport: way}, token, nil
}

// hideToken keeps the token out of an error: what a host or a proxy answers may repeat it, and errors reach the log.
func hideToken(err error, token string) error {
	if err == nil || token == "" || !strings.Contains(err.Error(), token) {
		return err
	}

	return errors.New(strings.ReplaceAll(err.Error(), token, "[token]"))
}
