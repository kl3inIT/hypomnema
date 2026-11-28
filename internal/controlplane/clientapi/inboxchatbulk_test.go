package clientapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

func TestInboxChatBulkRestoresDefaultAndOwnerChoices(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newSecretaryChatsWorld(t, botFolder{})
	_, secret, err := w.svc.AddToken(ctx, "bulk-test", bearertoken.Owner)
	require.NoError(t, err)

	const path = "/inbox/chat-sources/zalo/main/bulk"

	post := func(path string, body []byte, want int) []byte {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url+BasePath+path, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		raw, err := io.ReadAll(resp.Body)
		assert.NoError(t, resp.Body.Close())
		require.NoError(t, err)
		require.Equal(t, want, resp.StatusCode, string(raw))

		return raw
	}

	list := func() openapi.InboxChatList {
		return w.chatsOf(t, "zalo:main", openapi.ListInboxChatsParams{})
	}

	set, err := w.owner.SetInboxChatWithResponse(ctx, "zalo", "main", "2002", openapi.InboxChatChoice{Reads: true})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, set.StatusCode())

	before := list()
	skip := post(path, []byte(`{"reads":false,"chat_ids":["1001","2002"]}`), http.StatusOK)
	require.Equal(t, map[string]string{
		"1001": "direct skipped by owner", "2002": "group skipped by owner",
		"3003": "bot skipped by default", "4004": "bot skipped by default",
	}, readsByID(list()))

	read := post(path, []byte(`{"reads":true,"filter":{"q":"alpha","kind":"group","state":"skipped"}}`), http.StatusOK)
	assert.Equal(t, map[string]string{
		"1001": "direct skipped by owner", "2002": "group read by owner",
		"3003": "bot skipped by default", "4004": "bot skipped by default",
	}, readsByID(list()))

	post(path+"/undo", skip, http.StatusConflict)
	assert.Equal(t, "direct skipped by owner", readsByID(list())["1001"], "a stale receipt restores none of the batch")

	post(path+"/undo", read, http.StatusNoContent)
	post(path+"/undo", skip, http.StatusNoContent)
	assert.Equal(t, before, list(), "including who set each choice and when")

	empty := post(path, []byte(`{"reads":true,"filter":{"state":"read"}}`), http.StatusOK)
	assert.JSONEq(t, `{"changes":[]}`, strings.TrimSpace(string(empty)))

	var receipt struct {
		Changes []json.RawMessage
	}
	require.NoError(t, json.Unmarshal(skip, &receipt))
	require.Len(t, receipt.Changes, 2)
}
