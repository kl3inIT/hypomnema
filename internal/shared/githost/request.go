package githost

import (
	"bytes"
	"context"
	"encoding/json"
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
// conflict, failed checks).
type RequestState struct {
	Status   RequestStatus
	Head     string
	Conflict bool
	Failed   []string
}

// maxRequestBytes bounds the host's answer about a pull request; one page of 100 checks is far smaller.
const maxRequestBytes = 4 << 20

// ReadRequest reads pull request r on host, of kind, with token. On GitHub it is one GraphQL query: the request's
// state, mergeable and head, and the status check rollup of its last commit with each check's isRequired, as gh
// reads it for `gh pr checks --required` (cli/cli api/query_builder.go:279-310, pkg/cmd/pr/checks/aggregate.go:38);
// a required check failed when its state or conclusion is in gh's fail or cancel bucket (ERROR, FAILURE,
// TIMED_OUT, ACTION_REQUIRED, or GitHub's spelling of canceled; aggregate.go:70-84), and a check run again counts
// by its newest run only (eliminateDuplicates, aggregate.go:96-120). On GitLab it is the merge request (GET
// /projects/:id/merge_requests/:iid): its state, has_conflicts, sha and head pipeline, which is the required check
// when "Pipelines must succeed" is on, as the worker's auto-merge step checks (githost.go, gitlab AutoMerge).
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
                    name status conclusion startedAt isRequired(pullRequestNumber: $number)
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
	Name       string `json:"name"`
	Context    string `json:"context"`
	State      string `json:"state"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	StartedAt  string `json:"startedAt"`
	CreatedAt  string `json:"createdAt"`
	IsRequired bool   `json:"isRequired"`
	CheckSuite struct {
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
					State      string `json:"state"`
					Mergeable  string `json:"mergeable"`
					HeadRefOid string `json:"headRefOid"`
					Commits    struct {
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

	state := RequestState{Head: pr.HeadRefOid, Conflict: pr.Mergeable == "CONFLICTING"}

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

func gitlabRequestState(body []byte) (RequestState, error) {
	var mr struct {
		State        string `json:"state"`
		HasConflicts bool   `json:"has_conflicts"`
		SHA          string `json:"sha"`
		HeadPipeline *struct {
			Status string `json:"status"`
		} `json:"head_pipeline"`
	}

	if err := json.Unmarshal(body, &mr); err != nil {
		return RequestState{}, fmt.Errorf("reading GitLab's merge request: %w", err)
	}

	state := RequestState{Head: mr.SHA, Conflict: mr.HasConflicts}

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
