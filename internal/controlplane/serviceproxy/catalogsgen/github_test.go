package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const restFixture = `{
  "openapi": "3.0.3",
  "servers": [{"url": "https://api.github.com"}],
  "paths": {
    "/repos/{owner}/{repo}/pulls/{pull_number}/merge": {
      "parameters": [],
      "get": {"operationId": "pulls/check-if-merged", "x-github": {"category": "pulls"}},
      "put": {"operationId": "pulls/merge", "x-github": {"category": "pulls"}}
    },
    "/repos/{owner}/{repo}/releases/{release_id}/assets": {
      "get": {"operationId": "repos/list-release-assets", "x-github": {"category": "releases"}},
      "post": {
        "operationId": "repos/upload-release-asset",
        "servers": [{"url": "https://uploads.github.com"}],
        "x-github": {"category": "releases"}
      }
    }
  }
}`

const schemaFixture = `
type Query implements Node @docsCategory(name: "meta") {
  viewer: User!
  repository(owner: String!, name: String!): Repository
}

type Mutation @docsCategory(name: "meta") {
  mergePullRequest(input: MergePullRequestInput!): MergePullRequestPayload @docsCategory(name: "pulls")
  deleteIssue(input: DeleteIssueInput!): DeleteIssuePayload @docsCategory(name: "issues")
  addUpvote(input: AddUpvoteInput!): AddUpvotePayload
}
`

// Scenario: the generator keeps, of GitHub's REST description, every operation's method, host, path, category and
// operationId, the upload on the host its own servers name, and nothing of a path's other keys; of the schema, every
// query and mutation with its @docsCategory, none for a field that has none.
func TestTheGeneratorKeepsEveryOperationOfGitHubsDeclaration(t *testing.T) {
	t.Parallel()

	d, err := githubOperations("2.103.3", []byte(restFixture), []byte(schemaFixture))
	require.NoError(t, err)

	assert.Equal(t, "2.103.3", d.GH)
	assert.Equal(t, []restOperation{
		{"GET", "api.github.com", "/repos/{owner}/{repo}/pulls/{pull_number}/merge", "pulls", "pulls/check-if-merged"},
		{"PUT", "api.github.com", "/repos/{owner}/{repo}/pulls/{pull_number}/merge", "pulls", "pulls/merge"},
		{"GET", "api.github.com", "/repos/{owner}/{repo}/releases/{release_id}/assets", "releases",
			"repos/list-release-assets"},
		{"POST", "uploads.github.com", "/repos/{owner}/{repo}/releases/{release_id}/assets", "releases",
			"repos/upload-release-asset"},
	}, d.REST)
	assert.Equal(t, []graphqlField{
		{"mutation", "addUpvote", ""},
		{"mutation", "deleteIssue", "issues"},
		{"mutation", "mergePullRequest", "pulls"},
		{"query", "repository", ""},
		{"query", "viewer", ""},
	}, d.GraphQL)

	written, err := encodeGitHub(d)
	require.NoError(t, err)

	var back githubDeclaration
	require.NoError(t, json.Unmarshal(written, &back))
	assert.Equal(t, d, back, "the file reads back as written")
	assert.Contains(t, string(written), "\n    {\"method\":\"PUT\"", "one operation per line")
}

// ghMerge is gh's merge as it sends it: a struct given to Mutate, and one aliased field in another.
const ghMerge = `package merge

func merge(c Client) error {
	var mutation struct {
		MergePullRequest struct{} ` + "`graphql:\"mergePullRequest(input: $input)\"`" + `
		Y                struct{} ` + "`graphql:\"m0: deleteIssue(input:$input)\"`" + `
	}

	return c.Mutate("github.com", "PullRequestMerge", &mutation, nil)
}
`

// ghRepo is gh's reading of a repository (api/queries_repo.go at v2.103.3): a struct of a type of the package given
// to Query, whose field has no tag, and a query it writes itself with fmt verbs in it.
const ghRepo = `package api

type viewerQuery struct {
	Viewer struct{ Login string }
	Alias  struct{} ` + "`graphql:\"o: organization(login: $org)\"`" + `
	Frag   struct{} ` + "`graphql:\"... on Query\"`" + `
}

func repo(c Client, fields string) error {
	query := fmt.Sprintf(` + "`" + `query RepositoryInfo($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {%s}
		__typename
	}` + "`" + `, fields)

	var v viewerQuery
	if err := c.Query("github.com", "Viewer", &v, nil); err != nil {
		return err
	}

	_ = fmt.Sprintf("add_%03d: addProjectV2ItemById(input: $input_%03d)", 1, 1)

	return c.GraphQL("github.com", query, nil, nil)
}
`

// ghProjects is gh's projects client (pkg/cmd/project/shared/queries at v2.103.3): a query sent through its own
// wrapper, doQueryWithProgressIndicator, which passes its query, of type any, on to Query (queries.go:94-97;
// resolve.go:119-165), and a function that only passes a host on, which is no wrapper.
const ghProjects = `package queries

type projectItemByURL struct {
	Resource struct {
		Typename string ` + "`graphql:\"__typename\"`" + `
	} ` + "`graphql:\"resource(url: $url)\"`" + `
}

type options struct{ IO string }

func (c *Client) doQueryWithProgressIndicator(name string, query any, variables map[string]any) error {
	err := c.apiClient.Query(name, query, variables)
	return handleError(err)
}

func (c *Client) ProjectItemIDByURL(rawURL string) error {
	var query projectItemByURL
	return c.doQueryWithProgressIndicator("GetProjectItemByURL", &query, nil)
}

func fetch(host string) error {
	var q struct{ License struct{} }
	return client.Query(host, "License", &q, nil)
}

func run() error {
	opts := &options{}
	return fetch(opts)
}
`

// Scenario: the generator reads every root field gh sends (decision 9c of #713): the mutations it gives Mutate, the
// queries it gives Query (a field's tag past its alias, or its name), the queries it writes itself, fmt verbs and
// all, and any field called with an input; its tests and what is not Go are left out. A query root field the schema
// lacks, as a mutation, fails the generator (review of #746: `repository` was not checked; second review:
// `resource`, sent through the projects' wrapper, was not either).
func TestTheGeneratorFailsOnAFieldGhSendsThatTheSchemaLacks(t *testing.T) {
	t.Parallel()

	tarball := gzipTar(t, map[string]string{
		"cli-2.103.3/pkg/cmd/pr/merge/http.go":                  ghMerge,
		"cli-2.103.3/api/queries_repo.go":                       ghRepo,
		"cli-2.103.3/pkg/cmd/project/shared/queries/queries.go": ghProjects,
		"cli-2.103.3/api/queries_test.go":                       "package api\n\nvar q = `query { onlyInATest }`\n",
		"cli-2.103.3/README.md":                                 "notInGo(input: x)",
	})

	sent, err := ghFields(tarball)
	require.NoError(t, err)
	assert.Equal(t, []graphqlField{
		{Type: "mutation", Field: "addProjectV2ItemById"},
		{Type: "mutation", Field: "deleteIssue"},
		{Type: "mutation", Field: "mergePullRequest"},
		{Type: "query", Field: "license"},
		{Type: "query", Field: "organization"},
		{Type: "query", Field: "repository"},
		{Type: "query", Field: "resource"},
		{Type: "query", Field: "viewer"},
	}, sent, "the projects' resource is read through their wrapper; a host passed on is no query")

	d, err := githubOperations("2.103.3", []byte(restFixture), []byte(schemaFixture))
	require.NoError(t, err)
	assert.Equal(t, []string{"mutation addProjectV2ItemById", "query license", "query organization",
		"query resource"}, lacks(d, sent), "a declaration without resource reports it missing (second review of #746)")

	withoutRepository := strings.Replace(schemaFixture, "  repository(owner: String!, name: String!): Repository\n",
		"", 1)
	d, err = githubOperations("2.103.3", []byte(restFixture), []byte(withoutRepository))
	require.NoError(t, err)
	assert.Contains(t, lacks(d, sent), "query repository")
}

// Scenario: a query gh writes is read by its tokens: an operation's name and variables, aliases, arguments, nested
// selections, fragments beside it and fmt verbs do not hide or invent a root field.
func TestAQueryGhWritesIsReadForItsRootFields(t *testing.T) {
	t.Parallel()

	got := operationRoots(`query PullRequestForBranch($owner: String!, $repo: String!) {
		r: repository(owner: $owner, name: $repo) { pullRequests(first: 30) { nodes { ...pr } } }
		%[1]s
		viewer { login }
	}
	fragment pr on PullRequest { number }
	mutation M { addComment(input: {body: "a { b"}) { clientMutationId } }`)

	assert.Equal(t, []graphqlField{
		{Type: "query", Field: "repository"},
		{Type: "query", Field: "viewer"},
		{Type: "mutation", Field: "addComment"},
	}, got)
	assert.Empty(t, operationRoots(`{"query": "not a document"}`))
}

// Scenario: a download whose SHA-256 is not the pinned one is refused.
func TestADeclarationThatChangedIsRefused(t *testing.T) {
	t.Parallel()

	body := []byte("declaration")
	sum := sha256.Sum256(body)

	require.NoError(t, checkSum(body, source{URL: "https://example.com/d", SHA256: hex.EncodeToString(sum[:])}))
	require.ErrorIs(t, checkSum([]byte("changed"), source{URL: "https://example.com/d",
		SHA256: hex.EncodeToString(sum[:])}), errChanged)
}

func gzipTar(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var b bytes.Buffer

	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)

	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)),
			Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	return b.Bytes()
}
