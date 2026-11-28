// Package githubapptest is a GitHub that answers the owner's GitHub App in memory (issue #1262): the manifest's code
// exchange, the App's installations and their tokens, the checks of a pull request, and a redelivery; and for the
// calls of issue #1232 a collaborator's permission, a reaction and a comment posted. Tests give its Transport to the
// App's HTTP client; nothing reaches the network.
package githubapptest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The App the fake registers, and its one installation.
const (
	AppID        = 4242
	BotUserID    = 4343
	OwnerUserID  = 164090677
	Slug         = "hypomnema-owner"
	Installation = 77
	Head         = "abc1234def5678"
)

// GitHub answers the App's requests.
type GitHub struct {
	// PEM is the App's private key and Secret its webhook secret, as the code exchange gives them.
	PEM    string
	Secret string
	// Repositories are those the installation covers, owner/name.
	Repositories []string

	mu sync.Mutex
	// checks is the conclusion of the head's one required check: "" while it runs.
	checks string
	// completedAt is when the head's required check ended, as GitHub says it; state is the pull request's, OPEN,
	// CLOSED or MERGED.
	completedAt string
	state       string
	// reads counts the reads of the pull request's checks, minted the installation tokens made; asked holds the
	// Authorization of each read.
	reads    int
	botReads int
	minted   int
	asked    []string
	// scopes holds the repository_ids each token was asked for, nil for a token of the whole installation.
	scopes [][]int64
	// head is the pull request's head.
	head string
	// held are the next reads of the checks held back, in order (HoldNextRead).
	held []*heldRead
	// permissions are the collaborators' permissions by login in lower case (SetPermission); posted is what the App
	// posted, in order (Posted).
	permissions map[string]collaborator
	posted      []Post
	// refused are the paths a POST is refused at, with the status (Refuse); reviews the reviews made (POST
	// .../pulls/{n}/reviews), in order.
	refused map[string]int
	reviews []Made
}

// Made is a review made on a pull request through the API: by whom GitHub took it to be, from the token that made
// it (an installation's token is the App's account, any other the owner's), and the delivery GitHub then sends.
type Made struct {
	By    Person
	URL   string
	Event []byte
}

// Refuse makes every POST to path answer status, and nothing is posted; 0 lets it through again.
func (g *GitHub) Refuse(path string, status int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.refused == nil {
		g.refused = map[string]int{}
	}

	g.refused[path] = status
}

// Reviews are the reviews made through the API, in order.
func (g *GitHub) Reviews() []Made {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]Made(nil), g.reviews...)
}

type collaborator struct {
	id         int64
	permission string
}

// Post is a request the App posted to a repository: a reaction or a comment, its path, the Authorization it carried
// and its body as JSON.
type Post struct {
	Path string
	Auth string
	Body map[string]any
}

// SetPermission makes login, GitHub's user id, a collaborator of every repository with permission: admin, write,
// read or none, as GET /repos/{owner}/{repo}/collaborators/{username}/permission answers it. A login never set is
// not found.
func (g *GitHub) SetPermission(login string, id int64, permission string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.permissions == nil {
		g.permissions = map[string]collaborator{}
	}

	g.permissions[strings.ToLower(login)] = collaborator{id: id, permission: permission}
}

// Posted is every reaction and comment the App posted, in order.
func (g *GitHub) Posted() []Post {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]Post(nil), g.posted...)
}

type heldRead struct {
	arrived chan struct{}
	release chan struct{}
}

// HoldNextRead makes the next read of the checks answer as GitHub is when it arrives, but give that answer only once
// release is called; arrived is closed when the answer is made, so a test can change GitHub meanwhile. Called again
// before that read comes, it holds the read after it.
func (g *GitHub) HoldNextRead() (<-chan struct{}, func()) {
	g.mu.Lock()
	defer g.mu.Unlock()

	h := &heldRead{arrived: make(chan struct{}), release: make(chan struct{})}
	g.held = append(g.held, h)

	return h.arrived, sync.OnceFunc(func() { close(h.release) })
}

// RepositoryID is the id GitHub gives a repository of the installation: 1000 and its place in Repositories.
func (g *GitHub) RepositoryID(fullName string) int64 {
	for i, name := range g.Repositories {
		if strings.EqualFold(name, fullName) {
			return int64(1000 + i)
		}
	}

	return 0
}

// Scopes is the repository_ids each installation token was asked for, in order; nil for one of the whole
// installation.
func (g *GitHub) Scopes() [][]int64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([][]int64(nil), g.scopes...)
}

// Push moves the pull request's head to sha, whose required check runs.
func (g *GitHub) Push(sha string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.head, g.checks = sha, ""
}

// New returns a GitHub with a fresh App key, whose installation covers kl3inIT/hypomnema and kl3inIT/MemoryOS.
func New(t *testing.T) *GitHub {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	block := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	return &GitHub{ //nolint:gosec // G101: a test's made-up secret
		PEM: string(block), Secret: "whsec-the-webhook-secret",
		Repositories: []string{"kl3inIT/hypomnema", "kl3inIT/MemoryOS"}, head: Head,
	}
}

// Client is an HTTP client that reaches this GitHub.
func (g *GitHub) Client() *http.Client { return &http.Client{Transport: g, Timeout: time.Minute} }

// Conclude sets the conclusion of the head's required check: "" runs it again.
func (g *GitHub) Conclude(conclusion string) { g.ConcludeAt(conclusion, "2026-11-27T10:05:00Z") }

// ConcludeAt sets the conclusion of the head's required check and when it ended: a rerun of the same head ends later.
func (g *GitHub) ConcludeAt(conclusion, completedAt string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.checks, g.completedAt = conclusion, completedAt
}

// SetState sets the pull request's state: OPEN, CLOSED or MERGED.
func (g *GitHub) SetState(state string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.state = state
}

// prState is the pull request's state as GitHub answers it. g.mu is held.
func (g *GitHub) prState() string {
	if g.state == "" {
		return "OPEN"
	}

	return g.state
}

// Reads is how many times the pull request's checks were read.
func (g *GitHub) Reads() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.reads
}

// Asked is the Authorization each read of the checks carried.
func (g *GitHub) Asked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), g.asked...)
}

// BotReads is how often GitHub was asked for the App's bot user.
func (g *GitHub) BotReads() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.botReads
}

// Token is the n-th installation token minted, from 1.
func Token(n int) string { return fmt.Sprintf("ghs_installation_token_%d", n) }

// RoundTrip answers one request.
func (g *GitHub) RoundTrip(r *http.Request) (*http.Response, error) {
	var sent []byte
	if r.Body != nil {
		sent, _ = io.ReadAll(r.Body)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	auth := r.Header.Get("Authorization")
	path := r.URL.Path

	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/app-manifests/") &&
		strings.HasSuffix(path, "/conversions"):
		return answer(http.StatusCreated, map[string]any{
			"id": AppID, "slug": Slug, "html_url": "https://github.com/apps/" + Slug,
			"owner": map[string]any{"login": "kl3inIT", "id": OwnerUserID}, "pem": g.PEM, "webhook_secret": g.Secret,
		})
	case r.Method == http.MethodGet && path == "/users/"+Slug+"[bot]":
		g.botReads++

		return answer(http.StatusOK, map[string]any{"id": BotUserID})
	case r.Method == http.MethodGet && path == "/users/kl3inIT":
		return answer(http.StatusOK, map[string]any{"id": OwnerUserID})
	case r.Method == http.MethodGet && path == "/app/installations":
		if !strings.HasPrefix(auth, "Bearer ey") {
			return answer(http.StatusUnauthorized, map[string]any{"message": "a JWT is required"})
		}

		return answer(http.StatusOK, []any{map[string]any{"id": Installation, "account": map[string]any{"login": "kl3inIT"}}})
	case r.Method == http.MethodPost && path == fmt.Sprintf("/app/installations/%d/access_tokens", Installation):
		g.minted++

		var asked struct {
			RepositoryIDs []int64 `json:"repository_ids"`
		}

		_ = json.Unmarshal(sent, &asked)
		g.scopes = append(g.scopes, asked.RepositoryIDs)

		return answer(http.StatusCreated, map[string]any{
			"token": Token(g.minted), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	case r.Method == http.MethodGet && path == "/installation/repositories":
		repos := make([]any, 0, len(g.Repositories))
		for i, name := range g.Repositories {
			repos = append(repos, map[string]any{"id": 1000 + i, "full_name": name})
		}

		return answer(http.StatusOK, map[string]any{"repositories": repos})
	case r.Method == http.MethodGet && path == "/app/hook/deliveries":
		return answer(http.StatusOK, []any{map[string]any{"id": 9, "guid": "guid-9", "event": "ping"}})
	case r.Method == http.MethodPost && path == "/app/hook/deliveries/9/attempts":
		return answer(http.StatusAccepted, map[string]any{})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/") && strings.HasSuffix(path, "/permission"):
		parts := strings.Split(path, "/")
		if len(parts) != 7 || parts[4] != "collaborators" {
			break
		}

		c, ok := g.permissions[strings.ToLower(parts[5])]
		if !ok {
			return answer(http.StatusNotFound, map[string]any{"message": "Not Found"})
		}

		return answer(http.StatusOK, map[string]any{
			"permission": c.permission, "user": map[string]any{"login": parts[5], "id": c.id},
		})
	case r.Method == http.MethodPost && g.refused[path] != 0:
		return answer(g.refused[path], map[string]any{"message": "refused"})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/repos/") && strings.HasSuffix(path, "/reviews"):
		return g.review(path, auth, sent)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/repos/") &&
		(strings.HasSuffix(path, "/reactions") || strings.HasSuffix(path, "/comments") ||
			strings.HasSuffix(path, "/replies")):
		var body map[string]any
		_ = json.Unmarshal(sent, &body)
		g.posted = append(g.posted, Post{Path: path, Auth: auth, Body: body})

		if strings.HasSuffix(path, "/reactions") {
			return answer(http.StatusCreated, map[string]any{"id": len(g.posted), "content": body["content"]})
		}

		return answer(http.StatusCreated, map[string]any{
			"id": 70000 + len(g.posted), "html_url": fmt.Sprintf("https://github.com%s#issuecomment-%d",
				strings.TrimPrefix(strings.TrimSuffix(path, "/comments"), "/repos"), 70000+len(g.posted)),
		})
	case r.Method == http.MethodPost && path == "/graphql":
		g.reads++
		g.asked = append(g.asked, auth)

		status, conclusion, completed := "IN_PROGRESS", any(nil), any(nil)
		if g.checks != "" {
			status, conclusion, completed = "COMPLETED", g.checks, g.completedAt
		}

		resp, err := answer(http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{
			"pullRequest": map[string]any{
				"state": g.prState(), "mergeable": "MERGEABLE", "headRefOid": g.head, "headRefName": "hyp/tsk_1",
				"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{
					"statusCheckRollup": map[string]any{"contexts": map[string]any{
						"pageInfo": map[string]any{"hasNextPage": false},
						"nodes": []any{map[string]any{
							"__typename": "CheckRun", "name": "Required", "status": status, "conclusion": conclusion,
							"startedAt": "2026-11-27T10:00:00Z", "completedAt": completed, "isRequired": true,
						}},
					}},
				}}}},
			},
		}}})

		if len(g.held) > 0 {
			h := g.held[0]
			g.held = g.held[1:]

			g.mu.Unlock()
			close(h.arrived)
			<-h.release
			g.mu.Lock()
		}

		return resp, err
	}

	return answer(http.StatusNotFound, map[string]any{"message": "Not Found"})
}

// review makes a review on a pull request as GitHub's POST /repos/{owner}/{repo}/pulls/{n}/reviews does: by the
// App's account when an installation's token made it, else by the owner's; and the pull_request_review delivery
// GitHub sends the App for it. g.mu is held.
func (g *GitHub) review(path, auth string, sent []byte) (*http.Response, error) {
	parts := strings.Split(path, "/")

	number, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil || len(parts) != 7 {
		return answer(http.StatusNotFound, map[string]any{"message": "Not Found"})
	}

	var asked struct {
		Event string `json:"event"`
		Body  string `json:"body"`
	}

	_ = json.Unmarshal(sent, &asked)

	by := Person{Login: "kl3inIT", ID: OwnerUserID}
	if strings.HasPrefix(auth, "Bearer ghs_installation_token_") {
		by = Person{Login: Slug + "[bot]", ID: BotUserID, Type: "Bot"}
	}

	id := int64(880 + len(g.reviews))
	state := map[string]string{"APPROVE": "approved", "REQUEST_CHANGES": "changes_requested"}[asked.Event]

	if state == "" {
		state = "commented"
	}

	made := Made{By: by, URL: ReviewURL(number, id), Event: ReviewAt(by, number, id, state, asked.Body, time.Now())}
	g.reviews = append(g.reviews, made)

	return answer(http.StatusOK, map[string]any{
		"id": id, "html_url": made.URL, "state": strings.ToUpper(state), "user": person(by),
		"submitted_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func answer(status int, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(string(raw))), ContentLength: int64(len(raw)),
	}, nil
}

// WorkflowRunCompleted is the body of a workflow_run delivery: the run on the head of pull request number of
// kl3inIT/hypomnema completed with conclusion.
func WorkflowRunCompleted(number int, conclusion string) []byte {
	raw, err := json.Marshal(map[string]any{
		"action": "completed", "installation": map[string]any{"id": Installation},
		"repository": map[string]any{"full_name": "kl3inIT/hypomnema"},
		"workflow_run": map[string]any{
			"head_sha": Head, "status": "completed", "conclusion": conclusion,
			"pull_requests": []any{map[string]any{"number": number, "head": map[string]any{"sha": Head}}},
		},
	})
	if err != nil {
		panic(err)
	}

	return raw
}

// Person is who writes a comment or a review on GitHub: a login and GitHub's id of it, and the account's type, User
// or Bot.
type Person struct {
	Login string
	ID    int64
	Type  string
}

// Comment is an issue_comment or pull_request_review_comment created on kl3inIT/hypomnema: by whom, on which issue or
// pull request, its id and body, and the body of the issue or pull request it is on.
type Comment struct {
	By          Person
	Number      int
	PullRequest bool
	ID          int64
	Body        string
	IssueBody   string
	// Path and Line place a review comment in the diff; InReplyTo is the thread's first comment when it is a reply.
	Path      string
	Line      int
	InReplyTo int64
}

func person(p Person) map[string]any {
	if p.Type == "" {
		p.Type = "User"
	}

	return map[string]any{"login": p.Login, "id": p.ID, "type": p.Type}
}

func base() map[string]any {
	return map[string]any{
		"installation": map[string]any{"id": Installation},
		"repository":   map[string]any{"full_name": "kl3inIT/hypomnema", "id": 1000},
	}
}

func encode(body map[string]any) []byte {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}

	return raw
}

// IssueComment is the body of an issue_comment delivery: c created on its issue or pull request.
func IssueComment(c Comment) []byte {
	body := base()
	issue := map[string]any{
		"number": c.Number, "title": "Make the store faster", "body": c.IssueBody,
		"html_url": fmt.Sprintf("https://github.com/kl3inIT/hypomnema/issues/%d", c.Number),
		"user":     person(Person{Login: "someone-else", ID: 999}),
	}

	if c.PullRequest {
		issue["pull_request"] = map[string]any{
			"html_url": fmt.Sprintf("https://github.com/kl3inIT/hypomnema/pull/%d", c.Number),
		}
		issue["html_url"] = fmt.Sprintf("https://github.com/kl3inIT/hypomnema/pull/%d", c.Number)
	}

	body["action"] = "created"
	body["issue"] = issue
	body["comment"] = map[string]any{
		"id": c.ID, "body": c.Body, "user": person(c.By),
		"html_url": fmt.Sprintf("%s#issuecomment-%d", issue["html_url"], c.ID),
	}
	body["sender"] = person(c.By)

	return encode(body)
}

// ReviewComment is the body of a pull_request_review_comment delivery: c created in the diff of its pull request.
func ReviewComment(c Comment) []byte {
	body := base()
	pr := pullRequest(c.Number, c.IssueBody)
	body["action"] = "created"
	body["pull_request"] = pr
	body["comment"] = map[string]any{
		"id": c.ID, "body": c.Body, "user": person(c.By), "path": c.Path, "line": c.Line, "in_reply_to_id": c.InReplyTo,
		"html_url": fmt.Sprintf("%s#discussion_r%d", pr["html_url"], c.ID),
	}
	body["sender"] = person(c.By)

	return encode(body)
}

// Review is the body of a pull_request_review delivery: a review submitted now by by on pull request number, with its
// id, state and body.
func Review(by Person, number int, id int64, state, text string) []byte {
	return ReviewAt(by, number, id, state, text, time.Now())
}

// ReviewAt is Review submitted at at.
func ReviewAt(by Person, number int, id int64, state, text string, at time.Time) []byte {
	body := base()
	pr := pullRequest(number, "")
	body["action"] = "submitted"
	body["pull_request"] = pr
	body["review"] = map[string]any{
		"id": id, "body": text, "state": state, "user": person(by),
		"html_url":     fmt.Sprintf("%s#pullrequestreview-%d", pr["html_url"], id),
		"submitted_at": at.UTC().Format(time.RFC3339),
	}
	body["sender"] = person(by)

	return encode(body)
}

// ReviewURL is the address of review id on pull request number of kl3inIT/hypomnema, as Review gives it.
func ReviewURL(number int, id int64) string {
	return fmt.Sprintf("https://github.com/kl3inIT/hypomnema/pull/%d#pullrequestreview-%d", number, id)
}

// PullRequestOpened is the body of a pull_request delivery: by opened pull request number with body.
func PullRequestOpened(by Person, number int, text string) []byte {
	body := base()
	pr := pullRequest(number, text)
	pr["user"] = person(by)
	body["action"] = "opened"
	body["pull_request"] = pr
	body["sender"] = person(by)

	return encode(body)
}

func pullRequest(number int, text string) map[string]any {
	return map[string]any{
		"number": number, "title": "Make the store faster", "body": text, "state": "open",
		"html_url": fmt.Sprintf("https://github.com/kl3inIT/hypomnema/pull/%d", number),
		"head":     map[string]any{"sha": Head, "ref": "hyp/tsk_1"},
	}
}
