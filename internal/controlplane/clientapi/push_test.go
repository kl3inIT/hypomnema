package clientapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// fakePusher is the Web Push sender at the API's boundary: it serves a key and records the devices it tests.
type fakePusher struct{ tested []string }

func (p *fakePusher) PublicKey(context.Context) (string, error) { return "BPublicKey", nil }

func (p *fakePusher) Test(_ context.Context, sub orchestration.PushSubscription) error {
	p.tested = append(p.tested, sub.Endpoint)

	return nil
}

// Scenario: the owner turns notifications on in his iPhone's Home Screen app (issue #1047): the page reads the
// key to subscribe with, keeps the browser's subscription, sees it listed as his device, sends it a test push and
// removes it. A coordinator's token may do none of it; a control plane that sends no push says so.
func TestTheOwnerKeepsTestsAndRemovesHisDevices(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("owner", bearertoken.Owner)
	coordinator, _ := w.token("pi", bearertoken.Coordinator)

	resp, err := owner.ListPushSubscriptionsWithResponse(ctx)
	require.NoError(t, err)
	assert.Equal(t, openapi.Unavailable, errorCode(t, resp.Body, resp.StatusCode(), http.StatusServiceUnavailable))

	pusher := &fakePusher{}
	w.srv.WithPusher(pusher)

	body := openapi.NewPushSubscription{Endpoint: "https://web.push.apple.com/QGuQ", Label: ptr("iPhone · Safari")}
	body.Keys.P256dh, body.Keys.Auth = "BKey", "auth"

	put, err := owner.PutPushSubscriptionWithResponse(ctx, body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, put.StatusCode(), string(put.Body))
	assert.Equal(t, "iPhone · Safari", put.JSON200.Label)

	list, err := owner.ListPushSubscriptionsWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, list.StatusCode(), string(list.Body))
	assert.Equal(t, "BPublicKey", list.JSON200.PublicKey)
	require.Len(t, list.JSON200.Data, 1)
	assert.Equal(t, body.Endpoint, list.JSON200.Data[0].Endpoint)
	assert.NotContains(t, string(list.Body), "auth", "the browser's keys are never answered")

	id := put.JSON200.Id

	tested, err := owner.TestPushSubscriptionWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, tested.StatusCode(), string(tested.Body))
	assert.Equal(t, []string{body.Endpoint}, pusher.tested)

	denied, err := coordinator.ListPushSubscriptionsWithResponse(ctx)
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, denied.Body, denied.StatusCode(), http.StatusForbidden))

	deniedPut, err := coordinator.PutPushSubscriptionWithResponse(ctx, body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, deniedPut.StatusCode())

	deleted, err := owner.DeletePushSubscriptionWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, deleted.StatusCode(), string(deleted.Body))

	again, err := owner.DeletePushSubscriptionWithResponse(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, openapi.NotFound, errorCode(t, again.Body, again.StatusCode(), http.StatusNotFound))

	insecure := body
	insecure.Endpoint = "http://push.example/x"
	refused, err := owner.PutPushSubscriptionWithResponse(ctx, insecure)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, refused.StatusCode())
}
