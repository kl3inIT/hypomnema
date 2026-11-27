package clientapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// fakeSender plays the owner's accounts: it keeps what it was asked to send and answers the message it sent.
type fakeSender struct {
	mu   sync.Mutex
	sent []sentReply
}

type sentReply struct {
	source inboxstore.Source
	text   string
}

func (f *fakeSender) SendAsOwner(_ context.Context, t inboxstore.Todo, text string) (inboxstore.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sent = append(f.sent, sentReply{source: t.Source, text: text})

	return inboxstore.Link{
		Kind: inboxstore.LinkWhatsAppSent, Ref: "3EB0SENT", Account: t.Source.Account, Chat: t.Source.ID,
	}, nil
}

func (f *fakeSender) replies() []sentReply {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]sentReply(nil), f.sent...)
}

// kaiAsks is an item the Routine's Bot read in the owner's WhatsApp chat with Kai, with a reply drafted.
func (w *inboxWorld) kaiAsks(t *testing.T) openapi.Todo {
	t.Helper()

	why := openapi.TodoWhyOpenAskedDirectly
	resp, err := w.routineBot.CreateTodoWithResponse(t.Context(), &openapi.CreateTodoParams{}, openapi.NewTodo{
		Title: "Reply to Kai", Draft: ptr("Hi Kai, Monday works."), WhyOpen: &why,
		Source: &openapi.NewTodoSource{
			Kind: ptr(openapi.TodoSourceKindWhatsapp), Id: ptr("8491@s.whatsapp.net"), Account: ptr("me"),
			Quote: ptr("Can we start Monday?"),
			Ref:   &openapi.TodoLink{Kind: openapi.TodoLinkKindWhatsappMessage, Ref: "3EB0ASK"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode(), string(resp.Body))

	return *resp.JSON201
}

// Scenario: issue #1111, section 3. The owner presses Send on a draft he edited: the text shown goes through his
// own account to the item's own chat, never another, and the item closes with the message sent as its evidence. A
// second press with the revision he read sends nothing. A Bot's token, and a coordinator's, cannot send at all.
func TestTheOwnerSendsADraftToTheItemsOwnChatAndItCloses(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newInboxWorld(t)
	sender := &fakeSender{}
	w.srv.WithInboxSender(sender)

	td := w.kaiAsks(t)
	send := openapi.TodoSend{Revision: td.Revision, Text: "Hi Kai, Monday works. See you then!"}

	for name, c := range map[string]*openapi.ClientWithResponses{
		"chat bot": w.chatBot, "routine bot": w.routineBot, "coordinator": w.coordinator,
	} {
		resp, err := c.SendTodoDraftWithResponse(ctx, td.Id, send)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode(), "%s: %s", name, resp.Body)
	}

	require.Empty(t, sender.replies(), "a Bot never sends as the owner")

	resp, err := w.owner.SendTodoDraftWithResponse(ctx, td.Id, send)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))

	assert.Equal(t, []sentReply{{
		source: inboxstore.Source{
			Kind: inboxstore.FromWhatsApp, ID: "8491@s.whatsapp.net", Account: "me", Quote: "Can we start Monday?",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "3EB0ASK"},
		},
		text: "Hi Kai, Monday works. See you then!",
	}}, sender.replies(), "the text as shown, to the item's own chat")

	got := resp.JSON200.Todo
	assert.Equal(t, openapi.TodoStateDone, got.State)
	require.NotNil(t, got.DoneBy)
	assert.Equal(t, openapi.TodoAssigneeKindMe, got.DoneBy.Kind)
	assert.Equal(t, []openapi.TodoLink{{
		Kind: openapi.TodoLinkKindWhatsappSent, Ref: "3EB0SENT", Account: ptr("me"), Chat: ptr("8491@s.whatsapp.net"),
	}}, got.Evidence, "the message sent closes it")
	assert.Equal(t, resp.JSON200.Sent, got.Evidence[0])

	again, err := w.owner.SendTodoDraftWithResponse(ctx, td.Id, send)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, again.StatusCode(), string(again.Body))
	assert.Len(t, sender.replies(), 1, "a second press sends nothing")

	byHand := w.add(ctx, t, "Book the flight", toOwner)
	none, err := w.owner.SendTodoDraftWithResponse(ctx, byHand.Id,
		openapi.TodoSend{Revision: byHand.Revision, Text: "hello"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, none.StatusCode(), "an item from no chat has nowhere to send")
	assert.Len(t, sender.replies(), 1)
}

// Scenario: issue #1111, sections 1 and 2, over the API. The owner hands an item to the main Bot with a prompt: it
// shows the Bot as assignee, its chat, and the prompt first in its thread. A Bot's token cannot hand an item over;
// the Bot comments on its item, and a coordinator cannot comment.
func TestTheOwnerHandsAnItemToTheMainBotAndTheBotComments(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newInboxWorld(t)
	td := w.add(ctx, t, "Fix the login page", toOwner)
	hand := openapi.TodoHand{Revision: td.Revision, Prompt: ptr("Give it to a worker to fix the code.")}

	refused, err := w.chatBot.HandTodoWithResponse(ctx, td.Id, hand)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), string(refused.Body))

	resp, err := w.owner.HandTodoWithResponse(ctx, td.Id, hand)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))

	got := *resp.JSON200
	assert.Equal(t, toMain, got.Assignee, "absent, the main Bot")
	require.NotNil(t, got.ChatId)

	chat, err := w.svc.GetChat(ctx, *got.ChatId)
	require.NoError(t, err)
	assert.Equal(t, orchestration.DefaultBotID, chat.Bot)

	require.NotNil(t, got.Events)
	last := (*got.Events)[len(*got.Events)-1]
	assert.Equal(t, inboxstore.EventCommented, last.Kind)
	assert.Equal(t, "Give it to a worker to fix the code.", last.After["text"])

	said, err := w.chatBot.CommentTodoWithResponse(ctx, td.Id, openapi.TodoComment{Text: "Started: Run run_1."})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, said.StatusCode(), string(said.Body))

	events := *said.JSON200.Events
	assert.Equal(t, openapi.TodoActorKindBot, events[len(events)-1].Actor.Kind)

	none, err := w.coordinator.CommentTodoWithResponse(ctx, td.Id, openapi.TodoComment{Text: "hi"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, none.StatusCode(), string(none.Body))
}

// The first Hand opens the chat reserved for its upload. A file-only comment is
// also owner input, and both the thread and the Bot's pending turn keep the file.
func TestInboxFilesReachTheOwnersTurn(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newInboxWorld(t)
	item := w.add(ctx, t, "Review the screenshot", toOwner)
	const chat = "cht_abcdefghijklmnopqrstuvwxyz"
	file := func(chat, name, media string) string {
		return fmt.Sprintf(`[{"name":%q,"path":%q,"type":%q,"size":32}]`, name, "attachments/"+chat+"/"+name, media)
	}
	handBody := fmt.Sprintf(`{"revision":%d,"chat_id":%q,"attachments":%s}`,
		item.Revision, chat, file(chat, "brief.pdf", "application/pdf"))
	hand, err := w.owner.HandTodoWithBodyWithResponse(ctx, item.Id, "application/json", strings.NewReader(handBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, hand.StatusCode(), string(hand.Body))
	require.Equal(t, ptr(chat), hand.JSON200.ChatId)

	owner, err := w.svc.OwnerTurn(ctx, chat)
	require.NoError(t, err)
	require.True(t, owner, "the Hand with only files is the owner's own turn")

	resp, err := w.owner.CommentTodoWithBodyWithResponse(ctx, item.Id, "application/json", strings.NewReader(
		`{"text":"","attachments":`+file(chat, "screenshot.png", "image/png")+`}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))

	owner, err = w.svc.OwnerTurn(ctx, chat)
	require.NoError(t, err)
	assert.True(t, owner, "the comment with only files is the owner's own turn")

	wakes, err := w.svc.Undelivered(ctx, chat)
	require.NoError(t, err)
	require.Len(t, wakes, 2)
	for i, name := range []string{"brief.pdf", "screenshot.png"} {
		assert.Equal(t, orchestration.ChatWake, wakes[i].Kind)
		require.Len(t, wakes[i].Attachments, 1)
		assert.Equal(t, "attachments/"+chat+"/"+name, wakes[i].Attachments[0].Path)
	}
	got := w.get(t, item.Id)
	require.NotNil(t, got.Events)
	last := (*got.Events)[len(*got.Events)-1]
	assert.Equal(t, "commented", last.Kind)
	assert.Equal(t, chat, last.After["chat"])
	assert.Contains(t, fmt.Sprint(last.After["attachments"]), "attachments/"+chat+"/screenshot.png")

	// A pass changes the destination for new files, while the old comment
	// still opens its screenshot in the chat that received it.
	passed, err := w.chatBot.EditTodoWithResponse(ctx, item.Id,
		openapi.TodoEdit{Revision: got.Revision, Assignee: &w.vmap})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, passed.StatusCode(), string(passed.Body))
	require.NotNil(t, passed.JSON200.ChatId)

	next := *passed.JSON200.ChatId
	require.NotEqual(t, chat, next)

	stale, err := w.owner.CommentTodoWithBodyWithResponse(ctx, item.Id, "application/json", strings.NewReader(
		`{"text":"","attachments":`+file(chat, "screenshot.png", "image/png")+`}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, stale.StatusCode(), string(stale.Body))

	sent, err := w.owner.CommentTodoWithBodyWithResponse(ctx, item.Id, "application/json", strings.NewReader(
		`{"text":"For VMAP","attachments":`+file(next, "tiles.png", "image/png")+`}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, sent.StatusCode(), string(sent.Body))

	require.NotNil(t, sent.JSON200.Events)
	events := *sent.JSON200.Events
	assert.Equal(t, last, events[len(*got.Events)-1])
	assert.Equal(t, next, events[len(events)-1].After["chat"])

	wakes, err = w.svc.Undelivered(ctx, next)
	require.NoError(t, err)
	require.Len(t, wakes, 2)
	assert.Empty(t, wakes[0].Attachments, "the pass does not carry the old Bot's files")
	require.Len(t, wakes[1].Attachments, 1)
	assert.Equal(t, "attachments/"+next+"/tiles.png", wakes[1].Attachments[0].Path)
}
