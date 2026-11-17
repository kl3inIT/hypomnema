package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clientGoRoutes are routes as client-go registers them (merge_requests.go, labels.go at v3.46.1), one of them
// never called.
const clientGoRoutes = `package gitlab

var (
	routeProjectsIDMergeRequestsID                = route("projects/%s/merge_requests/%d")
	routeProjectsIDMergeRequestsIDMerge           = route("projects/%s/merge_requests/%d/merge")
	routeProjectsIDMergeRequestsIDCancelAutoMerge = route("projects/%s/merge_requests/%d/cancel_auto_merge")
	routeProjectsIDLabels                         = route("projects/%s/labels")
	routeProjectsIDLabelsID                       = route("projects/%s/labels/%s")
	routeProjectsIDRepositoryArchive              = route("projects/%s/repository/archive%s")
	routeNeverCalled                              = route("projects/%s/never")
)
`

// clientGoCalls are client-go's calls in each of the ways it makes them: options given to do, with a method and
// without (GET), options gathered in a slice whose path is appended apart (labels.go), and a request built with
// NewRequest from a string, a fmt.Sprintf, a variable and a helper that returns the path, whose method is changed
// after (generic_packages.go, PublishPackageFile: built as a GET, sent as a PUT; review of #777).
const clientGoCalls = `package gitlab

import (
	"fmt"
	"net/http"
)

func (s *MergeRequestsService) GetMergeRequest(pid any, mr int64) (*MergeRequest, *Response, error) {
	return do[*MergeRequest](s.client,
		withPath(routeProjectsIDMergeRequestsID, ProjectID{pid}, mr),
	)
}

func (s *MergeRequestsService) AcceptMergeRequest(pid any, mr int64) (*MergeRequest, *Response, error) {
	return do[*MergeRequest](s.client,
		withMethod(http.MethodPut),
		withPath(routeProjectsIDMergeRequestsIDMerge, ProjectID{pid}, mr),
	)
}

func (s *MergeRequestsService) CancelMergeWhenPipelineSucceeds(pid any, mr int64) (*MergeRequest, *Response, error) {
	reqOpts := []doOption{
		withMethod(http.MethodPost),
		withPath(routeProjectsIDMergeRequestsIDCancelAutoMerge, ProjectID{pid}, mr),
	}

	return do[*MergeRequest](s.client, reqOpts...)
}

func (s *LabelsService) DeleteLabel(pid any, lid any) (*Response, error) {
	reqOpts := make([]doOption, 0, 4)
	reqOpts = append(reqOpts,
		withMethod(http.MethodDelete),
	)

	if lid != nil {
		reqOpts = append(reqOpts, withPath(routeProjectsIDLabelsID, ProjectID{pid}, LabelID{lid}))
	} else {
		reqOpts = append(reqOpts, withPath(routeProjectsIDLabels, ProjectID{pid}))
	}

	_, resp, err := do[none](s.client, reqOpts...)
	return resp, err
}

func (s *GenericPackagesService) FormatPackageURL(pid any, name string) (string, error) {
	u := fmt.Sprintf("projects/%s/packages/generic/%s", PathEscape(pid), name)
	return u, nil
}

func (s *GenericPackagesService) PublishPackageFile(pid any, name string) error {
	u, err := s.FormatPackageURL(pid, name)
	req, err := s.client.NewRequest(http.MethodGet, u, nil, nil)
	req.Method = http.MethodPut
	_ = req
	return err
}

func (s *RepositoriesService) Archive(pid any) error {
	u := fmt.Sprintf("projects/%s/repository/archive", PathEscape(pid))
	_, err := s.client.NewRequest(http.MethodGet, u, nil, nil)
	_, err = s.client.NewRequest(http.MethodPost, "groups/import", nil, nil)
	_, err = s.client.NewRequest(http.MethodGet, fmt.Sprintf("orbit/%s", "status"), nil, nil)
	_, err = client.NewRequest(config.method, config.path, nil, nil)
	return err
}
`

// Scenario: the generator reads every method and route client-go calls (#740, decision 3 of #713): a call's route
// and method (GET when it names none), options gathered in a slice whose method and path are appended apart, and the
// requests it builds itself, with the method they are sent with. Tests and the packages under the module's root are left out; a route no call names is
// said.
func TestTheGeneratorReadsEveryOperationClientGoCalls(t *testing.T) {
	t.Parallel()

	const root = "gitlab.com/gitlab-org/api/client-go/v3@v3.46.1/"

	files, err := clientGoFiles(zipOf(t, map[string]string{
		root + "routes.go":              clientGoRoutes,
		root + "calls.go":               clientGoCalls,
		root + "calls_test.go":          "package gitlab\n\nvar routeOnlyInATest = route(\"test\")\n",
		root + "testing/fixtures.go":    "package testing\n\nvar routeOfASubpackage = route(\"sub\")\n",
		root + "README.md":              "route(\"readme\")",
		"elsewhere/v3@v3.46.1/other.go": "package other\n",
	}))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{root + "routes.go", root + "calls.go"}, slices.Collect(maps.Keys(files)))

	ops, unnamed, err := clientGoOperations(files)
	require.NoError(t, err)
	assert.Equal(t, []gitlabOperation{
		{"POST", "groups/import"},
		{"GET", "orbit/%s"},
		{"DELETE", "projects/%s/labels"},
		{"DELETE", "projects/%s/labels/%s"},
		{"GET", "projects/%s/merge_requests/%d"},
		{"POST", "projects/%s/merge_requests/%d/cancel_auto_merge"},
		{"PUT", "projects/%s/merge_requests/%d/merge"},
		{"PUT", "projects/%s/packages/generic/%s"},
		{"GET", "projects/%s/repository/archive"},
	}, ops)
	assert.Equal(t, []string{"projects/%s/never", "projects/%s/repository/archive%s"}, unnamed)

	written, err := encodeGitLab(gitlabDeclaration{Glab: "1.130.0", ClientGo: "v3.46.1", Operations: ops})
	require.NoError(t, err)

	var back gitlabDeclaration
	require.NoError(t, json.Unmarshal(written, &back))
	assert.Equal(t, gitlabDeclaration{Glab: "1.130.0", ClientGo: "v3.46.1", Operations: ops}, back)
	assert.Contains(t, string(written), "\n    {\"method\":\"PUT\"", "one operation per line")
}

// Scenario: the client is read at the version glab's go.mod requires.
func TestTheClientIsTheVersionGlabRequires(t *testing.T) {
	t.Parallel()

	mod := []byte("module gitlab.com/gitlab-org/cli\n\ngo 1.27.0\n\nrequire (\n\tgithub.com/x/y v1.0.0\n" +
		"\tgitlab.com/gitlab-org/api/client-go/v3 v3.46.1\n\tk8s.io/client-go v0.37.2\n)\n")

	version, err := requiredVersion(mod, clientGoModule)
	require.NoError(t, err)
	assert.Equal(t, "v3.46.1", version)

	_, err = requiredVersion([]byte("module x\n"), clientGoModule)
	require.Error(t, err)
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var b bytes.Buffer

	zw := zip.NewWriter(&b)

	for name, body := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}

	require.NoError(t, zw.Close())

	return b.Bytes()
}
