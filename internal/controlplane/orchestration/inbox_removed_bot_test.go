package orchestration

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// Scenario: issue #1124, section 2. The Thư ký Bot added items to the owner's Inbox and was given one; then the
// owner removed it with its chats. Its items that are not closed pass to the main Bot, each with an event saying
// so: the one it added stays added by it in the history but is the main Bot's to edit now, and the one given to it
// is given to the main Bot. One it closed stays as it was. The main Bot then records one done for the owner, with
// his message in its chat as evidence.
func TestARemovedBotsOpenItemsPassToTheMainBot(t *testing.T) {
	t.Parallel()

	w := newSeedWorld(t)
	ctx := t.Context()
	inbox := w.s.Inbox()

	thuKy, err := w.s.PutBot(ctx, Bot{Name: "thu-ky"})
	require.NoError(t, err)

	byThuKy := inboxstore.Actor{Kind: inboxstore.Bot, ID: thuKy.ID}
	fromWhatsApp := inboxstore.Source{
		Kind: inboxstore.FromWhatsApp, ID: "8491@s.whatsapp.net", Account: "main", Quote: "Anh trả lời em nhé",
		Ref: &inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "3EB0A1"},
	}

	added, err := inbox.Create(ctx, byThuKy, inboxstore.NewTodo{
		Title: "Reply to Đức Anh", State: inboxstore.Open, Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Source: fromWhatsApp, WhyOpen: inboxstore.AskedDirectly,
	})
	require.NoError(t, err)

	given, err := inbox.Create(ctx, inboxOwner, inboxstore.NewTodo{
		Title: "Summarise the standup", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: thuKy.ID},
	})
	require.NoError(t, err)

	closed, err := inbox.Create(ctx, byThuKy, inboxstore.NewTodo{
		Title: "Send the slides", State: inboxstore.Open, Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Source: fromWhatsApp, WhyOpen: inboxstore.AskedDirectly,
	})
	require.NoError(t, err)

	closed, err = inbox.Drop(ctx, inboxstore.Change{Actor: inboxOwner, ID: closed.ID, Revision: closed.Revision},
		"Sent already", nil)
	require.NoError(t, err)

	_, err = w.s.ForceDeleteBot(ctx, thuKy.ID)
	require.NoError(t, err)

	got, err := inbox.Get(ctx, added.ID)
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Actor{Kind: inboxstore.Bot, ID: DefaultBotID}, got.CreatedBy)
	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.Me}, got.Assignee, "who does it does not change")
	assert.Equal(t, added.Revision+1, got.Revision)

	last := got.Events[len(got.Events)-1]
	assert.Equal(t, inboxstore.Actor{Kind: inboxstore.System, ID: "bots"}, last.Actor)
	assert.Equal(t, inboxstore.EventEdited, last.Kind)
	assert.JSONEq(t, `{"created_by":"`+thuKy.ID+`"}`, string(last.Before))
	assert.JSONEq(t, `{"created_by":"`+DefaultBotID+`","reason":"Bot thu-ky was removed; its items pass to the `+
		`main Bot"}`, string(last.After))
	assert.Equal(t, inboxstore.Actor{Kind: inboxstore.Bot, ID: thuKy.ID}, got.Events[0].Actor,
		"the history still says who added it")

	gotGiven, err := inbox.Get(ctx, given.ID)
	require.NoError(t, err)
	assert.Equal(t, inboxstore.Assignee{Kind: inboxstore.ABot, Name: DefaultBotID}, gotGiven.Assignee)
	assert.Equal(t, inboxOwner, gotGiven.CreatedBy)

	gotClosed, err := inbox.Get(ctx, closed.ID)
	require.NoError(t, err)
	assert.Equal(t, closed.Revision, gotClosed.Revision, "a closed item stays as it was")
	assert.Equal(t, byThuKy, gotClosed.CreatedBy)

	chat, err := w.s.CreateChat(ctx, DefaultBotID, "")
	require.NoError(t, err)

	said, err := w.s.AddChatMessage(ctx, chat.ID, ChatOwner, "Anh trả lời Đức Anh rồi")
	require.NoError(t, err)

	done, err := inbox.Done(ctx, inboxstore.Change{Actor: inboxBot, ID: got.ID, Revision: got.Revision, Chat: chat.ID},
		inboxstore.Closing{
			DoneBy: inboxstore.Assignee{Kind: inboxstore.Me}, How: inboxstore.ClosedByBot,
			Evidence: []inboxstore.Link{{Kind: inboxstore.LinkChatMessage, Ref: strconv.FormatInt(said.Seq, 10)}},
		})
	require.NoError(t, err, "the main Bot closes the removed Bot's item")
	assert.Equal(t, inboxstore.Done, done.State)
}
