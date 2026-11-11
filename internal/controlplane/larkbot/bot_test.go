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
	org      = "tenant-of-the-app"
	ownerID  = "ou_owner"
	linhID   = "ou_linh"
	appID    = "cli_a1b2c3d4"
	secret   = "app-secret-never-printed"
	ownerDM  = "oc_owner_dm"
	linhDM   = "oc_linh_dm"
	eventual = 5 * time.Second
	tick     = 10 * time.Millisecond
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

// lark stands in for Lark: it keeps what the bot sent and answers the two
// lookups from what it was told about its members.
type lark struct {
	mu sync.Mutex
	// take and press are the bot's handlers, once the bot listens.
	take  func(larkbot.Message)
	press func(larkbot.Press) string
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
	// usedSecret is the secret the bot connected with.
	usedSecret string
}

func newLark() *lark {
	return &lark{
		cards:  map[string]*card{},
		asked:  map[string]larkbot.Card{},
		marks:  map[string][]string{},
		emails: map[string]string{"owner@example.com": ownerID, "linh@example.com": linhID},
		names:  map[string]string{linhID: "Linh"},
	}
}

func (l *lark) Listen(_ context.Context, take func(larkbot.Message), press func(larkbot.Press) string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.refuse != nil {
		return l.refuse
	}

	l.take, l.press = take, press

	return nil
}

func (l *lark) SendCard(_ context.Context, openID string, c larkbot.Card) (cardID, messageID string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if openID != ownerID {
		return "", "", errors.New("a question goes to the owner only")
	}

	id := fmt.Sprintf("ask-%d", len(l.asked)+1)
	l.asked[id] = c

	return id, "om_" + id, nil
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
	press := l.press
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

// say is a person writing to the bot.
func (l *lark) say(m larkbot.Message) {
	l.mu.Lock()
	take := l.take
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
	// changed wakes the bot as a commit does.
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

type world struct {
	t    *testing.T
	path string
	// web is the web client's address the bot is given, when it is given one.
	web   string
	orch  *orchestration.Service
	asks  *asking
	vault *vault.Service
	lark  *lark
	pi    *pi
	bot   *larkbot.Bot
	svc   *larkbot.Service
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
		t: t, path: path, lark: newLark(),
		pi: &pi{watchers: map[string][]chan coordinator.Frame{}, working: map[string]bool{}},
	}
	w.orch = orchestration.New(db, platform.SystemClock{}, platform.NewID)
	w.asks = &asking{Service: w.orch, questions: map[string]orchestration.Question{}}
	// Any commit wakes the bot; a chat that is made and never used is one.
	w.asks.changed = func() { _, _ = w.orch.CreateChat(context.Background(), "wake") }
	w.vault = vault.New(db, platform.SystemClock{}, platform.NewID, keys,
		func(context.Context, string) ([]string, error) { return nil, nil })
	w.start()

	return w
}

// start is the control plane starting: a bot and its settings over the same
// store and the same Lark.
func (w *world) start() {
	logger := slog.New(slog.DiscardHandler)

	connect := func(_, appSecret string) larkbot.Lark {
		w.lark.mu.Lock()
		w.lark.usedSecret = appSecret
		w.lark.mu.Unlock()

		return w.lark
	}

	var svc *larkbot.Service

	w.bot = larkbot.New(w.asks, w.pi, func(ctx context.Context, b orchestration.LarkBot) (larkbot.Lark, error) {
		return svc.Open(ctx, b)
	}, logger).WithWebURL(w.web)
	svc = larkbot.NewService(w.asks, w.vault, w.bot, connect)
	w.svc = svc
}

// setUp is the owner setting the bot up and switching it on, and the bot
// connecting.
func (w *world) setUp(mode orchestration.LarkBotMode) {
	w.t.Helper()

	on := true
	_, err := w.svc.Set(w.t.Context(), larkbot.Settings{
		AppID: appID, AppSecret: secret, Owner: "owner@example.com", Mode: mode, Enabled: &on,
	})
	require.NoError(w.t, err)
	w.run()
}

func (w *world) run() {
	w.t.Helper()

	go func() { _ = w.bot.Run(w.t.Context()) }()

	require.Eventually(w.t, func() bool { return w.bot.State().Connected }, eventual, tick)
}

func direct(id, chat, sender, text string) larkbot.Message {
	return larkbot.Message{
		ID: id, ChatID: chat, Direct: true, Text: text, IsText: true, Sender: sender, FromPerson: true,
		SenderOrg: org, Org: org,
	}
}

// chatOf is the chat with the coordinator of a Lark chat, once there is one.
func (w *world) chatOf(larkChat string) string {
	w.t.Helper()

	var id string

	require.Eventually(w.t, func() bool {
		chats, err := w.orch.LarkBotChats(w.t.Context())
		require.NoError(w.t, err)

		for _, c := range chats {
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

// Scenario: the owner writes to his bot at Lark and the coordinator answers.
// His message becomes a message of a chat with the coordinator, as one typed
// on the web is. The answer shows in Lark as one card that is being written
// and ends as the kept reply. Lark delivering the same message again adds
// nothing, and a restart of the control plane sends nothing twice.
func TestTheOwnersMessageReachesTheCoordinatorAndItsAnswerComesBackAsACard(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(orchestration.LarkBotOwner)

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
	w.run()
	w.lark.say(direct("om_2", ownerDM, ownerID, "thanks"))
	require.Eventually(t, func() bool { return len(w.texts(chat)) == 4 }, eventual, tick)
	assert.Len(t, w.lark.sent(), 2, "what was sent before the restart is not sent again")
}

// Scenario: the bot is for its owner only, then he adds Linh, then he opens
// it to his organization. A stranger is told why nothing happens and reaches
// no chat; Linh, once added, gives work in a chat of her own, and the
// coordinator is told who she is and that approvals are the owner's; someone
// of another organization is refused in every mode.
func TestWhoMayTalkToTheBotIsTheOwnersChoice(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(orchestration.LarkBotOwner)

	w.lark.say(direct("om_1", linhDM, linhID, "deploy it"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 1 }, eventual, tick)
	assert.Equal(t, linhDM+": This bot takes messages from its owner only.", w.lark.noticed()[0])

	chats, err := w.orch.LarkBotChats(t.Context())
	require.NoError(t, err)
	assert.Empty(t, chats, "a refused message starts no chat")

	_, err = w.svc.Set(t.Context(), larkbot.Settings{Mode: orchestration.LarkBotAllowlist})
	require.NoError(t, err)

	w.lark.say(direct("om_2", linhDM, linhID, "deploy it"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 2 }, eventual, tick)
	assert.Contains(t, w.lark.noticed()[1], "the people he added")

	added, err := w.svc.AddPerson(t.Context(), "linh@example.com")
	require.NoError(t, err)
	assert.Equal(t, orchestration.LarkBotPerson{
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

	require.NoError(t, w.svc.RemovePerson(t.Context(), "linh@example.com"))
	w.lark.say(direct("om_5", linhDM, linhID, "again"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 3 }, eventual, tick)

	_, err = w.svc.Set(t.Context(), larkbot.Settings{Mode: orchestration.LarkBotEveryone})
	require.NoError(t, err)

	w.lark.say(direct("om_6", linhDM, linhID, "again"))
	require.Eventually(t, func() bool { return len(w.texts(hers)) == 2 }, eventual, tick)

	outsider := direct("om_7", "oc_other", "ou_other", "hello")
	outsider.SenderOrg = "another-tenant"
	w.lark.say(outsider)
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 4 }, eventual, tick)
	assert.Equal(t, "oc_other: This bot serves members of its Lark organization only.", w.lark.noticed()[3])
}

// Scenario: things the bot does not serve. A picture gets a short notice; a
// message in a group and a message from another app are left alone; with the
// coordinator not running the sender is told nothing will answer; a bot the
// owner switched off takes nothing at all.
func TestWhatTheBotDoesNotServe(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(orchestration.LarkBotOwner)

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
	assert.Contains(t, w.lark.noticed()[1], "The coordinator is not running")
	assert.Equal(t, []string{"owner: are you there"}, w.texts(w.chatOf(ownerDM)), "the message is kept")

	off := false
	_, err := w.svc.Set(t.Context(), larkbot.Settings{Enabled: &off})
	require.NoError(t, err)

	w.lark.say(direct("om_5", ownerDM, ownerID, "still there"))
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, w.lark.noticed(), 2)
	assert.Len(t, w.texts(w.chatOf(ownerDM)), 1)
}

// Scenario: the owner sets the bot up. It needs the app, its secret and who
// the owner is; his email is looked up at Lark with the app, which a wrong
// email fails. The secret is sealed in the vault and used to connect; a bot
// that cannot connect says why; removing the bot removes the secret.
func TestTheOwnerSetsTheBotUp(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, err := w.svc.Status(ctx)
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	_, err = w.svc.Set(ctx, larkbot.Settings{AppID: appID, AppSecret: secret})
	require.ErrorIs(t, err, orchestration.ErrInvalid, "a new bot needs its owner")

	_, err = w.svc.Set(ctx, larkbot.Settings{AppID: appID, Owner: "owner@example.com"})
	require.ErrorIs(t, err, orchestration.ErrInvalid, "an app's id comes with its secret")

	_, err = w.svc.Set(ctx, larkbot.Settings{AppID: appID, AppSecret: secret, Owner: "nobody@example.com"})
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	require.ErrorContains(t, err, "nobody@example.com")

	status, err := w.svc.Set(ctx, larkbot.Settings{AppID: appID, AppSecret: secret, Owner: "owner@example.com"})
	require.NoError(t, err)
	assert.Equal(t, appID, status.Bot.AppID)
	assert.Equal(t, ownerID, status.Bot.OwnerOpenID)
	assert.Equal(t, orchestration.LarkBotOwner, status.Bot.Mode, "closed unless the owner opens it")
	assert.False(t, status.Bot.Enabled, "a new bot is off")
	assert.False(t, status.State.Connected)

	raw, err := os.ReadFile(w.path)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), secret)

	w.lark.refuse = errors.New("Lark refused the app")

	on := true
	_, err = w.svc.Set(ctx, larkbot.Settings{Enabled: &on})
	require.NoError(t, err)

	go func() { _ = w.bot.Run(ctx) }()

	require.Eventually(t, func() bool { return w.bot.State().LastError != "" }, eventual, tick)
	assert.Contains(t, w.bot.State().LastError, "Lark refused the app")
	assert.Equal(t, secret, w.lark.usedSecret)

	require.NoError(t, w.svc.Remove(ctx))

	_, err = w.vault.Get(ctx, "lark-bot-app")
	require.ErrorIs(t, err, vault.ErrNotFound)

	_, err = w.svc.Status(ctx)
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
	w.setUp(orchestration.LarkBotEveryone)

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

	chats, err := w.orch.LarkBotChats(t.Context())
	require.NoError(t, err)
	assert.Empty(t, chats, "an answer to a question is not a message for the coordinator")

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
	w.setUp(orchestration.LarkBotOwner)

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
	assert.Contains(t, w.lark.noticed()[0], "Queued")

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
	assert.Contains(t, w.lark.cardAt(1).text, "Looking\n\nThe coordinator could not answer this.")
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
	w.setUp(orchestration.LarkBotOwner)

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
// stops the turn that runs in his chat and says so, or says nothing runs;
// /new gives his next message a new chat with the coordinator, and the old
// one stays.
func TestTheOwnerStopsTheCoordinatorAndStartsOver(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.setUp(orchestration.LarkBotOwner)

	w.lark.say(direct("om_1", ownerDM, ownerID, "refactor everything"))
	first := w.chatOf(ownerDM)

	w.lark.say(direct("om_2", ownerDM, ownerID, "/stop"))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 1 }, eventual, tick)
	assert.Equal(t, ownerDM+": Nothing is running.", w.lark.noticed()[0])

	w.pi.works(first, true)
	w.lark.say(direct("om_3", ownerDM, ownerID, " /STOP "))
	require.Eventually(t, func() bool { return len(w.lark.noticed()) == 2 }, eventual, tick)
	assert.Equal(t, ownerDM+": Stopped.", w.lark.noticed()[1])
	assert.Equal(t, []string{first}, w.pi.aborted)

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
	assert.Equal(t, []string{"owner: refactor everything"}, w.texts(first), "the commands are not messages")
}
