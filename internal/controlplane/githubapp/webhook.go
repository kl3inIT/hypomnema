package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// The headers of a webhook delivery.
const (
	SignatureHeader = "X-Hub-Signature-256"
	EventHeader     = "X-Github-Event"
	DeliveryHeader  = "X-Github-Delivery"
)

// MaxEventBytes bounds a delivery's body, as Omnara's EventBodyMaxBytes (webhooks.go:18). GitHub caps a payload at
// 25 MB, but none of the events subscribed to comes near this.
const MaxEventBytes = 2 << 20

// ValidSignature says whether body was signed with secret: X-Hub-Signature-256 is one "sha256=" and the hex of the
// HMAC-SHA256 of the body, compared in constant time (Omnara's ValidSignature, webhooks.go:21-34; GitHub's
// "Validating webhook deliveries").
func ValidSignature(header http.Header, body []byte, secret string) bool {
	values := header.Values(SignatureHeader)
	if secret == "" || len(body) > MaxEventBytes || len(values) != 1 ||
		len(values[0]) != len("sha256=")+sha256.Size*2 || !strings.HasPrefix(values[0], "sha256=") {
		return false
	}

	signature, err := hex.DecodeString(strings.TrimPrefix(values[0], "sha256="))
	if err != nil || len(signature) != sha256.Size {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)

	return hmac.Equal(signature, mac.Sum(nil))
}

// Sign is the X-Hub-Signature-256 GitHub sends with body, for tests and a delivery made here.
func Sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)

	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Event is what is read of a delivery: its name and id from the headers, and the fields of the body the control
// plane acts on. The headers are not signed (Omnara's GitHubIntegrationInboxProvider, github_event.go:44-45): the id
// only says which delivery this is, and a body signed once does nothing new when sent again.
type Event struct {
	Name     string `json:"-"`
	Delivery string `json:"-"`

	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	CheckSuite  *Suite       `json:"check_suite"`
	WorkflowRun *Suite       `json:"workflow_run"`
	PullRequest *PullRequest `json:"pull_request"`
	// Sender is who caused the delivery, as GitHub signed it (issue #1232); Issue, Comment and Review are a
	// conversation's.
	Sender  User     `json:"sender"`
	Issue   *Issue   `json:"issue"`
	Comment *Comment `json:"comment"`
	Review  *Comment `json:"review"`
}

// User is a GitHub account: its login, GitHub's id of it, and its type, User or Bot.
type User struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

// Issue is an issue_comment's issue: its number, its title, and whether it is a pull request.
type Issue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	PullRequest *struct{} `json:"pull_request"`
}

// Comment is a comment's or a review's: its id, its author, its body and address, and where a review comment is in
// the diff.
type Comment struct {
	ID      int64  `json:"id"`
	User    User   `json:"user"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	// InReplyToID is, for a review comment that replies in a thread, the thread's first comment.
	InReplyToID int64 `json:"in_reply_to_id"`
	// SubmittedAt is when a review was submitted.
	SubmittedAt time.Time `json:"submitted_at"`
}

// Suite is a check suite or a workflow run: the commit it ran on, its status and conclusion, and the pull requests
// whose head that commit is (empty for a fork's).
type Suite struct {
	HeadSHA      string `json:"head_sha"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	PullRequests []struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_requests"`
}

// PullRequest is a pull_request event's: its number, title, state, whether it merged, and its head.
type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	Head   struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
}

// Repo is the event's repository as RepositoryName has it, "" when it names none.
func (e Event) Repo() string {
	name, _ := RepositoryName(e.Repository.FullName)

	return name
}

// ErrSignature says a delivery is not signed with the App's secret.
var ErrSignature = errors.New("the delivery is not signed with the GitHub App's webhook secret")

// DecodeEvent checks a delivery's signature with secret, then reads it. A delivery whose signature does not hold is
// ErrSignature, and nothing of it is read.
func DecodeEvent(header http.Header, body []byte, secret string) (Event, error) {
	if !ValidSignature(header, body, secret) {
		return Event{}, ErrSignature
	}

	var e Event
	if err := json.Unmarshal(body, &e); err != nil {
		return Event{}, errors.New("the delivery is not JSON")
	}

	e.Name, e.Delivery = header.Get(EventHeader), header.Get(DeliveryHeader)
	if !headerValue(e.Name) || !headerValue(e.Delivery) {
		return Event{}, errors.New("the delivery has no event or id")
	}

	return e, nil
}

// headerValue says whether s can be an event's name or a delivery's id (Omnara's validHeaderValue, webhooks.go).
func headerValue(s string) bool {
	return s != "" && len(s) <= 128 && !strings.ContainsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_'
	})
}
