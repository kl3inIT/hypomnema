package orchestration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
)

// Web Push scenarios (issue #1047), in the same style as questions_test.go.

func queuedPushes(t *testing.T, s *Service) []PushNotice {
	t.Helper()

	n, err := s.QueuedPushes(t.Context())
	require.NoError(t, err)

	return n
}

// Scenario: each of the four kinds of what waits on the owner queues one push, with its words, in the
// transaction that makes it: a question for him, one the coordinator forwards to him, a pending Approval, a
// Connection request and a to-do a Bot proposes. What does not wait on him queues nothing: a question to the
// coordinator, the same Approval asked again, a to-do he writes himself or one a Bot opens in his chat.
func TestEachKindThatWaitsOnTheOwnerQueuesOnePush(t *testing.T) {
	t.Parallel()

	w := newRequestWorld(t)
	ctx := t.Context()
	tasks := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"}, NewTask{Ref: "b", Profile: "implementer",
		Spec: "b"})
	_, ca := w.start(tasks[0].ID)
	_, cb := w.start(tasks[1].ID)

	forOwner := w.ask(ca, libraryChoice(AddressOwner))
	forCoordinator := w.ask(cb, libraryChoice(AddressCoordinator))
	assert.Len(t, queuedPushes(t, w.s), 1, "a question to the coordinator waits on no one yet")
	require.NoError(t, w.s.ForwardQuestion(ctx, forCoordinator))

	asked := share("", "digest-1")
	asked.ChatID = w.main.ID
	approval, _, err := w.s.AskApproval(ctx, asked)
	require.NoError(t, err)
	_, _, err = w.s.AskApproval(ctx, asked)
	require.NoError(t, err)

	request := w.request(t, "google:team")

	bot := inboxstore.Actor{Kind: inboxstore.Bot, ID: w.bot.ID}
	proposed, err := w.s.Inbox().Create(ctx, bot, inboxstore.NewTodo{
		Title: "Reply to Lan about the contract", State: inboxstore.Proposed,
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Source: inboxstore.Source{Kind: inboxstore.FromWhatsApp, ID: "8490@s.whatsapp.net", Account: "main",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "3EB0"}},
	})
	require.NoError(t, err)

	_, err = w.s.Inbox().Create(ctx, inboxstore.Actor{Kind: inboxstore.Owner}, inboxstore.NewTodo{
		Title: "Buy milk", State: inboxstore.Open, Source: inboxstore.Source{Kind: inboxstore.ByHand},
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
	})
	require.NoError(t, err)
	_, err = w.s.Inbox().Create(ctx, bot, inboxstore.NewTodo{
		Title: "Send the contract", State: inboxstore.Open,
		Source:   inboxstore.Source{Kind: inboxstore.FromChat, ID: w.main.ID},
		Assignee: inboxstore.Assignee{Kind: inboxstore.ABot, Name: w.bot.ID},
	})
	require.NoError(t, err)

	run := "Run “add Google login to second-bite”"
	question := libraryChoice(AddressOwner).Text
	want := []PushNotice{
		{Kind: PushQuestion, ItemID: forOwner, Waiting: true, Title: run + " asks", Body: question, Path: "/"},
		{Kind: PushQuestion, ItemID: forCoordinator, Waiting: true, Title: run + " asks", Body: question, Path: "/"},
		{Kind: PushApproval, ItemID: approval.ID, Waiting: true, Title: "hr waits for your approval",
			Body: "Share a file: Release notes", Path: "/chats/" + w.main.ID},
		{Kind: PushConnectionRequest, ItemID: request.ID, Waiting: true, Title: "hr asks for a connection",
			Body: "google:team: to read the team's calendar", Path: "/chats/" + w.main.ID},
		{Kind: PushTodo, ItemID: proposed.ID, Waiting: true, Title: "hr added a to-do",
			Body: "Reply to Lan about the contract", Path: "/"},
	}

	got := queuedPushes(t, w.s)
	for i := range got {
		got[i].Seq = 0
	}

	assert.Equal(t, want, got)
}

// Scenario: the owner answers before the push goes. The queued push says its item no longer waits, so the sender
// sends nothing; once sent it is off the queue. A deleted item is gone from what it says too.
func TestAPushWhoseItemWasAnsweredNoLongerWaits(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	_, c := w.start(task.ID)
	id := w.ask(c, libraryChoice(AddressOwner))

	_, err := w.s.AnswerQuestion(ctx, id, AddressOwner, Answer{Option: choice(0)})
	require.NoError(t, err)

	got := queuedPushes(t, w.s)
	require.Len(t, got, 1)
	assert.False(t, got[0].Waiting)

	require.NoError(t, w.s.PushSent(ctx, got[0].Seq))
	assert.Empty(t, queuedPushes(t, w.s))
}

// Scenario: the owner turns notifications on in a browser, then again after a reinstall, which keeps the same
// endpoint with new keys (Coder's upsert): one row, its id kept, its keys new. A second device adds a row; a
// device the push service says is gone is deleted by its endpoint; new VAPID keys delete every device.
func TestAPushSubscriptionIsOneRowPerBrowser(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	endpoint := "https://web.push.apple.com/QGuQyavXutnMH"

	first, err := w.s.SavePushSubscription(ctx, NewPushSubscription{
		Endpoint: endpoint, P256dh: "key-1", Auth: "auth-1", Label: "iPhone · Safari",
	})
	require.NoError(t, err)

	again, err := w.s.SavePushSubscription(ctx, NewPushSubscription{Endpoint: endpoint, P256dh: "key-2", Auth: "auth-2"})
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, "iPhone · Safari", again.Label, "the service worker's renewal gives no label and keeps it")
	assert.Equal(t, "key-2", again.P256dh)
	assert.Equal(t, "auth-2", again.Auth)

	_, err = w.s.SavePushSubscription(ctx, NewPushSubscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/abc", P256dh: "k", Auth: "a", Label: "Mac · Chrome",
	})
	require.NoError(t, err)

	all, err := w.s.ListPushSubscriptions(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, first.ID, all[0].ID)

	for _, bad := range []NewPushSubscription{
		{Endpoint: "http://push.example/x", P256dh: "k", Auth: "a"},
		{Endpoint: "https://push.example/x", Auth: "a"},
		{Endpoint: "https://push.example/x", P256dh: "k", Auth: "a", Label: strings.Repeat("x", 101)},
	} {
		_, err := w.s.SavePushSubscription(ctx, bad)
		require.ErrorIs(t, err, ErrInvalid)
	}

	require.NoError(t, w.s.DeletePushSubscriptionByEndpoint(ctx, endpoint))
	_, err = w.s.GetPushSubscription(ctx, first.ID)
	require.ErrorIs(t, err, ErrNotFound)
	require.ErrorIs(t, w.s.DeletePushSubscription(ctx, first.ID), ErrNotFound)

	n, err := w.s.DeleteAllPushSubscriptions(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}
