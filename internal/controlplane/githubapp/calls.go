package githubapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// The calls of the App (issue #1232): a comment, a review comment or a review that names the App (@<app-slug>), on a
// repository it is installed on, by someone who may write there. The rows of the issue's design: who called is the
// signed delivery's sender, never the text (row 2), checked with the installation's token; the comment gets 👀 at
// once and one reply when the work ends (row 3); the issue or pull request has one Inbox item, given to Hypo (row 4);
// only the caller's own words after the mention are the request (row 5); the owner's own account is his turn
// (row 6), the account that registered the App (row 7).

// Mentions says whether body names the App, slug its handle, as a word: "@slug" or "@slug[bot]", in any case, not part
// of a longer name, an address or a link's text. As Omnara's githubMentionsBot (github_event.go:363-393); its word
// characters are its githubMentionWord's.
func Mentions(body, slug string) bool {
	_, ok := mention(body, slug)

	return ok
}

// mention is where the first mention of the App in body ends, and whether there is one.
func mention(body, slug string) (int, bool) {
	lower := strings.ToLower(body)
	slug = strings.ToLower(strings.TrimSuffix(slug, "[bot]"))

	if slug == "" {
		return 0, false
	}

	for offset := 0; offset < len(lower); {
		index := strings.Index(lower[offset:], "@"+slug)
		if index < 0 {
			return 0, false
		}

		start := offset + index
		end := start + 1 + len(slug)

		if strings.HasPrefix(lower[end:], "[bot]") {
			end += len("[bot]")
		}

		before := start == 0 || !mentionWord(lower[start-1])
		after := end == len(lower) || (!mentionWord(lower[end]) && lower[end] != '[')

		if end < len(lower) && lower[end] == '.' && (end+1 == len(lower) || !mentionWord(lower[end+1])) {
			after = true
		}

		if before && after {
			return end, true
		}

		offset = end
	}

	return 0, false
}

func mentionWord(ch byte) bool {
	return ch >= 128 || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' ||
		ch == '-' || ch == '_' || ch == '@' || ch == '.'
}

// Request is the caller's request in body: their words after the first mention of the App, to the end (#1232, row
// 4: "the caller's line after the mention is the request"). Empty when nothing follows the mention.
func Request(body, slug string) string {
	end, ok := mention(body, slug)
	if !ok {
		return ""
	}

	return strings.TrimSpace(strings.TrimLeft(body[end:], ",:;!?."))
}

// conversation is what a delivery's call is made of, when it is one: its kind, its issue or pull request and the
// comment or review.
type conversation struct {
	kind        string
	number      int
	pullRequest bool
	title       string
	comment     *Comment
}

// conversationOf reads the comment or review of a delivery that may be a call: an issue_comment or a
// pull_request_review_comment created, a pull_request_review submitted, as Omnara's githubPayloadEventType takes
// them (github_event.go:298-317). An edit is not a call: claude-code-action starts no run on a comment edited by
// someone else, and an edit of one's own comment would repeat work done.
func conversationOf(ev Event) (conversation, bool) {
	switch {
	case ev.Name == "issue_comment" && ev.Action == "created" && ev.Issue != nil && ev.Comment != nil:
		return conversation{kind: inboxstore.CallComment, number: ev.Issue.Number,
			pullRequest: ev.Issue.PullRequest != nil, title: ev.Issue.Title, comment: ev.Comment}, true
	case ev.Name == "pull_request_review_comment" && ev.Action == "created" && ev.PullRequest != nil &&
		ev.Comment != nil:
		return conversation{kind: inboxstore.CallReviewComment, number: ev.PullRequest.Number, pullRequest: true,
			title: ev.PullRequest.Title, comment: ev.Comment}, true
	case ev.Name == "pull_request_review" && ev.Action == "submitted" && ev.PullRequest != nil && ev.Review != nil:
		return conversation{kind: inboxstore.CallReview, number: ev.PullRequest.Number, pullRequest: true,
			title: ev.PullRequest.Title, comment: ev.Review}, true
	}

	return conversation{}, false
}

// validLogin says whether s can be a person's login on GitHub.
func validLogin(s string) bool {
	return s != "" && len(s) <= 39 && !strings.ContainsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-'
	})
}

// call acts on a delivery that may be a call of the App. A delivery that is no call, or a call by someone who may
// not make one, is left with nothing recorded and no answer on GitHub (#1232, row 2).
func (a *App) call(ctx context.Context, cred Credential, ev Event) error {
	conv, ok := conversationOf(ev)
	if !ok || conv.number <= 0 || conv.comment.ID <= 0 || !Mentions(conv.comment.Body, cred.Slug) {
		return nil
	}

	// A person, who wrote the comment themselves: the App's own comments and every bot's are no call (claude-code-
	// action refuses bots unless listed, permissions.ts:180-185; Omnara takes only a User's, github_event.go:255-262).
	sender := ev.Sender
	if sender.Type != "User" || conv.comment.User.Type != "User" || sender.ID <= 0 ||
		conv.comment.User.ID != sender.ID || !validLogin(sender.Login) {
		return nil
	}

	repo := ev.Repo()

	installation, ok, err := a.store().InstallationOf(ctx, repo)
	if err != nil || !ok || installation.InstallationID != ev.Installation.ID {
		return err
	}

	token, ok, err := a.TokenFor(ctx, Host, repo)
	if err != nil || !ok {
		return err
	}

	allowed, err := a.client.CanWrite(ctx, token.Value, repo, sender)
	if err != nil {
		return fmt.Errorf("reading %s's permission on %s: %w", sender.Login, repo, err)
	}

	if !allowed {
		a.logger.InfoContext(ctx, "a mention of the GitHub App by someone who may not write is ignored",
			slog.String("event", "githubapp.call_ignored"), slog.String("repository", repo),
			slog.String("login", sender.Login))

		return nil
	}

	place := ""
	if conv.kind == inboxstore.CallReviewComment && conv.comment.Path != "" {
		place = conv.comment.Path
		if conv.comment.Line > 0 {
			place += ":" + strconv.Itoa(conv.comment.Line)
		}
	}

	item, fresh, err := a.svc.Inbox().Call(ctx, inboxstore.GitHubCall{
		Delivery: ev.Delivery, Repository: repo, Number: conv.number, PullRequest: conv.pullRequest,
		Title: conv.title, Kind: conv.kind, CommentID: conv.comment.ID, URL: conv.comment.HTMLURL,
		Login: sender.Login, Owner: a.isOwner(cred, sender), Request: Request(conv.comment.Body, cred.Slug),
		Place: place,
	})
	if err != nil || !fresh {
		return err
	}

	a.logger.InfoContext(ctx, "a call of the GitHub App went to the Inbox", slog.String("event", "githubapp.called"),
		slog.String("repository", repo), slog.Int("number", conv.number), slog.String("login", sender.Login),
		slog.String("todo", item.ID))

	// GitHub has no reaction on a review: only a comment gets 👀, as Omnara acknowledges the two comment kinds alone
	// (github_event.go:436-460).
	if conv.kind == inboxstore.CallReview {
		return nil
	}

	return a.client.React(ctx, token.Value, repo, conv.kind == inboxstore.CallReviewComment, conv.comment.ID)
}

// isOwner says whether a caller is the owner's account: the one that registered the App (#1232, row 7), by its login,
// and by GitHub's id of it when the registration kept one, since a login given up can be taken by someone else.
func (a *App) isOwner(cred Credential, u User) bool {
	return cred.Owner != "" && strings.EqualFold(cred.Owner, u.Login) && (cred.OwnerID == 0 || cred.OwnerID == u.ID)
}

// reviewed records the review the App itself left on a pull request on the item of its calls, the work of the Bot
// whose attempt is on the item (inboxstore.RecordReview). Only the App's own account's review is: its login is its
// slug with [bot], which no person's can be (claude-code-action actor.ts).
func (a *App) reviewed(ctx context.Context, cred Credential, ev Event) error {
	r := ev.Review
	if ev.Action != "submitted" || ev.PullRequest == nil || r == nil || r.User.Type != "Bot" ||
		!strings.EqualFold(r.User.Login, cred.Slug+"[bot]") || !strings.HasPrefix(r.HTMLURL, "https://github.com/") {
		return nil
	}

	_, err := a.svc.Inbox().RecordReview(ctx, ev.Repo(), ev.PullRequest.Number, r.HTMLURL)

	return err
}

// replyRetry is how long the replies wait after GitHub refused one before they are tried again.
const replyRetry = time.Minute

// Run answers on GitHub the calls whose item closed (#1232, row 3), then again after each commit, until ctx ends: the
// calls kept are the queue, read once their item is closed, as the web push sender reads its outbox after each commit
// (webpush.Sender.Run). A reply GitHub refused is tried again a minute later.
func (a *App) Run(ctx context.Context) {
	wake := a.svc.Subscribe(ctx)

	for {
		if err := a.reply(ctx); err != nil && ctx.Err() == nil {
			a.logger.WarnContext(ctx, "answering a call of the GitHub App", slog.String("event", "githubapp.reply_failed"),
				slog.String("error", err.Error()))

			retry := time.NewTimer(replyRetry)
			select {
			case <-ctx.Done():
				retry.Stop()

				return
			case <-retry.C:
			}

			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
}

// reply answers every item closed whose calls wait: one comment each, where its latest call was made, mentioning each
// caller, with the outcome. A repository the App no longer covers cannot be answered: its calls are marked so, with
// no address.
func (a *App) reply(ctx context.Context) error {
	replies, err := a.svc.Inbox().Replies(ctx)
	if err != nil || len(replies) == 0 {
		return err
	}

	for _, r := range replies {
		last := r.Calls[len(r.Calls)-1]

		deliveries := make([]string, 0, len(r.Calls))
		for _, c := range r.Calls {
			deliveries = append(deliveries, c.Delivery)
		}

		token, ok, err := a.TokenFor(ctx, Host, last.Repository)
		if err != nil {
			return err
		}

		address := ""

		if ok {
			ctx, cancel := context.WithTimeout(ctx, gitHubRead)
			address, err = a.client.Reply(ctx, token.Value, last.Repository, last.Number,
				last.Kind == inboxstore.CallReviewComment, last.CommentID, replyText(r))

			cancel()

			if err != nil {
				return fmt.Errorf("answering the calls of %s on %s#%d: %w", r.Todo.ID, last.Repository, last.Number,
					err)
			}
		}

		if err := a.svc.Inbox().Replied(ctx, deliveries, address); err != nil {
			return err
		}
	}

	return nil
}

// maxReplyReason bounds the part of the item's close reason a reply quotes.
const maxReplyReason = 2000

// replyText is the answer to an item's calls: each caller named, how the item ended, the Bot's reason, and the links
// of its evidence on the web (the review, the pull request); a Run of the control plane's is not a page they can open.
func replyText(r inboxstore.Reply) string {
	var b strings.Builder

	seen := map[string]bool{}

	for _, c := range r.Calls {
		if !seen[strings.ToLower(c.Login)] {
			seen[strings.ToLower(c.Login)] = true
			b.WriteString("@" + c.Login + " ")
		}
	}

	switch r.Todo.State {
	case inboxstore.Done:
		b.WriteString("Done.")
	case inboxstore.Duplicate:
		b.WriteString("Closed as a duplicate of another item.")
	default:
		b.WriteString("Closed without doing it.")
	}

	if reason := []rune(strings.TrimSpace(r.Todo.CloseReason)); len(reason) > 0 {
		if len(reason) > maxReplyReason {
			reason = append(reason[:maxReplyReason-1], '…')
		}

		b.WriteString(" " + string(reason))
	}

	var links []string

	for _, l := range r.Todo.Evidence {
		if (l.Kind == inboxstore.LinkURL || l.Kind == inboxstore.LinkPullRequest) && webLink(l.Ref) {
			links = append(links, "- "+l.Ref)
		}
	}

	if len(links) > 0 {
		b.WriteString("\n\n" + strings.Join(links, "\n"))
	}

	return b.String()
}

// webLink says whether ref is a page on the web, https.
func webLink(ref string) bool {
	u, err := url.Parse(ref)

	return err == nil && u.Scheme == "https" && u.Host != ""
}

// CanWrite says whether u may write to repo, as GET /repos/{owner}/{repo}/collaborators/{username}/permission answers
// with token, the installation's: admin or write (maintain is answered write), and the account answered is u's own
// id. Not a collaborator (404) may not. As claude-code-action's checkWritePermissions (permissions.ts:189-206) and
// Omnara's CanDirectPullRequest (permissions.go:12-37).
func (c Client) CanWrite(ctx context.Context, token, repo string, u User) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, gitHubRead)
	defer cancel()

	var answer struct {
		Permission string `json:"permission"`
		User       User   `json:"user"`
	}

	err := c.do(ctx, http.MethodGet, "/repos/"+repo+"/collaborators/"+url.PathEscape(u.Login)+"/permission", token,
		nil, &answer)

	var refused *Error
	if errors.As(err, &refused) && refused.Status == http.StatusNotFound {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return answer.User.ID == u.ID && (answer.Permission == "admin" || answer.Permission == "write"), nil
}

// React puts 👀 on a comment with token: an issue's or a pull request's conversation comment, or a review comment
// (POST .../issues/comments/{id}/reactions or .../pulls/comments/{id}/reactions), as Omnara's AcknowledgeComment
// (reactions.go:10-29).
func (c Client) React(ctx context.Context, token, repo string, reviewComment bool, id int64) error {
	ctx, cancel := context.WithTimeout(ctx, gitHubRead)
	defer cancel()

	kind := "issues"
	if reviewComment {
		kind = "pulls"
	}

	path := "/repos/" + repo + "/" + kind + "/comments/" + strconv.FormatInt(id, 10) + "/reactions"

	return c.do(ctx, http.MethodPost, path, token, map[string]string{"content": "eyes"}, nil)
}

// Reply posts body with token as a reply to a call and returns its address: in the review comment's thread for a
// review comment (POST /repos/{owner}/{repo}/pulls/{n}/comments/{id}/replies), else in the issue's or pull request's
// conversation (POST /repos/{owner}/{repo}/issues/{n}/comments), as claude-code-action's createInitialComment
// (create-initial.ts:67-84).
func (c Client) Reply(ctx context.Context, token, repo string, number int, reviewComment bool, id int64,
	body string,
) (string, error) {
	path := "/repos/" + repo + "/issues/" + strconv.Itoa(number) + "/comments"
	if reviewComment {
		path = "/repos/" + repo + "/pulls/" + strconv.Itoa(number) + "/comments/" + strconv.FormatInt(id, 10) +
			"/replies"
	}

	var answer struct {
		HTMLURL string `json:"html_url"`
	}

	if err := c.do(ctx, http.MethodPost, path, token, map[string]string{"body": body}, &answer); err != nil {
		return "", err
	}

	return answer.HTMLURL, nil
}
