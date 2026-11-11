// Package larkbot is the Lark bot the owner gives work through (issue #136):
// a Channel. A person's direct message to the bot at Lark becomes a message
// of a chat with the coordinator, and what the coordinator says in that chat
// is sent back as a card that fills as the text is written.
//
// It follows the Feishu channels of OpenClaw (extensions/feishu) and Hermes
// (plugins/platforms/feishu) and the owner's MemoryOS (mem-246-lark-bot):
// events come over a persistent connection, so no public address is opened;
// a delivery repeated by Lark is dropped by the message's id; who may talk to
// the bot is closed by default.
package larkbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Message is a message a person sent, as the bot needs it.
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
}

// Lark is what the bot does at Lark. The SDK is behind it (lark.go).
type Lark interface {
	// Listen opens the persistent connection and gives each message to take.
	// It returns when the connection cannot be opened; an open connection
	// stays for the life of the process.
	Listen(ctx context.Context, take func(Message)) error
	// SendText sends a short notice to a chat.
	SendText(ctx context.Context, chatID, text string) error
	// OpenCard sends a card that is still being written and returns its id.
	OpenCard(ctx context.Context, chatID, text string) (cardID string, err error)
	// UpdateCard replaces the card's text; seq rises with each call.
	UpdateCard(ctx context.Context, cardID, text string, seq int) error
	// CloseCard sets the card's final text and ends its writing.
	CloseCard(ctx context.Context, cardID, text string, seq int) error
	// OpenIDOf finds a member of the app's organization by email.
	OpenIDOf(ctx context.Context, email string) (string, error)
	// NameOf reads a member's name.
	NameOf(ctx context.Context, openID string) (string, error)
}

// Store is what the bot keeps (orchestration.Service).
type Store interface {
	LarkBot(ctx context.Context) (orchestration.LarkBot, error)
	LarkBotPerson(ctx context.Context, openID string) (orchestration.LarkBotPerson, error)
	TakeLarkMessage(ctx context.Context, messageID string) (bool, error)
	ForgetLarkMessagesBefore(ctx context.Context, before time.Time) error
	LarkBotChatFor(ctx context.Context, larkChatID, openID, title string) (orchestration.LarkBotChat, error)
	LarkBotChats(ctx context.Context) ([]orchestration.LarkBotChat, error)
	MarkLarkBotSent(ctx context.Context, larkChatID string, seq int64) error
	AddChatMessage(ctx context.Context, chatID string, role orchestration.ChatRole, text string,
	) (orchestration.ChatMessage, error)
	ListChatMessages(ctx context.Context, chatID string, after int64, limit int) ([]orchestration.ChatMessage, error)
	Subscribe(ctx context.Context) <-chan struct{}
}

// Coordinator is the part of the coordinator's supervisor the bot uses.
type Coordinator interface {
	Enabled() bool
	Watch(chatID string) (frames <-chan coordinator.Frame, stop func())
}

// What the bot says in its own voice. English, as the web client is.
const (
	noticeOwnerOnly = "This bot takes messages from its owner only."
	noticeAllowlist = "This bot takes messages from its owner and the people he added."
	noticeOutsider  = "This bot serves members of its Lark organization only."
	noticeTextOnly  = "This bot reads text messages only for now."
	noticeNoAnswer  = "The coordinator is not running, so nothing will answer. Your message is kept."
	noticeFailed    = "This message could not be taken. Try again."
	writing         = "…"
)

const (
	// cardEvery is the least time between two updates of a card: Lark allows
	// five a second on one card (MemoryOS, from Lark's limits).
	cardEvery = 500 * time.Millisecond
	// closeAfter is how long a card waits for its kept message once the
	// coordinator stopped working, before it is closed with what was shown.
	closeAfter = 5 * time.Second
	// forgetAfter is how long a taken message is remembered: Lark's last
	// delivery of an event comes about seven hours after the first.
	forgetAfter = 24 * time.Hour
	// retryEvery is how often a bot that is not set up, or could not connect,
	// is looked at again.
	retryEvery  = 30 * time.Second
	sendTimeout = 30 * time.Second
)

// Bot is the running bot.
type Bot struct {
	store  Store
	coord  Coordinator
	open   func(ctx context.Context, bot orchestration.LarkBot) (Lark, error)
	logger *slog.Logger
	now    func() time.Time

	mu sync.Mutex
	// lark is the connection in use; relays the Lark chats being answered.
	lark      Lark
	appID     string
	connected bool
	lastError string
	relays    map[string]struct{}
}

// New returns a bot. open makes the bot's connection to Lark from what the
// owner set up; it is where the app's secret is read.
func New(store Store, coord Coordinator, open func(context.Context, orchestration.LarkBot) (Lark, error),
	logger *slog.Logger,
) *Bot {
	return &Bot{store: store, coord: coord, open: open, logger: logger, now: time.Now, relays: map[string]struct{}{}}
}

// State is whether the bot is connected to Lark.
type State struct {
	// Connected says the persistent connection was opened; AppID is the app
	// it was opened as, which a later change of the bot's app does not
	// replace until the control plane starts again.
	Connected bool
	AppID     string
	// LastError is why the last attempt to connect failed.
	LastError string
}

// State returns the bot's state.
func (b *Bot) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()

	return State{Connected: b.connected, AppID: b.appID, LastError: b.lastError}
}

// Run connects the bot once it is set up and switched on, and keeps it. A bot
// that is not set up yet is looked at again every half minute, so setting one
// up needs no restart. Lark's SDK cannot close a persistent connection, so a
// connection once opened stays until the process ends: switching the bot off
// makes it take no message, and another app is used from the next start.
func (b *Bot) Run(ctx context.Context) error {
	for {
		if b.start(ctx) {
			break
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retryEvery):
		}
	}

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := b.store.ForgetLarkMessagesBefore(ctx, b.now().Add(-forgetAfter)); err != nil {
				b.logger.WarnContext(ctx, "old Lark messages were not forgotten", slog.String("event", "larkbot.forget_failed"),
					slog.Any("error", err))
			}
		}
	}
}

// start opens the connection when the bot is set up and on, and says whether
// it did.
func (b *Bot) start(ctx context.Context) bool {
	bot, err := b.store.LarkBot(ctx)
	if err != nil || !bot.Enabled {
		return false
	}

	lark, err := b.open(ctx, bot)
	if err == nil {
		err = lark.Listen(ctx, func(m Message) { b.Take(ctx, m) })
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if err != nil {
		b.lastError = err.Error()
		b.logger.WarnContext(ctx, "the Lark bot did not connect", slog.String("event", "larkbot.connect_failed"),
			slog.Any("error", err))

		return false
	}

	b.lark, b.appID, b.connected, b.lastError = lark, bot.AppID, true, ""
	b.logger.InfoContext(ctx, "the Lark bot is connected", slog.String("event", "larkbot.connected"),
		slog.String("app_id", bot.AppID))

	chats, err := b.store.LarkBotChats(ctx)
	if err != nil {
		b.logger.WarnContext(ctx, "the Lark bot's chats were not read", slog.String("event", "larkbot.chats_failed"),
			slog.Any("error", err))
	}

	for _, c := range chats {
		b.relayLocked(ctx, lark, c)
	}

	return true
}

// Take handles one message from Lark. It is called from the connection, which
// must acknowledge the event within three seconds, so it does its work on its
// own goroutine.
func (b *Bot) Take(ctx context.Context, m Message) {
	b.mu.Lock()
	lark := b.lark
	b.mu.Unlock()

	if lark == nil {
		return
	}

	go func() {
		if err := b.take(ctx, lark, m); err != nil {
			b.logger.WarnContext(ctx, "a Lark message was not taken", slog.String("event", "larkbot.take_failed"),
				slog.String("message_id", m.ID), slog.Any("error", err))
			b.notice(ctx, lark, m.ChatID, noticeFailed)
		}
	}()
}

func (b *Bot) take(ctx context.Context, lark Lark, m Message) error {
	bot, err := b.store.LarkBot(ctx)
	if err != nil || !bot.Enabled {
		return nil //nolint:nilerr // A bot that was switched off or removed takes nothing and says nothing.
	}

	// Only what a person wrote to the bot directly: a group is not served yet, and another app's message is
	// never an instruction.
	if !m.FromPerson || !m.Direct || m.ID == "" || m.ChatID == "" {
		return nil
	}

	first, err := b.store.TakeLarkMessage(ctx, m.ID)
	if err != nil || !first {
		return err
	}

	if refusal := b.refusal(ctx, bot, m); refusal != "" {
		b.logger.InfoContext(ctx, "a Lark message was refused", slog.String("event", "larkbot.refused"),
			slog.String("sender", m.Sender), slog.String("reason", refusal))
		b.notice(ctx, lark, m.ChatID, refusal)

		return nil
	}

	if !m.IsText || strings.TrimSpace(m.Text) == "" {
		b.notice(ctx, lark, m.ChatID, noticeTextOnly)

		return nil
	}

	text, title := m.Text, ""
	if m.Sender != bot.OwnerOpenID {
		// Someone the owner let in may give work, and is named: the name is a label of the message, not an
		// instruction, as OpenClaw and MemoryOS fence a sender's name.
		name := b.nameOf(ctx, lark, m.Sender)
		text = fmt.Sprintf("[Lark message from %s, who is not the owner. Questions for approval go to the owner.]\n%s",
			name, m.Text)
		title = "Lark: " + name
	}

	chat, err := b.store.LarkBotChatFor(ctx, m.ChatID, m.Sender, title)
	if err != nil {
		return err
	}

	b.mu.Lock()
	b.relayLocked(ctx, lark, chat)
	b.mu.Unlock()

	if _, err := b.store.AddChatMessage(ctx, chat.ChatID, orchestration.ChatOwner, text); err != nil {
		return err
	}

	if !b.coord.Enabled() {
		b.notice(ctx, lark, m.ChatID, noticeNoAnswer)
	}

	return nil
}

// refusal says why a sender may not talk to the bot, or nothing when they
// may.
func (b *Bot) refusal(ctx context.Context, bot orchestration.LarkBot, m Message) string {
	// A custom app belongs to one organization, which every event names (MemoryOS, rule 2).
	if m.SenderOrg == "" || m.SenderOrg != m.Org {
		return noticeOutsider
	}

	if m.Sender == bot.OwnerOpenID {
		return ""
	}

	switch bot.Mode {
	case orchestration.LarkBotEveryone:
		return ""
	case orchestration.LarkBotAllowlist:
		if _, err := b.store.LarkBotPerson(ctx, m.Sender); err == nil {
			return ""
		}

		return noticeAllowlist
	default:
		return noticeOwnerOnly
	}
}

// nameOf is how a sender who is not the owner is named to the coordinator:
// the name the owner added them under, else their name at Lark, else their
// id.
func (b *Bot) nameOf(ctx context.Context, lark Lark, openID string) string {
	if p, err := b.store.LarkBotPerson(ctx, openID); err == nil && p.Name != "" {
		return p.Name
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	if name, err := lark.NameOf(ctx, openID); err == nil && name != "" {
		return name
	}

	return openID
}

func (b *Bot) notice(ctx context.Context, lark Lark, chatID, text string) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	if err := lark.SendText(ctx, chatID, text); err != nil {
		b.logger.WarnContext(ctx, "a notice was not sent to Lark", slog.String("event", "larkbot.notice_failed"),
			slog.Any("error", err))
	}
}

// relayLocked starts sending a chat's replies to its Lark chat, once.
func (b *Bot) relayLocked(ctx context.Context, lark Lark, chat orchestration.LarkBotChat) {
	if _, running := b.relays[chat.LarkChatID]; running {
		return
	}

	b.relays[chat.LarkChatID] = struct{}{}

	go (&relay{bot: b, lark: lark, chat: chat, sent: chat.SentSeq}).run(ctx)
}

// relay sends what the coordinator says in one chat to its Lark chat. The
// text as it is written comes from the supervisor's live frames and fills a
// card; the kept message that follows is what the card ends as, so a frame
// that was lost changes nothing (the web client is put right the same way).
type relay struct {
	bot  *Bot
	lark Lark
	chat orchestration.LarkBotChat
	// sent is the last kept message that was sent.
	sent int64
	// frames is the coordinator's live frames of the chat.
	frames <-chan coordinator.Frame

	// The card being written: its id, the text shown, when it was last
	// updated and its next sequence.
	card    string
	text    strings.Builder
	shown   string
	updated time.Time
	seq     int
}

func (r *relay) run(ctx context.Context) {
	frames, stop := r.bot.coord.Watch(r.chat.ChatID)
	defer stop()

	r.frames = frames

	wake := r.bot.store.Subscribe(ctx)

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

			r.close(ctx, r.text.String())
		}
	}
}

// frame handles one live frame and says whether the card being written
// should be closed soon: the coordinator stopped and its reply may not come.
func (r *relay) frame(ctx context.Context, f coordinator.Frame) bool {
	switch f.Event {
	case coordinator.FrameReset:
		r.close(ctx, r.text.String())
	case coordinator.FrameDelta:
		r.delta(ctx, f.Data)
	case coordinator.FrameState:
		data, ok := f.Data.(map[string]any)

		return ok && data["working"] == false && r.card != ""
	}

	return false
}

// delta adds text the coordinator is writing to the card, opening it with the
// first text and updating it at most twice a second.
func (r *relay) delta(ctx context.Context, data any) {
	d, ok := data.(map[string]any)
	if !ok || d["kind"] != "text" {
		return
	}

	added, _ := d["delta"].(string)
	r.text.WriteString(added)

	text := r.text.String()
	if strings.TrimSpace(text) == "" {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	if r.card == "" {
		id, err := r.lark.OpenCard(ctx, r.chat.LarkChatID, text+writing)
		if err != nil {
			r.failed(ctx, "opening a card", err)

			return
		}

		r.card, r.shown, r.updated, r.seq = id, text, r.bot.now(), 1

		return
	}

	if r.bot.now().Sub(r.updated) < cardEvery {
		return
	}

	if err := r.lark.UpdateCard(ctx, r.card, text+writing, r.seq); err != nil {
		r.failed(ctx, "updating a card", err)
	}

	r.shown, r.updated = text, r.bot.now()
	r.seq++
}

// kept sends the coordinator's kept messages that were not sent yet: each
// ends the card being written, or is a card of its own when none is.
func (r *relay) kept(ctx context.Context) {
	for {
		msgs, err := r.bot.store.ListChatMessages(ctx, r.chat.ChatID, r.sent, orchestration.ChatPageDefault)
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

		for _, m := range msgs {
			if m.Role == orchestration.ChatCoordinator && strings.TrimSpace(m.Text) != "" {
				r.close(ctx, m.Text)
			}

			r.sent = m.Seq
		}

		if err := r.bot.store.MarkLarkBotSent(ctx, r.chat.LarkChatID, r.sent); err != nil {
			r.failed(ctx, "recording what was sent", err)

			return
		}
	}
}

// close ends the card being written with its final text, or sends the text as
// a card of its own; with no card and no text it does nothing.
func (r *relay) close(ctx context.Context, text string) {
	card := r.card
	seq := r.seq

	r.card, r.shown, r.seq = "", "", 0
	r.text.Reset()

	if strings.TrimSpace(text) == "" {
		if card == "" {
			return
		}

		text = "(no answer)"
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	if card == "" {
		id, err := r.lark.OpenCard(ctx, r.chat.LarkChatID, text)
		if err != nil {
			r.failed(ctx, "sending a reply", err)

			return
		}

		card, seq = id, 1
	}

	if err := r.lark.CloseCard(ctx, card, text, seq); err != nil {
		r.failed(ctx, "closing a card", err)
	}
}

func (r *relay) failed(ctx context.Context, doing string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}

	r.bot.logger.WarnContext(ctx, "the Lark bot failed "+doing, slog.String("event", "larkbot.send_failed"),
		slog.String("chat_id", r.chat.ChatID), slog.Any("error", err))
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
