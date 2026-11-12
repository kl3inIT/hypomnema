package larkbot_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/larkbot"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

const (
	org     = "tenant-of-the-app"
	ownerID = "ou_owner"
	linhID  = "ou_linh"
	appID   = "cli_a1b2c3d4"
	secret  = "app-secret-never-printed"
	ownerDM = "oc_owner_dm"
	linhDM  = "oc_linh_dm"
	// main is the first Channel, which talks to the first Bot. maps is a second
	// one, with an app of its own: Lark names the same people otherwise there.
	main      = "main"
	maps      = "maps"
	mapsApp   = "cli_m4p5"
	mapsOwner = "ou_owner_in_maps"
	mapsLinh  = "ou_linh_in_maps"
	mapsDM    = "oc_owner_dm_in_maps"
	mapsLinhs = "oc_linh_dm_in_maps"
	eventual  = 5 * time.Second
	tick      = 10 * time.Millisecond
)

// card is a card the stand-in for Lark was asked to send.
type card struct {
	chat string
	text string
	// replyTo is the message the card answers, when it was sent as a reply.
	replyTo string
	closed  bool
	// updates counts the times its text was replaced before it was closed.
	updates int
}

// lark stands in for Lark as one app: it keeps what the Channel sent and
// answers the two lookups from what it was told about its members.
type lark struct {
	mu sync.Mutex
	// owner is the owner's open_id in this app.
	owner string
	// on are the Channel's handlers, once it listens.
	on larkbot.Events
	// groups are the groups the bot is in, as Lark lists them.
	groups []larkbot.Group
	// asked are the cards sent to a person, by card id, as they are now.
	asked map[string]larkbot.Card
	// marks are the emoji on each message, in the order they were put.
	marks   map[string][]string
	refuse  error
	notices []string
	cards   map[string]*card
	order   []string
	emails  map[string]string
	names   map[string]string
	// usedSecret is the secret the Channel connected with.
	usedSecret string
}

// newLark is an app at Lark in which the owner and Linh have these ids.
func newLark(owner, linh string) *lark {
	return &lark{
		owner:  owner,
		cards:  map[string]*card{},
		asked:  map[string]larkbot.Card{},
		marks:  map[string][]string{},
		emails: map[string]string{"owner@example.com": owner, "linh@example.com": linh},
		names:  map[string]string{linh: "Linh"},
	}
}

func (l *lark) Listen(_ context.Context, on larkbot.Events) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.refuse != nil {
		return l.refuse
	}

	l.on = on

	return nil
}

// botID is the bot's own open_id in every app of these tests.
const botID = "ou_the_bot"

func (l *lark) BotOpenID(context.Context) (string, error) { return botID, nil }

func (l *lark) Groups(context.Context) ([]larkbot.Group, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]larkbot.Group(nil), l.groups...), nil
}

func (l *lark) SendCard(_ context.Context, openID string, c larkbot.Card) (cardID, messageID string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if openID != l.owner {
		return "", "", errors.New("a question goes to the owner only")
	}

	id := fmt.Sprintf("ask-%d", len(l.asked)+1)
	l.asked[id] = c

	// A message's id is one at all of Lark, a card's id is its app's.
	return id, "om_" + strings.TrimPrefix(l.owner+"_", ownerID+"_") + id, nil
}

func (l *lark) ReplaceCard(_ context.Context, cardID string, c larkbot.Card, _ int) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.asked[cardID] = c

	return nil
}

// askedCard is a card sent to the owner, as it is now.
func (l *lark) askedCard(id string) (larkbot.Card, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	c, ok := l.asked[id]

	return c, ok
}

// pressed is a person pressing a button; it returns what they are told.
func (l *lark) pressed(operator string, b larkbot.Button) string {
	l.mu.Lock()
	press := l.on.Press
	l.mu.Unlock()

	return press(larkbot.Press{Operator: operator, Value: b.Value})
}

func (l *lark) SendText(_ context.Context, chatID, text string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.notices = append(l.notices, chatID+": "+text)

	return nil
}

func (l *lark) React(_ context.Context, messageID, emoji string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.marks[messageID] = append(l.marks[messageID], emoji)

	return emoji, nil
}

func (l *lark) Unreact(_ context.Context, messageID, reactionID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	for i, emoji := range l.marks[messageID] {
		if emoji == reactionID {
			l.marks[messageID] = append(l.marks[messageID][:i:i], l.marks[messageID][i+1:]...)

			return nil
		}
	}

	return errors.New("no such reaction")
}

// marked is the emoji on a message now.
func (l *lark) marked(messageID string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string{}, l.marks[messageID]...)
}

// cardAt is the n-th card sent, as it is now.
func (l *lark) cardAt(n int) card {
	l.mu.Lock()
	defer l.mu.Unlock()

	if n >= len(l.order) {
		return card{}
	}

	return *l.cards[l.order[n]]
}

func (l *lark) OpenCard(_ context.Context, chatID, replyTo, text string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	id := fmt.Sprintf("card-%d", len(l.order)+1)
	l.cards[id] = &card{chat: chatID, text: text, replyTo: replyTo}
	l.order = append(l.order, id)

	return id, nil
}

func (l *lark) UpdateCard(_ context.Context, cardID, text string, _ int) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.cards[cardID].text = text
	l.cards[cardID].updates++

	return nil
}

func (l *lark) CloseCard(_ context.Context, cardID, text string, _ int) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.cards[cardID].text, l.cards[cardID].closed = text, true

	return nil
}

func (l *lark) OpenIDOf(_ context.Context, email string) (string, error) {
	if id, ok := l.emails[email]; ok {
		return id, nil
	}

	return "", errors.New("no member has the email " + email)
}

func (l *lark) NameOf(_ context.Context, openID string) (string, error) { return l.names[openID], nil }

// sent is what Lark was sent, in order: each card as "chat: text" with
// "(writing)" while it is not closed.
func (l *lark) sent() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]string, 0, len(l.order))

	for _, id := range l.order {
		c := l.cards[id]

		state := ""
		if !c.closed {
			state = " (writing)"
		}

		out = append(out, c.chat+": "+c.text+state)
	}

	return out
}

func (l *lark) noticed() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.notices...)
}

// say is a person writing to the app.
func (l *lark) say(m larkbot.Message) {
	l.mu.Lock()
	take := l.on.Message
	l.mu.Unlock()

	take(m)
}

// pi stands in for the coordinator's supervisor: the test writes its frames.
type pi struct {
	mu       sync.Mutex
	off      bool
	watchers map[string][]chan coordinator.Frame
	// working are the chats pi works on; aborted the ones it was told to stop.
	working map[string]bool
	aborted []string
}

func (p *pi) Working(chatID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.working[chatID]
}

func (p *pi) Abort(_ context.Context, chatID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.aborted = append(p.aborted, chatID)
	p.working[chatID] = false

	return nil
}

// stopped are the chats pi was told to stop.
func (p *pi) stopped() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.aborted...)
}

func (p *pi) works(chatID string, working bool) {
	p.mu.Lock()
	p.working[chatID] = working
	p.mu.Unlock()
}

func (p *pi) Enabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return !p.off
}

func (p *pi) Watch(chatID string) (<-chan coordinator.Frame, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ch := make(chan coordinator.Frame, 64)
	p.watchers[chatID] = append(p.watchers[chatID], ch)

	return ch, func() {}
}

func (p *pi) frame(chatID, event string, data map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, ch := range p.watchers[chatID] {
		ch <- coordinator.Frame{Event: event, Data: data}
	}
}

func (p *pi) watched(chatID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.watchers[chatID]) > 0
}

// asking is the store with the owner's questions kept in memory: a question
// needs a worker on a node to ask it, which these tests do not run. It holds
// questions and takes the first answer, as the store does.
type asking struct {
	*orchestration.Service

	mu        sync.Mutex
	questions map[string]orchestration.Question
	// changed wakes the Channels as a commit does.
	changed func()
}

func (a *asking) ask(q orchestration.Question) {
	a.mu.Lock()
	q.AddressedTo, q.Status = orchestration.AddressOwner, orchestration.QuestionOpen
	a.questions[q.ID] = q
	a.mu.Unlock()

	a.changed()
}

func (a *asking) ListQuestions(_ context.Context, f orchestration.QuestionFilter) ([]orchestration.Question, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var out []orchestration.Question

	for _, q := range a.questions {
		if q.Status == f.Status && q.AddressedTo == f.AddressedTo {
			out = append(out, q)
		}
	}

	return out, nil
}

func (a *asking) GetQuestion(_ context.Context, id string) (orchestration.Question, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	q, ok := a.questions[id]
	if !ok {
		return orchestration.Question{}, orchestration.ErrNotFound
	}

	return q, nil
}

func (a *asking) AnswerQuestion(_ context.Context, id string, by orchestration.Addressee, ans orchestration.Answer,
) (orchestration.Question, error) {
	a.mu.Lock()

	q, ok := a.questions[id]

	switch {
	case !ok:
		a.mu.Unlock()

		return orchestration.Question{}, orchestration.ErrNotFound
	case q.Status != orchestration.QuestionOpen:
		a.mu.Unlock()

		return orchestration.Question{}, fmt.Errorf("%w: question %s already has an answer", orchestration.ErrConflict, id)
	case len(q.Options) > 0 && ans.Option == nil:
		a.mu.Unlock()

		return orchestration.Question{}, fmt.Errorf("%w: choose one of the options", orchestration.ErrInvalid)
	}

	q.Status, q.Answer, q.AnsweredBy = orchestration.QuestionAnswered, &ans, by
	a.questions[id] = q
	a.mu.Unlock()

	a.changed()

	return q, nil
}

// later is the wall clock moved on by what a test says has passed.
type later struct {
	mu sync.Mutex
	by time.Duration
}

func (l *later) Now() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()

	return time.Now().Add(l.by)
}

func (l *later) pass(d time.Duration) {
	l.mu.Lock()
	l.by += d
	l.mu.Unlock()
}

type world struct {
	t     *testing.T
	path  string
	clock *later
	// web is the web client's address the Channels are given, when they are
	// given one.
	web   string
	orch  *orchestration.Service
	asks  *asking
	vault *vault.Service
	// lark is the app of the Channel main, larkMaps the app of maps.
	lark     *lark
	larkMaps *lark
	pi       *pi
	channels *larkbot.Channels
	// running says the Channels of this start are being run.
	running bool
	svc     *larkbot.Service
}

func newWorld(t *testing.T) *world {
	t.Helper()

	path := filepath.Join(t.TempDir(), "hypomnema.db")

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	entry, err := vault.GenerateKey("k1")
	require.NoError(t, err)
	keys, err := vault.ParseKeyring(entry, "k1")
	require.NoError(t, err)

	w := &world{
		t: t, path: path, clock: &later{}, lark: newLark(ownerID, linhID), larkMaps: newLark(mapsOwner, mapsLinh),
		pi: &pi{watchers: map[string][]chan coordinator.Frame{}, working: map[string]bool{}},
	}
	w.orch = orchestration.New(db, w.clock, platform.NewID)
	w.asks = &asking{Service: w.orch, questions: map[string]orchestration.Question{}}
	// Any commit wakes the Channels; a chat that is made and never used is one.
	w.asks.changed = func() { _, _ = w.orch.CreateChat(context.Background(), "", "wake") }
	w.vault = vault.New(db, platform.SystemClock{}, platform.NewID, keys,
		func(context.Context, string) ([]string, error) { return nil, nil })
	w.start()

	return w
}

// start is the control plane starting: the Channels and their settings over
// the same store and the same Lark.
func (w *world) start() {
	logger := slog.New(slog.DiscardHandler)

	connect := func(app, appSecret string) larkbot.Lark {
		l := w.lark
		if app == mapsApp {
			l = w.larkMaps
		}

		l.mu.Lock()
		l.usedSecret = appSecret
		l.mu.Unlock()

		return l
	}

	var svc *larkbot.Service

	w.channels = larkbot.New(w.asks, w.pi,
		func(ctx context.Context, c orchestration.LarkChannel) (larkbot.Lark, error) { return svc.Open(ctx, c) },
		logger).WithWebURL(w.web).WithClock(w.clock)
	w.running = false
	svc = larkbot.NewService(w.asks, w.vault, w.channels, connect)
	w.svc = svc
}

// setUp is the owner setting the Channel main up and switching it on, and
// the Channel connecting.
func (w *world) setUp(mode orchestration.LarkChannelMode) {
	w.t.Helper()

	on := true
	_, err := w.svc.Set(w.t.Context(), main, larkbot.Settings{
		AppID: appID, AppSecret: secret, Owner: "owner@example.com", Mode: mode, Enabled: &on,
	})
	require.NoError(w.t, err)
	w.run(main)
}

// setUpMaps is the owner making the Bot vmap and a second Channel, maps,
// that talks to it through another Lark app.
func (w *world) setUpMaps(mode orchestration.LarkChannelMode) {
	w.t.Helper()

	_, err := w.orch.PutBot(w.t.Context(), orchestration.Bot{Name: "vmap", Persona: "You answer about maps."})
	require.NoError(w.t, err)

	on := true
	st, err := w.svc.Set(w.t.Context(), maps, larkbot.Settings{
		AppID: mapsApp, AppSecret: "the-secret-of-maps", Owner: "owner@example.com", Bot: "vmap", Mode: mode,
		Enabled: &on,
	})
	require.NoError(w.t, err)
	require.Equal(w.t, mapsOwner, st.Channel.OwnerOpenID, "the owner has another id in each app")
	w.run(maps)
}

// run is the Channels running, until a Channel is connected.
func (w *world) run(channel string) {
	w.t.Helper()

	if !w.running {
		w.running = true

		go func() { _ = w.channels.Run(w.t.Context()) }()
	}

	require.Eventually(w.t, func() bool { return w.channels.State(channel).Connected }, eventual, tick)
}

// larkChats are the Lark chats every Channel answers in.
func (w *world) larkChats() []orchestration.LarkChannelChat {
	w.t.Helper()

	channels, err := w.orch.LarkChannels(w.t.Context())
	require.NoError(w.t, err)

	var out []orchestration.LarkChannelChat

	for _, c := range channels {
		chats, err := w.orch.LarkChannelChats(w.t.Context(), c.Name)
		require.NoError(w.t, err)

		out = append(out, chats...)
	}

	return out
}

func direct(id, chat, sender, text string) larkbot.Message {
	return larkbot.Message{
		ID: id, ChatID: chat, Direct: true, Text: text, IsText: true, Sender: sender, FromPerson: true,
		SenderOrg: org, Org: org,
	}
}

// chatOf is the chat with a Bot of a Lark chat, once there is one.
func (w *world) chatOf(larkChat string) string {
	w.t.Helper()

	var id string

	require.Eventually(w.t, func() bool {
		for _, c := range w.larkChats() {
			if c.LarkChatID == larkChat {
				id = c.ChatID
			}
		}

		return id != "" && w.pi.watched(id)
	}, eventual, tick)

	return id
}

func (w *world) texts(chatID string) []string {
	w.t.Helper()

	msgs, err := w.orch.ListChatMessages(w.t.Context(), chatID, 0, 0)
	require.NoError(w.t, err)

	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, string(m.Role)+": "+m.Text)
	}

	return out
}

// answer is the coordinator writing a reply in pieces and keeping it.
func (w *world) answer(chatID string, pieces ...string) {
	w.t.Helper()

	w.pi.frame(chatID, coordinator.FrameState, map[string]any{"working": true})
	w.pi.frame(chatID, coordinator.FrameReset, nil)

	for _, piece := range pieces {
		w.pi.frame(chatID, coordinator.FrameDelta, map[string]any{"kind": "text", "index": 0, "delta": piece})
	}

	_, err := w.orch.AddChatMessage(w.t.Context(), chatID, orchestration.ChatCoordinator, strings.Join(pieces, ""))
	require.NoError(w.t, err)
	w.pi.frame(chatID, coordinator.FrameState, map[string]any{"working": false})
}

// Scenario: the owner writes to his Channel at Lark and the coordinator answers.
// His message becomes a message of a chat with the coordinator, as one typed
// on the web is. The answer shows in Lark as one card that is being written
// and ends as the kept reply. Lark delivering the same message again adds
// nothing, and a restart of the control plane sends nothing twice.
func TestTheOwnersMessageReachesTheCoordinatorAndItsAnswerComesBackAsACard(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(orchestration.LarkChannelOwner)

	w.lark.say(direct("om_1", ownerDM, ownerID, "fix the login page"))
	chat := w.chatOf(ownerDM)
	require.Eventually(t, func() bool { return len(w.texts(chat)) == 1 }, eventual, tick)
	assert.Equal(t, []string{"owner: fix the login page"}, w.texts(chat))

	w.lark.say(direct("om_1", ownerDM, ownerID, "fix the login page"))

	w.answer(chat, "I gave it ", "to a worker.")
	require.Eventually(t, func() bool {
		return assert.ObjectsAreEqual([]string{ownerDM + ": I gave it to a worker."}, w.lark.sent())
	}, eventual, tick, "one card, closed with the kept reply")

	// The coordinator speaks again later, with nothing written live: a Run it follows ended.
	_, err := w.orch.AddChatMessage(t.Context(), chat, orchestration.ChatCoordinator, "The pull request is open.")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(w.lark.sent()) == 2 }, eventual, tick)
	assert.Equal(t, ownerDM+": The pull request is open.", w.lark.sent()[1])

	assert.Equal(t, []string{
		"owner: fix the login page", "coordinator: I gave it to a worker.", "coordinator: The pull request is open.",
	}, w.texts(chat), "the message Lark delivered twice was taken once")
	assert.Empty(t, w.lark.noticed())

	w.start()
	w.run(main)
	w.lark.say(direct("om_2", ownerDM, ownerID, "thanks"))
	require.Eventually(t, func() bool { return len(w.texts(chat)) == 4 }, eventual, tick)
	assert.Len(t, w.lark.sent(), 2, "what was sent before the restart is not sent again")
}

// Scenario: the Channel is for its owner only, then he adds Linh, then he opens
// it to his organization. A stranger is told why nothing happens and reaches
// no chat; Linh, once added, gives work in a chat of her own, and the
// coordinator is told who she is and that approvals are the owner's; someone
// of another organization is refused in every mode.
func TestWhoMayTalkThroughAChannelIsTheOwnersChoice(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(orchestration.LarkChannelOwner)

	w.lark.say(direct("om_1", linhDM, linhID, "deploy it"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 1 }, eventual, tick)
	assert.Equal(t, linhDM+": This bot takes messages from its owner only.", w.lark.noticed()[0])

	assert.Empty(t, w.larkChats(), "a refused message starts no chat")

	_, err := w.svc.Set(t.Context(), main, larkbot.Settings{Mode: orchestration.LarkChannelAllowlist})
	require.NoError(t, err)

	w.lark.say(direct("om_2", linhDM, linhID, "deploy it"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 2 }, eventual, tick)
	assert.Contains(t, w.lark.noticed()[1], "the people he added")

	added, err := w.svc.AddPerson(t.Context(), main, "linh@example.com")
	require.NoError(t, err)
	assert.Equal(t, orchestration.LarkChannelPerson{
		OpenID: linhID, Email: "linh@example.com", Name: "Linh", CreatedAt: added.CreatedAt,
	}, added)

	w.lark.say(direct("om_3", linhDM, linhID, "deploy it"))
	hers := w.chatOf(linhDM)
	require.Eventually(t, func() bool { return len(w.texts(hers)) == 1 }, eventual, tick)
	assert.Equal(t, []string{
		"owner: [Lark message from Linh, who is not the owner. Questions for approval go to the owner.]\ndeploy it",
	}, w.texts(hers))

	w.lark.say(direct("om_4", ownerDM, ownerID, "status?"))
	assert.NotEqual(t, hers, w.chatOf(ownerDM), "each person's direct chat is its own chat with the coordinator")

	require.NoError(t, w.svc.RemovePerson(t.Context(), main, "linh@example.com"))
	w.lark.say(direct("om_5", linhDM, linhID, "again"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 3 }, eventual, tick)

	_, err = w.svc.Set(t.Context(), main, larkbot.Settings{Mode: orchestration.LarkChannelEveryone})
	require.NoError(t, err)

	w.lark.say(direct("om_6", linhDM, linhID, "again"))
	require.Eventually(t, func() bool { return len(w.texts(hers)) == 2 }, eventual, tick)

	outsider := direct("om_7", "oc_other", "ou_other", "hello")
	outsider.SenderOrg = "another-tenant"
	w.lark.say(outsider)
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 4 }, eventual, tick)
	assert.Equal(t, "oc_other: This bot serves members of its Lark organization only.", w.lark.noticed()[3])
}

// Scenario: things a Channel does not serve. A picture gets a short notice; a
// message in a group and a message from another app are left alone; with the
// coordinator not running the sender is told nothing will answer; a Channel
// the owner switched off takes nothing at all.
func TestWhatAChannelDoesNotServe(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(orchestration.LarkChannelOwner)

	picture := direct("om_1", ownerDM, ownerID, "")
	picture.IsText = false
	w.lark.say(picture)
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 1 }, eventual, tick)
	assert.Equal(t, ownerDM+": This bot reads text messages only for now.", w.lark.noticed()[0])

	group := direct("om_2", "oc_group", ownerID, "hello all")
	group.Direct = false
	w.lark.say(group)

	app := direct("om_3", ownerDM, "cli_other", "an app's message")
	app.FromPerson = false
	w.lark.say(app)

	w.pi.mu.Lock()
	w.pi.off = true
	w.pi.mu.Unlock()

	w.lark.say(direct("om_4", ownerDM, ownerID, "are you there"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 2 }, eventual, tick)
	assert.Equal(t, ownerDM+": coordinator is not running, so nothing will answer. Your message is kept.",
		w.lark.noticed()[1], "the notice names the Bot of the chat")
	assert.Equal(t, []string{"owner: are you there"}, w.texts(w.chatOf(ownerDM)), "the message is kept")

	off := false
	_, err := w.svc.Set(t.Context(), main, larkbot.Settings{Enabled: &off})
	require.NoError(t, err)

	w.lark.say(direct("om_5", ownerDM, ownerID, "still there"))
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, w.lark.noticed(), 2)
	assert.Len(t, w.texts(w.chatOf(ownerDM)), 1)
}

// Scenario: the owner sets a Channel up. It needs a name, the app, its secret
// and who the owner is; his email is looked up at Lark with the app, which a
// wrong email fails. It talks to the first Bot unless he names another, which
// must exist. The secret is sealed in the vault and used to connect; a
// Channel that cannot connect says why; removing it removes the secret.
func TestTheOwnerSetsAChannelUp(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.svc.Status(ctx, main)
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	_, err = w.svc.Set(ctx, "Not A Name", larkbot.Settings{AppID: appID, AppSecret: secret, Owner: "owner@example.com"})
	require.ErrorIs(t, err, orchestration.ErrInvalid, "a Channel is named as a Bot is")

	_, err = w.svc.Set(ctx, main, larkbot.Settings{AppID: appID, AppSecret: secret})
	require.ErrorIs(t, err, orchestration.ErrInvalid, "a new Channel needs its owner")

	_, err = w.svc.Set(ctx, main, larkbot.Settings{AppID: appID, Owner: "owner@example.com"})
	require.ErrorIs(t, err, orchestration.ErrInvalid, "an app's id comes with its secret")

	_, err = w.svc.Set(ctx, main, larkbot.Settings{AppID: appID, AppSecret: secret, Owner: "nobody@example.com"})
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	require.ErrorContains(t, err, "nobody@example.com")

	status, err := w.svc.Set(ctx, main, larkbot.Settings{AppID: appID, AppSecret: secret, Owner: "owner@example.com"})
	require.NoError(t, err)
	assert.Equal(t, appID, status.Channel.AppID)
	assert.Equal(t, ownerID, status.Channel.OwnerOpenID)
	assert.Equal(t, orchestration.DefaultBot, status.Channel.Bot, "it talks to the first Bot unless told another")
	assert.Equal(t, orchestration.LarkChannelOwner, status.Channel.Mode, "closed unless the owner opens it")
	assert.False(t, status.Channel.Enabled, "a new Channel is off")
	assert.False(t, status.State.Connected)

	_, err = w.svc.Set(ctx, main, larkbot.Settings{Bot: "nobody"})
	require.ErrorIs(t, err, orchestration.ErrInvalid, "the Bot must exist")

	raw, err := os.ReadFile(w.path)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), secret)

	w.lark.refuse = errors.New("Lark refused the app")

	on := true
	_, err = w.svc.Set(ctx, main, larkbot.Settings{Enabled: &on})
	require.NoError(t, err)

	go func() { _ = w.channels.Run(ctx) }()

	require.Eventually(t, func() bool { return w.channels.State(main).LastError != "" }, eventual, tick)
	assert.Contains(t, w.channels.State(main).LastError, "Lark refused the app")
	assert.Equal(t, secret, w.lark.usedSecret)

	_, err = w.vault.Get(ctx, "lark-channel-main")
	require.NoError(t, err, "each Channel's secret has its own name")

	require.NoError(t, w.svc.Remove(ctx, main))

	_, err = w.vault.Get(ctx, "lark-channel-main")
	require.ErrorIs(t, err, vault.ErrNotFound)

	_, err = w.svc.Status(ctx, main)
	require.ErrorIs(t, err, orchestration.ErrNotFound)
}

// Scenario: a worker asks the owner to choose, and later asks an open
// question; a third question he answers on the web. Each reaches him at Lark
// as a card. Someone else pressing a button answers nothing; his own press
// answers, and the card then says what was answered and has no buttons. The
// open question is answered by his reply to its card, which does not go to
// the coordinator as a message. The card of the question answered elsewhere
// closes too.
func TestTheOwnerAnswersAWorkersQuestionOnItsCard(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(orchestration.LarkChannelEveryone)

	w.asks.ask(orchestration.Question{
		ID: "q_1", Text: "Which database?", Options: []orchestration.Option{{Label: "Postgres"}, {Label: "SQLite"}},
	})

	var card larkbot.Card

	require.Eventually(t, func() bool {
		var ok bool

		card, ok = w.lark.askedCard("ask-1")

		return ok
	}, eventual, tick)
	assert.Equal(t, "Which database?", card.Text)
	require.Len(t, card.Buttons, 2)
	assert.Equal(t, "SQLite", card.Buttons[1].Label)

	assert.Equal(t, "Only the owner answers this.", w.lark.pressed(linhID, card.Buttons[0]))

	q, err := w.asks.GetQuestion(t.Context(), "q_1")
	require.NoError(t, err)
	assert.Equal(t, orchestration.QuestionOpen, q.Status, "another person's press answers nothing")

	assert.Equal(t, "Answered.", w.lark.pressed(ownerID, card.Buttons[1]))

	q, err = w.asks.GetQuestion(t.Context(), "q_1")
	require.NoError(t, err)
	require.NotNil(t, q.Answer.Option)
	assert.Equal(t, 1, *q.Answer.Option)
	assert.Equal(t, orchestration.AddressOwner, q.AnsweredBy)

	require.Eventually(t, func() bool {
		c, _ := w.lark.askedCard("ask-1")

		return len(c.Buttons) == 0
	}, eventual, tick)

	closed, _ := w.lark.askedCard("ask-1")
	assert.Equal(t, "Answered by the owner: SQLite", closed.Note)
	assert.Contains(t, w.lark.pressed(ownerID, card.Buttons[0]), "not taken", "a second press changes nothing")

	w.asks.ask(orchestration.Question{ID: "q_2", Text: "What should the page be called?"})
	require.Eventually(t, func() bool {
		_, ok := w.lark.askedCard("ask-2")

		return ok
	}, eventual, tick)

	open, _ := w.lark.askedCard("ask-2")
	assert.Empty(t, open.Buttons)
	assert.Equal(t, "Reply to this card to answer.", open.Note)

	reply := direct("om_r1", ownerDM, ownerID, "Sign in")
	reply.ReplyTo = "om_ask-2"
	w.lark.say(reply)

	require.Eventually(t, func() bool {
		c, _ := w.lark.askedCard("ask-2")

		return c.Note == "Answered by the owner: Sign in"
	}, eventual, tick)
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 1 }, eventual, tick)
	assert.Equal(t, []string{ownerDM + ": Answered."}, w.lark.noticed())

	assert.Empty(t, w.larkChats(), "an answer to a question is not a message for the coordinator")

	w.asks.ask(orchestration.Question{ID: "q_3", Text: "Deploy now?", Options: []orchestration.Option{{Label: "Yes"}}})
	require.Eventually(t, func() bool {
		_, ok := w.lark.askedCard("ask-3")

		return ok
	}, eventual, tick)

	zero := 0
	_, err = w.asks.AnswerQuestion(t.Context(), "q_3", orchestration.AddressCoordinator,
		orchestration.Answer{Option: &zero})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		c, _ := w.lark.askedCard("ask-3")

		return c.Note == "Answered by the coordinator: Yes"
	}, eventual, tick)
}

// Scenario: the owner asks for work and watches Lark while the coordinator
// does it. His message is marked as being worked on; the reply is a card that
// answers that message and first shows what tool is running, then the text;
// it ends with a link to the same chat on the web, and the mark comes off. A
// second message sent meanwhile is told it waits. When the model's call
// fails, what was written stays with a line that says so and the message is
// marked as failed.
func TestTheOwnerSeesTheCoordinatorWork(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.web = "https://hyp.example/"
	w.start()
	w.setUp(orchestration.LarkChannelOwner)

	w.lark.say(direct("om_1", ownerDM, ownerID, "fix the login page"))
	chat := w.chatOf(ownerDM)
	require.Eventually(t, func() bool { return len(w.lark.marked("om_1")) == 1 }, eventual, tick)
	assert.Equal(t, []string{"Typing"}, w.lark.marked("om_1"))

	w.pi.works(chat, true)
	w.pi.frame(chat, coordinator.FrameState, map[string]any{"working": true})
	w.pi.frame(chat, coordinator.FrameTool, map[string]any{
		"phase": "start", "id": "t1", "name": "bash", "args": map[string]any{"command": "hypomnema runs create"},
	})
	require.Eventually(t, func() bool { return len(w.lark.sent()) == 1 }, eventual, tick)
	assert.Equal(t, "om_1", w.lark.cardAt(0).replyTo, "the card answers the message")
	assert.Equal(t, `⚡ bash: {"command":"hypomnema runs create"}`, w.lark.cardAt(0).text)

	w.lark.say(direct("om_2", ownerDM, ownerID, "and the logout page"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 1 }, eventual, tick)
	assert.Equal(t, ownerDM+": ⏳ Queued: coordinator is still working on the message before.", w.lark.noticed()[0])

	w.pi.frame(chat, coordinator.FrameTool, map[string]any{"phase": "end", "id": "t1"})
	w.pi.frame(chat, coordinator.FrameReset, nil)
	w.pi.frame(chat, coordinator.FrameDelta, map[string]any{"kind": "text", "index": 0, "delta": "A worker has it."})

	_, err := w.orch.AddChatMessage(t.Context(), chat, orchestration.ChatCoordinator, "A worker has it.")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return w.lark.cardAt(0).closed }, eventual, tick)
	assert.Equal(t, "A worker has it.\n\n[Open in Hypomnema](https://hyp.example/chats/"+chat+")", w.lark.cardAt(0).text)
	assert.Len(t, w.lark.sent(), 1, "the card that showed the tool is the card of the reply")
	require.Eventually(t, func() bool { return len(w.lark.marked("om_1")) == 0 }, eventual, tick)
	assert.Empty(t, w.lark.marked("om_2"), "nothing waits once the reply is there")

	w.lark.say(direct("om_3", ownerDM, ownerID, "status?"))
	require.Eventually(t, func() bool { return len(w.lark.marked("om_3")) == 1 }, eventual, tick)
	w.pi.frame(chat, coordinator.FrameReset, nil)
	w.pi.frame(chat, coordinator.FrameDelta, map[string]any{"kind": "text", "index": 0, "delta": "Looking"})

	_, err = w.orch.AddChatMessage(t.Context(), chat, orchestration.ChatSystem, "pi's model call failed: overloaded")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return w.lark.cardAt(1).closed }, eventual, tick)
	assert.Contains(t, w.lark.cardAt(1).text, "Looking\n\ncoordinator could not answer this.")
	require.Eventually(t, func() bool {
		return assert.ObjectsAreEqual([]string{"CrossMark"}, w.lark.marked("om_3"))
	}, eventual, tick)
}

// Scenario: the coordinator's reply is longer than a card holds and quotes a
// document that mentions everyone. It goes on in a second card instead of
// being lost, and the mention is written out so that nobody is notified.
func TestALongReplyGoesOnInAnotherCardAndMentionsNobody(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(orchestration.LarkChannelOwner)

	w.lark.say(direct("om_1", ownerDM, ownerID, "summarize the handbook"))
	chat := w.chatOf(ownerDM)

	long := "<at id=all></at> read this.\n" + strings.Repeat("A line of the handbook.\n", 500)
	_, err := w.orch.AddChatMessage(t.Context(), chat, orchestration.ChatCoordinator, long)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(w.lark.sent()) == 2 && w.lark.cardAt(1).closed }, eventual, tick)
	first, second := w.lark.cardAt(0).text, w.lark.cardAt(1).text
	assert.True(t, strings.HasPrefix(first, "&lt;at id=all>&lt;/at> read this."), first[:40])
	assert.LessOrEqual(t, len([]rune(first)), 8000)
	assert.True(t, strings.HasSuffix(strings.TrimSpace(second), "A line of the handbook."))
	whole := strings.ReplaceAll(strings.ReplaceAll(long, "<at", "&lt;at"), "</at", "&lt;/at")
	assert.Equal(t, strings.TrimSpace(whole), strings.TrimSpace(first+"\n"+second),
		"nothing of the reply is lost between the cards")
}

// Scenario: the owner stops the coordinator from Lark and starts over. /stop
// stops the turn that runs in his chat and says so, or says nothing runs.
// The card of the stopped turn closes with what was written and a line that
// says it was stopped, though pi reports the turn as a failed call, and his
// message is not marked as failed. /new gives his next message a new chat
// with the coordinator, and the old one stays.
func TestTheOwnerStopsTheCoordinatorAndStartsOver(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(orchestration.LarkChannelOwner)

	w.lark.say(direct("om_1", ownerDM, ownerID, "refactor everything"))
	first := w.chatOf(ownerDM)

	w.lark.say(direct("om_2", ownerDM, ownerID, "/stop"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 1 }, eventual, tick)
	assert.Equal(t, ownerDM+": Nothing is running.", w.lark.noticed()[0])

	w.pi.works(first, true)
	w.pi.frame(first, coordinator.FrameState, map[string]any{"working": true})
	w.pi.frame(first, coordinator.FrameReset, nil)
	w.pi.frame(first, coordinator.FrameDelta, map[string]any{"kind": "text", "index": 0, "delta": "Renaming the"})
	require.Eventually(t, func() bool { return len(w.lark.sent()) == 1 }, eventual, tick)

	w.lark.say(direct("om_3", ownerDM, ownerID, " /STOP "))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 2 }, eventual, tick)
	assert.Equal(t, ownerDM+": Stopped.", w.lark.noticed()[1])
	assert.Equal(t, []string{first}, w.pi.stopped())

	_, err := w.orch.AddChatMessage(t.Context(), first, orchestration.ChatSystem,
		"pi's model call failed: Request was aborted")
	require.NoError(t, err)
	w.pi.frame(first, coordinator.FrameState, map[string]any{"working": false})
	require.Eventually(t, func() bool { return w.lark.cardAt(0).closed }, eventual, tick)
	assert.Equal(t, "Renaming the\n\nStopped.", w.lark.cardAt(0).text, "stopped, not failed")
	require.Eventually(t, func() bool { return len(w.lark.marked("om_1")) == 0 }, eventual, tick)
	assert.Len(t, w.lark.sent(), 1, "the stopped turn has one card")

	w.lark.say(direct("om_4", ownerDM, ownerID, "/new"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 3 }, eventual, tick)
	assert.Equal(t, ownerDM+": A new conversation has started.", w.lark.noticed()[2])

	w.lark.say(direct("om_5", ownerDM, ownerID, "something else"))

	var second string

	require.Eventually(t, func() bool {
		second = w.chatOf(ownerDM)

		return second != first && len(w.texts(second)) == 1
	}, eventual, tick)
	assert.Equal(t, []string{"owner: something else"}, w.texts(second))
	assert.Equal(t, []string{"owner: refactor everything", "system: pi's model call failed: Request was aborted"},
		w.texts(first), "the commands are not messages")
	assert.Empty(t, w.lark.marked("om_1"), "a stopped message is not marked as failed")
}

// botOf is the Bot a chat talks with.
func (w *world) botOf(chatID string) string {
	w.t.Helper()

	chat, err := w.orch.GetChat(w.t.Context(), chatID)
	require.NoError(w.t, err)

	return chat.Bot
}

// Scenario: the owner has two Bots and a door to each at Lark: the Channel
// main talks to the coordinator, the Channel maps, another Lark app, to the
// Bot vmap. A message through each opens a chat with that Channel's Bot, and
// an answer goes back through the Channel it came in by. Linh is let in on
// maps only: Lark names her otherwise in each app, so each Channel has its
// own people. A notice names the Bot of the chat, and /new on maps opens a
// new chat with vmap. A third Channel on the app of maps is refused and
// keeps no secret, and vmap cannot be removed while a Channel talks to it.
func TestTwoChannelsAreDoorsToTwoBots(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	w.setUp(orchestration.LarkChannelAllowlist)
	w.setUpMaps(orchestration.LarkChannelAllowlist)

	w.lark.say(direct("om_1", ownerDM, ownerID, "fix the login page"))
	w.larkMaps.say(direct("om_2", mapsDM, mapsOwner, "where is the depot"))

	first, second := w.chatOf(ownerDM), w.chatOf(mapsDM)
	assert.Equal(t, orchestration.DefaultBot, w.botOf(first))
	assert.Equal(t, "vmap", w.botOf(second))

	w.answer(second, "At the ", "north gate.")
	require.Eventually(t, func() bool {
		return assert.ObjectsAreEqual([]string{mapsDM + ": At the north gate."}, w.larkMaps.sent())
	}, eventual, tick)
	assert.Empty(t, w.lark.sent(), "an answer goes back through the Channel its chat came in by")

	added, err := w.svc.AddPerson(ctx, maps, "linh@example.com")
	require.NoError(t, err)
	assert.Equal(t, mapsLinh, added.OpenID, "her id in the app of maps")

	w.larkMaps.say(direct("om_3", mapsLinhs, mapsLinh, "and the warehouse"))
	assert.Equal(t, "vmap", w.botOf(w.chatOf(mapsLinhs)))

	w.lark.say(direct("om_4", linhDM, linhID, "deploy it"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 1 }, eventual, tick)
	assert.Contains(t, w.lark.noticed()[0], "the people he added", "she was added to maps, not to main")

	w.pi.works(second, true)
	w.larkMaps.say(direct("om_5", mapsDM, mapsOwner, "and its hours"))
	require.Eventually(t, func() bool { return len(w.larkMaps.noticed()) == 1 }, eventual, tick)
	assert.Equal(t, mapsDM+": ⏳ Queued: vmap is still working on the message before.", w.larkMaps.noticed()[0])
	w.pi.works(second, false)

	w.larkMaps.say(direct("om_6", mapsDM, mapsOwner, "/new"))
	require.Eventually(t, func() bool { return len(w.larkMaps.noticed()) == 2 }, eventual, tick)
	w.larkMaps.say(direct("om_7", mapsDM, mapsOwner, "another thing"))

	var third string

	require.Eventually(t, func() bool {
		third = w.chatOf(mapsDM)

		return third != second
	}, eventual, tick)
	assert.Equal(t, "vmap", w.botOf(third), "a new conversation is with the Channel's Bot")

	_, err = w.svc.Set(ctx, "third", larkbot.Settings{
		AppID: mapsApp, AppSecret: "the-secret-of-maps", Owner: "owner@example.com",
	})
	require.ErrorIs(t, err, orchestration.ErrConflict, "one app is one Channel")
	require.ErrorContains(t, err, "channel maps")

	_, err = w.vault.Get(ctx, "lark-channel-third")
	require.ErrorIs(t, err, vault.ErrNotFound, "a refused Channel keeps no secret")

	all, err := w.svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, []string{"main coordinator", "maps vmap"},
		[]string{all[0].Channel.Name + " " + all[0].Channel.Bot, all[1].Channel.Name + " " + all[1].Channel.Bot})
	assert.Empty(t, all[0].People)
	assert.Len(t, all[1].People, 1)

	err = w.orch.DeleteBot(ctx, "vmap")
	require.ErrorIs(t, err, orchestration.ErrConflict)
	require.ErrorContains(t, err, "Lark channels talk to")
}

// asked counts the question cards an app was sent.
func (l *lark) askedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.asked)
}

// Scenario: workers ask the owner while he has two Channels. The question of
// a Run that his chat through maps started reaches him through maps, where
// he has another id, and he answers it there. The question of a Run started
// on the web reaches him through main, the first Channel. With main switched
// off, the next such question goes through maps, the first that is on.
func TestAQuestionReachesTheOwnerThroughTheChannelItsRunCameFrom(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	w.setUp(orchestration.LarkChannelOwner)
	w.setUpMaps(orchestration.LarkChannelOwner)

	w.larkMaps.say(direct("om_1", mapsDM, mapsOwner, "add the depot to the map"))

	fromMaps, err := w.orch.CreateRun(ctx, orchestration.NewRun{
		Objective: "Add the depot", CreatedBy: "vmap", ChatID: w.chatOf(mapsDM),
	})
	require.NoError(t, err)

	w.asks.ask(orchestration.Question{
		ID: "q_maps", RunID: fromMaps.ID, Text: "Which depot?",
		Options: []orchestration.Option{{Label: "North"}, {Label: "South"}},
	})

	var card larkbot.Card

	require.Eventually(t, func() bool {
		var ok bool

		card, ok = w.larkMaps.askedCard("ask-1")

		return ok
	}, eventual, tick)
	assert.Equal(t, "Which depot?", card.Text)

	fromWeb, err := w.orch.CreateRun(ctx, orchestration.NewRun{Objective: "Tidy the docs", CreatedBy: "owner"})
	require.NoError(t, err)

	w.asks.ask(orchestration.Question{ID: "q_web", RunID: fromWeb.ID, Text: "Which docs?"})
	require.Eventually(t, func() bool {
		c, ok := w.lark.askedCard("ask-1")

		return ok && c.Text == "Which docs?"
	}, eventual, tick)
	assert.Equal(t, 1, w.lark.askedCount(), "the question from maps went through maps only")
	assert.Equal(t, 1, w.larkMaps.askedCount(), "the question from the web went through main only")

	assert.Equal(t, "Only the owner answers this.", w.larkMaps.pressed(ownerID, card.Buttons[1]),
		"his id in the app of main is nobody in the app of maps")
	assert.Equal(t, "Answered.", w.larkMaps.pressed(mapsOwner, card.Buttons[1]))
	require.Eventually(t, func() bool {
		c, _ := w.larkMaps.askedCard("ask-1")

		return c.Note == "Answered by the owner: South"
	}, eventual, tick)

	off := false
	_, err = w.svc.Set(ctx, main, larkbot.Settings{Enabled: &off})
	require.NoError(t, err)

	w.asks.ask(orchestration.Question{ID: "q_web_2", RunID: fromWeb.ID, Text: "Publish them?"})
	require.Eventually(t, func() bool {
		c, ok := w.larkMaps.askedCard("ask-2")

		return ok && c.Text == "Publish them?"
	}, eventual, tick)
	assert.Equal(t, 1, w.lark.askedCount(), "a Channel that is off sends nothing")
}

const team = "oc_maps_team"

// mentionOfBot is how Lark writes a mention of the bot in a message's text.
var mentionOfBot = larkbot.Mention{Key: "@_user_1", OpenID: botID, Name: "Hypomnema"}

// inGroup is a person writing in the group team, from the app's organization.
func inGroup(id, sender, text string, mentions ...larkbot.Mention) larkbot.Message {
	m := direct(id, team, sender, text)
	m.Direct, m.Mentions = false, mentions

	return m
}

// groups are the Channel main's groups as the owner sees them, as "chat
// (name): on|off".
func (w *world) groups() []string {
	w.t.Helper()

	st, err := w.svc.Status(w.t.Context(), main)
	require.NoError(w.t, err)

	out := make([]string, 0, len(st.Groups))

	for _, g := range st.Groups {
		state := "off"
		if g.Enabled {
			state = "on"
		}

		out = append(out, fmt.Sprintf("%s (%s): %s", g.ChatID, g.Name, state))
	}

	return out
}

// quiet waits a little and says nothing new reached Lark or a chat.
func (w *world) quiet(notices, chats int) {
	w.t.Helper()

	time.Sleep(200 * time.Millisecond)
	assert.Len(w.t, w.lark.noticed(), notices, "nothing is said")
	assert.Len(w.t, w.larkChats(), chats, "no chat is started")
}

// Scenario: the bot of the Channel main is in a group (issue #246). The
// group is listed when the Channel connects, off. While it is off, the bot
// says nothing there, even to the owner. Once the owner switches it on, a
// message that mentions the bot is a message of the group's one chat with
// the coordinator, opened with the sender's name; a message that does not
// mention it, or mentions everyone, is left alone and not recorded. The
// answer quotes the question in the group, with no link to the web client.
// The group's chat is a group's, so the Bot's memory stays out of it.
func TestTheBotAnswersInAGroupTheOwnerSwitchedOn(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.web = "https://hyp.example/"
	w.start()
	w.lark.groups = []larkbot.Group{{ChatID: team, Name: "Maps team"}}
	w.lark.names[ownerID] = "Đạt [admin]" + string(rune(7)) + string(rune(10))
	w.setUp(orchestration.LarkChannelOwner)

	require.Eventually(t, func() bool { return len(w.groups()) == 1 }, eventual, tick)
	assert.Equal(t, []string{team + " (Maps team): off"}, w.groups())

	w.lark.say(inGroup("om_1", ownerID, "@_user_1 what is new?", mentionOfBot))
	w.quiet(0, 0)

	g, err := w.svc.SetGroup(t.Context(), main, team, true)
	require.NoError(t, err)
	assert.True(t, g.Enabled)

	w.lark.say(inGroup("om_2", ownerID, "lunch at noon"))
	w.lark.say(inGroup("om_3", ownerID, "@_all hello everyone", larkbot.Mention{Key: "@_all", Name: "all"}))
	w.quiet(0, 0)

	w.lark.say(inGroup("om_4", ownerID, "@_user_1 what did @_user_10 ship?", mentionOfBot,
		larkbot.Mention{Key: "@_user_10", OpenID: linhID, Name: "Linh"}))
	chat := w.chatOf(team)
	require.Eventually(t, func() bool { return len(w.texts(chat)) == 1 }, eventual, tick)
	assert.Equal(t, []string{"owner: [Đạt admin]: what did @Linh ship?"}, w.texts(chat),
		"the bot's mention is taken out, another's is named, and the name is a plain label")

	group, err := w.orch.IsLarkGroupChat(t.Context(), chat)
	require.NoError(t, err)
	assert.True(t, group, "the coordinator starts without its memory in a group's chat")

	w.answer(chat, "The map tiles.")
	require.Eventually(t, func() bool { return w.lark.cardAt(0).closed }, eventual, tick)
	assert.Equal(t, card{chat: team, text: "The map tiles.", replyTo: "om_4", closed: true}, w.lark.cardAt(0),
		"the answer quotes the question and has no link to the web client")

	st, err := w.svc.Status(t.Context(), main)
	require.NoError(t, err)
	require.NotNil(t, st.Groups[0].AskedAt, "the owner sees when the group last asked")
	assert.Empty(t, w.lark.noticed())
}

// Scenario: in a group, the Channel's own choice of who may ask is applied
// to each sender who mentions the bot, and a sender it refuses gets no reply
// and starts nothing. With everyone, every member of the group may ask, one
// of another organization too. /new needs the mention, and starts the
// group's one chat over for everyone; four idle hours do the same.
func TestWhoMayAskInAGroupIsTheChannelsChoice(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.lark.groups = []larkbot.Group{{ChatID: team, Name: "Maps team"}}
	w.setUp(orchestration.LarkChannelOwner)
	require.Eventually(t, func() bool { return len(w.groups()) == 1 }, eventual, tick)

	_, err := w.svc.SetGroup(t.Context(), main, team, true)
	require.NoError(t, err)

	w.lark.say(inGroup("om_1", linhID, "@_user_1 deploy it", mentionOfBot))
	w.quiet(0, 0)

	_, err = w.svc.Set(t.Context(), main, larkbot.Settings{Mode: orchestration.LarkChannelAllowlist})
	require.NoError(t, err)
	w.lark.say(inGroup("om_2", linhID, "@_user_1 deploy it", mentionOfBot))
	w.quiet(0, 0)

	_, err = w.svc.AddPerson(t.Context(), main, "linh@example.com")
	require.NoError(t, err)
	w.lark.say(inGroup("om_3", linhID, "@_user_1 deploy it", mentionOfBot))

	first := w.chatOf(team)
	require.Eventually(t, func() bool { return len(w.texts(first)) == 1 }, eventual, tick)
	assert.Equal(t, []string{"owner: [Linh]: deploy it"}, w.texts(first))

	_, err = w.svc.Set(t.Context(), main, larkbot.Settings{Mode: orchestration.LarkChannelEveryone})
	require.NoError(t, err)

	guest := inGroup("om_4", "ou_guest", "@_user_1 and the docs?", mentionOfBot)
	guest.SenderOrg = "another-tenant"
	w.lark.say(guest)
	require.Eventually(t, func() bool { return len(w.texts(first)) == 2 }, eventual, tick)
	assert.Equal(t, "owner: [ou_guest]: and the docs?", w.texts(first)[1], "a guest of the group may ask too")

	w.lark.say(inGroup("om_5", linhID, "/new"))
	w.quiet(0, 1)

	w.lark.say(inGroup("om_6", linhID, "@_user_1 /new", mentionOfBot))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 1 }, eventual, tick)
	assert.Equal(t, team+": A new conversation has started.", w.lark.noticed()[0])

	w.lark.say(inGroup("om_7", ownerID, "@_user_1 status?", mentionOfBot))

	var second string

	require.Eventually(t, func() bool {
		second = w.chatOf(team)

		return second != first && len(w.texts(second)) == 1
	}, eventual, tick)

	stillGroup, err := w.orch.IsLarkGroupChat(t.Context(), first)
	require.NoError(t, err)
	assert.True(t, stillGroup, "a chat the group left behind still starts without the memory")

	// Two members ask at once after four idle hours: both questions are in the one new chat.
	w.clock.pass(5 * time.Hour)
	w.lark.say(inGroup("om_8", ownerID, "@_user_1 good morning", mentionOfBot))
	w.lark.say(inGroup("om_9", linhID, "@_user_1 morning", mentionOfBot))

	var third string

	require.Eventually(t, func() bool {
		third = w.chatOf(team)

		return third != second && len(w.texts(third)) == 2
	}, eventual, tick, "a group's conversation idle for four hours is over")
	assert.Len(t, w.texts(second), 1)

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, third, w.chatOf(team), "the second question did not start yet another chat")
	assert.Len(t, w.texts(third), 2)
}

// Scenario: the roster follows Lark. A group the bot is added to is listed at
// once, off; a group it leaves is gone; and a group Lark's list no longer
// holds, read when the Channel connects, is removed. A group the bot is
// mentioned in before any listing is put on the list, off.
func TestAChannelsGroupsFollowLark(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.lark.groups = []larkbot.Group{{ChatID: team, Name: "Maps team"}, {ChatID: "oc_ops", Name: "Ops"}}
	w.setUp(orchestration.LarkChannelOwner)
	require.Eventually(t, func() bool { return len(w.groups()) == 2 }, eventual, tick)

	w.lark.mu.Lock()
	on := w.lark.on
	w.lark.mu.Unlock()

	on.Joined(larkbot.Group{ChatID: "oc_new", Name: "New"})
	require.Eventually(t, func() bool { return len(w.groups()) == 3 }, eventual, tick)

	on.Left("oc_ops")
	require.Eventually(t, func() bool { return len(w.groups()) == 2 }, eventual, tick)
	assert.ElementsMatch(t, []string{team + " (Maps team): off", "oc_new (New): off"}, w.groups())

	w.lark.say(larkbot.Message{
		ID: "om_1", ChatID: "oc_unlisted", Text: "@_user_1 hi", IsText: true, Sender: ownerID, FromPerson: true,
		SenderOrg: org, Org: org, Mentions: []larkbot.Mention{mentionOfBot},
	})
	require.Eventually(t, func() bool { return len(w.groups()) == 3 }, eventual, tick)
	assert.Contains(t, w.groups(), "oc_unlisted (): off")
	assert.Empty(t, w.larkChats(), "a group that is off is not answered")

	w.lark.mu.Lock()
	w.lark.groups = []larkbot.Group{{ChatID: team, Name: "Maps team, renamed"}}
	w.lark.mu.Unlock()

	w.clock.pass(time.Minute)
	w.start()
	w.run(main)
	require.Eventually(t, func() bool { return len(w.groups()) == 1 }, eventual, tick)
	assert.Equal(t, []string{team + " (Maps team, renamed): off"}, w.groups())

	// The bot leaves a group it talked in and is added again: the group starts as one never seen, off, and its
	// first question starts a new conversation.
	_, err := w.svc.SetGroup(t.Context(), main, team, true)
	require.NoError(t, err)
	w.lark.say(inGroup("om_2", ownerID, "@_user_1 hello", mentionOfBot))
	before := w.chatOf(team)

	w.lark.mu.Lock()
	on = w.lark.on
	w.lark.mu.Unlock()

	on.Left(team)
	require.Eventually(t, func() bool { return len(w.groups()) == 0 }, eventual, tick)
	on.Joined(larkbot.Group{ChatID: team, Name: "Maps team"})
	require.Eventually(t, func() bool { return len(w.groups()) == 1 }, eventual, tick)
	assert.Equal(t, []string{team + " (Maps team): off"}, w.groups())

	_, err = w.svc.SetGroup(t.Context(), main, team, true)
	require.NoError(t, err)
	w.lark.say(inGroup("om_3", ownerID, "@_user_1 back again", mentionOfBot))
	require.Eventually(t, func() bool {
		after := w.chatOf(team)

		return after != before && len(w.texts(after)) == 1
	}, eventual, tick)
}
