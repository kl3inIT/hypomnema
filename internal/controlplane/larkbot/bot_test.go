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
	chat   string
	text   string
	closed bool
	// updates counts the times its text was replaced before it was closed.
	updates int
}

// lark stands in for Lark: it keeps what the bot sent and answers the two
// lookups from what it was told about its members.
type lark struct {
	mu sync.Mutex
	// take is the bot's handler, once the bot listens.
	take    func(larkbot.Message)
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
		emails: map[string]string{"owner@example.com": ownerID, "linh@example.com": linhID},
		names:  map[string]string{linhID: "Linh"},
	}
}

func (l *lark) Listen(_ context.Context, take func(larkbot.Message)) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.refuse != nil {
		return l.refuse
	}

	l.take = take

	return nil
}

func (l *lark) SendText(_ context.Context, chatID, text string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.notices = append(l.notices, chatID+": "+text)

	return nil
}

func (l *lark) OpenCard(_ context.Context, chatID, text string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	id := fmt.Sprintf("card-%d", len(l.order)+1)
	l.cards[id] = &card{chat: chatID, text: text}
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

type world struct {
	t     *testing.T
	path  string
	orch  *orchestration.Service
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

	w := &world{t: t, path: path, lark: newLark(), pi: &pi{watchers: map[string][]chan coordinator.Frame{}}}
	w.orch = orchestration.New(db, platform.SystemClock{}, platform.NewID)
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

	w.bot = larkbot.New(w.orch, w.pi, func(ctx context.Context, b orchestration.LarkBot) (larkbot.Lark, error) {
		return svc.Open(ctx, b)
	}, logger)
	svc = larkbot.NewService(w.orch, w.vault, w.bot, connect)
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
