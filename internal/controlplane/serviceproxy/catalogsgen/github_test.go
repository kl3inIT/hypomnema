package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// Scenario: the generator reads what gh sends with an input object, in its struct tags and in the queries it
// writes, leaving its tests out, and names what the schema lacks.
func TestTheGeneratorFailsOnAFieldGhSendsThatTheSchemaLacks(t *testing.T) {
	t.Parallel()

	tarball := gzipTar(t, map[string]string{
		"cli-2.103.3/pkg/cmd/pr/merge/http.go": "var m struct{ X struct{} `graphql:\"mergePullRequest(input: $input)\"`" +
			"\n Y struct{} `graphql:\"m0: deleteIssue(input:$input)\"` }",
		"cli-2.103.3/api/queries_projects_v2.go": "fmt.Sprintf(\"add_%03d: addProjectV2ItemById(input: $input_%03d)\")",
		"cli-2.103.3/api/queries_test.go":        "`graphql:\"onlyInATest(input: $input)\"`",
		"cli-2.103.3/README.md":                  "notInGo(input: x)",
	})

	sent, err := ghFields(tarball)
	require.NoError(t, err)
	assert.Equal(t, []string{"addProjectV2ItemById", "deleteIssue", "mergePullRequest"}, sent)

	d, err := githubOperations("2.103.3", []byte(restFixture), []byte(schemaFixture))
	require.NoError(t, err)
	assert.Equal(t, []string{"addProjectV2ItemById"}, lacks(d, sent))
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
