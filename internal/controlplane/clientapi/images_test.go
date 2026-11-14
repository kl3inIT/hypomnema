package clientapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// imagesOver is the Images page's settings as the control plane wires them:
// the gateway's, with the rests a running gateway keeps.
type imagesOver struct {
	*modelgateway.Service

	rests map[string]map[string]time.Time
}

func (i imagesOver) ImageRests(context.Context) (map[string]map[string]time.Time, error) {
	return i.rests, nil
}

// madeImages stands for the gateway making images: it keeps what it was asked.
type madeImages struct {
	asked modelgateway.ImageRequest
	chat  string
	err   error
}

func (m *madeImages) Make(
	_ context.Context, chatID string, req modelgateway.ImageRequest,
) (modelgateway.ImageResult, error) {
	m.asked, m.chat = req, chatID
	if m.err != nil {
		return modelgateway.ImageResult{}, m.err
	}

	return modelgateway.ImageResult{
		Provider: "antigravity", Model: "gemini-3.1-flash-image", Created: 1,
		Images: []modelgateway.GeneratedImage{{B64JSON: "Zm94", MIME: "image/png", RevisedPrompt: "a fox"}},
		Errors: []modelgateway.ImageFailure{{Provider: "codex", Model: "gpt-image-2", Message: "limited"}},
	}, nil
}

// Scenario: the owner opens Settings → Gateway → Images. Codex has one
// account on the free plan, which makes no images, and one on a team plan
// that rests after a limit: Codex rests until then. Antigravity has no
// account. The owner sets Antigravity's model to try a newer one, puts it
// first and turns Codex off; a coordinator reads and changes nothing.
func TestTheOwnerSeesAndChangesTheImageProviders(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	g := gatewayOver(t, w.svc)

	free, err := w.svc.AddAccount(ctx, "codex", "free")
	require.NoError(t, err)
	require.NoError(t, w.svc.SetAccountPlan(ctx, free.ID, "", "free"))
	team, err := w.svc.AddAccount(ctx, "codex", "team")
	require.NoError(t, err)
	require.NoError(t, w.svc.SetAccountPlan(ctx, team.ID, "", "team"))

	until := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)
	w.srv.WithAccounts(&signedIn{svc: w.svc, states: map[string]subscription.State{
		free.ID: subscription.StateActive, team.ID: subscription.StateActive,
	}, removed: map[string]bool{}}).WithImages(&madeImages{},
		imagesOver{Service: g, rests: map[string]map[string]time.Time{"codex": {team.ID: until}}})

	owner, _ := w.token("web", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	listed, err := pi.GetImagesWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))

	codex := listed.JSON200.Providers[0]
	assert.Equal(t, openapi.ImageProviderLinkStateResting, codex.State)
	assert.Equal(t, 1, codex.Accounts, "the free plan's account makes no images")
	require.NotNil(t, codex.RestingUntil)
	assert.WithinDuration(t, until, *codex.RestingUntil, time.Second)
	assert.Equal(t, "gpt-image-2", codex.Model)
	assert.Equal(t, []string{"gpt-image-2"}, codex.SuggestedModels)

	antigravity := listed.JSON200.Providers[1]
	assert.Equal(t, openapi.ImageProviderLinkStateNotSetUp, antigravity.State)
	assert.Equal(t, []string{"gemini-3.1-flash-image", "gemini-3-pro-image"}, antigravity.SuggestedModels)

	model := "gemini-nano-banana-2.1"
	refused, err := pi.SetImageProviderWithResponse(ctx, "antigravity", openapi.ImageProviderChange{Model: &model})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	set, err := owner.SetImageProviderWithResponse(ctx, "antigravity", openapi.ImageProviderChange{Model: &model})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, set.StatusCode(), string(set.Body))
	assert.Equal(t, model, set.JSON200.Providers[1].Model)
	assert.True(t, set.JSON200.Providers[1].ModelChosen)

	bad := "no spaces"
	invalid, err := owner.SetImageProviderWithResponse(ctx, "antigravity", openapi.ImageProviderChange{Model: &bad})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, invalid.StatusCode())

	ordered, err := owner.SetImageOrderWithResponse(ctx, openapi.ImageOrder{
		Providers: []openapi.ImageProvider{"antigravity", "codex"},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, ordered.StatusCode(), string(ordered.Body))
	assert.Equal(t, openapi.ImageProvider("antigravity"), ordered.JSON200.Providers[0].Provider)

	off := false
	switched, err := owner.SetImageProviderWithResponse(ctx, "codex", openapi.ImageProviderChange{On: &off})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, switched.StatusCode(), string(switched.Body))
	assert.Equal(t, openapi.ImageProviderLinkStateOff, switched.JSON200.Providers[1].State)
}

// Scenario: a token asks for an edit: the request reaches the gateway as it
// was asked, with no chat for a token that belongs to none (a Bot's pi
// token names its chat, whose key the call is recorded on), and the answer
// names the provider, the model and why the one before did not answer. A
// limit of every provider is 429 with Retry-After.
func TestAnImageIsAskedThroughTheClientAPI(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	made := &madeImages{}
	w.srv.WithImages(made, imagesOver{Service: gatewayOver(t, w.svc)})

	owner, _ := w.token("web", bearertoken.Owner)

	n, size := 2, "1536x1024"
	resp, err := owner.GenerateImagesWithResponse(ctx, openapi.ImageRequest{
		Prompt: "a fox", N: &n, Size: &size,
		Images: &[]openapi.ImageInput{{Data: []byte("\x89PNG fake")}},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))
	assert.Equal(t, openapi.ImageProvider("antigravity"), resp.JSON200.Provider)
	assert.Equal(t, "Zm94", resp.JSON200.Images[0].B64Json)
	assert.Equal(t, []openapi.WebSearchError{{Provider: "codex", Message: "gpt-image-2: limited"}},
		val(resp.JSON200.Errors))

	assert.Equal(t, 2, made.asked.N)
	assert.Equal(t, "1536x1024", made.asked.Size)
	require.Len(t, made.asked.Images, 1)
	assert.Equal(t, []byte("\x89PNG fake"), made.asked.Images[0].Data)
	assert.Empty(t, made.chat, "the owner's token belongs to no chat")

	made.err = &modelgateway.ImageError{Failures: []modelgateway.ImageFailure{
		{Provider: "codex", Status: http.StatusTooManyRequests, RetryAfter: 90 * time.Second, Message: "limited"},
	}}

	limited, err := owner.GenerateImagesWithResponse(ctx, openapi.ImageRequest{Prompt: "a fox"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, limited.StatusCode())
	assert.Equal(t, "90", limited.HTTPResponse.Header.Get("Retry-After"))
}
