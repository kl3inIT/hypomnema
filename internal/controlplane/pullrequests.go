package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/connproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// followPullRequestsEvery is how often the pull requests left with auto-merge on are read: pstack's watch-pr polls
// every 60 s by default (skills/poteto-mode/scripts/watch-pr/cli.ts:110) (issue #1053).
const followPullRequestsEvery = time.Minute

// pullRequestTimeout bounds one read of a pull request, as identityTimeout bounds a read of a token's account.
const pullRequestTimeout = 10 * time.Second

// followPullRequests follows, every interval until ctx ends, the pull requests that succeeded Tasks left with
// auto-merge on (orchestration.FollowPullRequests): a conflict or a failed required check is sent to the author
// as a follow-up, and a merge or a close wakes the coordinator (issue #1053).
func followPullRequests(ctx context.Context, svc *orchestration.Service, tokens connections, logger *slog.Logger,
	every time.Duration,
) {
	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := svc.FollowPullRequests(ctx, tokens.readPullRequest); err != nil && ctx.Err() == nil {
				logger.WarnContext(ctx, "following the pull requests left with auto-merge on",
					slog.String("event", "controlplane.pull_requests_failed"), slog.String("error", err.Error()))
			}
		}
	}
}

// readPullRequest reads a followed pull request with the token of its repository's Connection, the one its
// worker was given, by the Connection's route, as the account of a token is read (gitIdentities.lookup). A
// repository without a token is not read: orchestration.ErrCannotFollow, as its worker used the node's own login
// and was told to wait for the checks itself (node/runner's prompt).
func (c connections) readPullRequest(ctx context.Context, f orchestration.FollowedRequest,
) (githost.RequestState, error) {
	repo := f.Request.Repo

	conn, ok, err := c.svc.ConnectionFor(ctx, repo.Host, repo.Path)
	if err != nil {
		return githost.RequestState{}, err
	}

	if !ok {
		return githost.RequestState{}, fmt.Errorf("%s/%s: %w", repo.Host, repo.Path, orchestration.ErrCannotFollow)
	}

	opened, err := c.vault.Open(ctx, conn.Secret)
	if err != nil {
		return githost.RequestState{}, vaultError(err)
	}

	token := opened.Payload[vault.KeyValue]
	if token == "" {
		return githost.RequestState{}, fmt.Errorf("%s/%s: %w", repo.Host, repo.Path, orchestration.ErrCannotFollow)
	}

	if c.identities == nil {
		return githost.RequestState{}, fmt.Errorf("no route to %s", repo.Host)
	}

	way, err := c.identities.ways.For(connproxy.Route{Proxy: conn.Proxy, Dial: conn.DialAddress})
	if err != nil {
		return githost.RequestState{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, pullRequestTimeout)
	defer cancel()

	st, err := githost.ReadRequest(ctx, &http.Client{Transport: way}, f.Kind, f.Request, token)
	if err != nil {
		// What a host or a proxy answers may repeat the token: it never reaches the log.
		return githost.RequestState{}, errors.New(strings.ReplaceAll(err.Error(), token, "[token]"))
	}

	return st, nil
}
