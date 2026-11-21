package serviceproxy_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Scenario: issue #1023 with the owner's choice of 2026-11-21 on #1037 (review round 2): the Thư ký Bot only
// reads the owner's own accounts. In its own chat, on his turn, with his Lark sign-in and his Google set to
// Allow for sends, its tools still only read: a Lark message and a Gmail send are refused at once, asking nobody,
// sending nothing. A Bot that uses his accounts sends the same request on the same Allow.
func TestABotThatReadsTheOwnersAccountsNeverWritesThroughThem(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 100*time.Millisecond)
	ctx := t.Context()

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "lark", Label: "me", Secret: "conn-lark-me", AppLabel: "app",
	})
	require.NoError(t, err)
	_, err = w.svc.SetConnectionChats(ctx, "lark", "me", true)
	require.NoError(t, err)
	require.NoError(t, w.svc.SetActionPolicy(ctx, "lark", "me", "lark.im.write", orchestration.PolicyAllow))
	require.NoError(t, w.svc.SetActionPolicy(ctx, "google", "work", "gmail.send", orchestration.PolicyAllow))

	piOf := func(name, access string) string {
		t.Helper()

		bot, err := w.svc.PutBot(ctx, orchestration.Bot{
			Name: name, OwnerAccounts: access,
			Connections: orchestration.AllowList{Limited: true, Names: []string{"lark:me", "google:work"}},
		})
		require.NoError(t, err)
		chat, err := w.svc.CreateChat(ctx, bot.ID, "")
		require.NoError(t, err)
		_, err = w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatOwner, "trả lời Lan giúp anh")
		require.NoError(t, err)
		secret, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
		require.NoError(t, err)

		return secret
	}

	send := []byte(`{"receive_id":"oc_1","msg_type":"text","content":"{\"text\":\"Dạ em gửi ạ\"}"}`)
	contentType, mailBody := mail("lan@example.com", "Báo giá", "Dạ em gửi ạ.", 1)

	reader := piOf("Thư ký", orchestration.OwnerAccountsRead)

	lark, refused := w.openTo("lark", reader, "me", "open.larksuite.com", "")
	require.Zero(t, refused)
	assert.Equal(t, http.StatusOK, lark.do(http.MethodGet, "/open-apis/im/v1/chats/oc_1", "", nil).Status,
		"reads stay")

	got := lark.do(http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=chat_id", "application/json", send)
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Contains(t, got.Body, "only reads the owner's own accounts")

	gmail, refused := w.open(reader, "work", "gmail.googleapis.com", "")
	require.Zero(t, refused)
	got = gmail.do(http.MethodPost, sendPath, contentType, mailBody)
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Contains(t, got.message(), agenttool.OwnerReadOnlyRefusal)

	assert.Empty(t, w.approvals(""), "nothing is asked")
	for _, s := range w.service.received() {
		assert.Equal(t, http.MethodGet, s.Method, "only the read reached a service: %s", s.URL)
	}

	user := piOf("Trợ lý", orchestration.OwnerAccountsUse)

	gmail, refused = w.open(user, "work", "gmail.googleapis.com", "")
	require.Zero(t, refused)
	assert.Equal(t, http.StatusOK, gmail.do(http.MethodPost, sendPath, contentType, mailBody).Status,
		"a Bot that uses his accounts sends on his Allow")
}
