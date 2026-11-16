package modelgateway_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
)

// Scenario (issue #664): the owner hands out a key that may search the web but
// not read pages or make images. Its search is answered; a page read, Codex's
// search of another kind and an image are refused with 403 before any provider
// is asked. A key given no kind may use none of them, and a key made without a
// limit, as every key before, may use all.
func TestAKeyUsesOnlyTheKindsItWasGiven(t *testing.T) {
	t.Parallel()

	g, page := webGateway(t, exaFound)

	searcher, searchKey, err := g.svc.AddKey(t.Context(), "searcher", nil,
		[]modelgateway.Endpoint{modelgateway.EndpointSearch})
	require.NoError(t, err)
	assert.Equal(t, []modelgateway.Endpoint{modelgateway.EndpointSearch}, searcher.Endpoints)

	status, _, body := g.post(t, "/v1/search", searchKey, `{"query":"hypomnema"}`)
	assert.Equal(t, http.StatusOK, status, body)

	for _, path := range []string{"/v1/web/fetch", "/v1/images/generations", "/v1/images/edits"} {
		status, _, body = g.post(t, path, searchKey, `{"url":"`+page.URL+`","prompt":"a cat"}`)
		assert.Equal(t, http.StatusForbidden, status, path)
		assert.Contains(t, body, `"code":"endpoint_not_allowed"`, path)
	}

	_, chatOnly, err := g.svc.AddKey(t.Context(), "chat-only", nil, []modelgateway.Endpoint{})
	require.NoError(t, err)

	for _, path := range []string{"/v1/search", "/v1/alpha/search"} {
		status, _, _ = g.post(t, path, chatOnly, `{"query":"hypomnema"}`)
		assert.Equal(t, http.StatusForbidden, status, path)
	}

	status, _, body = g.post(t, "/v1/web/fetch", g.key, `{"url":"`+page.URL+`"}`)
	assert.Equal(t, http.StatusOK, status, body)
}

// Scenario: a key's kinds are kept once each in one order however they were
// asked, come back when the key authenticates, and a kind that is not one is
// refused as the key is made.
func TestAKeysKindsAreKeptAndChecked(t *testing.T) {
	t.Parallel()

	g := newGateway(t)

	_, secret, err := g.AddKey(t.Context(), "artist", nil, []modelgateway.Endpoint{
		modelgateway.EndpointImages, modelgateway.EndpointSearch, modelgateway.EndpointImages,
	})
	require.NoError(t, err)

	k, err := g.Authenticate(t.Context(), secret)
	require.NoError(t, err)
	assert.Equal(t, []modelgateway.Endpoint{modelgateway.EndpointSearch, modelgateway.EndpointImages}, k.Endpoints)
	assert.False(t, k.Reaches(modelgateway.EndpointFetch))

	_, _, err = g.AddKey(t.Context(), "chat", nil, []modelgateway.Endpoint{"chat"})
	require.ErrorIs(t, err, modelgateway.ErrInvalid)
}
