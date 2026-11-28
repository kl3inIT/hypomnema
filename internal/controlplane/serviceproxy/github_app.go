package serviceproxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
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

var codeRabbitCommentPath = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/[1-9][0-9]*/comments$`)

// codeRabbitTrigger is the exact REST comment approved for the coordinator's review rounds (issue #1339).
func codeRabbitTrigger(req *http.Request, body []byte, s session) bool {
	if !s.caller.Coordinator || s.caller.ReadRun || s.runKey != "" || s.integration != GitHubID ||
		s.host != githubAPI || req.Method != http.MethodPost || req.URL.RawQuery != "" {
		return false
	}

	parts := codeRabbitCommentPath.FindStringSubmatch(req.URL.EscapedPath())
	if parts == nil || !githubPathPart(parts[1]) || !githubPathPart(parts[2]) {
		return false
	}

	var comment map[string]json.RawMessage
	var message string

	return json.Unmarshal(body, &comment) == nil && len(comment) == 1 &&
		json.Unmarshal(comment["body"], &message) == nil && message == "@coderabbitai review"
}

// appToken is the owner's GitHub App's token for a request to GitHub's API that names a repository the App is
// installed on: the Bot's gh then counts against the App's limit, not the owner's (issue #1262). Any other request
// goes with its Connection's token, as before.
func (p *Proxy) appToken(ctx context.Context, req *http.Request, body []byte, s session) (string, bool) {
	if p.cfg.RepoToken == nil || s.host != githubAPI || codeRabbitTrigger(req, body, s) {
		return "", false
	}

	repo, ok := githubRepositoryOf(req, body)
	if !ok {
		return "", false
	}

	return p.cfg.RepoToken(ctx, s.integration, repo)
}

// AppReview is a review the owner's GitHub App makes on a pull request through the proxy (issue #1232; review rounds
// 2 and 3 of PR #1323): the chat and Bot of the pi token whose gh sends it, the Inbox attempt fixed before its POST
// went out, the repository (owner/name in lower case) and the pull request; and, once GitHub answered, GitHub's id
// of the review and its address.
type AppReview struct {
	Chat       string
	Bot        string
	Attempt    string
	Repository string
	Number     int
	ID         int64
	URL        string
}

// AppReviews ties the reviews the App makes through the proxy to the Inbox attempt they are made in: the control
// plane's own wiring, as the agent API's send tools record what they send in the item's attempt (RecordSent).
type AppReviews interface {
	// AppReviewAttempt is the attempt a review made now from chat by bot on the pull request would be the work of:
	// read before the POST goes out, so that an answer that comes late never counts for a later attempt. "" for none.
	AppReviewAttempt(ctx context.Context, chat, bot, repository string, number int) (string, error)
	// AppReviewed records a review GitHub made, in the attempt read before its POST.
	AppReviewed(ctx context.Context, r AppReview)
}

// reviewPath is GitHub's POST /repos/{owner}/{repo}/pulls/{pull_number}/reviews.
var reviewPath = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/([0-9]+)/reviews$`)

// maxReviewAnswer bounds what is read of GitHub's answer to a review made, and of it unpacked.
const maxReviewAnswer = 1 << 20

// reviewAttempt is the review a request is about to make with the App's token, with the attempt it would be the work
// of, and false when it is none: a POST .../pulls/{n}/reviews by a pi token, which names its chat and its Bot (a
// Routine's script or a shared coordinator token names neither), in an attempt of that chat.
func (p *Proxy) reviewAttempt(ctx context.Context, req *http.Request, s session) (AppReview, bool) {
	m := reviewPath.FindStringSubmatch(req.URL.Path)
	if p.cfg.AppReviews == nil || req.Method != http.MethodPost || m == nil || s.caller.ChatID == "" ||
		s.caller.Bot == "" {
		return AppReview{}, false
	}

	repo, ok := ownerRepo(m[1], m[2])
	number, err := strconv.Atoi(m[3])

	if !ok || err != nil || number <= 0 {
		return AppReview{}, false
	}

	r := AppReview{Chat: s.caller.ChatID, Bot: s.caller.Bot, Repository: repo, Number: number}

	r.Attempt, err = p.cfg.AppReviews.AppReviewAttempt(ctx, r.Chat, r.Bot, repo, number)
	if err != nil {
		p.cfg.Logger.WarnContext(ctx, "the attempt of a review the GitHub App makes could not be read",
			slog.String("event", "serviceproxy.app_review_unread"), slog.String("repository", repo),
			slog.String("error", err.Error()))

		return AppReview{}, false
	}

	return r, r.Attempt != ""
}

// appReviewed hands on a review the App's token made (a 2xx answer to the POST reviewAttempt read) to AppReviews,
// by GitHub's id of it, and gives resp back with its body as GitHub sent it: what gh asked for, gzip included, is
// what it gets. A copy is unpacked by its Content-Encoding to be read (review round 3 of PR #1323).
func (p *Proxy) appReviewed(ctx context.Context, resp *http.Response, r AppReview) {
	if resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
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

	if err == nil {
		var plain []byte

		plain, err = unpacked(raw, resp.Header.Get("Content-Encoding"))
		if err == nil {
			err = json.Unmarshal(plain, &made)
		}
	}

	if err != nil || made.ID <= 0 || !strings.HasPrefix(made.HTMLURL, "https://github.com/") {
		p.cfg.Logger.WarnContext(ctx, "a review the GitHub App made could not be read",
			slog.String("event", "serviceproxy.app_review_unread"), slog.String("repository", r.Repository))

		return
	}

	r.ID, r.URL = made.ID, made.HTMLURL
	p.cfg.AppReviews.AppReviewed(ctx, r)
}

// unpacked is body as its Content-Encoding packed it: none, gzip, or deflate (zlib, or raw as some servers send it).
// Another coding is not read.
func unpacked(body []byte, coding string) ([]byte, error) {
	var r io.Reader

	switch strings.ToLower(strings.TrimSpace(coding)) {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}

		r = zr
	case "deflate":
		if zr, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			r = zr
		} else {
			r = flate.NewReader(bytes.NewReader(body))
		}
	default:
		return nil, fmt.Errorf("the answer's coding %q is not read", coding)
	}

	return io.ReadAll(io.LimitReader(r, maxReviewAnswer))
}
