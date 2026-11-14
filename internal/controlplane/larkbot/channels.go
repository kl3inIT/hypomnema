// Package larkbot runs the Lark Channels (issues #136 and #169). A Channel is
// a door to one Bot: a Lark app and who may talk through it. A person's
// direct message to the app at Lark becomes a message of a chat with the
// Channel's Bot, and what the Bot says in that chat is sent back as a card
// that fills as the text is written. In a Lark group the owner switched on,
// a message that mentions the bot is a message of the group's one chat with
// the Bot (issue #246). Each Channel has its own connection to Lark; several
// Channels may talk to one Bot (OpenClaw's bindings).
//
// It follows the Feishu channels of OpenClaw (extensions/feishu) and Hermes
// (plugins/platforms/feishu) and the owner's MemoryOS (mem-246-lark-bot):
// events come over a persistent connection, so no public address is opened;
// a delivery repeated by Lark is dropped by the message's id; who may talk to
// a Channel is closed by default.
package larkbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Message is a message a person sent, as a Channel needs it.
type Message struct {
	// ID is the message's id at Lark; ChatID the chat it is in.
	ID     string
	ChatID string
	// Direct says the chat is the person's direct chat with the bot.
	Direct bool
	// Text is the text of a text message; IsText says it is one.
	Text   string
	IsText bool
	// Sender is the sender's open_id; FromPerson says a person sent it, not
	// an app.
	Sender     string
	FromPerson bool
	// SenderOrg and Org are the organization of the sender and of the bot's
	// app (Lark's tenant_key).
	SenderOrg string
	Org       string
	// ReplyTo is the message this one replies to, when it is a reply.
	ReplyTo string
	// Mentions are the people and bots the message mentions; Text holds each
	// as its Key.
	Mentions []Mention
	// Menu says the message is a press of the bot's menu, for which Lark
	// holds no message to mark or to reply to.
	Menu bool
}

// Mention is one mention in a message: the placeholder Lark writes for it in
// the text, and who it names. A mention of everyone (@all) names nobody.
type Mention struct {
	Key    string
	OpenID string
	Name   string
}

// Group is a Lark group the Channel's bot is in.
type Group struct {
	ChatID string
	Name   string
}

// Events are what a Channel is given from its connection: each message, each
// press of a card's button, whose answer is shown to who pressed, each press
// of the bot's menu, and the bot being added to a group or leaving one.
type Events struct {
	Message func(Message)
	Press   func(Press) string
	Menu    func(MenuPress)
	Joined  func(Group)
	Left    func(chatID string)
}

// MenuPress is a person pressing an item of the bot's menu, in their direct
// chat with the bot.
type MenuPress struct {
	// Operator is the open_id of who pressed; Key the item's event key, as
	// the owner set it in Lark's developer console.
	Operator string
	Key      string
	// At is when it was pressed, which tells a press from Lark's repeated
	// delivery of it; Org is the app's organization.
	At  string
	Org string
}

// Press is a person pressing a button of a card.
type Press struct {
	// Operator is the open_id of who pressed.
	Operator string
	// Value is what the button carries.
	Value map[string]string
}

// Card is a card that is not being written: a question with its choices, or
// how it ended.
type Card struct {
	Title string
	// Color is the header's: orange for a question that waits, green for one
	// that was answered, red for one that was canceled.
	Color string
	// Text is Markdown.
	Text string
	// Buttons are the choices; none on a card that can no longer be answered.
	Buttons []Button
	// Note is small print under the buttons.
	Note string
}

// Button is one choice of a card.
type Button struct {
	Label string
	Value map[string]string
}

// Lark is what a Channel does at Lark as its app. The SDK is behind it
// (lark.go).
type Lark interface {
	// Listen opens the persistent connection and gives what comes over it to
	// on. It returns when the connection cannot be opened; an open
	// connection stays for the life of the process.
	Listen(ctx context.Context, on Events) error
	// BotOpenID reads the bot's own open_id, which a mention of it carries.
	BotOpenID(ctx context.Context) (string, error)
	// Groups lists the groups the bot is in, the whole list or an error.
	Groups(ctx context.Context) ([]Group, error)
	// SendCard sends a card to a person and returns its id and its message's.
	SendCard(ctx context.Context, openID string, card Card) (cardID, messageID string, err error)
	// ReplaceCard replaces a card sent with SendCard; seq rises with each call.
	ReplaceCard(ctx context.Context, cardID string, card Card, seq int) error
	// SendText sends a short notice to a chat. Here and in SendFile and
	// OpenCard, a chat may be named by a person's open_id (ou_...), which is
	// their direct chat with the bot.
	SendText(ctx context.Context, chatID, text string) error
	// SendFile uploads a file and sends it to a chat.
	SendFile(ctx context.Context, chatID string, a Attachment) error
	// OpenCard sends a card that is still being written and returns its id
	// and the id of the chat Lark put it in. With replyTo it is a reply to
	// that message, which Lark shows quoted.
	OpenCard(ctx context.Context, chatID, replyTo, text string) (cardID, larkChatID string, err error)
	// React puts an emoji on a message and returns the reaction's id;
	// Unreact takes it off.
	React(ctx context.Context, messageID, emoji string) (reactionID string, err error)
	Unreact(ctx context.Context, messageID, reactionID string) error
	// UpdateCard replaces the card's text; seq rises with each call.
	UpdateCard(ctx context.Context, cardID, text string, seq int) error
	// CloseCard sets the card's final text and ends its writing.
	CloseCard(ctx context.Context, cardID, text string, seq int) error
	// OpenIDOf finds a member of the app's organization by email.
	OpenIDOf(ctx context.Context, email string) (string, error)
	// NameOf reads a member's name.
	NameOf(ctx context.Context, openID string) (string, error)
}

// Store is what the Channels keep (orchestration.Service).
type Store interface {
	LarkChannel(ctx context.Context, name string) (orchestration.LarkChannel, error)
	LarkChannels(ctx context.Context) ([]orchestration.LarkChannel, error)
	LarkChannelPerson(ctx context.Context, channel, openID string) (orchestration.LarkChannelPerson, error)
	TakeLarkMessage(ctx context.Context, channel, messageID string) (bool, error)
	ForgetLarkMessagesBefore(ctx context.Context, before time.Time) error
	LarkChannelChatFor(ctx context.Context, channel, larkChatID, openID, title string,
	) (orchestration.LarkChannelChat, error)
	LarkGroupChatFor(ctx context.Context, channel, larkChatID, title string) (orchestration.LarkChannelChat, error)
	LarkChannelChats(ctx context.Context, channel string) ([]orchestration.LarkChannelChat, error)
	LarkChannelGroup(ctx context.Context, channel, chatID string) (orchestration.LarkChannelGroup, error)
	JoinLarkChannelGroup(ctx context.Context, channel, chatID, name string) error
	LeaveLarkChannelGroup(ctx context.Context, channel, chatID string) error
	ListedLarkChannelGroups(ctx context.Context, channel string, listed []orchestration.LarkChannelGroup,
		readAt time.Time) ([]string, error)
	AskedInLarkChannelGroup(ctx context.Context, channel, chatID string) error
	ResetLarkChannelChat(ctx context.Context, channel, larkChatID, title string) (orchestration.LarkChannelChat, error)
	MarkLarkChannelSent(ctx context.Context, larkChatID string, seq int64) error
	RenameLarkChannelChat(ctx context.Context, channel, oldID, newID string) error
	ListQuestions(ctx context.Context, f orchestration.QuestionFilter) ([]orchestration.Question, error)
	GetQuestion(ctx context.Context, id string) (orchestration.Question, error)
	AnswerQuestion(ctx context.Context, id string, by orchestration.Addressee, a orchestration.Answer,
	) (orchestration.Question, error)
	LarkChannelForRun(ctx context.Context, runID string) (string, error)
	RecordLarkQuestionCard(ctx context.Context, c orchestration.LarkQuestionCard) error
	LarkQuestionCardOf(ctx context.Context, questionID string) (orchestration.LarkQuestionCard, error)
	LarkQuestionCardByMessage(ctx context.Context, channel, messageID string) (orchestration.LarkQuestionCard, error)
	OpenLarkQuestionCards(ctx context.Context, channel string) ([]orchestration.LarkQuestionCard, error)
	CloseLarkQuestionCard(ctx context.Context, questionID string, seq int) error
	AddChatMessage(ctx context.Context, chatID string, role orchestration.ChatRole, text string,
	) (orchestration.ChatMessage, error)
	ListChatMessages(ctx context.Context, chatID string, after int64, limit int) ([]orchestration.ChatMessage, error)
	Subscribe(ctx context.Context) <-chan struct{}
}

// Coordinator is the part of the supervisor of the Bots' pi the Channels use.
type Coordinator interface {
	Enabled() bool
	Working(chatID string) bool
	Abort(ctx context.Context, chatID string) error
	Watch(chatID string) (frames <-chan coordinator.Frame, stop func())
}

// What a Channel says in its own voice. English, as the web client is. To a
// person at Lark the Channel's app is "this bot", which is Lark's word for it.
// The notices with %s name the Bot of the chat.
const (
	noticeOwnerOnly = "This bot takes messages from its owner only."
	noticeAllowlist = "This bot takes messages from its owner and the people he added."
	noticeOutsider  = "This bot serves members of its Lark organization only."
	noticeTextOnly  = "This bot reads text messages only for now."
	noticeNoAnswer  = "%s is not running, so nothing will answer. Your message is kept."
	noticeFailed    = "This message could not be taken. Try again."
	noticeQueued    = "⏳ Queued: %s is still working on the message before."
	noticeNew       = "A new conversation has started."
	noticeStopped   = "Stopped."
	noticeIdle      = "Nothing is running."
	noticeNoReply   = "%s could not answer this. Try again."
	writing         = "…"
)

// The commands a person can type, as OpenClaw, Hermes and MemoryOS have /new
// and Hermes /stop.
// openIDPrefix starts a person's open_id, which names their direct chat
// with the bot until Lark gives the chat's own id.
const openIDPrefix = "ou_"

const (
	commandNew  = "/new"
	commandStop = "/stop"
	// commandMenu opens what a press of the bot's menu says to the Bot: /menu
	// and the item's key (OpenClaw, monitor.account.ts handleBotMenu).
	commandMenu = "/menu"
)

// The emoji a Channel puts on a person's message, as OpenClaw and Hermes do:
// Typing while the Bot works on it, taken off when the reply is
// there, and a cross when it failed. Nothing marks success: the reply does.
const (
	emojiWorking = "Typing"
	emojiFailed  = "CrossMark"
)

const (
	// cardEvery is the least time between two updates of a card: Lark allows
	// five a second on one card (MemoryOS, from Lark's limits).
	cardEvery = 500 * time.Millisecond
	// closeAfter is how long a card waits for its kept message once the
	// Bot stopped working, before it is closed with what was shown.
	closeAfter = 5 * time.Second
	// cardLimit is the most characters one card shows: a card holds 30 KB and
	// a character may take three bytes. A longer reply goes on in further
	// cards, as OpenClaw and Hermes split one (MemoryOS cuts it).
	cardLimit = 8000
	// statusPreview is how much of a tool's arguments its status line shows.
	statusPreview = 60
	// forgetAfter is how long a taken message is remembered: Lark's last
	// delivery of an event comes about seven hours after the first.
	forgetAfter = 24 * time.Hour
	// retryEvery is how often the Channels are looked at again, for one that
	// could not connect.
	retryEvery  = 30 * time.Second
	sendTimeout = 30 * time.Second
	// listEvery is how often a Channel reads Lark's whole list of its bot's
	// groups, besides when it connects (MemoryOS, ChannelSupervisor.LISTING).
	listEvery = 10 * time.Minute
	// groupIdle is how long a group's conversation waits for a question
	// before the next one starts a new one (MemoryOS,
	// ChannelProperties.groupConversationIdle).
	groupIdle = 4 * time.Hour
	// speakerLength is the most characters of the name that labels a
	// sender's message (MemoryOS, ChannelInboundService.ASKER_LENGTH), and of
	// a menu item's key.
	speakerLength = 64
)

// Channels runs the Lark Channels: one connection to Lark for each.
type Channels struct {
	store  Store
	coord  Coordinator
	open   func(ctx context.Context, channel orchestration.LarkChannel) (Lark, error)
	logger *slog.Logger
	now    func() time.Time
	// webURL is the web client's address, for the link under a reply.
	webURL string
	// files reads the Bots' folders, for the files a Bot sends; nil when
	// they cannot be read.
	files Files
	// look asks Run to look at the Channels now: one was set up or changed.
	look chan struct{}

	mu sync.Mutex
	// conns are the connections that were opened, by their Channel's name;
	// starting the Channels whose connection is being opened; failures why a
	// Channel's last attempt to connect failed.
	conns    map[string]*conn
	starting map[string]bool
	failures map[string]string
}

// conn is one Channel's connection to Lark and the Lark chats it answers in.
type conn struct {
	all *Channels
	// name is the Channel's; appID the app the connection was opened as.
	name  string
	appID string
	lark  Lark

	mu     sync.Mutex
	relays map[string]*relay
	// botID is the bot's own open_id, read once it is first needed.
	botID string
	// turns let one message of each Lark group at a time choose its chat.
	turns map[string]*sync.Mutex
}

// WithWebURL sets the web client's address; a reply then ends with a link to
// its chat there (MemoryOS's "Continue in MemoryOS", after Dust).
func (c *Channels) WithWebURL(url string) *Channels {
	c.webURL = strings.TrimSuffix(url, "/")

	return c
}

// WithFiles sets how the Bots' folders are read, for the files a Bot sends
// into its Lark chat (issue #312). Without it a file a Bot asks to send is
// refused.
func (c *Channels) WithFiles(files Files) *Channels {
	c.files = files

	return c
}

// WithClock sets the clock that decides when a group's conversation is over;
// the wall clock by default.
func (c *Channels) WithClock(clock platform.Clock) *Channels {
	c.now = clock.Now

	return c
}

// New returns the Channels' runner. open makes a Channel's connection to
// Lark from what the owner set up; it is where the app's secret is read.
func New(store Store, coord Coordinator, open func(context.Context, orchestration.LarkChannel) (Lark, error),
	logger *slog.Logger,
) *Channels {
	return &Channels{
		store: store, coord: coord, open: open, logger: logger, now: time.Now, look: make(chan struct{}, 1),
		conns: map[string]*conn{}, starting: map[string]bool{}, failures: map[string]string{},
	}
}

// State is whether a Channel is connected to Lark.
type State struct {
	// Connected says the persistent connection was opened; AppID is the app
	// it was opened as, which a later change of the Channel's app does not
	// replace until the control plane starts again.
	Connected bool
	AppID     string
	// LastError is why the last attempt to connect failed.
	LastError string
}

// State returns a Channel's state.
func (c *Channels) State(channel string) State {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cn := c.conns[channel]; cn != nil {
		return State{Connected: true, AppID: cn.appID}
	}

	return State{LastError: c.failures[channel]}
}

// Look has Run look at the Channels now, so that one the owner just set up
// or switched on connects at once.
func (c *Channels) Look() {
	select {
	case c.look <- struct{}{}:
	default:
	}
}

// Run connects each Channel that is set up and switched on, and keeps them.
// The Channels are looked at again when one is set (Look) and every half
// minute, so a new Channel needs no restart and one that could not connect
// is tried again. Lark's SDK cannot close a persistent connection, so a
// connection once opened stays until the process ends: a Channel that is
// switched off or removed takes no message, and another app is used from the
// next start.
func (c *Channels) Run(ctx context.Context) error {
	retry := time.NewTicker(retryEvery)
	defer retry.Stop()

	forget := time.NewTicker(time.Hour)
	defer forget.Stop()

	for {
		c.connect(ctx)

		select {
		case <-ctx.Done():
			return nil
		case <-c.look:
		case <-retry.C:
		case <-forget.C:
			if err := c.store.ForgetLarkMessagesBefore(ctx, c.now().Add(-forgetAfter)); err != nil {
				c.logger.WarnContext(ctx, "old Lark messages were not forgotten",
					slog.String("event", "larkbot.forget_failed"), slog.Any("error", err))
			}
		}
	}
}

// connect opens the connection of every Channel that is on and has none.
// Each is opened on its own goroutine: one that Lark keeps waiting does not
// hold the others back.
func (c *Channels) connect(ctx context.Context) {
	channels, err := c.store.LarkChannels(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.logger.WarnContext(ctx, "the Lark channels were not read", slog.String("event", "larkbot.channels_failed"),
				slog.Any("error", err))
		}

		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, ch := range channels {
		if !ch.Enabled || c.conns[ch.Name] != nil || c.starting[ch.Name] {
			continue
		}

		c.starting[ch.Name] = true

		go c.start(ctx, ch)
	}
}

// start opens one Channel's connection.
func (c *Channels) start(ctx context.Context, ch orchestration.LarkChannel) {
	cn := &conn{all: c, name: ch.Name, appID: ch.AppID, relays: map[string]*relay{}, turns: map[string]*sync.Mutex{}}

	lark, err := c.open(ctx, ch)
	if err == nil {
		cn.lark = lark
		err = lark.Listen(ctx, Events{
			Message: func(m Message) { cn.take(ctx, m) },
			Press:   func(p Press) string { return cn.press(ctx, p) },
			Menu:    func(p MenuPress) { cn.menu(ctx, p) },
			Joined:  func(g Group) { go cn.joined(ctx, g) },
			Left:    func(chatID string) { go cn.left(ctx, chatID) },
		})
	}

	c.mu.Lock()
	delete(c.starting, ch.Name)

	if err != nil {
		c.failures[ch.Name] = err.Error()
		c.mu.Unlock()
		c.logger.WarnContext(ctx, "a Lark channel did not connect", slog.String("event", "larkbot.connect_failed"),
			slog.String("channel", ch.Name), slog.Any("error", err))

		return
	}

	c.conns[ch.Name] = cn
	delete(c.failures, ch.Name)
	c.mu.Unlock()

	c.logger.InfoContext(ctx, "a Lark channel is connected", slog.String("event", "larkbot.connected"),
		slog.String("channel", ch.Name), slog.String("app_id", ch.AppID))

	chats, err := c.store.LarkChannelChats(ctx, ch.Name)
	if err != nil {
		c.logger.WarnContext(ctx, "a Lark channel's chats were not read", slog.String("event", "larkbot.chats_failed"),
			slog.String("channel", ch.Name), slog.Any("error", err))
	}

	cn.mu.Lock()
	for _, chat := range chats {
		cn.relayLocked(ctx, chat)
	}
	cn.mu.Unlock()

	go cn.questions(ctx)
	go cn.roster(ctx)
}

// roster keeps the Channel's groups: Lark's whole list of the bot's groups
// is read when the Channel connects and every ten minutes after, which adds
// the groups the bot was added to unseen and removes those it left
// (MemoryOS, ChannelGroupRoster.listed). It is the only way to know the
// groups for an app that does not subscribe the bot's added and deleted
// events.
func (cn *conn) roster(ctx context.Context) {
	every := time.NewTicker(listEvery)
	defer every.Stop()

	for {
		cn.list(ctx)

		select {
		case <-ctx.Done():
			return
		case <-every.C:
		}
	}
}

func (cn *conn) list(ctx context.Context) {
	readAt := cn.all.now()

	listCtx, cancel := context.WithTimeout(ctx, 5*sendTimeout)
	groups, err := cn.lark.Groups(listCtx)

	cancel()

	if err != nil {
		if ctx.Err() == nil {
			cn.all.logger.WarnContext(ctx, "a Lark channel's groups were not listed",
				slog.String("event", "larkbot.groups_failed"), slog.String("channel", cn.name), slog.Any("error", err))
		}

		return
	}

	listed := make([]orchestration.LarkChannelGroup, 0, len(groups))
	for _, g := range groups {
		listed = append(listed, orchestration.LarkChannelGroup{ChatID: g.ChatID, Name: g.Name})
	}

	left, err := cn.all.store.ListedLarkChannelGroups(ctx, cn.name, listed, readAt)
	if err != nil {
		cn.all.logger.WarnContext(ctx, "a Lark channel's groups were not recorded",
			slog.String("event", "larkbot.groups_failed"), slog.String("channel", cn.name), slog.Any("error", err))

		return
	}

	if len(left) > 0 {
		cn.all.logger.InfoContext(ctx, "a Lark channel's bot is no longer in groups",
			slog.String("event", "larkbot.groups_left"), slog.String("channel", cn.name), slog.Int("count", len(left)))
	}
}

// joined records a group the bot was added to; it is off until the owner
// switches it on, and the bot says nothing there (no greeting on join).
func (cn *conn) joined(ctx context.Context, g Group) {
	if err := cn.all.store.JoinLarkChannelGroup(ctx, cn.name, g.ChatID, g.Name); err != nil {
		cn.all.logger.WarnContext(ctx, "a Lark group was not recorded", slog.String("event", "larkbot.group_failed"),
			slog.String("channel", cn.name), slog.Any("error", err))
	}
}

// left forgets a group the bot left.
func (cn *conn) left(ctx context.Context, chatID string) {
	if err := cn.all.store.LeaveLarkChannelGroup(ctx, cn.name, chatID); err != nil {
		cn.all.logger.WarnContext(ctx, "a Lark group was not forgotten", slog.String("event", "larkbot.group_failed"),
			slog.String("channel", cn.name), slog.Any("error", err))
	}
}

// take handles one message from Lark. It is called from the connection, which
// must acknowledge the event within three seconds, so it does its work on its
// own goroutine. A message that arrives before the connection is known to be
// open is taken all the same.
func (cn *conn) take(ctx context.Context, m Message) {
	go func() {
		if err := cn.taken(ctx, m); err != nil {
			cn.all.logger.WarnContext(ctx, "a Lark message was not taken", slog.String("event", "larkbot.take_failed"),
				slog.String("channel", cn.name), slog.String("message_id", m.ID), slog.Any("error", err))
			cn.notice(ctx, m.ChatID, noticeFailed)
		}
	}()
}

func (cn *conn) taken(ctx context.Context, m Message) error {
	store := cn.all.store

	ch, err := store.LarkChannel(ctx, cn.name)
	if err != nil || !ch.Enabled {
		return nil //nolint:nilerr // A Channel that was switched off or removed takes nothing and says nothing.
	}

	// Only what a person wrote: another app's message is never an instruction.
	if !m.FromPerson || m.ID == "" || m.ChatID == "" {
		return nil
	}

	if !m.Direct {
		return cn.inGroup(ctx, ch, m)
	}

	first, err := store.TakeLarkMessage(ctx, cn.name, m.ID)
	if err != nil || !first {
		return err
	}

	return cn.direct(ctx, ch, m)
}

// menu handles a press of the bot's menu (issue #312): it is a message
// "/menu <key>" from who pressed, in their direct chat with the bot, through
// the same rules as one they typed (OpenClaw, monitor.account.ts
// handleBotMenu). Like a message, it is done on its own goroutine.
func (cn *conn) menu(ctx context.Context, p MenuPress) {
	go func() {
		if err := cn.menued(ctx, p); err != nil {
			cn.all.logger.WarnContext(ctx, "a press of the Lark menu was not taken",
				slog.String("event", "larkbot.take_failed"), slog.String("channel", cn.name), slog.Any("error", err))
		}
	}()
}

func (cn *conn) menued(ctx context.Context, p MenuPress) error {
	store := cn.all.store

	ch, err := store.LarkChannel(ctx, cn.name)
	if err != nil || !ch.Enabled {
		return nil //nolint:nilerr // A Channel that was switched off or removed takes nothing and says nothing.
	}

	key := speaker(p.Key)
	if p.Operator == "" || key == "" {
		return nil
	}

	// A press has no message id, so one is made of its key and when it was pressed, as OpenClaw makes it, which
	// Lark's repeated delivery of the press repeats.
	id := "bot-menu:" + key + ":" + p.At

	first, err := store.TakeLarkMessage(ctx, cn.name, id)
	if err != nil || !first {
		return err
	}

	known, err := cn.directChat(ctx, p.Operator)
	if err != nil {
		return err
	}

	// The presser is taken as a member of the app's organization, which the event names: the app is a custom
	// app of that organization, which nobody outside it can open, and OpenClaw makes no check either (owner,
	// 2026-11-13). The Channel's mode still decides, as for a typed message. A person whose direct chat is not
	// known yet is answered by their open_id, and Lark's answer names the chat (OpenClaw answers a press to
	// user:<open_id>).
	chatID := known
	if chatID == "" {
		chatID = p.Operator
	}

	return cn.direct(ctx, ch, Message{
		ID: id, ChatID: chatID, Direct: true, Text: commandMenu + " " + key, IsText: true, Sender: p.Operator,
		FromPerson: true, SenderOrg: p.Org, Org: p.Org, Menu: true,
	})
}

// directChat is the id of the direct chat a person has with the bot, which a
// press of the menu does not name: the one they wrote in before, the
// person's open_id while Lark has not named it, or none.
func (cn *conn) directChat(ctx context.Context, openID string) (string, error) {
	chats, err := cn.all.store.LarkChannelChats(ctx, cn.name)
	if err != nil {
		return "", err
	}

	for _, c := range chats {
		if !c.Group && c.OpenID == openID {
			return c.LarkChatID, nil
		}
	}

	return "", nil
}

// direct handles a direct message that was taken: who may send it, then what
// it is.
func (cn *conn) direct(ctx context.Context, ch orchestration.LarkChannel, m Message) error {
	store := cn.all.store

	if refusal := cn.refusal(ctx, ch, m); refusal != "" {
		cn.all.logger.InfoContext(ctx, "a Lark message was refused", slog.String("event", "larkbot.refused"),
			slog.String("channel", cn.name), slog.String("sender", m.Sender), slog.String("reason", refusal))
		cn.notice(ctx, m.ChatID, refusal)

		return nil
	}

	if !m.IsText || strings.TrimSpace(m.Text) == "" {
		cn.notice(ctx, m.ChatID, noticeTextOnly)

		return nil
	}

	// The owner's reply to a question's card is his answer in his own words, not a message for the Bot.
	if m.Sender == ch.OwnerOpenID && m.ReplyTo != "" {
		if card, err := store.LarkQuestionCardByMessage(ctx, cn.name, m.ReplyTo); err == nil {
			cn.notice(ctx, m.ChatID, cn.answer(ctx, card.QuestionID, orchestration.Answer{Text: m.Text}))

			return nil
		}
	}

	switch strings.ToLower(strings.TrimSpace(m.Text)) {
	case commandNew:
		return cn.startOver(ctx, m, "")
	case commandStop:
		return cn.stop(ctx, m)
	}

	text, title := m.Text, ""
	if m.Sender != ch.OwnerOpenID {
		// Someone the owner let in may give work, and is named: the name is a label of the message, not an
		// instruction, as OpenClaw and MemoryOS fence a sender's name.
		name := cn.nameOf(ctx, m.Sender)
		text = fmt.Sprintf("[Lark message from %s, who is not the owner. Questions for approval go to the owner.]\n%s",
			name, defuseLabels(m.Text))
		title = "Lark: " + name
	}

	chat, err := store.LarkChannelChatFor(ctx, cn.name, m.ChatID, m.Sender, title)
	if err != nil {
		return err
	}

	return cn.deliver(ctx, m, chat, text)
}

// inGroup handles a message in a group (issue #246). It is served only when
// the owner switched the group on (OpenClaw's groupPolicy allowlist), the
// message mentions the bot, @all being no mention (OpenClaw, MemoryOS), and
// the Channel's mode lets its sender ask, applied to each sender (Hermes,
// OpenClaw's groupSenderAllowFrom). Anything else is dropped without a word
// and is not recorded (Hermes, MemoryOS). A command needs the mention too
// (OpenClaw).
func (cn *conn) inGroup(ctx context.Context, ch orchestration.LarkChannel, m Message) error {
	store := cn.all.store

	text, mentioned, err := cn.addressed(ctx, m)
	if err != nil || !mentioned {
		return err
	}

	g, err := store.LarkChannelGroup(ctx, cn.name, m.ChatID)
	if errors.Is(err, orchestration.ErrNotFound) {
		// A group the roster does not hold yet, as the bot was added since the last listing: it is put on the
		// owner's list, off, as MemoryOS lists a group that is not allowed when it is asked there.
		return store.JoinLarkChannelGroup(ctx, cn.name, m.ChatID, "")
	}

	if err != nil || !g.Enabled {
		return err
	}

	if refusal := cn.refusalOf(ctx, ch, m.Sender); refusal != "" {
		cn.all.logger.InfoContext(ctx, "a Lark group message was refused", slog.String("event", "larkbot.refused"),
			slog.String("channel", cn.name), slog.String("sender", m.Sender), slog.String("reason", refusal))

		return nil
	}

	first, err := store.TakeLarkMessage(ctx, cn.name, m.ID)
	if err != nil || !first {
		return err
	}

	if !m.IsText {
		cn.notice(ctx, m.ChatID, noticeTextOnly)

		return nil
	}

	// Mentioning the bot with nothing to ask says nothing to answer (MemoryOS).
	if strings.TrimSpace(text) == "" {
		return nil
	}

	title := "Lark: " + g.ChatID
	if g.Name != "" {
		title = "Lark: " + g.Name
	}

	if strings.ToLower(strings.TrimSpace(text)) == commandStop {
		return cn.stop(ctx, m)
	}

	// Which chat a group's message goes to, and whether that one is over, is decided for one message of the group
	// at a time: two messages after an idle spell would otherwise each start a new chat.
	turn := cn.turnOf(m.ChatID)
	turn.Lock()
	defer turn.Unlock()

	if strings.ToLower(strings.TrimSpace(text)) == commandNew {
		// The group's one conversation starts over for every member (OpenClaw's /new, MemoryOS).
		return cn.startOver(ctx, m, title)
	}

	if g, err = store.LarkChannelGroup(ctx, cn.name, m.ChatID); err != nil {
		return err
	}

	// A group's conversation idle for four hours is over: the next question starts a new one (MemoryOS). So is
	// the one of a group nobody asked in since it was added: the bot left it and came back, which starts as a
	// group never seen does (MemoryOS, ChannelGroupRoster).
	if g.AskedAt == nil || cn.all.now().Sub(*g.AskedAt) >= groupIdle {
		if err := cn.restart(ctx, m.ChatID, title); err != nil {
			return err
		}
	}

	chat, err := store.LarkGroupChatFor(ctx, cn.name, m.ChatID, title)
	if err != nil {
		return err
	}

	if err := store.AskedInLarkChannelGroup(ctx, cn.name, m.ChatID); err != nil {
		return err
	}

	// Every message, the owner's too, opens with its sender's name, which the group's prompt says is the only
	// label Hypomnema adds (MemoryOS, ChannelInboundService.asked).
	return cn.deliver(ctx, m, chat, "["+cn.nameOf(ctx, m.Sender)+"]: "+defuseLabels(text))
}

// addressed is a group message's text without the bot's own mentions, and
// whether it mentions the bot. Another person's mention stays, by name rather
// than by the placeholder Lark writes for it (MemoryOS,
// LarkChannelAdapter.withoutBotMentions). A mention of everyone names nobody,
// so it is not one of the bot.
func (cn *conn) addressed(ctx context.Context, m Message) (string, bool, error) {
	bot, err := cn.bot(ctx)
	if err != nil {
		return "", false, err
	}

	mentioned := false
	mentions := make([]Mention, 0, len(m.Mentions))

	for _, at := range m.Mentions {
		if at.OpenID != "" && at.OpenID == bot {
			mentioned = true
		}

		if at.Key != "" {
			mentions = append(mentions, at)
		}
	}

	// The placeholders are replaced in one pass, the longest first, so that @_user_1 is not taken out of
	// @_user_10.
	sort.SliceStable(mentions, func(i, j int) bool { return len(mentions[i].Key) > len(mentions[j].Key) })

	pairs := make([]string, 0, 2*len(mentions))

	for _, at := range mentions {
		switch {
		case at.OpenID != "" && at.OpenID == bot:
			pairs = append(pairs, at.Key, "")
		case at.Name != "":
			pairs = append(pairs, at.Key, "@"+at.Name)
		}
	}

	return strings.TrimSpace(strings.NewReplacer(pairs...).Replace(m.Text)), mentioned, nil
}

// turnOf is what lets one message of a Lark group at a time choose the
// group's chat.
func (cn *conn) turnOf(larkChatID string) *sync.Mutex {
	cn.mu.Lock()
	defer cn.mu.Unlock()

	turn := cn.turns[larkChatID]
	if turn == nil {
		turn = &sync.Mutex{}
		cn.turns[larkChatID] = turn
	}

	return turn
}

// bot is the bot's own open_id, read from Lark the first time it is needed
// and kept; a failed read is tried again with the next message.
func (cn *conn) bot(ctx context.Context) (string, error) {
	cn.mu.Lock()
	id := cn.botID
	cn.mu.Unlock()

	if id != "" {
		return id, nil
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	id, err := cn.lark.BotOpenID(ctx)
	if err != nil {
		return "", err
	}

	cn.mu.Lock()
	cn.botID = id
	cn.mu.Unlock()

	return id, nil
}

// deliver gives a person's message to the Bot of its chat and marks it as
// being worked on.
func (cn *conn) deliver(ctx context.Context, m Message, chat orchestration.LarkChannelChat, text string) error {
	store := cn.all.store

	cn.mu.Lock()
	r := cn.relayLocked(ctx, chat)
	cn.mu.Unlock()

	busy := cn.all.coord.Working(chat.ChatID)

	// A press of the menu has no message to quote or mark as being worked on.
	if !m.Menu {
		r.awaits(m.ID)
	}

	if _, err := store.AddChatMessage(ctx, chat.ChatID, orchestration.ChatOwner, text); err != nil {
		r.forget(m.ID)

		return err
	}

	switch {
	case !cn.all.coord.Enabled():
		// Nothing will answer it, so no reply quotes it.
		r.forget(m.ID)
		cn.notice(ctx, m.ChatID, fmt.Sprintf(noticeNoAnswer, chat.Bot))

		return nil
	case busy:
		// The message waits for the turn that is running, and the person is told, as Hermes tells one who
		// writes while it works.
		cn.notice(ctx, m.ChatID, fmt.Sprintf(noticeQueued, chat.Bot))
	}

	if !m.Menu {
		r.asked(ctx, m.ID)
	}

	return nil
}

// startOver is /new: the next message in the Lark chat starts a new chat with
// the Channel's Bot, titled title.
func (cn *conn) startOver(ctx context.Context, m Message, title string) error {
	if err := cn.restart(ctx, m.ChatID, title); err != nil {
		return err
	}

	cn.notice(ctx, m.ChatID, noticeNew)

	return nil
}

// restart gives a Lark chat a new chat with the Channel's Bot, and relays
// that one. A Lark chat with no chat yet needs none.
func (cn *conn) restart(ctx context.Context, larkChatID, title string) error {
	chat, err := cn.all.store.ResetLarkChannelChat(ctx, cn.name, larkChatID, title)
	if errors.Is(err, orchestration.ErrNotFound) {
		return nil
	}

	if err != nil {
		return err
	}

	cn.mu.Lock()
	if old := cn.relays[larkChatID]; old != nil {
		old.stop()
		delete(cn.relays, larkChatID)
	}

	cn.relayLocked(ctx, chat)
	cn.mu.Unlock()

	return nil
}

// stop is /stop: the Bot stops the turn it is running in the person's chat.
// The server ends that turn with a stopped row, which closes its card as
// stopped, not as failed.
func (cn *conn) stop(ctx context.Context, m Message) error {
	cn.mu.Lock()
	r := cn.relays[m.ChatID]
	cn.mu.Unlock()

	if r == nil || !cn.all.coord.Working(r.chat.ChatID) {
		cn.notice(ctx, m.ChatID, noticeIdle)

		return nil
	}

	if err := cn.all.coord.Abort(ctx, r.chat.ChatID); err != nil {
		return err
	}

	cn.notice(ctx, m.ChatID, noticeStopped)

	return nil
}

// refusal says why a sender may not talk through the Channel in a direct
// message, or nothing when they may.
func (cn *conn) refusal(ctx context.Context, ch orchestration.LarkChannel, m Message) string {
	// A custom app belongs to one organization, which every event names (MemoryOS, rule 2). In a group the
	// check is not made: there everyone is every member of the group (owner, 2026-11-12).
	if m.SenderOrg == "" || m.SenderOrg != m.Org {
		return noticeOutsider
	}

	return cn.refusalOf(ctx, ch, m.Sender)
}

// refusalOf says why the Channel's mode does not let a sender ask, or
// nothing when it does: the same choice in a direct message and, for each
// sender, in a group.
func (cn *conn) refusalOf(ctx context.Context, ch orchestration.LarkChannel, sender string) string {
	if sender == ch.OwnerOpenID {
		return ""
	}

	switch ch.Mode {
	case orchestration.LarkChannelEveryone:
		return ""
	case orchestration.LarkChannelAllowlist:
		if _, err := cn.all.store.LarkChannelPerson(ctx, cn.name, sender); err == nil {
			return ""
		}

		return noticeAllowlist
	default:
		return noticeOwnerOnly
	}
}

// nameOf is how a sender is named to the Bot: the name the owner added them
// under, else their name at Lark, else their id. It is a label, so it is cut
// to a plain name that cannot pass for an instruction or end the label.
func (cn *conn) nameOf(ctx context.Context, openID string) string {
	if p, err := cn.all.store.LarkChannelPerson(ctx, cn.name, openID); err == nil {
		if name := speaker(p.Name); name != "" {
			return name
		}
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	if name, err := cn.lark.NameOf(ctx, openID); err == nil {
		if name := speaker(name); name != "" {
			return name
		}
	}

	return speaker(openID)
}

// speaker is a name as a label of a message: brackets, control characters
// and runs of white space become one space, and it is cut to a sane length
// (MemoryOS, ChannelInboundService.asked).
func speaker(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '[' || r == ']' || unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return ' '
		default:
			return r
		}
	}, name)
	name = strings.Join(strings.Fields(name), " ")

	if r := []rune(name); len(r) > speakerLength {
		name = strings.TrimSpace(string(r[:speakerLength]))
	}

	return name
}

func (cn *conn) notice(ctx context.Context, chatID, text string) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	if err := cn.lark.SendText(ctx, chatID, text); err != nil {
		cn.all.logger.WarnContext(ctx, "a notice was not sent to Lark", slog.String("event", "larkbot.notice_failed"),
			slog.String("channel", cn.name), slog.Any("error", err))
	}
}

// TellOwner sends the owner a card through every Channel of a Bot that is on
// and connected: what a Routine of the Bot has to tell him when nobody is in
// a chat (issue #211; OpenClaw sends a failure alert to the job's owner). A
// Channel through which he already reads the chat named is left out, so he
// is told once.
func (c *Channels) TellOwner(ctx context.Context, bot, chatID, title, text string) {
	channels, err := c.store.LarkChannels(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "the Lark channels were not read", slog.String("event", "larkbot.channels_failed"),
			slog.Any("error", err))

		return
	}

	for _, ch := range channels {
		c.mu.Lock()
		cn := c.conns[ch.Name]
		c.mu.Unlock()

		if ch.Bot != bot || !ch.Enabled || cn == nil || ch.OwnerOpenID == "" || cn.reads(ctx, chatID) {
			continue
		}

		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		_, _, err := cn.lark.SendCard(sendCtx, ch.OwnerOpenID, Card{Title: title, Color: "red", Text: inert(text)})

		cancel()

		if err != nil {
			c.logger.WarnContext(ctx, "the owner was not told at Lark", slog.String("event", "larkbot.notice_failed"),
				slog.String("channel", ch.Name), slog.Any("error", err))
		}
	}
}

// reads reports whether a chat is one of the Channel's: what is said in it
// reaches Lark already.
func (cn *conn) reads(ctx context.Context, chatID string) bool {
	if chatID == "" {
		return false
	}

	chats, err := cn.all.store.LarkChannelChats(ctx, cn.name)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(chats, func(c orchestration.LarkChannelChat) bool { return c.ChatID == chatID })
}

// relayLocked starts sending a chat's replies to its Lark chat, once, and
// returns what sends them.
func (cn *conn) relayLocked(ctx context.Context, chat orchestration.LarkChannelChat) *relay {
	if r, running := cn.relays[chat.LarkChatID]; running {
		return r
	}

	ctx, stop := context.WithCancel(ctx)
	r := &relay{all: cn.all, cn: cn, lark: cn.lark, chat: chat, sent: chat.SentSeq, stop: stop}
	cn.relays[chat.LarkChatID] = r

	go r.run(ctx)

	return r
}

// relay sends what the Bot says in one chat to its Lark chat. The
// text as it is written comes from the supervisor's live frames and fills a
// card; the kept message that follows is what the card ends as, so a frame
// that was lost changes nothing (the web client is put right the same way).
type relay struct {
	all  *Channels
	cn   *conn
	lark Lark
	chat orchestration.LarkChannelChat
	stop context.CancelFunc
	// sent is the last kept message that was sent.
	sent int64
	// frames is the Bot's live frames of the chat.
	frames <-chan coordinator.Frame

	// The card being written: its id, the text the Bot wrote, what a
	// tool that runs is doing, what the card shows, when it was last updated
	// and its next sequence.
	card    string
	text    strings.Builder
	status  string
	shown   string
	updated time.Time
	seq     int

	mu sync.Mutex
	// waiting are the person's messages that have no reply yet, with the
	// reaction that says the Bot works on each.
	waiting []waiting
}

// stopped closes the card of a turn the person stopped: with the text it had
// and a line that says so. With no card and no text the notice that answered
// /stop has said it.
func (r *relay) stopped(ctx context.Context, text string) {
	text = strings.TrimSpace(text)

	switch {
	case text != "":
		text += "\n\n" + noticeStopped
	case r.card != "":
		text = noticeStopped
	}

	r.finish(ctx, text)
}

// waiting is a message of a person the Bot has not answered.
type waiting struct {
	message  string
	reaction string
}

// awaits records a person's message as waiting for a reply, before the Bot is
// given it: an answer that comes at once still quotes it (issue #377).
func (r *relay) awaits(messageID string) {
	r.mu.Lock()
	r.waiting = append(r.waiting, waiting{message: messageID})
	r.mu.Unlock()
}

// asked marks a message that awaits a reply as being worked on. When the
// reply came while the mark was being put, the mark is taken off again.
func (r *relay) asked(ctx context.Context, messageID string) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	// A reaction that cannot be put (the app lacks the scope) costs the person only the mark.
	reaction, err := r.lark.React(ctx, messageID, emojiWorking)
	if err != nil {
		r.failed(ctx, "marking a message as being worked on", err)

		return
	}

	r.mu.Lock()

	kept := false

	for i := range r.waiting {
		if r.waiting[i].message == messageID {
			r.waiting[i].reaction, kept = reaction, true

			break
		}
	}

	r.mu.Unlock()

	if kept {
		return
	}

	if err := r.lark.Unreact(ctx, messageID, reaction); err != nil {
		r.failed(ctx, "taking a mark off a message", err)
	}
}

// forget drops a message that was never given to the Bot.
func (r *relay) forget(messageID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.waiting = slices.DeleteFunc(r.waiting, func(w waiting) bool { return w.message == messageID })
}

// replyTo is the oldest message that waits for a reply, which the next card
// answers.
func (r *relay) replyTo() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.waiting) == 0 {
		return ""
	}

	return r.waiting[0].message
}

// settle takes the mark off every message that waited: the reply is there,
// or the Bot stopped. With failed, the last one is marked as failed.
func (r *relay) settle(ctx context.Context, failed bool) {
	r.mu.Lock()
	waited := r.waiting
	r.waiting = nil
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	for i, w := range waited {
		if w.reaction != "" {
			if err := r.lark.Unreact(ctx, w.message, w.reaction); err != nil {
				r.failed(ctx, "taking a mark off a message", err)
			}
		}

		if failed && i == len(waited)-1 {
			if _, err := r.lark.React(ctx, w.message, emojiFailed); err != nil {
				r.failed(ctx, "marking a message as failed", err)
			}
		}
	}
}

func (r *relay) run(ctx context.Context) {
	frames, stop := r.all.coord.Watch(r.chat.ChatID)
	defer stop()

	r.frames = frames

	wake := r.all.store.Subscribe(ctx)

	var closing <-chan time.Time

	r.kept(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case f := <-frames:
			if r.frame(ctx, f) {
				closing = time.After(closeAfter)
			}
		case <-wake:
			r.kept(ctx)
		case <-closing:
			closing = nil

			// The Bot stopped and kept no reply: what was written stays, and nothing waits any more.
			r.finish(ctx, r.text.String())
			r.settle(ctx, false)
		}
	}
}

// frame handles one live frame and says whether the Bot stopped, so
// that a card still open is closed soon if no reply comes.
func (r *relay) frame(ctx context.Context, f coordinator.Frame) bool {
	data, _ := f.Data.(map[string]any)

	switch f.Event {
	case coordinator.FrameReset:
		// A new reply begins. A card that shows only what a tool did is kept for it.
		if strings.TrimSpace(r.text.String()) != "" {
			r.finish(ctx, r.text.String())
		}

		r.status = ""
	case coordinator.FrameDelta:
		if data["kind"] == "text" {
			added, _ := data["delta"].(string)
			r.text.WriteString(added)
			r.show(ctx, false)
		}
	case coordinator.FrameTool:
		// What a tool that runs is doing, under the text, as Hermes and OpenClaw show progress.
		r.status = ""
		if data["phase"] == "start" {
			r.status = statusOf(data)
		}

		r.show(ctx, true)
	case coordinator.FrameState:
		return data["working"] == false
	}

	return false
}

// statusOf is the line a running tool shows: its name and the start of its
// arguments.
func statusOf(data map[string]any) string {
	name, _ := data["name"].(string)
	if name == "" {
		return ""
	}

	preview := ""
	if raw, ok := data["args"].(json.RawMessage); ok {
		preview = string(raw)
	} else if raw, err := json.Marshal(data["args"]); err == nil && string(raw) != "null" {
		preview = string(raw)
	}

	preview = strings.Join(strings.Fields(preview), " ")
	if r := []rune(preview); len(r) > statusPreview {
		preview = string(r[:statusPreview]) + "…"
	}

	if preview == "" {
		return "⚡ " + name
	}

	return "⚡ " + name + ": " + inert(preview)
}

// show brings the card up to what the Bot wrote and is doing: it
// opens the card with the first thing to show, and updates it at most twice a
// second unless now is set.
func (r *relay) show(ctx context.Context, now bool) {
	written, _ := mediaOf(r.text.String())
	body := inert(cut(written))

	switch {
	case r.status != "" && strings.TrimSpace(body) != "":
		body += "\n\n" + r.status
	case r.status != "":
		body = r.status
	case strings.TrimSpace(body) != "":
		body += writing
	default:
		return
	}

	if body == r.shown {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	if r.card == "" {
		id, larkChat, err := r.lark.OpenCard(ctx, r.chat.LarkChatID, r.replyTo(), body)
		if err != nil {
			r.failed(ctx, "opening a card", err)

			return
		}

		r.named(ctx, larkChat)

		r.card, r.shown, r.updated, r.seq = id, body, r.all.now(), 1

		return
	}

	if !now && r.all.now().Sub(r.updated) < cardEvery {
		return
	}

	if err := r.lark.UpdateCard(ctx, r.card, body, r.seq); err != nil {
		r.failed(ctx, "updating a card", err)
	}

	r.shown, r.updated = body, r.all.now()
	r.seq++
}

// kept sends the Bot's kept messages that were not sent yet: each
// ends the card being written, or is a card of its own when none is.
func (r *relay) kept(ctx context.Context) {
	for {
		msgs, err := r.all.store.ListChatMessages(ctx, r.chat.ChatID, r.sent, orchestration.ChatPageDefault)
		if err != nil || len(msgs) == 0 {
			return
		}

		// A reply is kept after its last piece was written, but the two arrive on their own channels. Every
		// piece of a reply that was just read is already waiting, so it is taken first: a piece taken after
		// its reply would open a card nothing closes.
		for pending := true; pending; {
			select {
			case f := <-r.frames:
				r.frame(ctx, f)
			default:
				pending = false
			}
		}

		for i := 0; i < len(msgs); i++ {
			m := msgs[i]

			switch {
			case m.Role == orchestration.ChatCoordinator && strings.TrimSpace(m.Text) != "" &&
				i+1 < len(msgs) && msgs[i+1].Kind == orchestration.ChatStopped:
				// What a turn the person stopped had written is kept together with its stopped row (coordinator
				// keepReply): the card closes with it and says it was stopped.
				r.stopped(ctx, m.Text)
				r.settle(ctx, false)

				i++
				m = msgs[i]
			case m.Role == orchestration.ChatCoordinator && strings.TrimSpace(m.Text) != "":
				r.finish(ctx, m.Text)
				r.settle(ctx, false)
			case m.Kind == orchestration.ChatStopped:
				// The server recorded that the person stopped the turn: it was stopped, not failed.
				r.stopped(ctx, r.text.String())
				r.settle(ctx, false)
			case m.Kind == orchestration.ChatFailed:
				// The model's call failed: what was written stays, with a line that says why it stops there
				// (MemoryOS), and the message is marked as failed (Hermes).
				partial := strings.TrimSpace(r.text.String())
				if partial != "" {
					partial += "\n\n"
				}

				r.finish(ctx, partial+fmt.Sprintf(noticeNoReply, r.chat.Bot))
				r.settle(ctx, true)
			}

			r.sent = m.Seq
		}

		if err := r.all.store.MarkLarkChannelSent(ctx, r.chat.LarkChatID, r.sent); err != nil {
			r.failed(ctx, "recording what was sent", err)

			return
		}
	}
}

// webLink says the Channel's replies end with a link to the web client. The
// Channel is read at each reply, so the owner's switch counts from the next
// one; a Channel that cannot be read goes without the link.
func (r *relay) webLink(ctx context.Context) bool {
	ch, err := r.all.store.LarkChannel(ctx, r.chat.Channel)

	return err == nil && ch.WebLink
}

// finish ends the card being written with its final text, or sends the text
// as a card of its own. A text longer than a card goes on in further cards.
// The files the text asks for follow it (issue #312). With no card, no text
// and no file it does nothing.
func (r *relay) finish(ctx context.Context, text string) {
	card, seq, replyTo := r.card, r.seq, r.replyTo()

	r.card, r.shown, r.status, r.seq = "", "", "", 0
	r.text.Reset()

	text, files := mediaOf(text)
	defer r.attach(ctx, files)

	if strings.TrimSpace(text) == "" {
		switch {
		case card == "":
			return
		case len(files) > 0:
			// A card that showed what a tool did, for a reply that is only files: it ends naming them.
			names := make([]string, 0, len(files))
			for _, f := range files {
				names = append(names, shownName(f))
			}

			text = strings.Join(names, "\n")
		default:
			text = "(no answer)"
		}
	}

	parts := split(inert(text))
	// A group's reply has no link: its conversation goes on in the group, and only the owner opens the web
	// client (MemoryOS, ChannelInboundService: a group answer has no closing line). A Channel for other people
	// has it switched off (owner, 2026-11-13).
	if r.all.webURL != "" && !r.chat.Group && r.webLink(ctx) {
		parts[len(parts)-1] += "\n\n[Open in Hypomnema](" + r.all.webURL + "/chats/" + r.chat.ChatID + ")"
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	for i, part := range parts {
		if i > 0 || card == "" {
			id, larkChat, err := r.lark.OpenCard(ctx, r.chat.LarkChatID, replyTo, part)
			if err != nil {
				r.failed(ctx, "sending a reply", err)

				return
			}

			r.named(ctx, larkChat)

			card, seq, replyTo = id, 1, ""
		}

		if err := r.lark.CloseCard(ctx, card, part, seq); err != nil {
			r.failed(ctx, "closing a card", err)
		}
	}
}

// named records the Lark chat a reply was sent to, for a direct chat kept
// under the person's open_id until Lark named it (issue #312: a press of the
// menu names no chat). The chat and its relay go by that id from then on.
func (r *relay) named(ctx context.Context, larkChatID string) {
	old := r.chat.LarkChatID
	if !strings.HasPrefix(old, openIDPrefix) || larkChatID == "" || larkChatID == old {
		return
	}

	if err := r.all.store.RenameLarkChannelChat(ctx, r.chat.Channel, old, larkChatID); err != nil {
		r.failed(ctx, "recording the chat Lark named", err)

		return
	}

	r.cn.mu.Lock()
	if r.cn.relays[old] == r {
		delete(r.cn.relays, old)
		r.cn.relays[larkChatID] = r
	}

	r.chat.LarkChatID = larkChatID
	r.cn.mu.Unlock()
}

func (r *relay) failed(ctx context.Context, doing string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}

	r.all.logger.WarnContext(ctx, "a Lark channel failed "+doing, slog.String("event", "larkbot.send_failed"),
		slog.String("channel", r.chat.Channel), slog.String("chat_id", r.chat.ChatID), slog.Any("error", err))
}

// inert writes Lark's mention tags out instead of letting them act: an <at>
// in a reply, or in a document the reply quotes, would notify whoever it
// names, a whole group for <at id=all> (MemoryOS, LarkReplyCard.inert).
func inert(text string) string {
	return mentionTag.ReplaceAllString(text, "&lt;$1$2")
}

var mentionTag = regexp.MustCompile(`(?i)<(/?)(at|person)\b`)

// cut is at most a card's worth of a text that is still being written.
func cut(text string) string {
	r := []rune(text)
	if len(r) <= cardLimit {
		return text
	}

	return strings.TrimSpace(string(r[:cardLimit])) + " …"
}

// split cuts a finished text into parts a card each, at a line's end where
// there is one in the last fifth of a part.
func split(text string) []string {
	r := []rune(text)

	var parts []string

	for len(r) > cardLimit {
		at := cardLimit
		for i := cardLimit; i > cardLimit*4/5; i-- {
			if r[i-1] == '\n' {
				at = i

				break
			}
		}

		parts = append(parts, strings.TrimSpace(string(r[:at])))
		r = r[at:]
	}

	return append(parts, string(r))
}

// textOf is the text of a Lark text message's content, which is JSON.
func textOf(content string) string {
	var parsed struct {
		Text string `json:"text"`
	}

	if json.Unmarshal([]byte(content), &parsed) != nil {
		return ""
	}

	return parsed.Text
}

// A question for the owner is a card at Lark (issue #136), as OpenClaw's and
// Hermes' approval cards are: each choice is a button, and a reply to the
// card answers in the owner's own words. Only the owner's press counts, by
// the open_id of who pressed (MemoryOS: the approver is checked by open_id).
// A card says how its question ended, and then has no buttons.

const (
	answered      = "Answered."
	onlyTheOwner  = "Only the owner answers this."
	questionTitle = "A worker asks"
	// buttonQuestion and buttonOption are what a choice's button carries.
	buttonQuestion = "question"
	buttonOption   = "option"
)

// questions sends the owner's open questions that go out through this
// Channel as cards and closes the cards of questions that ended, on every
// change of the store.
func (cn *conn) questions(ctx context.Context) {
	wake := cn.all.store.Subscribe(ctx)

	for {
		if err := cn.syncQuestions(ctx); err != nil && ctx.Err() == nil {
			cn.all.logger.WarnContext(ctx, "the owner's questions were not brought to Lark",
				slog.String("event", "larkbot.questions_failed"), slog.String("channel", cn.name), slog.Any("error", err))
		}

		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
}

func (cn *conn) syncQuestions(ctx context.Context) error {
	store := cn.all.store

	ch, err := store.LarkChannel(ctx, cn.name)
	if err != nil || !ch.Enabled {
		// A Channel that was switched off or removed sends nothing.
		return nil
	}

	open, err := store.ListQuestions(ctx, orchestration.QuestionFilter{
		AddressedTo: orchestration.AddressOwner, Status: orchestration.QuestionOpen,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	for _, q := range open {
		if _, err := store.LarkQuestionCardOf(ctx, q.ID); !errors.Is(err, orchestration.ErrNotFound) {
			continue
		}

		// A question goes out through one Channel: the one its Run's chat came in through, else the first.
		if via, err := store.LarkChannelForRun(ctx, q.RunID); err != nil || via != cn.name {
			continue
		}

		cardID, messageID, err := cn.lark.SendCard(ctx, ch.OwnerOpenID, questionCard(q))
		if err != nil {
			return fmt.Errorf("sending question %s: %w", q.ID, err)
		}

		if err := store.RecordLarkQuestionCard(ctx, orchestration.LarkQuestionCard{
			QuestionID: q.ID, Channel: cn.name, CardID: cardID, MessageID: messageID,
		}); err != nil {
			return err
		}
	}

	cards, err := store.OpenLarkQuestionCards(ctx, cn.name)
	if err != nil {
		return err
	}

	for _, c := range cards {
		q, err := store.GetQuestion(ctx, c.QuestionID)
		if err != nil || q.Status == orchestration.QuestionOpen {
			continue
		}

		if err := cn.lark.ReplaceCard(ctx, c.CardID, endedCard(q), c.Seq+1); err != nil {
			return fmt.Errorf("closing the card of question %s: %w", q.ID, err)
		}

		if err := store.CloseLarkQuestionCard(ctx, c.QuestionID, c.Seq+1); err != nil {
			return err
		}
	}

	return nil
}

// questionCard is a question as the owner sees it at Lark.
func questionCard(q orchestration.Question) Card {
	card := Card{Title: questionTitle, Color: "orange", Text: inert(q.Text)}

	for i, o := range q.Options {
		card.Buttons = append(card.Buttons, Button{
			Label: o.Label, Value: map[string]string{buttonQuestion: q.ID, buttonOption: fmt.Sprint(i)},
		})
	}

	if len(q.Options) == 0 {
		card.Note = "Reply to this card to answer."
	}

	return card
}

// endedCard is the card of a question that can no longer be answered: it
// says why, as OpenMausBot's cards do.
func endedCard(q orchestration.Question) Card {
	card := Card{Title: questionTitle, Color: "green", Text: inert(q.Text)}

	switch {
	case q.Status == orchestration.QuestionCanceled:
		card.Color = "red"
		card.Note = "Canceled: " + q.CancelReason
	case q.Answer != nil:
		var parts []string

		if q.Answer.Option != nil && *q.Answer.Option >= 0 && *q.Answer.Option < len(q.Options) {
			parts = append(parts, q.Options[*q.Answer.Option].Label)
		}

		if strings.TrimSpace(q.Answer.Text) != "" {
			parts = append(parts, q.Answer.Text)
		}

		card.Note = fmt.Sprintf("Answered by the %s: %s", q.AnsweredBy, inert(strings.Join(parts, " — ")))
	default:
		card.Color = "grey"
		card.Note = "Closed."
	}

	return card
}

// press handles a press of a card's button and returns what the person who
// pressed is told. It is called from the connection, which must answer Lark
// within three seconds; an answer is one write.
func (cn *conn) press(ctx context.Context, p Press) string {
	ch, err := cn.all.store.LarkChannel(ctx, cn.name)
	if err != nil || !ch.Enabled {
		return ""
	}

	if p.Operator != ch.OwnerOpenID {
		return onlyTheOwner
	}

	var option int
	if _, err := fmt.Sscan(p.Value[buttonOption], &option); err != nil || p.Value[buttonQuestion] == "" {
		return ""
	}

	return cn.answer(ctx, p.Value[buttonQuestion], orchestration.Answer{Option: &option})
}

// answer records the owner's answer and returns what he is told.
func (cn *conn) answer(ctx context.Context, questionID string, a orchestration.Answer) string {
	if _, err := cn.all.store.AnswerQuestion(ctx, questionID, orchestration.AddressOwner, a); err != nil {
		cn.all.logger.InfoContext(ctx, "an answer from Lark was not taken", slog.String("event", "larkbot.answer_refused"),
			slog.String("channel", cn.name), slog.String("question_id", questionID), slog.Any("error", err))

		return "That answer was not taken: " + err.Error()
	}

	return answered
}
