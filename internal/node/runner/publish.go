package runner

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/bridge"
	"github.com/kl3inIT/hypomnema/internal/node/gitssh"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

func canPublish(o nodeproto.Offer) bool {
	return o.Repo != "" && o.RepoHost == githost.GitHubHost && (!o.RoleRouted || o.Profile == "implementer")
}

// pushBranch is the branch the Task's commits go to: the branch of the Task it continues, else its own (issue #690).
func pushBranch(o nodeproto.Offer) string {
	if o.PushBranch != "" {
		return o.PushBranch
	}
	return worktree.Branch(o.TaskID)
}

// startBranch is the branch the Task's worktree starts from: the Run's base, or the branch it continues or stacks on.
func startBranch(o nodeproto.Offer) string {
	if o.StartFrom != "" {
		return o.StartFrom
	}
	return o.Base
}

// prBase is the base of the pull request the Task opens.
func prBase(o nodeproto.Offer) string {
	if o.PRBase != "" {
		return o.PRBase
	}
	return o.Base
}

func githubPushRemote(o nodeproto.Offer) bool {
	if o.RepoSSHURL == "" {
		return true
	}
	remote := o.RepoSSHURL
	if strings.HasPrefix(remote, "ssh://") {
		parsed, err := url.Parse(remote)
		if err != nil {
			return false
		}
		switch parsed.Host {
		case "github.com", "github.com:22", "ssh.github.com:443":
			parsed.Scheme, parsed.Host, parsed.User = "https", githost.GitHubHost, nil
			remote = parsed.String()
		default:
			return false
		}
	}
	repo, err := githost.Parse(remote)
	return err == nil && repo.Host == githost.GitHubHost && strings.EqualFold(repo.Path, o.Repo)
}

func (s *Supervisor) repoSource(o nodeproto.Offer) (worktree.Source, error) {
	src, err := worktree.SourceOf(o.RepoHost, o.RepoKind, o.Repo, o.RepoToken)
	src.Identity = githost.Identity{Name: o.GitName, Email: o.GitEmail}
	if err != nil || o.RepoSSHURL == "" {
		return src, err
	}
	if s.URL == "" || o.AgentKey == "" {
		return worktree.Source{}, errors.New("the repository is cloned over SSH, but the node has no key or control plane " +
			"address to ask for the SSH key with")
	}
	return src.OverSSH(o.RepoSSHURL, gitssh.Env(s.Exe, s.URL, o.AgentKey))
}

func (s *Supervisor) relayPublish(ctx context.Context, w *Worker) {
	if !canPublish(w.offer) {
		return
	}
	dir := bridge.Dir(w.dir)
	requests, err := bridge.PublishRequests(dir)
	if err != nil {
		w.Report(ctx, err)
		return
	}
	for _, id := range requests {
		if ctx.Err() != nil {
			return
		}
		result, done, err := bridge.Published(dir, id)
		if err != nil {
			w.Report(ctx, err)
			continue
		}
		if !done {
			message, err := s.publishBranch(ctx, w)
			result = bridge.PublishResult{Message: message, Failed: err != nil}
			if err != nil {
				result.Message = err.Error()
			}
		}
		w.Report(ctx, bridge.CompletePublish(dir, id, result))
	}
}

func (s *Supervisor) publishBranch(ctx context.Context, w *Worker) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	o := w.offer
	if !githubPushRemote(o) {
		return "", errors.New("the configured SSH destination does not match this Run's GitHub repository")
	}
	src, err := s.repoSource(o)
	if err != nil {
		return "", err
	}
	branch := pushBranch(o)
	sha, err := worktree.Publish(ctx, src, o.Worktree, o.TaskID, branch)
	if err != nil {
		return "", err
	}
	if sha == "" {
		return "The branch is already up to date. No new push evidence was recorded.", nil
	}
	if err := w.r.PublishedHead(ctx, nodeproto.PublishedHead{Branch: branch, SHA: sha}); err != nil {
		return "", fmt.Errorf("pushed %s but could not queue its push evidence: %w", branch, err)
	}
	return "Pushed " + branch + " at " + sha + ".", nil
}
