package serviceproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// RepoToken gives a token of the owner's GitHub App's installation on a repository (owner/name) of a git host's
// Integration, and false when the App does not cover it (issue #1262): the control plane's own wiring.
type RepoToken func(ctx context.Context, integration, repository string) (string, bool)

// githubRepositoryOf is the repository a request to GitHub's API names, as owner/name, when it names one: REST's
// /repos/{owner}/{repo}/…, or a GraphQL body whose variables are owner with repo or name, as gh sends them (its
// queries take $owner and $repo; issue #1262). A request by a repository's id, or one that names none, names none.
func githubRepositoryOf(req *http.Request, body []byte) (string, bool) {
	if req.Method == http.MethodPost && req.URL.Path == "/graphql" {
		var sent struct {
			Variables map[string]any `json:"variables"`
		}

		if json.Unmarshal(body, &sent) != nil {
			return "", false
		}

		owner, _ := sent.Variables["owner"].(string)

		name, _ := sent.Variables["repo"].(string)
		if name == "" {
			name, _ = sent.Variables["name"].(string)
		}

		return ownerRepo(owner, name)
	}

	rest, ok := strings.CutPrefix(req.URL.Path, "/repos/")
	if !ok {
		return "", false
	}

	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return "", false
	}

	return ownerRepo(parts[0], parts[1])
}

func ownerRepo(owner, name string) (string, bool) {
	if !githubPathPart(owner) || !githubPathPart(name) {
		return "", false
	}

	return strings.ToLower(owner + "/" + name), true
}

// appToken is the owner's GitHub App's token for a request to GitHub's API that names a repository the App is
// installed on: the Bot's gh then counts against the App's limit, not the owner's (issue #1262). Any other request
// goes with its Connection's token, as before.
func (p *Proxy) appToken(ctx context.Context, req *http.Request, body []byte, s session) (string, bool) {
	if p.cfg.RepoToken == nil || s.host != githubAPI {
		return "", false
	}

	repo, ok := githubRepositoryOf(req, body)
	if !ok {
		return "", false
	}

	return p.cfg.RepoToken(ctx, s.integration, repo)
}

// AppReview is a review the owner's GitHub App made on a pull request, as GitHub answered the POST that made it: the
// chat and Bot whose gh sent it, the repository (owner/name in lower case), the pull request, GitHub's id of the
// review and its address (issue #1232, review round 2 of PR #1323).
type AppReview struct {
	Chat       string
	Bot        string
	Repository string
	Number     int
	ID         int64
	URL        string
}

// AppReviewed records a review the App made through the proxy: the control plane's own wiring, which ties it to the
// Inbox attempt of that chat, as the agent API's send tools record what they sent (RecordSent).
type AppReviewed func(ctx context.Context, r AppReview)

// reviewPath is GitHub's POST /repos/{owner}/{repo}/pulls/{pull_number}/reviews.
var reviewPath = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/([0-9]+)/reviews$`)

// maxReviewAnswer bounds what is read of GitHub's answer to a review made.
const maxReviewAnswer = 1 << 20

// appReviewed hands on a review the App's token made (a 2xx answer to POST .../pulls/{n}/reviews) to the
// AppReviewed hook, by GitHub's id of it, and gives resp back with its body as it was. Nothing else is read.
func (p *Proxy) appReviewed(ctx context.Context, req *http.Request, resp *http.Response, s session) {
	m := reviewPath.FindStringSubmatch(req.URL.Path)
	if p.cfg.AppReviewed == nil || req.Method != http.MethodPost || m == nil ||
		resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
		return
	}

	repo, ok := ownerRepo(m[1], m[2])
	number, err := strconv.Atoi(m[3])

	if !ok || err != nil || number <= 0 {
		return
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReviewAnswer))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Del("Content-Length")
	resp.TransferEncoding = nil

	var made struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}

	if err != nil || json.Unmarshal(raw, &made) != nil || made.ID <= 0 ||
		!strings.HasPrefix(made.HTMLURL, "https://github.com/") {
		p.cfg.Logger.WarnContext(ctx, "a review the GitHub App made could not be read",
			slog.String("event", "serviceproxy.app_review_unread"), slog.String("repository", repo))

		return
	}

	p.cfg.AppReviewed(ctx, AppReview{
		Chat: s.chatID, Bot: s.caller.Bot, Repository: repo, Number: number, ID: made.ID, URL: made.HTMLURL,
	})
}
