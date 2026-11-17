// Package channels runs the Channels (issues #136, #169 and #404): the
// runtime every kind of messaging service shares. A Channel is a door to one
// Bot: an account at a service and who may talk through it. A person's
// direct message to the account becomes a message of a chat with the
// Channel's Bot, and what the Bot says in that chat is sent back as a reply
// that fills as the text is written. In a group the owner switched on, a
// message that mentions the account is a message of the group's one chat
// with the Bot (issue #246). Each Channel has its own connection; several
// Channels may talk to one Bot (OpenClaw's bindings).
//
// The runtime is shared and each kind's transport is its own, as MemoryOS
// splits its ChannelSupervisor and ChannelInboundService from each
// ChannelAdapter, OpenClaw its channel plugins from routing and DM policy,
// and Hermes its BasePlatformAdapter from the gateway (issue #404): this
// package holds who may talk, taking a message once, groups and mentions,
// /new and /stop, the reply relay and questions; a kind's Channel (Lark's in
// channels/lark) connects, maps its events and sends. Who may talk to a
// Channel is closed by default.
package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Store is what the Channels keep (orchestration.Service).
type Store interface {
	Channel(ctx context.Context, name string) (channelstore.Channel, error)
	ListChannels(ctx context.Context) ([]channelstore.Channel, error)
	ChannelPerson(ctx context.Context, channel, personID string) (channelstore.ChannelPerson, error)
	TakeChannelMessage(ctx context.Context, channel, messageID string) (bool, error)
	ForgetChannelMessagesBefore(ctx context.Context, before time.Time) error
	ChannelChatFor(ctx context.Context, channel, externalChatID, personID, title string,
	) (channelstore.ChannelChat, error)
	ChannelGroupChatFor(ctx context.Context, channel, externalChatID, title string) (channelstore.ChannelChat, error)
	ChannelChats(ctx context.Context, channel string) ([]channelstore.ChannelChat, error)
	ChannelGroup(ctx context.Context, channel, chatID string) (channelstore.ChannelGroup, error)
	JoinChannelGroup(ctx context.Context, channel, chatID, name string) error
	LeaveChannelGroup(ctx context.Context, channel, chatID string) error
	ListedChannelGroups(ctx context.Context, channel string, listed []channelstore.ChannelGroup,
		readAt time.Time) ([]string, error)
	AskedInChannelGroup(ctx context.Context, channel, chatID string) error
	ResetChannelChat(ctx context.Context, channel, externalChatID, title string) (channelstore.ChannelChat, error)
	MarkChannelSent(ctx context.Context, externalChatID string, seq int64) error
	RenameChannelChat(ctx context.Context, channel, oldID, newID string) error
	ListQuestions(ctx context.Context, f orchestration.QuestionFilter) ([]orchestration.Question, error)
	GetQuestion(ctx context.Context, id string) (orchestration.Question, error)
	AnswerQuestion(ctx context.Context, id string, by orchestration.Addressee, a orchestration.Answer,
	) (orchestration.Question, error)
	ChannelForRun(ctx context.Context, runID string) (string, error)
	RecordQuestionCard(ctx context.Context, c channelstore.QuestionCard) error
	QuestionCardOf(ctx context.Context, questionID string) (channelstore.QuestionCard, error)
	QuestionCardByMessage(ctx context.Context, channel, messageID string) (channelstore.QuestionCard, error)
	OpenQuestionCards(ctx context.Context, channel string) ([]channelstore.QuestionCard, error)
	CloseQuestionCard(ctx context.Context, questionID string, seq int) error
	AddChatMessage(ctx context.Context, chatID string, role orchestration.ChatRole, text string,
	) (orchestration.ChatMessage, error)
	SendOwnerMessage(ctx context.Context, chatID string, m orchestration.NewChatMessage, busy bool,
	) (*orchestration.ChatMessage, *orchestration.QueuedMessage, error)
	ListChatMessages(ctx context.Context, chatID string, after int64, limit int) ([]orchestration.ChatMessage, error)
	ArtifactsBeforeReply(ctx context.Context, chatID string, seq int64) ([]orchestration.ArtifactRef, error)
	ChatToolDisplay(ctx context.Context, chatID string) (orchestration.ToolDisplay, error)
	GetBot(ctx context.Context, id string) (orchestration.Bot, error)
	Subscribe(ctx context.Context) <-chan struct{}
}

// botName is what a person is told a Bot is called: its name, read at each notice since it may change (issue
// #466), or its id when it cannot be read.
func botName(ctx context.Context, store Store, id string) string {
	b, err := store.GetBot(ctx, id)
	if err != nil {
		return id
	}

	return b.Name
}

// Coordinator is the part of the supervisor of the Bots' pi the Channels use.
type Coordinator interface {
	Enabled() bool
	Working(chatID string) bool
	Abort(ctx context.Context, chatID string) error
	Watch(chatID string) (frames <-chan coordinator.Frame, stop func())
}

// / What a Channel says in its own voice. English, as the web client is. To a
// person at the service the Channel's account is "this bot". The notices
// with %s name the Bot of the chat, except noticeOutsider, which names the
// service.
const (
	noticeOwnerOnly = "This bot takes messages from its owner only."
	noticeAllowlist = "This bot takes messages from its owner and the people he added."
	noticeOutsider  = "This bot serves members of its %s organization only."
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
const (
	commandNew  = "/new"
	commandStop = "/stop"
	// commandMenu opens what a press of the account's menu says to the Bot:
	// /menu and the item's key (OpenClaw, monitor.account.ts handleBotMenu).
	commandMenu = "/menu"
)

const (
	// updateEvery is the least time between two updates of a reply: Lark
	// allows five a second on one card (MemoryOS, from Lark's limits).
	updateEvery = 500 * time.Millisecond
	// closeAfter is how long a reply waits for its kept message once the
	// Bot stopped working, before it is closed with what was shown.
	closeAfter = 5 * time.Second
	// statusPreview is how much of a tool's arguments its status line shows.
	statusPreview = 60
	// forgetAfter is how long a taken message is remembered: a service
	// delivers an event again for hours (Lark's last delivery comes about
	// seven hours after the first).
	forgetAfter = 24 * time.Hour
	// retryEvery is how often the Channels are looked at again, for one that
	// could not connect.
	retryEvery  = 30 * time.Second
	sendTimeout = 30 * time.Second
	// mediaTimeout is how long sending a file may take (OpenClaw
	// extensions/feishu/src/media.ts, FEISHU_MEDIA_HTTP_TIMEOUT_MS).
	mediaTimeout = 2 * time.Minute
	// listEvery is how often a Channel reads the service's whole list of its
	// account's groups, besides when it connects (MemoryOS,
	// ChannelSupervisor.LISTING).
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

// Channels runs the Channels: one connection for each.
type Channels struct {
	store    Store
	coord    Coordinator
	registry Registry
	vault    *vault.Service
	logger   *slog.Logger
	now      func() time.Time
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

// conn is one Channel's connection and the chats it answers in.
type conn struct {
	all *Channels
	// name is the Channel's; appID the account the connection was opened as.
	name  string
	appID string
	conn  Conn
	// caps are what the Channel's kind can do.
	caps Capabilities

	mu     sync.Mutex
	relays map[string]*relay
	// selfID is the account's own id, read once it is first needed.
	selfID string
	// turns let one message of each group at a time choose its chat.
	turns map[string]*sync.Mutex
}

// WithWebURL sets the web client's address; a reply then ends with a link to
// its chat there (MemoryOS's "Continue in MemoryOS", after Dust).
func (c *Channels) WithWebURL(url string) *Channels {
	c.webURL = strings.TrimSuffix(url, "/")

	return c
}

// WithFiles sets how the Bots' folders are read, for the files a Bot sends
// into its chat at the service (issue #312). Without it a file a Bot asks to send is
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

// New returns the Channels' runner. registry finds each Channel's kind, and
// the vault holds each account's secret, which is opened to connect.
func New(store Store, coord Coordinator, registry Registry, secrets *vault.Service, logger *slog.Logger) *Channels {
	return &Channels{
		store: store, coord: coord, registry: registry, vault: secrets, logger: logger, now: time.Now,
		look: make(chan struct{}, 1), conns: map[string]*conn{}, starting: map[string]bool{},
		failures: map[string]string{},
	}
}

// Capabilities are what the chats of a kind can do; an error when no Channel
// of that kind is registered.
func (c *Channels) Capabilities(kind Kind) (Capabilities, error) {
	ch, err := c.registry.Lookup(kind)
	if err != nil {
		return Capabilities{}, err
	}

	return ch.Capabilities(), nil
}

// Connect makes a Channel's connection with its account's secret, through
// the Channel of its kind. It is not listened to.
func (c *Channels) Connect(ctx context.Context, channel channelstore.Channel, secret string) (Conn, error) {
	kind, err := c.registry.Lookup(channel.Kind)
	if err != nil {
		return nil, err
	}

	return kind.Open(ctx, channel, secret)
}

// Open makes a Channel's connection from what the owner set up: its
// account's secret is opened from the vault for this call only.
func (c *Channels) Open(ctx context.Context, channel channelstore.Channel) (Conn, error) {
	if _, err := c.registry.Lookup(channel.Kind); err != nil {
		return nil, err
	}

	opened, err := c.vault.Open(ctx, channel.Secret)
	if err != nil {
		return nil, fmt.Errorf("opening the secret of channel %s: %w", channel.Name, err)
	}

	return c.Connect(ctx, channel, opened.Payload[vault.KeyValue])
}

// State is whether a Channel is connected.
type State struct {
	// Connected says the connection was opened; AppID is the account it was
	// opened as, which a later change of the Channel's account does not
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
// is tried again (MemoryOS, ChannelSupervisor). A connection once opened
// stays until the process ends, as Lark's SDK cannot close one: a Channel
// that is switched off or removed takes no message, and another account is
// used from the next start.
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
			if err := c.store.ForgetChannelMessagesBefore(ctx, c.now().Add(-forgetAfter)); err != nil {
				c.logger.WarnContext(ctx, "old channel messages were not forgotten",
					slog.String("event", "channels.forget_failed"), slog.Any("error", err))
			}
		}
	}
}

// connect opens the connection of every Channel that is on and has none.
// Each is opened on its own goroutine: one that its service keeps waiting
// does not hold the others back.
func (c *Channels) connect(ctx context.Context) {
	channels, err := c.store.ListChannels(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.logger.WarnContext(ctx, "the channels were not read", slog.String("event", "channels.channels_failed"),
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

// start opens one Channel's connection. A Channel whose kind is not
// registered does not connect, and says why.
func (c *Channels) start(ctx context.Context, ch channelstore.Channel) {
	cn := &conn{all: c, name: ch.Name, appID: ch.AppID, relays: map[string]*relay{}, turns: map[string]*sync.Mutex{}}

	caps, err := c.Capabilities(ch.Kind)
	if err == nil {
		cn.caps = caps
		cn.conn, err = c.Open(ctx, ch)
	}

	if err == nil {
		err = cn.conn.Listen(ctx, Events{
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
		c.logger.WarnContext(ctx, "a channel did not connect", slog.String("event", "channels.connect_failed"),
			slog.String("channel", ch.Name), slog.String("kind", string(ch.Kind)), slog.Any("error", err))

		return
	}

	c.conns[ch.Name] = cn
	delete(c.failures, ch.Name)
	c.mu.Unlock()

	c.logger.InfoContext(ctx, "a channel is connected", slog.String("event", "channels.connected"),
		slog.String("channel", ch.Name), slog.String("kind", string(ch.Kind)), slog.String("app_id", ch.AppID))

	chats, err := c.store.ChannelChats(ctx, ch.Name)
	if err != nil {
		c.logger.WarnContext(ctx, "a channel's chats were not read", slog.String("event", "channels.chats_failed"),
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

// roster keeps the Channel's groups: the service's whole list of the
// account's groups is read when the Channel connects and every ten minutes
// after, which adds the groups the account was added to unseen and removes
// those it left (MemoryOS, ChannelGroupRoster.listed). It is the only way to
// know the groups for an account that is not told when it is added to one or
// leaves it.
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
	groups, err := cn.conn.Groups(listCtx)

	cancel()

	if err != nil {
		if ctx.Err() == nil {
			cn.all.logger.WarnContext(ctx, "a channel's groups were not listed",
				slog.String("event", "channels.groups_failed"), slog.String("channel", cn.name), slog.Any("error", err))
		}

		return
	}

	listed := make([]channelstore.ChannelGroup, 0, len(groups))
	for _, g := range groups {
		listed = append(listed, channelstore.ChannelGroup{ChatID: g.ChatID, Name: g.Name})
	}

	left, err := cn.all.store.ListedChannelGroups(ctx, cn.name, listed, readAt)
	if err != nil {
		cn.all.logger.WarnContext(ctx, "a channel's groups were not recorded",
			slog.String("event", "channels.groups_failed"), slog.String("channel", cn.name), slog.Any("error", err))

		return
	}

	if len(left) > 0 {
		cn.all.logger.InfoContext(ctx, "a channel's bot is no longer in groups",
			slog.String("event", "channels.groups_left"), slog.String("channel", cn.name), slog.Int("count", len(left)))
	}
}

// joined records a group the bot was added to; it is off until the owner
// switches it on, and the bot says nothing there (no greeting on join).
func (cn *conn) joined(ctx context.Context, g Group) {
	if err := cn.all.store.JoinChannelGroup(ctx, cn.name, g.ChatID, g.Name); err != nil {
		cn.all.logger.WarnContext(ctx, "a group was not recorded", slog.String("event", "channels.group_failed"),
			slog.String("channel", cn.name), slog.Any("error", err))
	}
}

// left forgets a group the bot left.
func (cn *conn) left(ctx context.Context, chatID string) {
	if err := cn.all.store.LeaveChannelGroup(ctx, cn.name, chatID); err != nil {
		cn.all.logger.WarnContext(ctx, "a group was not forgotten", slog.String("event", "channels.group_failed"),
			slog.String("channel", cn.name), slog.Any("error", err))
	}
}

// take handles one message from the service. It is called from the
// connection, which must acknowledge the event soon (Lark: within three
// seconds), so it does its work on its own goroutine. A message that arrives before the connection is known to be
// open is taken all the same.
func (cn *conn) take(ctx context.Context, m Message) {
	go func() {
		if err := cn.taken(ctx, m); err != nil {
			cn.all.logger.WarnContext(ctx, "a message was not taken", slog.String("event", "channels.take_failed"),
				slog.String("channel", cn.name), slog.String("message_id", m.ID), slog.Any("error", err))
			cn.notice(ctx, m.ChatID, noticeFailed)
		}
	}()
}

func (cn *conn) taken(ctx context.Context, m Message) error {
	store := cn.all.store

	ch, err := store.Channel(ctx, cn.name)
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

	first, err := store.TakeChannelMessage(ctx, cn.name, m.ID)
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
			cn.all.logger.WarnContext(ctx, "a press of the menu was not taken",
				slog.String("event", "channels.take_failed"), slog.String("channel", cn.name), slog.Any("error", err))
		}
	}()
}

func (cn *conn) menued(ctx context.Context, p MenuPress) error {
	store := cn.all.store

	ch, err := store.Channel(ctx, cn.name)
	if err != nil || !ch.Enabled {
		return nil //nolint:nilerr // A Channel that was switched off or removed takes nothing and says nothing.
	}

	key := speaker(p.Key)
	if p.Operator == "" || key == "" {
		return nil
	}

	// A press has no message id, so one is made of its key and when it was pressed, as OpenClaw makes it, which
	// the service's repeated delivery of the press repeats.
	id := "bot-menu:" + key + ":" + p.At

	first, err := store.TakeChannelMessage(ctx, cn.name, id)
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
	// known yet is answered by their id, and the service's answer names the chat (OpenClaw answers a press to
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
// person's id while the service has not named it, or none.
func (cn *conn) directChat(ctx context.Context, personID string) (string, error) {
	chats, err := cn.all.store.ChannelChats(ctx, cn.name)
	if err != nil {
		return "", err
	}

	for _, c := range chats {
		if !c.Group && c.PersonID == personID {
			return c.ExternalChatID, nil
		}
	}

	return "", nil
}

// direct handles a direct message that was taken: who may send it, then what
// it is.
func (cn *conn) direct(ctx context.Context, ch channelstore.Channel, m Message) error {
	store := cn.all.store

	if refusal := cn.refusal(ctx, ch, m); refusal != "" {
		cn.all.logger.InfoContext(ctx, "a message was refused", slog.String("event", "channels.refused"),
			slog.String("channel", cn.name), slog.String("sender", m.Sender), slog.String("reason", refusal))
		cn.notice(ctx, m.ChatID, refusal)

		return nil
	}

	if !m.IsText || strings.TrimSpace(m.Text) == "" {
		cn.notice(ctx, m.ChatID, noticeTextOnly)

		return nil
	}

	// The owner's reply to a question's card is his answer in his own words, not a message for the Bot.
	if m.Sender == ch.OwnerID && m.ReplyTo != "" {
		if card, err := store.QuestionCardByMessage(ctx, cn.name, m.ReplyTo); err == nil {
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

	var sender *orchestration.ChatSender

	if m.Sender != ch.OwnerID {
		// Someone the owner let in may give work, and is named: the name is a label of the message, not an
		// instruction, as OpenClaw and MemoryOS fence a sender's name.
		name := cn.nameOf(ctx, m.Sender)
		platform := cn.caps.Platform
		text = fmt.Sprintf("[%s message from %s, who is not the owner. Questions for approval go to the owner.]\n%s",
			platform, name, defuseLabels(m.Text, platform))
		title = platform + ": " + name
		sender = &orchestration.ChatSender{ID: m.Sender, Name: name}
	}

	chat, err := store.ChannelChatFor(ctx, cn.name, m.ChatID, m.Sender, title)
	if err != nil {
		return err
	}

	return cn.deliver(ctx, m, chat, text, sender)
}

// inGroup handles a message in a group (issue #246). It is served only when
// the owner switched the group on (OpenClaw's groupPolicy allowlist), the
// message mentions the bot, @all being no mention (OpenClaw, MemoryOS), and
// the Channel's mode lets its sender ask, applied to each sender (Hermes,
// OpenClaw's groupSenderAllowFrom). Anything else is dropped without a word
// and is not recorded (Hermes, MemoryOS). A command needs the mention too
// (OpenClaw).
func (cn *conn) inGroup(ctx context.Context, ch channelstore.Channel, m Message) error {
	store := cn.all.store

	text, mentioned, err := cn.addressed(ctx, m)
	if err != nil || !mentioned {
		return err
	}

	g, err := store.ChannelGroup(ctx, cn.name, m.ChatID)
	if errors.Is(err, orchestration.ErrNotFound) {
		// A group the roster does not hold yet, as the bot was added since the last listing: it is put on the
		// owner's list, off, as MemoryOS lists a group that is not allowed when it is asked there.
		return store.JoinChannelGroup(ctx, cn.name, m.ChatID, "")
	}

	if err != nil || !g.Enabled {
		return err
	}

	if refusal := cn.refusalOf(ctx, ch, m.Sender); refusal != "" {
		cn.all.logger.InfoContext(ctx, "a group message was refused", slog.String("event", "channels.refused"),
			slog.String("channel", cn.name), slog.String("sender", m.Sender), slog.String("reason", refusal))

		return nil
	}

	first, err := store.TakeChannelMessage(ctx, cn.name, m.ID)
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

	title := cn.caps.Platform + ": " + g.ChatID
	if g.Name != "" {
		title = cn.caps.Platform + ": " + g.Name
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

	if g, err = store.ChannelGroup(ctx, cn.name, m.ChatID); err != nil {
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

	chat, err := store.ChannelGroupChatFor(ctx, cn.name, m.ChatID, title)
	if err != nil {
		return err
	}

	if err := store.AskedInChannelGroup(ctx, cn.name, m.ChatID); err != nil {
		return err
	}

	// Every message, the owner's too, opens with its sender's name, which the group's prompt says is the only
	// label Hypomnema adds (MemoryOS, ChannelInboundService.asked). A member who is not the owner is recorded as
	// its sender (issue #468), so the group's chat knows whose turn it is.
	name := cn.nameOf(ctx, m.Sender)

	var sender *orchestration.ChatSender
	if m.Sender != ch.OwnerID {
		sender = &orchestration.ChatSender{ID: m.Sender, Name: name}
	}

	return cn.deliver(ctx, m, chat, "["+name+"]: "+defuseLabels(text, cn.caps.Platform), sender)
}

// addressed is a group message's text without the account's own mentions,
// and whether it mentions the account. Another person's mention stays, by
// name rather than by the placeholder the service writes for it (MemoryOS,
// LarkChannelAdapter.withoutBotMentions). A mention of everyone names
// nobody, so it is not one of the account.
func (cn *conn) addressed(ctx context.Context, m Message) (string, bool, error) {
	bot, err := cn.self(ctx)
	if err != nil {
		return "", false, err
	}

	mentioned := false
	mentions := make([]Mention, 0, len(m.Mentions))

	for _, at := range m.Mentions {
		if at.PersonID != "" && at.PersonID == bot {
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
		case at.PersonID != "" && at.PersonID == bot:
			pairs = append(pairs, at.Key, "")
		case at.Name != "":
			pairs = append(pairs, at.Key, "@"+at.Name)
		}
	}

	return strings.TrimSpace(strings.NewReplacer(pairs...).Replace(m.Text)), mentioned, nil
}

// turnOf is what lets one message of a group at a time choose the group's
// chat.
func (cn *conn) turnOf(externalChatID string) *sync.Mutex {
	cn.mu.Lock()
	defer cn.mu.Unlock()

	turn := cn.turns[externalChatID]
	if turn == nil {
		turn = &sync.Mutex{}
		cn.turns[externalChatID] = turn
	}

	return turn
}

// self is the account's own id, read from the service the first time it is
// needed and kept; a failed read is tried again with the next message.
func (cn *conn) self(ctx context.Context) (string, error) {
	cn.mu.Lock()
	id := cn.selfID
	cn.mu.Unlock()

	if id != "" {
		return id, nil
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	id, err := cn.conn.Self(ctx)
	if err != nil {
		return "", err
	}

	cn.mu.Lock()
	cn.selfID = id
	cn.mu.Unlock()

	return id, nil
}

// deliver gives a person's message to the Bot of its chat and marks it as
// being worked on. sender is who wrote it when it is not the Channel's owner.
func (cn *conn) deliver(ctx context.Context, m Message, chat channelstore.ChannelChat, text string,
	sender *orchestration.ChatSender,
) error {
	store := cn.all.store

	cn.mu.Lock()
	r := cn.relayLocked(ctx, chat)
	cn.mu.Unlock()

	busy := cn.all.coord.Working(chat.ChatID)

	// A press of the menu has no message to quote or mark as being worked on.
	if !m.Menu {
		r.awaits(m.ID)
	}

	// A message written while pi works waits in the chat's queue until the turn settles (issue #497).
	if _, _, err := store.SendOwnerMessage(ctx, chat.ChatID,
		orchestration.NewChatMessage{Role: orchestration.ChatOwner, Text: text, Sender: sender}, busy); err != nil {
		r.forget(m.ID)

		return err
	}

	switch {
	case !cn.all.coord.Enabled():
		// Nothing will answer it, so no reply quotes it.
		r.forget(m.ID)
		cn.notice(ctx, m.ChatID, fmt.Sprintf(noticeNoAnswer, botName(ctx, store, chat.Bot)))

		return nil
	case busy:
		// The message waits for the turn that is running, and the person is told, as Hermes tells one who
		// writes while it works.
		cn.notice(ctx, m.ChatID, fmt.Sprintf(noticeQueued, botName(ctx, store, chat.Bot)))
	}

	if !m.Menu {
		r.asked(ctx, m.ID)
	}

	return nil
}

// startOver is /new: the next message in the service's chat starts a new chat with
// the Channel's Bot, titled title.
func (cn *conn) startOver(ctx context.Context, m Message, title string) error {
	if err := cn.restart(ctx, m.ChatID, title); err != nil {
		return err
	}

	cn.notice(ctx, m.ChatID, noticeNew)

	return nil
}

// restart gives a chat at the service a new chat with the Channel's Bot, and
// relays that one. A chat at the service with no chat yet needs none.
func (cn *conn) restart(ctx context.Context, externalChatID, title string) error {
	chat, err := cn.all.store.ResetChannelChat(ctx, cn.name, externalChatID, title)
	if errors.Is(err, orchestration.ErrNotFound) {
		return nil
	}

	if err != nil {
		return err
	}

	cn.mu.Lock()
	if old := cn.relays[externalChatID]; old != nil {
		old.stop()
		delete(cn.relays, externalChatID)
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
func (cn *conn) refusal(ctx context.Context, ch channelstore.Channel, m Message) string {
	// A custom app belongs to one organization, which every event names (MemoryOS, rule 2). In a group the
	// check is not made: there everyone is every member of the group (owner, 2026-11-12).
	if m.SenderOrg == "" || m.SenderOrg != m.Org {
		return fmt.Sprintf(noticeOutsider, cn.caps.Platform)
	}

	return cn.refusalOf(ctx, ch, m.Sender)
}

// refusalOf says why the Channel's mode does not let a sender ask, or
// nothing when it does: the same choice in a direct message and, for each
// sender, in a group.
func (cn *conn) refusalOf(ctx context.Context, ch channelstore.Channel, sender string) string {
	if sender == ch.OwnerID {
		return ""
	}

	switch ch.Mode {
	case channelstore.ChannelEveryone:
		return ""
	case channelstore.ChannelAllowlist:
		if _, err := cn.all.store.ChannelPerson(ctx, cn.name, sender); err == nil {
			return ""
		}

		return noticeAllowlist
	default:
		return noticeOwnerOnly
	}
}

// nameOf is how a sender is named to the Bot: the name the owner added them
// under, else their name at the service, else their id. It is a label, so it
// is cut to a plain name that cannot pass for an instruction or end the
// label.
func (cn *conn) nameOf(ctx context.Context, personID string) string {
	if p, err := cn.all.store.ChannelPerson(ctx, cn.name, personID); err == nil {
		if name := speaker(p.Name); name != "" {
			return name
		}
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	if name, err := cn.conn.NameOf(ctx, personID); err == nil {
		if name := speaker(name); name != "" {
			return name
		}
	}

	return speaker(personID)
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

	if err := cn.conn.Notice(ctx, chatID, text); err != nil {
		cn.all.logger.WarnContext(ctx, "a notice was not sent", slog.String("event", "channels.notice_failed"),
			slog.String("channel", cn.name), slog.Any("error", err))
	}
}

// TellOwner sends the owner a card through every Channel of a Bot that is on
// and connected: what a Routine of the Bot has to tell him when nobody is in
// a chat (issue #211; OpenClaw sends a failure alert to the job's owner). A
// Channel through which he already reads the chat named is left out, so he
// is told once.
func (c *Channels) TellOwner(ctx context.Context, bot, chatID, title, text string) {
	channels, err := c.store.ListChannels(ctx)
	if err != nil {
		c.logger.WarnContext(ctx, "the channels were not read", slog.String("event", "channels.channels_failed"),
			slog.Any("error", err))

		return
	}

	for _, ch := range channels {
		c.mu.Lock()
		cn := c.conns[ch.Name]
		c.mu.Unlock()

		if ch.Bot != bot || !ch.Enabled || cn == nil || ch.OwnerID == "" || cn.reads(ctx, chatID) {
			continue
		}

		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		_, err := cn.conn.SendQuestion(sendCtx, ch.OwnerID, Card{Title: title, Tone: ToneAlert, Text: text})

		cancel()

		if err != nil {
			c.logger.WarnContext(ctx, "the owner was not told", slog.String("event", "channels.notice_failed"),
				slog.String("channel", ch.Name), slog.Any("error", err))
		}
	}
}

// reads reports whether a chat is one of the Channel's: what is said in it
// reaches the service already.
func (cn *conn) reads(ctx context.Context, chatID string) bool {
	if chatID == "" {
		return false
	}

	chats, err := cn.all.store.ChannelChats(ctx, cn.name)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(chats, func(c channelstore.ChannelChat) bool { return c.ChatID == chatID })
}

// relayLocked starts sending a chat's replies to its chat at the service,
// once, and returns what sends them.
func (cn *conn) relayLocked(ctx context.Context, chat channelstore.ChannelChat) *relay {
	if r, running := cn.relays[chat.ExternalChatID]; running {
		return r
	}

	ctx, stop := context.WithCancel(ctx)
	r := &relay{all: cn.all, cn: cn, conn: cn.conn, chat: chat, sent: chat.SentSeq, stop: stop}
	cn.relays[chat.ExternalChatID] = r

	go r.run(ctx)

	return r
}

// relay sends what the Bot says in one chat to its chat at the service. The
// text as it is written comes from the supervisor's live frames and fills a
// reply; the kept message that follows is what the reply ends as, so a frame
// that was lost changes nothing (the web client is put right the same way).
type relay struct {
	all  *Channels
	cn   *conn
	conn Conn
	chat channelstore.ChannelChat
	stop context.CancelFunc
	// sent is the last kept message that was sent.
	sent int64
	// frames is the Bot's live frames of the chat.
	frames <-chan coordinator.Frame

	// The reply being written, the text the Bot wrote, what a tool that runs
	// is doing, what the reply shows and when it was last updated.
	reply   Reply
	text    strings.Builder
	status  string
	shown   string
	updated time.Time

	mu sync.Mutex
	// waiting are the person's messages that have no reply yet, with what
	// takes off the mark that says the Bot works on each.
	waiting []waiting
}

// stopped closes the reply of a turn the person stopped: with the text it had
// and a line that says so. With no reply and no text the notice that answered
// /stop has said it.
func (r *relay) stopped(ctx context.Context, text string) {
	text = strings.TrimSpace(text)

	switch {
	case text != "":
		text += "\n\n" + noticeStopped
	case r.reply != nil:
		text = noticeStopped
	}

	r.finish(ctx, text)
}

// waiting is a message of a person the Bot has not answered.
type waiting struct {
	message string
	unmark  func(context.Context) error
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

	// A mark that cannot be put (the app lacks the scope) costs the person only the mark.
	unmark, err := r.conn.Mark(ctx, messageID, MarkWorking)
	if err != nil {
		r.failed(ctx, "marking a message as being worked on", err)

		return
	}

	r.mu.Lock()

	kept := false

	for i := range r.waiting {
		if r.waiting[i].message == messageID {
			r.waiting[i].unmark, kept = unmark, true

			break
		}
	}

	r.mu.Unlock()

	if kept {
		return
	}

	if err := unmark(ctx); err != nil {
		r.failed(ctx, "taking a mark off a message", err)
	}
}

// forget drops a message that was never given to the Bot.
func (r *relay) forget(messageID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.waiting = slices.DeleteFunc(r.waiting, func(w waiting) bool { return w.message == messageID })
}

// replyTo is the oldest message that waits for a reply, which the next reply
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
		if w.unmark != nil {
			if err := w.unmark(ctx); err != nil {
				r.failed(ctx, "taking a mark off a message", err)
			}
		}

		if failed && i == len(waited)-1 {
			if _, err := r.conn.Mark(ctx, w.message, MarkFailed); err != nil {
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
// that a reply still open is closed soon if no kept one comes.
func (r *relay) frame(ctx context.Context, f coordinator.Frame) bool {
	data, _ := f.Data.(map[string]any)

	switch f.Event {
	case coordinator.FrameReset:
		// A new reply begins. The coordinator keeps a reply before it begins the next one, so the reply being
		// written ends with its kept message and what goes under it (its artifacts), not with what was written:
		// ended so, the kept reply would come again as a reply of its own (#443). A reply that shows only what a
		// tool did is kept for the next one.
		if strings.TrimSpace(r.text.String()) != "" {
			r.keep(ctx)
		}

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
		// What a tool that runs is doing, under the text, as Hermes and OpenClaw show progress, as much as the Bot's
		// tool display shows (issue #441): nothing when off, its name when on, the start of its arguments when full.
		// A setting that cannot be read shows nothing.
		display, err := r.all.store.ChatToolDisplay(ctx, r.chat.ChatID)
		if err != nil {
			display = orchestration.ToolsOff
		}

		shown, ok := coordinator.ShownFrame(display, f)
		if !ok {
			return false
		}

		data, _ = shown.Data.(map[string]any)
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

	return "⚡ " + name + ": " + preview
}

// show brings the reply up to what the Bot wrote and is doing: it
// opens the reply with the first thing to show, and updates it at most twice
// a second unless now is set.
func (r *relay) show(ctx context.Context, now bool) {
	written, _ := mediaOf(r.text.String())
	body := cut(written, r.cn.caps.MaxText)

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

	if r.reply == nil {
		reply, sentTo, err := r.conn.OpenReply(ctx, r.chat.ExternalChatID, r.replyTo(), body)
		if err != nil {
			r.failed(ctx, "opening a reply", err)

			return
		}

		r.named(ctx, sentTo)

		r.reply, r.shown, r.updated = reply, body, r.all.now()

		return
	}

	if !now && r.all.now().Sub(r.updated) < updateEvery {
		return
	}

	if err := r.reply.Update(ctx, body); err != nil {
		r.failed(ctx, "updating a reply", err)
	}

	r.shown, r.updated = body, r.all.now()
}

// kept sends the Bot's kept messages that were not sent yet: each
// ends the reply being written, or is a reply of its own when none is.
func (r *relay) kept(ctx context.Context) {
	for {
		msgs, err := r.all.store.ListChatMessages(ctx, r.chat.ChatID, r.sent, orchestration.ChatPageDefault)
		if err != nil || len(msgs) == 0 {
			return
		}

		// A reply is kept after its last piece was written, but the two arrive on their own channels. Every
		// piece of a reply that was just read is already waiting, so it is taken first: a piece taken after
		// its reply would open a reply nothing closes. A reset among them ends the reply before it with its own
		// kept message (frame), so each reply ends once, with its own text (#443).
		for pending := true; pending; {
			select {
			case f := <-r.frames:
				r.frame(ctx, f)
			default:
				pending = false
			}
		}

		sent := r.sent
		r.send(ctx, slices.DeleteFunc(msgs, func(m orchestration.ChatMessage) bool { return m.Seq <= sent }), false)

		if err := r.all.store.MarkChannelSent(ctx, r.chat.ExternalChatID, r.sent); err != nil {
			r.failed(ctx, "recording what was sent", err)

			return
		}
	}
}

// keep ends the reply being written with its kept message when a reset begins the next reply. That message is
// the first not sent that ends a reply: the coordinator keeps each reply before it begins the next, and the
// ones after it belong to replies whose frames were not read yet.
func (r *relay) keep(ctx context.Context) {
	msgs, err := r.all.store.ListChatMessages(ctx, r.chat.ChatID, r.sent, orchestration.ChatPageDefault)
	if err != nil || len(msgs) == 0 {
		return
	}

	r.send(ctx, msgs, true)

	if err := r.all.store.MarkChannelSent(ctx, r.chat.ExternalChatID, r.sent); err != nil {
		r.failed(ctx, "recording what was sent", err)
	}
}

// send ends a reply, or sends one, for each kept message; with one, it stops after the first that ends a reply.
func (r *relay) send(ctx context.Context, msgs []orchestration.ChatMessage, one bool) {
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		ended := true

		switch {
		case m.Role == orchestration.ChatCoordinator && strings.TrimSpace(m.Text) != "" &&
			i+1 < len(msgs) && msgs[i+1].Kind == orchestration.ChatStopped:
			// What a turn the person stopped had written is kept together with its stopped row (coordinator
			// keepReply): the reply closes with it and says it was stopped.
			r.stopped(ctx, m.Text)
			r.settle(ctx, false)

			i++
			m = msgs[i]
		case m.Role == orchestration.ChatCoordinator && strings.TrimSpace(m.Text) != "":
			r.finish(ctx, m.Text+r.artifacts(ctx, m.Seq))
			r.settle(ctx, false)
		case m.Kind == orchestration.ChatStopped:
			// The server recorded that the person stopped the turn: it was stopped, not failed.
			r.stopped(ctx, r.text.String())
			r.settle(ctx, false)
		case m.Kind == orchestration.ChatContinued:
			// The control plane restarted and the Bot goes on with the turn a restart cut (issue #716): the person
			// is told, and the message the Bot works on still waits for its reply.
			r.line(ctx, m.Text)

			ended = false
		case m.Kind == orchestration.ChatCut:
			// A restart cut the turn and it is not continued: the person is told, and the message is not worked
			// on any more.
			r.line(ctx, m.Text)
			r.settle(ctx, false)

			ended = false
		case m.Kind == orchestration.ChatFailed:
			// The model's call failed: what was written stays, with a line that says why it stops there
			// (MemoryOS), and the message is marked as failed (Hermes).
			partial := strings.TrimSpace(r.text.String())
			if partial != "" {
				partial += "\n\n"
			}

			r.finish(ctx, partial+fmt.Sprintf(noticeNoReply, botName(ctx, r.all.store, r.chat.Bot)))
			r.settle(ctx, true)
		default:
			ended = false
		}

		r.sent = m.Seq

		if one && ended {
			return
		}
	}
}

// artifacts names under a reply the artifacts the Bot showed in the chat since its last reply (issue #402, row
// 12), as OpenMausBot gives a channel without a web view a page's text fallback: each by its title, a link to it on
// the web where the reply may link there. A chat at a service shows no page, so the owner opens it there. "" when
// there are none.
func (r *relay) artifacts(ctx context.Context, seq int64) string {
	list, err := r.all.store.ArtifactsBeforeReply(ctx, r.chat.ChatID, seq)
	if err != nil {
		r.failed(ctx, "reading the chat's artifacts", err)

		return ""
	}

	if len(list) == 0 {
		return ""
	}

	link := r.all.webURL != "" && !r.chat.Group && r.webLink(ctx)

	var b strings.Builder

	b.WriteString("\n")

	for _, a := range list {
		title := strings.NewReplacer("[", "(", "]", ")").Replace(a.Title)
		if link {
			fmt.Fprintf(&b, "\n📎 [%s](%s/artifacts/%s)", title, r.all.webURL, a.ID)
		} else {
			fmt.Fprintf(&b, "\n📎 %s", title)
		}
	}

	return b.String()
}

// webLink says the Channel's replies end with a link to the web client. The
// Channel is read at each reply, so the owner's switch counts from the next
// one; a Channel that cannot be read goes without the link.
func (r *relay) webLink(ctx context.Context) bool {
	ch, err := r.all.store.Channel(ctx, r.chat.Channel)

	return err == nil && ch.WebLink
}

// finish ends the reply being written with its final text, or sends the text
// as a reply of its own. A text longer than a message of the service shows
// goes on in further replies, as OpenClaw and Hermes split one (MemoryOS
// cuts it). The files the text asks for follow it (issue #312). With no
// reply, no text and no file it does nothing.
func (r *relay) finish(ctx context.Context, text string) {
	reply, replyTo := r.reply, r.replyTo()

	r.reply, r.shown, r.status = nil, "", ""
	r.text.Reset()

	text, files := mediaOf(text)
	defer r.attach(ctx, files)

	if strings.TrimSpace(text) == "" {
		switch {
		case reply == nil:
			return
		case len(files) > 0:
			// A reply that showed what a tool did, for one that is only files: it ends naming them.
			names := make([]string, 0, len(files))
			for _, f := range files {
				names = append(names, shownName(f))
			}

			text = strings.Join(names, "\n")
		default:
			text = "(no answer)"
		}
	}

	parts := split(text, r.cn.caps.MaxText)
	// A group's reply has no link: its conversation goes on in the group, and only the owner opens the web
	// client (MemoryOS, ChannelInboundService: a group answer has no closing line). A Channel for other people
	// has it switched off (owner, 2026-11-13).
	if r.all.webURL != "" && !r.chat.Group && r.webLink(ctx) {
		parts[len(parts)-1] += "\n\n[Open in Hypomnema](" + r.all.webURL + "/chats/" + r.chat.ChatID + ")"
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	for i, part := range parts {
		if i > 0 || reply == nil {
			opened, sentTo, err := r.conn.OpenReply(ctx, r.chat.ExternalChatID, replyTo, part)
			if err != nil {
				r.failed(ctx, "sending a reply", err)

				return
			}

			r.named(ctx, sentTo)

			reply, replyTo = opened, ""
		}

		if err := reply.Close(ctx, part); err != nil {
			r.failed(ctx, "closing a reply", err)
		}
	}
}

// line sends a line of the control plane as a message of its own, leaving the reply being written, if any, as it
// is.
func (r *relay) line(ctx context.Context, text string) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	reply, sentTo, err := r.conn.OpenReply(ctx, r.chat.ExternalChatID, "", text)
	if err != nil {
		r.failed(ctx, "sending a line", err)

		return
	}

	r.named(ctx, sentTo)

	if err := reply.Close(ctx, text); err != nil {
		r.failed(ctx, "closing a line", err)
	}
}

// named records the chat at the service a reply was sent to, for a direct
// chat kept under the person's id until the service named it (issue #312: a
// press of the menu names no chat). The chat and its relay go by that id
// from then on.
func (r *relay) named(ctx context.Context, sentTo string) {
	old := r.chat.ExternalChatID
	if r.chat.Group || old != r.chat.PersonID || sentTo == "" || sentTo == old {
		return
	}

	if err := r.all.store.RenameChannelChat(ctx, r.chat.Channel, old, sentTo); err != nil {
		r.failed(ctx, "recording the chat the service named", err)

		return
	}

	r.cn.mu.Lock()
	if r.cn.relays[old] == r {
		delete(r.cn.relays, old)
		r.cn.relays[sentTo] = r
	}

	r.chat.ExternalChatID = sentTo
	r.cn.mu.Unlock()
}

func (r *relay) failed(ctx context.Context, doing string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}

	r.all.logger.WarnContext(ctx, "a channel failed "+doing, slog.String("event", "channels.send_failed"),
		slog.String("channel", r.chat.Channel), slog.String("chat_id", r.chat.ChatID), slog.Any("error", err))
}

// cut is at most a message's worth of a text that is still being written.
func cut(text string, limit int) string {
	r := []rune(text)
	if len(r) <= limit {
		return text
	}

	return strings.TrimSpace(string(r[:limit])) + " …"
}

// split cuts a finished text into parts of at most limit characters, at a
// line's end where there is one in the last fifth of a part.
func split(text string, limit int) []string {
	r := []rune(text)

	var parts []string

	for len(r) > limit {
		at := limit
		for i := limit; i > limit*4/5; i-- {
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

// A question for the owner is a card (issue #136), as OpenClaw's and Hermes'
// approval cards are: each choice is a button, and a reply to the card
// answers in the owner's own words. Only the owner's press counts, by the id
// of who pressed (MemoryOS: the approver is checked by open_id). A card says
// how its question ended, and then has no buttons.

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
			cn.all.logger.WarnContext(ctx, "the owner's questions were not brought to a channel",
				slog.String("event", "channels.questions_failed"), slog.String("channel", cn.name), slog.Any("error", err))
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

	ch, err := store.Channel(ctx, cn.name)
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
		if _, err := store.QuestionCardOf(ctx, q.ID); !errors.Is(err, orchestration.ErrNotFound) {
			continue
		}

		// A question goes out through one Channel: the one its Run's chat came in through, else the first.
		if via, err := store.ChannelForRun(ctx, q.RunID); err != nil || via != cn.name {
			continue
		}

		ref, err := cn.conn.SendQuestion(ctx, ch.OwnerID, questionCard(q))
		if err != nil {
			return fmt.Errorf("sending question %s: %w", q.ID, err)
		}

		if err := store.RecordQuestionCard(ctx, channelstore.QuestionCard{
			QuestionID: q.ID, Channel: cn.name, CardID: ref.ID, MessageID: ref.MessageID,
		}); err != nil {
			return err
		}
	}

	cards, err := store.OpenQuestionCards(ctx, cn.name)
	if err != nil {
		return err
	}

	for _, c := range cards {
		q, err := store.GetQuestion(ctx, c.QuestionID)
		if err != nil || q.Status == orchestration.QuestionOpen {
			continue
		}

		ref, err := cn.conn.ReplaceQuestion(ctx, QuestionRef{ID: c.CardID, MessageID: c.MessageID, Seq: c.Seq},
			endedCard(q))
		if err != nil {
			return fmt.Errorf("closing the card of question %s: %w", q.ID, err)
		}

		if err := store.CloseQuestionCard(ctx, c.QuestionID, ref.Seq); err != nil {
			return err
		}
	}

	return nil
}

// questionCard is a question as the owner sees it.
func questionCard(q orchestration.Question) Card {
	card := Card{Title: questionTitle, Tone: ToneAsking, Text: q.Text}

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
	card := Card{Title: questionTitle, Tone: ToneAnswered, Text: q.Text}

	switch {
	case q.Status == orchestration.QuestionCanceled:
		card.Tone = ToneAlert
		card.Note = "Canceled: " + q.CancelReason
	case q.Answer != nil:
		var parts []string

		if q.Answer.Option != nil && *q.Answer.Option >= 0 && *q.Answer.Option < len(q.Options) {
			parts = append(parts, q.Options[*q.Answer.Option].Label)
		}

		if strings.TrimSpace(q.Answer.Text) != "" {
			parts = append(parts, q.Answer.Text)
		}

		card.Note = fmt.Sprintf("Answered by the %s: %s", q.AnsweredBy, strings.Join(parts, " — "))
	default:
		card.Tone = ToneClosed
		card.Note = "Closed."
	}

	return card
}

// press handles a press of a card's button and returns what the person who
// pressed is told. It is called from the connection, which must answer the
// service soon (Lark: within three seconds); an answer is one write.
func (cn *conn) press(ctx context.Context, p Press) string {
	ch, err := cn.all.store.Channel(ctx, cn.name)
	if err != nil || !ch.Enabled {
		return ""
	}

	if p.Operator != ch.OwnerID {
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
		cn.all.logger.InfoContext(ctx, "an answer from a channel was not taken",
			slog.String("event", "channels.answer_refused"),
			slog.String("channel", cn.name), slog.String("question_id", questionID), slog.Any("error", err))

		return "That answer was not taken: " + err.Error()
	}

	return answered
}
