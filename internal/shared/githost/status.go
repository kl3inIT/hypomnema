package githost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// StatusState is a commit status's state.
type StatusState string

// The states the control plane posts.
const (
	StatusSuccess StatusState = "success"
	StatusFailure StatusState = "failure"
)

// Status is a commit status: Context names it ("review"), as a required check names it.
type Status struct {
	Context     string
	State       StatusState
	Description string
	TargetURL   string
}

// maxStatusDescription is the longest description GitHub keeps on a commit status (140 characters; a longer one
// is refused with 422); GitLab's is cut the same.
const maxStatusDescription = 140

// PostStatus posts s on commit sha of repo, with token (issue #1279). On GitHub it is POST
// /repos/:owner/:repo/statuses/:sha (state, context, description, target_url), the commit status
// claude-code-action's agent-approval-check posts and a required check reads, under api.github.com or a GitHub
// Enterprise host's /api/v3; on GitLab POST /projects/:id/statuses/:sha (state "failed" for a failure, name for
// the context). sha must be a full SHA. As ReadRequest, the token goes to the host alone and an error says only
// the host's status.
func PostStatus(ctx context.Context, client *http.Client, kind Kind, repo Repo, sha string, s Status,
	token string,
) error {
	if token == "" {
		return fmt.Errorf("posting a commit status on %s: there is no token", repo.Host)
	}

	description := s.Description
	if runes := []rune(description); len(runes) > maxStatusDescription {
		description = string(runes[:maxStatusDescription-1]) + "…"
	}

	var (
		u    *url.URL
		body map[string]string
	)

	switch kind {
	case GitHub:
		u = &url.URL{Scheme: "https", Host: "api.github.com", Path: "/repos/" + repo.Path + "/statuses/" + sha}
		if repo.Host != GitHubHost {
			u = &url.URL{Scheme: "https", Host: repo.Host, Path: "/api/v3/repos/" + repo.Path + "/statuses/" + sha}
		}

		body = map[string]string{"state": string(s.State), "context": s.Context, "description": description}
	case GitLab:
		state := string(s.State)
		if s.State == StatusFailure {
			state = "failed"
		}

		u = &url.URL{
			Scheme: "https", Host: repo.Host,
			Path:    "/api/v4/projects/" + repo.Path + "/statuses/" + sha,
			RawPath: "/api/v4/projects/" + url.PathEscape(repo.Path) + "/statuses/" + sha,
		}
		body = map[string]string{"state": state, "name": s.Context, "description": description}
	default:
		return fmt.Errorf("unknown kind of host %q", kind)
	}

	if s.TargetURL != "" {
		body["target_url"] = s.TargetURL
	}

	sent, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(sent))
	if err != nil {
		return fmt.Errorf("posting a commit status on %s: %w", repo.Host, err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	// A redirect is not followed: a status is posted where it was asked, and the token goes nowhere else.
	once := *client
	once.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := once.Do(req)
	if err != nil {
		return fmt.Errorf("posting a commit status on %s: %w", repo.Host, transportError(err))
	}
	defer resp.Body.Close()                                      //nolint:errcheck // the answer is not read
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) // let the connection be reused

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s answered HTTP %d to a commit status", repo.Host, resp.StatusCode)
	}

	return nil
}
