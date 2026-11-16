package serviceproxy_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
)

// Scenario (issue #645): a Bot asks to send a WhatsApp message as the owner. It is not a request to a service, but
// it is decided as one: it waits for his approval and is told so; once he approves it, the same act asked again
// runs once, and sending it again asks again; and when he sets the action to never it is refused without asking.
func TestSendingAsTheOwnerWaitsForHisApproval(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 50*time.Millisecond)
	ctx := t.Context()

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "whatsapp", Label: "me", Account: "+84 900 000 001", Secret: "conn-whatsapp-me",
	})
	require.NoError(t, err)

	send := func(text string) error {
		return w.proxy.Judge(ctx, serviceproxy.Act{
			Caller:      serviceproxy.Caller{Name: "bot b1 in chat " + w.chat, Coordinator: true, ChatID: w.chat},
			Integration: serviceproxy.WhatsAppID, Label: "me", ChatID: w.chat, Action: "whatsapp.send",
			Subject: "PMO", Fields: []orchestration.Field{{Label: "Text", Value: text}}, Hash: "h-" + text,
		})
	}
	refused := func(err error) string {
		t.Helper()

		var r *serviceproxy.RefusedError
		require.ErrorAs(t, err, &r)

		return r.Message
	}

	assert.Contains(t, refused(send("Gửi báo giá nhé")), "Waiting for the owner's approval")

	asked := w.pending()
	assert.Equal(t, "Send a message as you: PMO", asked.Title)
	assert.Equal(t, "whatsapp.send", asked.Action)
	assert.Equal(t, []orchestration.Field{{Label: "Text", Value: "Gửi báo giá nhé"}}, asked.Payload)

	_, err = w.proxy.Decide(ctx, asked.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)

	require.NoError(t, send("Gửi báo giá nhé"), "the approved message is sent")
	assert.Contains(t, refused(send("Gửi báo giá nhé")), "Waiting", "an approval sends once: the same again asks again")

	require.NoError(t, w.svc.SetActionPolicy(ctx, "whatsapp", "me", "whatsapp.send", orchestration.PolicyNever))
	assert.Contains(t, refused(send("Third")), "never")
	assert.Len(t, w.approvals(orchestration.ApprovalPending), 1, "never asks nobody: only the second send waits")
}
