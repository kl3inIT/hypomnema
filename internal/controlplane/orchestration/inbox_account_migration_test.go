package orchestration_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

func TestMigrationRepairsInboxConnectionLabels(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := orchestration.BeforeInboxConnectionRepair(t)
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	_, err := svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "whatsapp", Label: "main", Account: "+84 369 230 426", Secret: "conn-whatsapp-main",
	})
	require.NoError(t, err)

	for id, account := range map[string]string{
		"tdo_phone": "84369230426", "tdo_label": "main", "tdo_unknown": "84911111111",
	} {
		_, err := db.ExecContext(ctx, `INSERT INTO todos (
			id, title, state, source_kind, source_id, source_account, source_ref, created_by_kind, created_at, updated_at
		) VALUES (?, 'Reply to Kai', 'open', 'whatsapp', '8491@s.whatsapp.net', ?,
			'{"kind":"whatsapp_message","ref":"m1"}', 'bot', 1, 1)`, id, account)
		require.NoError(t, err)
	}

	require.NoError(t, orchestration.Migrate(ctx, db))

	for id, want := range map[string]string{
		"tdo_phone": "main", "tdo_label": "main", "tdo_unknown": "84911111111",
	} {
		item, err := svc.Inbox().Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, want, item.Source.Account, id)
	}

	_, token, err := svc.AddToken(ctx, "owner", bearertoken.Owner)
	require.NoError(t, err)

	api := clientapi.New(svc, slog.New(slog.DiscardHandler), nil).WithInboxSender(recordedReply{})
	mux := http.NewServeMux()
	require.NoError(t, api.Register(mux))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := openapi.NewClientWithResponses(server.URL+clientapi.BasePath, openapi.WithRequestEditorFn(
		func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)

			return nil
		}))
	require.NoError(t, err)

	resp, err := client.SendTodoDraftWithResponse(ctx, "tdo_phone", openapi.TodoSend{Revision: 1, Text: "Hi Kai"})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))
	assert.Equal(t, openapi.TodoStateDone, resp.JSON200.Todo.State)
	require.NotNil(t, resp.JSON200.Sent.Account)
	assert.Equal(t, "main", *resp.JSON200.Sent.Account)
}

// recordedReply reports which account the delivery provider was asked to use.
type recordedReply struct{}

func (recordedReply) SendAsOwner(_ context.Context, item inboxstore.Todo, _ string) (inboxstore.Link, error) {
	return inboxstore.Link{
		Kind: inboxstore.LinkWhatsAppSent, Ref: "sent", Account: item.Source.Account, Chat: item.Source.ID,
	}, nil
}
