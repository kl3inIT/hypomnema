package serviceproxy_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/superset/supersettest"
)

// Scenario (issue #567, rule 9): the owner's Google account "work" was
// granted Gmail read only. Pi's request to send mail is refused before it is
// sent, whatever the owner's answer for sending is, and asks nobody: the
// account cannot do it. Reading, which is granted, runs; an action that was
// never read is unknown and runs as before. The log says the verdict.
func TestAnActionTheAccountCannotDoIsRefusedBeforeItIsSent(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 100*time.Millisecond)
	ctx := t.Context()

	require.NoError(t, w.svc.SetConnectionAccess(ctx, "google", "work", serviceproxy.Assess(integration.Holdings{
		Integration: integration.GoogleID, Grants: []integration.Grant{{Service: "gmail", ReadOnly: true}},
	}, time.Now())))
	require.NoError(t, w.svc.SetActionPolicy(ctx, "google", "work", "gmail.send", orchestration.PolicyAllow))

	gmail := w.piTool("gmail.googleapis.com")
	contentType, body := mail("minh@example.com", "One", "First.", 1)

	got := gmail.do(http.MethodPost, sendPath, contentType, body)
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Equal(t, `Refused: work@example.com cannot "Gmail: Send mail" at google:work: gmail granted read only. `+
		"It was not run; do not try another way. Ask the owner to grant it, or to run `hypomnema integrations check "+
		"google:work` once it is granted.", got.message())
	assert.Empty(t, w.service.received(), "nothing reached Google")
	assert.Empty(t, w.approvals(""), "a request the account cannot make asks nobody")
	assert.Contains(t, w.logs.String(), "action=gmail.send verdict=missing")

	assert.Equal(t, http.StatusOK, gmail.do(http.MethodGet, "/gmail/v1/users/me/messages", "", nil).Status)

	drive := w.piTool("www.googleapis.com")
	require.NoError(t, w.svc.SetConnectionAccess(ctx, "google", "work", orchestration.ConnectionAccess{}))
	assert.Equal(t, http.StatusOK, drive.do(http.MethodGet, "/drive/v3/files", "", nil).Status,
		"what was never read runs as before")
	assert.Len(t, w.service.received(), 2)
}

// Scenario (issue #567, rule 10): Google refuses pi's reads with 403. The
// answer goes back to the tool unchanged, and the account's access is read
// again, once however many refusals come in a minute.
func TestARefusalFromTheServiceReadsTheAccountAgainOnce(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 100*time.Millisecond)
	w.service.refuse(http.StatusForbidden, `{"error":{"code":403,"message":"Request had insufficient scopes."}}`)
	gmail := w.piTool("gmail.googleapis.com")

	for range 3 {
		got := gmail.do(http.MethodGet, "/gmail/v1/users/me/messages", "", nil)
		assert.Equal(t, http.StatusForbidden, got.Status)
		assert.Equal(t, "Request had insufficient scopes.", got.message(), "the service's own answer")
	}

	select {
	case conn := <-w.rechecked:
		assert.Equal(t, "google:work", conn)
	case <-time.After(5 * time.Second):
		t.Fatal("the account's access was not read again")
	}

	select {
	case conn := <-w.rechecked:
		t.Fatalf("read again twice in a minute: %s", conn)
	case <-time.After(200 * time.Millisecond):
	}

	w.service.refuse(http.StatusNotFound, `{"error":{"code":404,"message":"Not found"}}`)
	assert.Equal(t, http.StatusNotFound, gmail.do(http.MethodGet, "/gmail/v1/users/me/messages/x", "", nil).Status)

	select {
	case conn := <-w.rechecked:
		t.Fatalf("a 404 is no reason to read the account again: %s", conn)
	case <-time.After(100 * time.Millisecond):
	}
}

// Scenario (issue #567, rule 10): the Lark app "work" may create chats
// (im:chat:create) but not send as the bot, so creating or changing in
// Messages is granted: one of its methods passes. Lark answers pi's send with
// its code for a scope the app has not applied for. The tool gets Lark's
// answer unchanged, and the account is read again instead of the action being
// marked from one call: creating a chat, another method of the same action,
// still goes to Lark, and so does the next send.
func TestLarksMissingScopeCodeReadsTheAccountAgain(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 100*time.Millisecond)
	ctx := t.Context()

	//nolint:gosec // A made-up name of a vault secret, not a credential.
	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "lark", Label: "work", Account: "Hypomnema", Secret: "conn-lark-work",
	})
	require.NoError(t, err)
	_, err = w.svc.SetConnectionChats(ctx, "lark", "work", true)
	require.NoError(t, err)
	require.NoError(t, w.svc.SetActionPolicy(ctx, "lark", "work", "lark.im.write", orchestration.PolicyAllow))
	require.NoError(t, w.svc.SetConnectionAccess(ctx, "lark", "work", serviceproxy.Assess(integration.Holdings{
		Integration: integration.LarkID, Scopes: []string{"im:chat:create"},
	}, time.Now())))

	refusal := `{"code":99991672,"msg":"Access denied. One of the following scopes is required: [im:message]."}`
	w.service.refuse(http.StatusBadRequest, refusal)

	lark, refused := w.openTo("lark", w.pi, "work", "open.larksuite.com", w.chat)
	require.Zero(t, refused)

	send := []byte(`{"receive_id":"oc_1","msg_type":"text","content":"{\"text\":\"hi\"}"}`)
	got := lark.do(http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=chat_id", "application/json", send)
	assert.Equal(t, http.StatusBadRequest, got.Status)
	assert.JSONEq(t, refusal, got.Body, "Lark's answer goes back unchanged")

	select {
	case conn := <-w.rechecked:
		assert.Equal(t, "lark:work", conn)
	case <-time.After(5 * time.Second):
		t.Fatal("the account's access was not read again")
	}

	conn, err := w.svc.ConnectionOf(ctx, "lark", "work")
	require.NoError(t, err)
	assert.Equal(t, orchestration.AccessGranted, conn.Access.Of("lark.im.write").Access,
		"one refused method does not take the action away")

	lark.do(http.MethodPost, "/open-apis/im/v1/chats", "application/json", []byte(`{"name":"Reports"}`))
	lark.do(http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=chat_id", "application/json", send)
	assert.Len(t, w.service.received(), 3, "every call still went to Lark")
}

// Scenario: what agents and the owner are told an account cannot do is its
// missing actions, in the order the owner reads them, from the role a
// Superset account was found to have.
func TestWhatAnAccountCannotDoIsListedInTheOwnersOrder(t *testing.T) {
	t.Parallel()

	roles, ok := integration.SupersetRolesOf(supersettest.ServiceReaderRoles)
	require.True(t, ok)
	require.Len(t, roles["ServiceReader"], 22, "the role as deployed")

	access := serviceproxy.Assess(integration.Holdings{Integration: integration.SupersetID, Roles: roles}, time.Now())
	cannot := serviceproxy.CannotOf(integration.SupersetID, access)

	ids := make([]string, 0, len(cannot))
	for _, c := range cannot {
		ids = append(ids, c.Action.ID)
	}

	assert.Equal(t, []string{"superset.dashboards.write", "superset.dashboards.delete"}, ids[:2])
	assert.Contains(t, ids, "superset.logs.read")
	assert.NotContains(t, ids, "superset.dashboards.read")
	assert.Len(t, cannot, access.Missing())
	assert.Empty(t, serviceproxy.CannotOf(integration.SupersetID, orchestration.ConnectionAccess{}))
}
