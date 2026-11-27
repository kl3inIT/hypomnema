package serviceproxy

import (
	"context"
	"encoding/json"
	"net/http"
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
