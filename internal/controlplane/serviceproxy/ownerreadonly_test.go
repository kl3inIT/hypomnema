package serviceproxy_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
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

// Scenario: review of #1037, round 3: the Bot's tool opened its connection to Gmail while the Bot used the owner's
// accounts; the owner then makes it read them only. The next send on that same open connection is refused: what
// the Bot may do is read at each request, never kept from when the connection opened.
func TestAnOpenConnectionFollowsTheBotsSettingAtEachRequest(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 100*time.Millisecond)
	ctx := t.Context()
	require.NoError(t, w.svc.SetActionPolicy(ctx, "google", "work", "gmail.send", orchestration.PolicyAllow))

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "Thư ký", Connections: orchestration.AllowList{Limited: true, Names: []string{"google:work"}},
	})
	require.NoError(t, err)
	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)
	secret, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	gmail, refused := w.open(secret, "work", "gmail.googleapis.com", "")
	require.Zero(t, refused)

	contentType, body := mail("lan@example.com", "Báo giá", "Dạ em gửi ạ.", 1)
	require.Equal(t, http.StatusOK, gmail.do(http.MethodPost, sendPath, contentType, body).Status, "it uses them")

	bot.OwnerAccounts = orchestration.OwnerAccountsRead
	_, err = w.svc.PutBot(ctx, bot)
	require.NoError(t, err)

	got := gmail.do(http.MethodPost, sendPath, contentType, body)
	assert.Equal(t, http.StatusForbidden, got.Status, "the same connection now only reads")
	assert.Contains(t, got.message(), agenttool.OwnerReadOnlyRefusal)
	assert.Len(t, w.service.received(), 1, "only the first send reached Google")
}

// Scenario: issue #1071, finding 1 of the fourth review of #1037: the Bot's pi sends a mail through the owner's
// Google, and the send waits for his approval. Meanwhile he makes the Bot read his accounts only, then approves the
// mail. The send is refused, nothing reaches Google, and no approval is spent: what the Bot may do is read again
// when the answer comes, not kept from before the wait. The same holds for a send the control plane makes itself (an
// act, here a WhatsApp message as the owner).
func TestAnApprovalAnsweredAfterTheBotWasMadeToReadOnlyDoesNotWrite(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	ctx := t.Context()

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "whatsapp", Label: "me", Account: "+84 900 000 001", Secret: "conn-whatsapp-me",
	})
	require.NoError(t, err)

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name: "Thư ký", OwnerAccounts: orchestration.OwnerAccountsUse,
		Connections: orchestration.AllowList{Limited: true, Names: []string{"google:work", "whatsapp:me"}},
	})
	require.NoError(t, err)
	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)
	secret, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	readOnly := func(access string) {
		t.Helper()

		bot.OwnerAccounts = access
		_, err := w.svc.PutBot(ctx, bot)
		require.NoError(t, err)
	}

	gmail, refused := w.open(secret, "work", "gmail.googleapis.com", "")
	require.Zero(t, refused)

	contentType, body := mail("lan@example.com", "Báo giá", "Dạ em gửi ạ.", 1)
	answered := make(chan answer, 1)
	go func() { answered <- gmail.do(http.MethodPost, sendPath, contentType, body) }()

	asked := w.pending()
	readOnly(orchestration.OwnerAccountsRead)
	_, err = w.proxy.Decide(ctx, asked.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)

	got := <-answered
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Contains(t, got.message(), agenttool.OwnerReadOnlyRefusal)
	assert.Empty(t, w.service.received(), "nothing reached Google")

	approval, err := w.svc.GetApproval(ctx, asked.ID)
	require.NoError(t, err)
	assert.False(t, approval.Used, "the approval was not spent")

	readOnly(orchestration.OwnerAccountsUse)

	opened, err := http.NewRequestWithContext(ctx, http.MethodPost, w.srv.URL, nil)
	require.NoError(t, err)
	opened.Header.Set("Authorization", "Bearer "+secret)

	judged := make(chan error, 1)
	go func() {
		judged <- w.proxy.Judge(ctx, serviceproxy.Act{
			Caller:      serviceproxy.Caller{Name: "bot in chat", Coordinator: true, ChatID: chat.ID, Bot: bot.ID},
			Integration: serviceproxy.WhatsAppID, Label: "me", ChatID: chat.ID, Action: "whatsapp.send",
			Subject: "PMO", Fields: []orchestration.Field{{Label: "Text", Value: "Gửi báo giá nhé"}}, Hash: "h-1",
			Opened: opened,
		})
	}()

	asked = w.pending()
	readOnly(orchestration.OwnerAccountsRead)
	_, err = w.proxy.Decide(ctx, asked.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)

	var refusedAct *serviceproxy.RefusedError
	require.ErrorAs(t, <-judged, &refusedAct, "the act is not done")
	assert.Equal(t, agenttool.OwnerReadOnlyRefusal, refusedAct.Message)
}
