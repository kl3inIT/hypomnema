package githost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// RequestStatus is where a pull request is: open, merged or closed without a merge.
type RequestStatus string

// The statuses of a pull request.
const (
	RequestOpen   RequestStatus = "open"
	RequestMerged RequestStatus = "merged"
	RequestClosed RequestStatus = "closed"
)

// RequestState is what the control plane reads of a pull request it follows (issue #1053): where it is, its head,
// whether it conflicts with its base, and the required checks of its head that failed. It is the facts pstack's
// watch-pr reads before it decides (skills/poteto-mode/scripts/watch-pr/policy.ts:59-140: merged, closed, a
// conflict, failed checks). Branch is its head branch and AutoMerge whether auto-merge is armed on it, read from
// the host rather than from a worker's summary (issue #1168), as Coder reads the head branch of a pull request
// (coderd/x/gitsync/gitsync.go:327-331).
type RequestState struct {
	Status    RequestStatus
	Head      string
	Branch    string
	AutoMerge bool
	Conflict  bool
	Failed    []string
	// Checks counts the checks of the head a wait for its CI looks at (issue #1262): the required ones, or every
	// one when none is required, as `gh pr checks --watch` waits for every check without --required. Pending names
	// those still running and Failing those that failed, each by its newest run. Read on GitHub only.
	Checks  int
	Pending []string
	Failing []string
	// Completed is when the last of those checks ended, as GitHub says it (a check run's completedAt, a status's
	// createdAt), RFC 3339 in UTC; "" while none ended. A rerun on the same head ends later (issue #1262, review round
	// 4).
	Completed string
}

// ErrNoRequest says a branch has no open pull request (yet).
var ErrNoRequest = errors.New("the branch has no open pull request")

// FindRequest finds and reads the pull request of branch in repo, as Coder's gitsync refreshes a chat's: a known
// request, the hint, is read first and kept unless its head branch is another one; else, or then, the branch's
// open pull request is resolved at the host and read (coderd/x/gitsync/gitsync.go:282-343). It returns
// ErrNoRequest when the branch has none open.
// An empty branch with a hint reads that exact request.
func FindRequest(ctx context.Context, client *http.Client, kind Kind, repo Repo, branch string, hint *Request,
	token string,
) (Request, RequestState, error) {
	if hint != nil {
		st, err := ReadRequest(ctx, client, kind, *hint, token)
		if err != nil {
			return Request{}, RequestState{}, err
		}

		if branch == "" || st.Branch == "" || st.Branch == branch {
			return *hint, st, nil
		}
	}

	r, err := branchRequest(ctx, client, kind, repo, branch, token)
	if err != nil {
		return Request{}, RequestState{}, err
	}

	st, err := ReadRequest(ctx, client, kind, r, token)
	if err != nil {
		return Request{}, RequestState{}, err
	}

	return r, st, nil
}

// branchRequest resolves the open pull request whose head is branch, the most recently updated: on GitHub GET
// /repos/:owner/:repo/pulls?head=owner:branch&state=open, as Coder's ResolveBranchPullRequest asks it
// (coderd/externalauth/gitprovider/github.go:207-225), under api.github.com or a GitHub Enterprise host's /api/v3;
// on GitLab GET /projects/:id/merge_requests?source_branch=branch&state=opened.
func branchRequest(ctx context.Context, client *http.Client, kind Kind, repo Repo, branch, token string,
) (Request, error) {
	if branch == "" {
		return Request{}, ErrNoRequest
	}

	var u *url.URL

	switch kind {
	case GitHub:
		owner, _, ok := strings.Cut(repo.Path, "/")
		if !ok {
			return Request{}, fmt.Errorf("%s is not owner/name", repo.Path)
		}

		u = &url.URL{Scheme: "https", Host: "api.github.com", Path: "/repos/" + repo.Path + "/pulls"}
		if repo.Host != GitHubHost {
			u = &url.URL{Scheme: "https", Host: repo.Host, Path: "/api/v3/repos/" + repo.Path + "/pulls"}
		}

		u.RawQuery = url.Values{
			"head": {owner + ":" + branch}, "state": {"open"}, "sort": {"updated"}, "direction": {"desc"},
			"per_page": {"1"},
		}.Encode()
	case GitLab:
		u = &url.URL{
			Scheme: "https", Host: repo.Host,
			Path:    "/api/v4/projects/" + repo.Path + "/merge_requests",
			RawPath: "/api/v4/projects/" + url.PathEscape(repo.Path) + "/merge_requests",
			RawQuery: url.Values{
				"source_branch": {branch}, "state": {"opened"}, "order_by": {"updated_at"}, "sort": {"desc"},
				"per_page": {"1"},
			}.Encode(),
		}
	default:
		return Request{}, fmt.Errorf("unknown kind of host %q", kind)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Request{}, fmt.Errorf("asking %s for a pull request: %w", repo.Host, err)
	}

	body, err := ask(client, req, token, repo.Host)
	if err != nil {
		return Request{}, err
	}

	var found []struct {
		Number int `json:"number"`
		IID    int `json:"iid"`
	}

	if err := json.Unmarshal(body, &found); err != nil {
		return Request{}, fmt.Errorf("reading %s's pull requests of a branch: %w", repo.Host, err)
	}

	if len(found) == 0 {
		return Request{}, ErrNoRequest
	}

	n := found[0].Number
	if kind == GitLab {
		n = found[0].IID
	}

	if n <= 0 {
		return Request{}, fmt.Errorf("%s answered a pull request without a number", repo.Host)
	}

	return Request{Repo: repo, Number: n}, nil
}

// maxRequestBytes bounds the host's answer about a pull request; one page of 100 checks is far smaller.
const maxRequestBytes = 4 << 20

// ReadRequest reads pull request r on host, of kind, with token. On GitHub it is one GraphQL query: the request's
// state, mergeable and head, and the status check rollup of its last commit with each check's isRequired, as gh
// reads it for `gh pr checks --required` (cli/cli api/query_builder.go:279-310, pkg/cmd/pr/checks/aggregate.go:38);
// a required check failed when its state or conclusion is in gh's fail or cancel bucket (ERROR, FAILURE,
// TIMED_OUT, ACTION_REQUIRED, or GitHub's spelling of canceled; aggregate.go:70-84), and a check run again counts
// by its newest run only (eliminateDuplicates, aggregate.go:96-120); its head branch is headRefName and auto-merge
// is armed when autoMergeRequest is not null, the fields gh's `pr view --json headRefName,autoMergeRequest`
// reads. On GitLab it is the merge request (GET /projects/:id/merge_requests/:iid): its state, has_conflicts, sha,
// source_branch, merge_when_pipeline_succeeds and head pipeline, which is the required check when "Pipelines must
// succeed" is on, as the worker's auto-merge step checks (githost.go, gitlab AutoMerge).
//
// As ReadIdentity, the token goes to the host alone, and no error holds what the host answered beyond its status.
func ReadRequest(ctx context.Context, client *http.Client, kind Kind, r Request, token string) (RequestState, error) {
	if token == "" {
		return RequestState{}, fmt.Errorf("reading %s: there is no token", r.Repo.Host)
	}

	var (
		req *http.Request
		err error
	)

	switch kind {
	case GitHub:
		return githubReadRequest(ctx, client, r, token)
	case GitLab:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, (&url.URL{
			Scheme: "https", Host: r.Repo.Host,
			Path:    "/api/v4/projects/" + r.Repo.Path + "/merge_requests/" + strconv.Itoa(r.Number),
			RawPath: "/api/v4/projects/" + url.PathEscape(r.Repo.Path) + "/merge_requests/" + strconv.Itoa(r.Number),
		}).String(), nil)
	default:
		return RequestState{}, fmt.Errorf("unknown kind of host %q", kind)
	}

	if err != nil {
		return RequestState{}, fmt.Errorf("asking %s for a pull request: %w", r.Repo.Host, err)
	}

	body, err := ask(client, req, token, r.Repo.Host)
	if err != nil {
		return RequestState{}, err
	}

	return gitlabRequestState(body)
}

// githubReadRequest reads the pull request and every page of its last commit's checks, 100 a page, following the
// end cursor until there is no next page, as gh's populateStatusChecks does (cli/cli v2.104.0
// pkg/cmd/pr/checks/checks.go:257-300); ctx bounds the whole read.
func githubReadRequest(ctx context.Context, client *http.Client, r Request, token string) (RequestState, error) {
	var (
		state  RequestState
		checks []githubCheck
		after  string
	)

	for page := 0; ; page++ {
		req, err := githubRequestQuery(ctx, r, after)
		if err != nil {
			return RequestState{}, fmt.Errorf("asking %s for a pull request: %w", r.Repo.Host, err)
		}

		body, err := ask(client, req, token, r.Repo.Host)
		if err != nil {
			return RequestState{}, err
		}

		got, more, next, err := githubRequestPage(body)
		if err != nil {
			return RequestState{}, err
		}

		if page == 0 {
			state = got
		}

		checks = append(checks, more...)

		if next == "" {
			break
		}

		after = next
	}

	state.Failed = failedRequired(checks)
	state.Checks, state.Pending, state.Failing, state.Completed = ciOf(checks)

	return state, nil
}

// ask sends req with token and returns the answer's body, as ReadIdentity sends its one request.
func ask(client *http.Client, req *http.Request, token, host string) ([]byte, error) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	sameOrigin := *client
	sameOrigin.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" || next.URL.Host != via[0].URL.Host || len(via) >= maxRedirects {
			return http.ErrUseLastResponse
		}

		return nil
	}

	resp, err := sameOrigin.Do(req)
	if err != nil {
		return nil, fmt.Errorf("asking %s for a pull request: %w", host, transportError(err))
	}
	defer resp.Body.Close() //nolint:errcheck // a read-only answer; nothing is left to report

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered HTTP %d about a pull request", host, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBytes))
	if err != nil {
		return nil, fmt.Errorf("reading %s's answer about a pull request: %w", host, transportError(err))
	}

	return body, nil
}

const githubRequestGraphQL = `query($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      state
      mergeable
      headRefOid
      headRefName
      autoMergeRequest { enabledAt }
      commits(last: 1) {
        nodes {
          commit {
            statusCheckRollup {
              contexts(first: 100, after: $after) {
                pageInfo { hasNextPage endCursor }
                nodes {
                  __typename
                  ... on StatusContext { context state createdAt isRequired(pullRequestNumber: $number) }
                  ... on CheckRun {
                    name status conclusion startedAt completedAt isRequired(pullRequestNumber: $number)
                    checkSuite { workflowRun { event workflow { name } } }
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}`

// githubRequestQuery is the GraphQL request: api.github.com/graphql for github.com, /api/graphql on a GitHub
// Enterprise host, as gh sends it.
func githubRequestQuery(ctx context.Context, r Request, after string) (*http.Request, error) {
	owner, name, ok := strings.Cut(r.Repo.Path, "/")
	if !ok || strings.Contains(name, "/") {
		return nil, fmt.Errorf("%s is not owner/name", r.Repo.Path)
	}

	endpoint := "https://api.github.com/graphql"
	if r.Repo.Host != GitHubHost {
		endpoint = (&url.URL{Scheme: "https", Host: r.Repo.Host, Path: "/api/graphql"}).String()
	}

	variables := map[string]any{"owner": owner, "name": name, "number": r.Number}
	if after != "" {
		variables["after"] = after
	}

	body, err := json.Marshal(map[string]any{"query": githubRequestGraphQL, "variables": variables})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	return req, nil
}

// githubCheck is a CheckRun or a StatusContext of the rollup.
type githubCheck struct {
	Name        string `json:"name"`
	Context     string `json:"context"`
	State       string `json:"state"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"startedAt"`
	CompletedAt string `json:"completedAt"`
	CreatedAt   string `json:"createdAt"`
	IsRequired  bool   `json:"isRequired"`
	CheckSuite  struct {
		WorkflowRun struct {
			Event    string `json:"event"`
			Workflow struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
}

// githubRequestPage reads one answer: the pull request's state, this page's checks, and the cursor of the next
// page, "" when there is none.
func githubRequestPage(body []byte) (RequestState, []githubCheck, string, error) {
	var answer struct {
		Data struct {
			Repository struct {
				PullRequest *struct {
					State            string    `json:"state"`
					Mergeable        string    `json:"mergeable"`
					HeadRefOid       string    `json:"headRefOid"`
					HeadRefName      string    `json:"headRefName"`
					AutoMergeRequest *struct{} `json:"autoMergeRequest"`
					Commits          struct {
						Nodes []struct {
							Commit struct {
								StatusCheckRollup *struct {
									Contexts struct {
										PageInfo struct {
											HasNextPage bool   `json:"hasNextPage"`
											EndCursor   string `json:"endCursor"`
										} `json:"pageInfo"`
										Nodes []githubCheck `json:"nodes"`
									} `json:"contexts"`
								} `json:"statusCheckRollup"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}

	if err := json.Unmarshal(body, &answer); err != nil {
		return RequestState{}, nil, "", fmt.Errorf("reading GitHub's pull request: %w", err)
	}

	pr := answer.Data.Repository.PullRequest
	if len(answer.Errors) > 0 || pr == nil {
		return RequestState{}, nil, "", fmt.Errorf("GitHub answered no pull request (%d errors)", len(answer.Errors))
	}

	state := RequestState{
		Head: pr.HeadRefOid, Branch: pr.HeadRefName, Conflict: pr.Mergeable == "CONFLICTING",
		AutoMerge: pr.AutoMergeRequest != nil,
	}

	switch pr.State {
	case "MERGED":
		state.Status = RequestMerged
	case "CLOSED":
		state.Status = RequestClosed
	default:
		state.Status = RequestOpen
	}

	var (
		checks []githubCheck
		next   string
	)

	for _, n := range pr.Commits.Nodes {
		if rollup := n.Commit.StatusCheckRollup; rollup != nil {
			checks = append(checks, rollup.Contexts.Nodes...)

			if rollup.Contexts.PageInfo.HasNextPage && rollup.Contexts.PageInfo.EndCursor != "" {
				next = rollup.Contexts.PageInfo.EndCursor
			}
		}
	}

	return state, checks, next, nil
}

// failedRequired names the required checks that failed, each check by its newest run (gh's eliminateDuplicates
// and fail and cancel buckets, aggregate.go:38-120).
func failedRequired(checks []githubCheck) []string {
	started := func(c githubCheck) string {
		if c.StartedAt != "" {
			return c.StartedAt
		}

		return c.CreatedAt
	}

	// RFC 3339 times in UTC, as GitHub gives them, sort as strings.
	slices.SortStableFunc(checks, func(a, b githubCheck) int { return strings.Compare(started(b), started(a)) })

	seen := map[string]bool{}

	var failed []string

	for _, c := range checks {
		key := "context:" + c.Context
		if c.Context == "" {
			key = "check:" + c.Name + "/" + c.CheckSuite.WorkflowRun.Workflow.Name + "/" + c.CheckSuite.WorkflowRun.Event
		}

		if seen[key] {
			continue
		}

		seen[key] = true

		state := c.State
		if state == "" && c.Status == "COMPLETED" {
			state = c.Conclusion
		}

		name := c.Name
		if name == "" {
			name = c.Context
		}

		switch state {
		case "ERROR", "FAILURE", "TIMED_OUT", "ACTION_REQUIRED", "CANCELLED": //nolint:misspell // GitHub's value
			if c.IsRequired {
				failed = append(failed, name)
			}
		}
	}

	slices.Sort(failed)

	return failed
}

// ciOf is what a wait for the head's CI reads of its checks: how many it looks at, which are pending and which
// failed, each check by its newest run, in gh's buckets (aggregate.go:38-120): a check run not COMPLETED or a status
// PENDING or EXPECTED is pending; a failure, an error, a time-out, an action required or a cancel fails. The required checks
// are looked at, or every check when none is required.
func ciOf(checks []githubCheck) (int, []string, []string, string) {
	newest := newestRuns(checks)

	looked := make([]githubCheck, 0, len(newest))
	for _, c := range newest {
		if c.IsRequired {
			looked = append(looked, c)
		}
	}

	if len(looked) == 0 {
		looked = newest
	}

	var (
		pending, failing []string
		completed        string
	)

	for _, c := range looked {
		name := c.Name
		if name == "" {
			name = c.Context
		}

		state := c.State
		if c.Context == "" {
			state = c.Conclusion
			if c.Status != "COMPLETED" {
				state = "PENDING"
			}
		}

		switch state {
		case "PENDING", "EXPECTED", "":
			pending = append(pending, name)

			continue
		case "ERROR", "FAILURE", "TIMED_OUT", "ACTION_REQUIRED", "CANCELLED": //nolint:misspell // GitHub's value
			failing = append(failing, name)
		}

		ended := c.CompletedAt
		if c.Context != "" {
			ended = c.CreatedAt
		}

		// RFC 3339 times in UTC, as GitHub gives them, compare as strings.
		completed = max(completed, ended)
	}

	slices.Sort(pending)
	slices.Sort(failing)

	return len(looked), pending, failing, completed
}

// newestRuns keeps each check's newest run (gh's eliminateDuplicates, aggregate.go:96-120).
func newestRuns(checks []githubCheck) []githubCheck {
	started := func(c githubCheck) string {
		if c.StartedAt != "" {
			return c.StartedAt
		}

		return c.CreatedAt
	}

	sorted := slices.Clone(checks)
	// RFC 3339 times in UTC, as GitHub gives them, sort as strings.
	slices.SortStableFunc(sorted, func(a, b githubCheck) int { return strings.Compare(started(b), started(a)) })

	seen := map[string]bool{}
	out := make([]githubCheck, 0, len(sorted))

	for _, c := range sorted {
		key := "context:" + c.Context
		if c.Context == "" {
			key = "check:" + c.Name + "/" + c.CheckSuite.WorkflowRun.Workflow.Name + "/" + c.CheckSuite.WorkflowRun.Event
		}

		if !seen[key] {
			seen[key] = true
			out = append(out, c)
		}
	}

	return out
}

func gitlabRequestState(body []byte) (RequestState, error) {
	var mr struct {
		State        string `json:"state"`
		HasConflicts bool   `json:"has_conflicts"`
		SHA          string `json:"sha"`
		SourceBranch string `json:"source_branch"`
		AutoMerge    bool   `json:"merge_when_pipeline_succeeds"`
		HeadPipeline *struct {
			Status string `json:"status"`
		} `json:"head_pipeline"`
	}

	if err := json.Unmarshal(body, &mr); err != nil {
		return RequestState{}, fmt.Errorf("reading GitLab's merge request: %w", err)
	}

	state := RequestState{Head: mr.SHA, Branch: mr.SourceBranch, AutoMerge: mr.AutoMerge, Conflict: mr.HasConflicts}

	switch mr.State {
	case "merged":
		state.Status = RequestMerged
	case "closed":
		state.Status = RequestClosed
	default:
		state.Status = RequestOpen
	}

	if mr.HeadPipeline != nil && (mr.HeadPipeline.Status == "failed" || mr.HeadPipeline.Status == "canceled") {
		state.Failed = []string{"pipeline"}
	}

	return state, nil
}
