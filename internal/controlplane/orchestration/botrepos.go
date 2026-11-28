package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// A Bot's repositories (issue #1334): the repositories whose work it owns, so that a Run created on one goes to its
// chat (run_custody.go, routeRun). It is Paperclip's project lead (packages/db/src/schema/projects.ts, leadAgentId),
// bound by repository at the owner's word (owner, 2026-11-28: "việc code cũng phải giao cho đúng bot"). A repository
// has at most one owning Bot, as a Paperclip issue has one assignee and a second checkout is a conflict
// (docs/api/issues.md, "Checkout"); the main Bot holds none and takes the Runs of a repository no Bot owns, as
// OpenClaw's default agent main takes what no binding matches (docs/concepts/multi-agent.md). Removing a Bot frees
// its repositories (bot_repos.bot_id, ON DELETE CASCADE).

// SetBotRepos replaces the repositories a Bot owns with those named, each as `runs create --repo` takes it: owner/name
// on github.com, a repository's https URL on a declared host, or a name registered with `hypomnema repos add`. Only the
// owner calls it (clientapi policy). An empty list frees them all. The main Bot owns none. A repository another Bot
// owns is refused, naming that Bot, and nothing changes.
func (s *Service) SetBotRepos(ctx context.Context, botID string, repos []string) (Bot, error) {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetBot(ctx, botID)
		if err != nil {
			return notFound(err, "bot", botID)
		}

		owned, err := s.resolveBotRepos(ctx, q, repos)
		if err != nil {
			return err
		}

		if len(owned) > 0 && botID == DefaultBotID {
			return fmt.Errorf("%w: the main bot owns no repository; it takes the Runs of those no other bot owns",
				ErrInvalid)
		}

		if err := q.DeleteReposOfBot(ctx, botID); err != nil {
			return fmt.Errorf("letting go of the repositories of bot %s: %w", row.Name, err)
		}

		for _, r := range owned {
			if err := claimRepo(ctx, q, now, botID, r); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return Bot{}, fmt.Errorf("setting the repositories of bot %s: %w", botID, err)
	}

	return s.GetBot(ctx, botID)
}

// resolveBotRepos reads each repository named as a Run would (resolveRepo), once each, in the order given.
func (s *Service) resolveBotRepos(ctx context.Context, q *store.Queries, given []string) ([]githost.Repo, error) {
	out := make([]githost.Repo, 0, len(given))

	for _, g := range given {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}

		r, _, err := s.resolveRepo(ctx, q, g)
		if err != nil {
			return nil, err
		}

		// One repository, however it is spelled: hosts compare paths without case (bot_repos.path).
		if slices.ContainsFunc(out, func(o githost.Repo) bool {
			return o.Host == r.Host && strings.EqualFold(o.Path, r.Path)
		}) {
			continue
		}

		out = append(out, r)
	}

	return out, nil
}

// claimRepo records that bot owns r; a repository another Bot owns is a conflict that names it.
func claimRepo(ctx context.Context, q *store.Queries, now int64, bot string, r githost.Repo) error {
	holder, err := q.OwnerOfRepo(ctx, store.OwnerOfRepoParams{Host: r.Host, Path: r.Path})

	switch {
	case err == nil:
		return fmt.Errorf("%w: %s belongs to bot %s (%s); take it from that bot first", ErrConflict, r, holder.Name,
			holder.BotID)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("reading the bot that owns %s: %w", r, err)
	}

	err = q.InsertBotRepo(ctx, store.InsertBotRepoParams{Host: r.Host, Path: r.Path, BotID: bot, Now: now})
	if err != nil {
		return fmt.Errorf("recording that bot %s owns %s: %w", bot, r, err)
	}

	return nil
}

// botRepoNames are the repositories of a bot_repos row each, as the owner writes them (githost.Repo.String).
func botRepoNames(rows []store.BotRepo) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, githost.Repo{Host: r.Host, Path: r.Path}.String())
	}

	return out
}

// reposOfBots are every Bot's repositories, by the Bot's id.
func reposOfBots(ctx context.Context, q *store.Queries) (map[string][]string, error) {
	rows, err := q.ListBotRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the bots' repositories: %w", err)
	}

	byBot := map[string][]store.BotRepo{}
	for _, r := range rows {
		byBot[r.BotID] = append(byBot[r.BotID], r)
	}

	out := make(map[string][]string, len(byBot))
	for bot, rs := range byBot {
		out[bot] = botRepoNames(rs)
	}

	return out, nil
}
