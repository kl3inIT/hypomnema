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

// deciding answers Approvals as the service proxy does, without a held request.
type deciding struct{ svc *orchestration.Service }

func (d deciding) Decide(ctx context.Context, id string, approve bool, scope orchestration.ApprovalScope,
) (orchestration.Approval, error) {
	return d.svc.DecideApproval(ctx, id, approve, scope)
}

// Scenario: pi asks twice in a chat; the owner approves one for the chat
// from the web and denies the other. The chat's messages then hold one row
// each, naming the Approval, what it asked, the answer, its scope and when.
func TestAnAnsweredApprovalIsListedWithItsChat(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.srv.WithApprovals(deciding{w.svc})
	owner, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	ask := func(action string) string {
		a, _, err := w.svc.AskApproval(ctx, orchestration.NewApproval{
			Integration: "google", Label: "work", Action: action, ActionName: "Send mail", ChatID: chat.ID,
			AskedBy: "pi", Title: "Send mail: minh@example.com", MatchHash: "digest",
		})
		require.NoError(t, err)

		return a.ID
	}
	approved, denied := ask("gmail.send"), ask("gmail.drafts.send")

	for id, decision := range map[string]openapi.ApprovalDecisionDecision{
		approved: openapi.ApproveSession, denied: openapi.Reject,
	} {
		resp, err := owner.DecideApprovalWithResponse(ctx, id, openapi.DecideApprovalJSONRequestBody{Decision: decision})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))
	}

	page, err := owner.ListChatMessagesWithResponse(ctx, chat.ID, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, page.StatusCode(), string(page.Body))

	byApproval := map[string]openapi.ChatMessage{}
	for _, m := range page.JSON200.Data {
		require.NotNil(t, m.ApprovalId)
		byApproval[*m.ApprovalId] = m
	}
	require.Len(t, byApproval, 2, "one row an answer")

	yes := byApproval[approved]
	require.NotNil(t, yes.Kind)
	assert.Equal(t, openapi.ChatKindApproval, *yes.Kind)
	require.NotNil(t, yes.Approval)
	assert.Equal(t, "Send mail: minh@example.com", yes.Approval.Title)
	assert.Equal(t, "Send mail", yes.Approval.ActionName)
	assert.Equal(t, openapi.ApprovalStatusApproved, yes.Approval.Status)
	require.NotNil(t, yes.Approval.Scope)
	assert.Equal(t, openapi.ChatApprovalScopeSession, *yes.Approval.Scope)
	assert.NotNil(t, yes.Approval.DecidedAt)

	no := byApproval[denied]
	require.NotNil(t, no.Approval)
	assert.Equal(t, openapi.ApprovalStatusRejected, no.Approval.Status)
	assert.Nil(t, no.Approval.Scope)
}
