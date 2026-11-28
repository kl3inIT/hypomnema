package webpush_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/inboxstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/controlplane/webpush"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

const publicURL = "https://hypo.example"

// world is a control plane's orchestration and vault over a fresh database, a fake push service, and a sender.
type world struct {
	t      *testing.T
	path   string
	svc    *orchestration.Service
	vault  *vault.Service
	keys   *webpush.Keys
	push   *pushService
	sender *webpush.Sender
	chat   orchestration.Chat
}

func newWorld(t *testing.T, url string) *world {
	t.Helper()

	path := sqlitetest.Migrated(t, orchestration.Migrate)

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keyring, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	w := &world{t: t, path: path, push: newPushService(t)}
	w.svc = orchestration.New(db, platform.SystemClock{}, platform.NewID)
	w.vault = vault.New(db, platform.SystemClock{}, platform.NewID, keyring, nil)
	w.keys = webpush.NewKeys(w.vault)
	w.sender = webpush.New(webpush.Config{
		Store: w.svc, Keys: w.keys, PublicURL: url, Client: w.push.srv.Client(),
		RetryWaits: []time.Duration{time.Millisecond, time.Millisecond},
	})

	w.chat, _, err = w.svc.OpenMainChat(t.Context(), orchestration.DefaultBotID, "", "", orchestration.ChatModel{})
	require.NoError(t, err)

	return w
}

// run starts the sender until the test ends.
func (w *world) run() {
	ctx, cancel := context.WithCancel(context.WithoutCancel(w.t.Context()))
	done := make(chan struct{})

	go func() {
		defer close(done)

		w.sender.Run(ctx)
	}()

	w.t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (w *world) subscribe(name string) browser {
	w.t.Helper()

	b := w.push.browser(name)
	_, err := w.svc.SavePushSubscription(w.t.Context(), orchestration.NewPushSubscription{
		Endpoint: b.endpoint, P256dh: b.p256dh(), Auth: b.authKey(), Label: name,
	})
	require.NoError(w.t, err)

	return b
}

// approval asks the owner to approve a request from the chat; a new digest is a new request.
func (w *world) approval(digest string) orchestration.Approval {
	w.t.Helper()

	a, granted, err := w.svc.AskApproval(w.t.Context(), orchestration.NewApproval{
		Integration: "google", Label: "work", Action: "gmail.send", ActionName: "Send mail", ChatID: w.chat.ID,
		AskedBy: "pi", Title: "Send mail to lan@example.com: the contract", MatchHash: digest,
	})
	require.NoError(w.t, err)
	require.False(w.t, granted)

	return a
}

// sent waits until nothing is queued: every push queued was sent or dropped.
func (w *world) sent() {
	w.t.Helper()

	require.Eventually(w.t, func() bool {
		n, err := w.svc.QueuedPushes(w.t.Context())

		return err == nil && len(n) == 0
	}, 10*time.Second, 5*time.Millisecond)
}

func (w *world) devices() []string {
	w.t.Helper()

	subs, err := w.svc.ListPushSubscriptions(w.t.Context())
	require.NoError(w.t, err)

	var labels []string
	for _, s := range subs {
		labels = append(labels, s.Label)
	}

	return labels
}

// Scenario: a Bot asks the owner to approve a request while his phone and his laptop are subscribed. Once the
// Approval is committed, each device gets one push, encrypted to its keys and signed with the VAPID key the API
// serves, kept five minutes, high urgency, named by the item (tag and Topic), with the Bot, the request and the
// chat's page. The same request asked again waits on the same Approval and pushes nothing more.
func TestAPendingApprovalReachesEveryDeviceOnceAfterItsCommit(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	phone, laptop := w.subscribe("phone"), w.subscribe("laptop")
	w.run()

	a := w.approval("digest-1")
	w.sent()
	w.approval("digest-1")

	// A later item shows the sender has gone past the repeat without pushing it.
	later := w.approval("digest-2")
	w.sent()

	public, err := w.sender.PublicKey(t.Context())
	require.NoError(t, err)

	bot, err := w.svc.GetBot(t.Context(), orchestration.DefaultBotID)
	require.NoError(t, err)

	topics := map[string]bool{}

	for _, b := range []browser{phone, laptop} {
		name := strings.TrimPrefix(b.endpoint, w.push.srv.URL+"/push/")
		got := w.push.got(name)
		require.Len(t, got, 2, "one push for each Approval, none for the repeat")

		first := got[0]
		assert.Equal(t, "300", first.header.Get("TTL"))
		assert.Equal(t, "high", first.header.Get("Urgency"))
		assert.Equal(t, "aes128gcm", first.header.Get("Content-Encoding"))
		assert.Len(t, first.header.Get("Topic"), 32)
		assert.Equal(t, public, vapid(t, first, w.push.srv.URL, publicURL))

		p := b.payload(t, first)
		assert.Equal(t, 8030, p.WebPush, "Declarative Web Push")
		assert.Equal(t, bot.Name+" waits for your approval", p.Notification.Title)
		assert.Equal(t, "Send mail to lan@example.com: the contract", p.Notification.Body)
		assert.Equal(t, publicURL+"/chats/"+w.chat.ID, p.Notification.Navigate)
		assert.Equal(t, "hypomnema-approval-"+a.ID, p.Notification.Tag)
		assert.Equal(t, "hypomnema-approval-"+later.ID, b.payload(t, got[1]).Notification.Tag)

		topics[first.header.Get("Topic")] = true
		assert.NotEqual(t, first.header.Get("Topic"), got[1].header.Get("Topic"), "another item, another Topic")
	}

	assert.Len(t, topics, 1, "the same item has the same Topic on every device")
}

// Scenario: two pushes of the same item carry the same tag and Topic, so the push service keeps only the newer of
// two still pending and the device shows the newer in place of the older.
func TestASecondPushOfTheSameItemReplacesTheFirst(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	phone := w.subscribe("phone")

	sub, err := w.svc.ListPushSubscriptions(t.Context())
	require.NoError(t, err)
	require.NoError(t, w.sender.Test(t.Context(), sub[0]))
	require.NoError(t, w.sender.Test(t.Context(), sub[0]))

	got := w.push.got("phone")
	require.Len(t, got, 2)
	assert.Equal(t, got[0].header.Get("Topic"), got[1].header.Get("Topic"))
	assert.Equal(t, phone.payload(t, got[0]).Notification.Tag, phone.payload(t, got[1]).Notification.Tag)
	assert.Equal(t, "Notifications work", phone.payload(t, got[0]).Notification.Title)
	assert.Equal(t, publicURL+"/settings/notifications", phone.payload(t, got[0]).Notification.Navigate)
}

// Scenario: the owner reinstalled the app on one device and cleared another's site data: the push service answers
// 410 and 404 for them. Both are deleted; the third device keeps its subscription; the push is off the queue.
func TestADeviceThePushServiceNoLongerKnowsIsDeleted(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	w.subscribe("gone")
	w.subscribe("missing")
	w.subscribe("kept")
	w.push.answer("gone", http.StatusGone)
	w.push.answer("missing", http.StatusNotFound)
	w.run()

	w.approval("digest-1")
	w.sent()

	assert.Equal(t, []string{"kept"}, w.devices())
	assert.Len(t, w.push.got("gone"), 1, "a gone device is not tried again")
}

// Scenario: a test push to a device the push service no longer knows tells the owner so, and the device is
// removed from his list.
func TestATestPushToAGoneDeviceRemovesIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	w.subscribe("gone")
	w.push.answer("gone", http.StatusGone)

	sub, err := w.svc.ListPushSubscriptions(t.Context())
	require.NoError(t, err)

	err = w.sender.Test(t.Context(), sub[0])
	require.ErrorIs(t, err, webpush.ErrGone)
	require.ErrorIs(t, err, orchestration.ErrNotFound, "the API answers not_found")
	assert.Empty(t, w.devices())
}

// Scenario: the push service is busy (503, then 429): the push is tried again and arrives on the third try. A
// service that keeps failing is tried three times, then the push is dropped; the device stays subscribed.
func TestAPushRefusedForNowIsTriedAgain(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	w.subscribe("busy")
	w.subscribe("down")
	w.push.answer("busy", http.StatusServiceUnavailable, http.StatusTooManyRequests)
	w.push.answer("down", http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusInternalServerError)
	w.run()

	w.approval("digest-1")
	w.sent()

	assert.Len(t, w.push.got("busy"), 3)
	assert.Len(t, w.push.got("down"), 3, "three tries, no more")
	// Both devices stay subscribed. The list is oldest first by the millisecond, and the two subscriptions above
	// are often made in the same one, where the order falls to their random ids (#1068): compare without order.
	assert.ElementsMatch(t, []string{"busy", "down"}, w.devices())
}

// Scenario: the owner answers the Approval in the app before the sender gets to it (the control plane was
// stopped): its push is not sent, and it is off the queue. With no device subscribed, nothing is sent either.
func TestAnItemAnsweredBeforeItsPushIsNotPushed(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	w.subscribe("phone")

	a := w.approval("digest-1")
	_, err := w.svc.DecideApproval(t.Context(), a.ID, false, "")
	require.NoError(t, err)

	w.run()
	w.sent()
	assert.Empty(t, w.push.got("phone"))
}

// Scenario: a control plane without an https public URL has no VAPID subject Apple accepts: it serves no key and
// sends nothing, and what was queued is dropped rather than kept forever.
func TestWithoutAnHTTPSPublicURLNothingIsPushed(t *testing.T) {
	t.Parallel()

	w := newWorld(t, "http://10.0.0.5:8080")
	w.subscribe("phone")

	_, err := w.sender.PublicKey(t.Context())
	require.ErrorIs(t, err, orchestration.ErrUnavailable)

	w.run()
	w.approval("digest-1")
	w.sent()
	assert.Empty(t, w.push.got("phone"))
}

// Scenario: the first reads of the VAPID key, however many at once, make one pair, kept in the vault sealed: the
// private key is in no database file in the clear. Regenerating makes another, which the next read returns.
func TestTheVAPIDKeyIsMadeOnceAndKeptSealed(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	ctx := t.Context()

	var (
		wg   sync.WaitGroup
		pubs [4]string
	)

	for i := range pubs {
		wg.Go(func() {
			public, err := w.sender.PublicKey(ctx)
			assert.NoError(t, err)

			pubs[i] = public
		})
	}

	wg.Wait()

	for _, p := range pubs {
		assert.Equal(t, pubs[0], p, "one pair, however many asked first")
	}

	private, public, err := w.keys.Pair(ctx)
	require.NoError(t, err)
	assert.Equal(t, pubs[0], public)
	assert.Len(t, public, 87, "a P-256 point, uncompressed, in unpadded base64url")

	secret, err := w.vault.Get(ctx, "web-push-vapid-key")
	require.NoError(t, err)
	assert.Equal(t, vault.KindGeneric, secret.Kind)

	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(w.path + suffix) //nolint:gosec // the test's own database files
		if err != nil {
			continue
		}

		assert.NotContains(t, string(raw), private, "the private key is sealed")
	}

	keep, err := w.keys.Replacement()
	require.NoError(t, err)
	_, err = w.svc.ReplacePushKeys(ctx, keep)
	require.NoError(t, err)

	_, again, err := w.keys.Pair(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, public, again, "the next read is the new pair")
}

// worker puts one Task of a Run from the chat on a node and has its worker ask the coordinator, as the client API's
// tests drive a Dispatch (clientapi_test.go, askFromAWorker).
func (w *world) workerAsks(text string) string {
	w.t.Helper()
	ctx := w.t.Context()

	node, _, err := w.svc.AddNode(ctx, "box", 0)
	require.NoError(w.t, err)
	require.NoError(w.t, w.svc.NodeHello(ctx, node.ID, orchestration.Hello{
		WorktreeRoot: "/srv/wt", Runtimes: []orchestration.RuntimeInfo{{Runtime: "claude"}},
		System: orchestration.SystemInfo{CPUs: 8, MemoryBytes: 16 << 30},
	}))
	_, err = w.svc.AddTokenAccount(ctx, "claude", "personal", func(id string) string { return "vault:" + id })
	require.NoError(w.t, err)

	run, err := w.svc.CreateRun(ctx, orchestration.NewRun{
		Objective: "draft the release notes", CreatedBy: "pi", ChatID: w.chat.ID, Bot: orchestration.DefaultBotID,
	})
	require.NoError(w.t, err)
	_, err = w.svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{{Ref: "a", Spec: "work"}})
	require.NoError(w.t, err)

	scheduled, err := w.svc.Schedule(ctx)
	require.NoError(w.t, err)
	require.Len(w.t, scheduled.Created, 1)

	call := orchestration.NodeCall{NodeID: node.ID, DispatchID: scheduled.Created[0], RequestID: "req_1"}
	accepted, err := w.svc.AcceptDispatch(ctx, call)
	require.NoError(w.t, err)

	call.Epoch, call.RequestID = accepted.Epoch, "req_2"
	require.NoError(w.t, w.svc.ReportStarted(ctx, call, "pid:1@1", "sess_1"))

	call.RequestID = "req_3"
	id, err := w.svc.AskQuestion(ctx, call, orchestration.Ask{Text: text, To: orchestration.AddressCoordinator})
	require.NoError(w.t, err)

	return id
}

// Scenario: the other three kinds of what waits on the owner each reach his phone once, after their commit, with
// the Bot, the item's own words and its page: a question the coordinator forwards to him, a Bot's request for a
// Connection and a to-do a Bot proposes from his WhatsApp. A question the coordinator keeps pushes nothing.
func TestAForwardedQuestionAConnectionRequestAndAToDoEachReachTheDeviceOnce(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	ctx := t.Context()
	phone := w.subscribe("phone")
	w.run()

	bot, err := w.svc.GetBot(ctx, orchestration.DefaultBotID)
	require.NoError(t, err)

	question := w.workerAsks("Ship the notes as they are, or wait for the changelog?")
	w.sent()
	assert.Empty(t, w.push.got("phone"), "a question for the coordinator waits on no one yet")

	require.NoError(t, w.svc.ForwardQuestion(ctx, question))
	w.sent()

	for _, c := range []orchestration.Connection{
		{Integration: "google", Label: "work", Secret: "s-work"},
		{Integration: "google", Label: "team", Secret: "s-team"},
	} {
		_, err := w.svc.SaveServiceConnection(ctx, c)
		require.NoError(t, err)
		_, err = w.svc.SetConnectionChats(ctx, c.Integration, c.Label, true)
		require.NoError(t, err)
	}

	hr, err := w.svc.PutBot(ctx, orchestration.Bot{
		Name:        "hr",
		Connections: orchestration.AllowList{Limited: true, Names: []string{"google:work"}},
		AgentSpec:   orchestration.AgentSpec{Instructions: "people"},
	})
	require.NoError(t, err)
	main, _, err := w.svc.OpenMainChat(ctx, hr.ID, "", "", orchestration.ChatModel{})
	require.NoError(t, err)

	request, err := w.svc.RequestConnection(ctx, hr.ID, main.ID, "google:team", "to read the team's calendar")
	require.NoError(t, err)
	w.sent()

	_, err = w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "whatsapp", Label: "main", Secret: "fixture",
	})
	require.NoError(t, err)

	todo, err := w.svc.Inbox().Create(ctx, inboxstore.Actor{Kind: inboxstore.Bot, ID: hr.ID}, inboxstore.NewTodo{
		Title: "Reply to Lan about the contract", State: inboxstore.Proposed,
		Assignee: inboxstore.Assignee{Kind: inboxstore.Me},
		Source: inboxstore.Source{Kind: inboxstore.FromWhatsApp, ID: "8490@s.whatsapp.net", Account: "main",
			Ref: &inboxstore.Link{Kind: inboxstore.LinkWhatsAppMessage, Ref: "3EB0"}},
	})
	require.NoError(t, err)
	w.sent()

	got := w.push.got("phone")
	require.Len(t, got, 3, "one push for each item, none for the question kept by the coordinator")

	want := []struct{ title, body, navigate, tag string }{
		{bot.Name + " asks", "Ship the notes as they are, or wait for the changelog?", "/chats/" + w.chat.ID,
			"question-" + question},
		{"hr asks for a connection", "google:team: to read the team's calendar", "/chats/" + main.ID,
			"connection_request-" + request.ID},
		{"hr added a to-do", "Reply to Lan about the contract", "/", "todo-" + todo.ID},
	}

	for i, r := range got {
		p := phone.payload(t, r)
		assert.Equal(t, want[i].title, p.Notification.Title)
		assert.Equal(t, want[i].body, p.Notification.Body)
		assert.Equal(t, publicURL+want[i].navigate, p.Notification.Navigate)
		assert.Equal(t, "hypomnema-"+want[i].tag, p.Notification.Tag)
		assert.Equal(t, "300", r.header.Get("TTL"))
		assert.Equal(t, "high", r.header.Get("Urgency"))
	}
}

// Scenario: an Approval's title is all characters JSON escapes six bytes each (review of #1050: 750 NULs). Cut to
// 1500 bytes it still overflows a push once encoded; the push is cut again to fit, and arrives.
func TestAnItemWhoseWordsGrowWhenEncodedStillArrives(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	phone := w.subscribe("phone")
	w.run()

	_, _, err := w.svc.AskApproval(t.Context(), orchestration.NewApproval{
		Integration: "google", Label: "work", Action: "gmail.send", ActionName: "Send mail", ChatID: w.chat.ID,
		AskedBy: "pi", Title: "Send " + strings.Repeat("\x00", 750) + `"mail"`, MatchHash: "digest-nul",
	})
	require.NoError(t, err)
	w.sent()

	got := w.push.got("phone")
	require.Len(t, got, 1)

	p := phone.payload(t, got[0])
	assert.True(t, strings.HasPrefix(p.Notification.Body, "Send \x00"), "%q", p.Notification.Body)
	assert.True(t, strings.HasSuffix(p.Notification.Body, "…"), "cut to fit")
	assert.Equal(t, publicURL+"/chats/"+w.chat.ID, p.Notification.Navigate)
}
