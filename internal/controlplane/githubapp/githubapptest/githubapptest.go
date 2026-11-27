// Package githubapptest is a GitHub that answers the owner's GitHub App in memory (issue #1262): the manifest's code
// exchange, the App's installations and their tokens, the checks of a pull request, and a redelivery. Tests give its
// Transport to the App's HTTP client; nothing reaches the network.
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
	"strings"
	"sync"
	"testing"
	"time"
)

// The App the fake registers, and its one installation.
const (
	AppID        = 4242
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
	// reads counts the reads of the pull request's checks, minted the installation tokens made; asked holds the
	// Authorization of each read.
	reads  int
	minted int
	asked  []string
	// scopes holds the repository_ids each token was asked for, nil for a token of the whole installation.
	scopes [][]int64
	// head is the pull request's head.
	head string
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
func (g *GitHub) Conclude(conclusion string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.checks = conclusion
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
			"owner": map[string]any{"login": "kl3inIT"}, "pem": g.PEM, "webhook_secret": g.Secret,
		})
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
	case r.Method == http.MethodPost && path == "/graphql":
		g.reads++
		g.asked = append(g.asked, auth)

		status, conclusion := "IN_PROGRESS", any(nil)
		if g.checks != "" {
			status, conclusion = "COMPLETED", g.checks
		}

		return answer(http.StatusOK, map[string]any{"data": map[string]any{"repository": map[string]any{
			"pullRequest": map[string]any{
				"state": "OPEN", "mergeable": "MERGEABLE", "headRefOid": g.head, "headRefName": "hyp/tsk_1",
				"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{
					"statusCheckRollup": map[string]any{"contexts": map[string]any{
						"pageInfo": map[string]any{"hasNextPage": false},
						"nodes": []any{map[string]any{
							"__typename": "CheckRun", "name": "Required", "status": status, "conclusion": conclusion,
							"startedAt": "2026-11-27T10:00:00Z", "isRequired": true,
						}},
					}},
				}}}},
			},
		}}})
	}

	return answer(http.StatusNotFound, map[string]any{"message": "Not Found"})
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
