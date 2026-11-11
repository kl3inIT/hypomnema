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
	"regexp"
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
	// ReplyTo is the message this one replies to, when it is a reply.
	ReplyTo string
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

// Lark is what the bot does at Lark. The SDK is behind it (lark.go).
type Lark interface {
	// Listen opens the persistent connection and gives each message to take
	// and each press of a card's button to press, whose answer is shown to
	// who pressed. It returns when the connection cannot be opened; an open
	// connection stays for the life of the process.
	Listen(ctx context.Context, take func(Message), press func(Press) string) error
	// SendCard sends a card to a person and returns its id and its message's.
	SendCard(ctx context.Context, openID string, card Card) (cardID, messageID string, err error)
	// ReplaceCard replaces a card sent with SendCard; seq rises with each call.
	ReplaceCard(ctx context.Context, cardID string, card Card, seq int) error
	// SendText sends a short notice to a chat.
	SendText(ctx context.Context, chatID, text string) error
	// OpenCard sends a card that is still being written and returns its id.
	// With replyTo it is a reply to that message, which Lark shows quoted.
	OpenCard(ctx context.Context, chatID, replyTo, text string) (cardID string, err error)
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

// Store is what the bot keeps (orchestration.Service).
type Store interface {
	LarkBot(ctx context.Context) (orchestration.LarkBot, error)
	LarkBotPerson(ctx context.Context, openID string) (orchestration.LarkBotPerson, error)
	TakeLarkMessage(ctx context.Context, messageID string) (bool, error)
	ForgetLarkMessagesBefore(ctx context.Context, before time.Time) error
	LarkBotChatFor(ctx context.Context, larkChatID, openID, title string) (orchestration.LarkBotChat, error)
	LarkBotChats(ctx context.Context) ([]orchestration.LarkBotChat, error)
	ResetLarkBotChat(ctx context.Context, larkChatID, title string) (orchestration.LarkBotChat, error)
	MarkLarkBotSent(ctx context.Context, larkChatID string, seq int64) error
	ListQuestions(ctx context.Context, f orchestration.QuestionFilter) ([]orchestration.Question, error)
	GetQuestion(ctx context.Context, id string) (orchestration.Question, error)
	AnswerQuestion(ctx context.Context, id string, by orchestration.Addressee, a orchestration.Answer,
	) (orchestration.Question, error)
	RecordLarkQuestionCard(ctx context.Context, c orchestration.LarkQuestionCard) error
	LarkQuestionCardOf(ctx context.Context, questionID string) (orchestration.LarkQuestionCard, error)
	LarkQuestionCardByMessage(ctx context.Context, messageID string) (orchestration.LarkQuestionCard, error)
	OpenLarkQuestionCards(ctx context.Context) ([]orchestration.LarkQuestionCard, error)
	CloseLarkQuestionCard(ctx context.Context, questionID string, seq int) error
	AddChatMessage(ctx context.Context, chatID string, role orchestration.ChatRole, text string,
	) (orchestration.ChatMessage, error)
	ListChatMessages(ctx context.Context, chatID string, after int64, limit int) ([]orchestration.ChatMessage, error)
	Subscribe(ctx context.Context) <-chan struct{}
}

// Coordinator is the part of the coordinator's supervisor the bot uses.
type Coordinator interface {
	Enabled() bool
	Working(chatID string) bool
	Abort(ctx context.Context, chatID string) error
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
	noticeQueued    = "⏳ Queued: the coordinator is still working on the message before."
	noticeNew       = "A new conversation has started."
	noticeStopped   = "Stopped."
	noticeIdle      = "Nothing is running."
	noticeNoReply   = "The coordinator could not answer this. Try again."
	writing         = "…"
)

// The commands a person can type, as OpenClaw, Hermes and MemoryOS have /new
// and Hermes /stop.
const (
	commandNew  = "/new"
	commandStop = "/stop"
)

// The emoji the bot puts on a person's message, as OpenClaw and Hermes do:
// Typing while the coordinator works on it, taken off when the reply is
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
	// coordinator stopped working, before it is closed with what was shown.
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
	relays    map[string]*relay
	// webURL is the web client's address, for the link under a reply.
	webURL string
}

// WithWebURL sets the web client's address; a reply then ends with a link to
// its chat there (MemoryOS's "Continue in MemoryOS", after Dust).
func (b *Bot) WithWebURL(url string) *Bot {
	b.webURL = strings.TrimSuffix(url, "/")

	return b
}

// New returns a bot. open makes the bot's connection to Lark from what the
// owner set up; it is where the app's secret is read.
func New(store Store, coord Coordinator, open func(context.Context, orchestration.LarkBot) (Lark, error),
	logger *slog.Logger,
) *Bot {
	return &Bot{store: store, coord: coord, open: open, logger: logger, now: time.Now, relays: map[string]*relay{}}
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
		err = lark.Listen(ctx, func(m Message) { b.Take(ctx, m) }, func(p Press) string { return b.Press(ctx, p) })
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

	go b.questions(ctx, lark)

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

	// The owner's reply to a question's card is his answer in his own words, not a message for the coordinator.
	if m.Sender == bot.OwnerOpenID && m.ReplyTo != "" {
		if card, err := b.store.LarkQuestionCardByMessage(ctx, m.ReplyTo); err == nil {
			b.notice(ctx, lark, m.ChatID, b.answer(ctx, card.QuestionID, orchestration.Answer{Text: m.Text}))

			return nil
		}
	}

	switch strings.ToLower(strings.TrimSpace(m.Text)) {
	case commandNew:
		return b.startOver(ctx, lark, m)
	case commandStop:
		return b.stop(ctx, lark, m)
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
	r := b.relayLocked(ctx, lark, chat)
	b.mu.Unlock()

	busy := b.coord.Working(chat.ChatID)

	if _, err := b.store.AddChatMessage(ctx, chat.ChatID, orchestration.ChatOwner, text); err != nil {
		return err
	}

	switch {
	case !b.coord.Enabled():
		b.notice(ctx, lark, m.ChatID, noticeNoAnswer)

		return nil
	case busy:
		// The message waits for the turn that is running, and the person is told, as Hermes tells one who
		// writes while it works.
		b.notice(ctx, lark, m.ChatID, noticeQueued)
	}

	r.asked(ctx, m.ID)

	return nil
}

// startOver is /new: the person's next message starts a new chat with the
// coordinator.
func (b *Bot) startOver(ctx context.Context, lark Lark, m Message) error {
	chat, err := b.store.ResetLarkBotChat(ctx, m.ChatID, "")
	if errors.Is(err, orchestration.ErrNotFound) {
		b.notice(ctx, lark, m.ChatID, noticeNew)

		return nil
	}

	if err != nil {
		return err
	}

	b.mu.Lock()
	if old := b.relays[m.ChatID]; old != nil {
		old.stop()
		delete(b.relays, m.ChatID)
	}

	b.relayLocked(ctx, lark, chat)
	b.mu.Unlock()

	b.notice(ctx, lark, m.ChatID, noticeNew)

	return nil
}

// stop is /stop: the coordinator stops the turn it is running in the
// person's chat.
func (b *Bot) stop(ctx context.Context, lark Lark, m Message) error {
	b.mu.Lock()
	r := b.relays[m.ChatID]
	b.mu.Unlock()

	if r == nil || !b.coord.Working(r.chat.ChatID) {
		b.notice(ctx, lark, m.ChatID, noticeIdle)

		return nil
	}

	if err := b.coord.Abort(ctx, r.chat.ChatID); err != nil {
		return err
	}

	b.notice(ctx, lark, m.ChatID, noticeStopped)

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

// relayLocked starts sending a chat's replies to its Lark chat, once, and
// returns what sends them.
func (b *Bot) relayLocked(ctx context.Context, lark Lark, chat orchestration.LarkBotChat) *relay {
	if r, running := b.relays[chat.LarkChatID]; running {
		return r
	}

	ctx, stop := context.WithCancel(ctx)
	r := &relay{bot: b, lark: lark, chat: chat, sent: chat.SentSeq, stop: stop}
	b.relays[chat.LarkChatID] = r

	go r.run(ctx)

	return r
}

// relay sends what the coordinator says in one chat to its Lark chat. The
// text as it is written comes from the supervisor's live frames and fills a
// card; the kept message that follows is what the card ends as, so a frame
// that was lost changes nothing (the web client is put right the same way).
type relay struct {
	bot  *Bot
	lark Lark
	chat orchestration.LarkBotChat
	stop context.CancelFunc
	// sent is the last kept message that was sent.
	sent int64
	// frames is the coordinator's live frames of the chat.
	frames <-chan coordinator.Frame

	// The card being written: its id, the text the coordinator wrote, what a
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
	// reaction that says the coordinator works on each.
	waiting []waiting
}

// waiting is a message of a person the coordinator has not answered.
type waiting struct {
	message  string
	reaction string
}

// asked marks a person's message as being worked on.
func (r *relay) asked(ctx context.Context, messageID string) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	// A reaction that cannot be put (the app lacks the scope) costs the person only the mark.
	reaction, err := r.lark.React(ctx, messageID, emojiWorking)
	if err != nil {
		r.failed(ctx, "marking a message as being worked on", err)
	}

	r.mu.Lock()
	r.waiting = append(r.waiting, waiting{message: messageID, reaction: reaction})
	r.mu.Unlock()
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
// or the coordinator stopped. With failed, the last one is marked as failed.
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

			// The coordinator stopped and kept no reply: what was written stays, and nothing waits any more.
			r.finish(ctx, r.text.String())
			r.settle(ctx, false)
		}
	}
}

// frame handles one live frame and says whether the coordinator stopped, so
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

// show brings the card up to what the coordinator wrote and is doing: it
// opens the card with the first thing to show, and updates it at most twice a
// second unless now is set.
func (r *relay) show(ctx context.Context, now bool) {
	body := inert(cut(r.text.String()))

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
		id, err := r.lark.OpenCard(ctx, r.chat.LarkChatID, r.replyTo(), body)
		if err != nil {
			r.failed(ctx, "opening a card", err)

			return
		}

		r.card, r.shown, r.updated, r.seq = id, body, r.bot.now(), 1

		return
	}

	if !now && r.bot.now().Sub(r.updated) < cardEvery {
		return
	}

	if err := r.lark.UpdateCard(ctx, r.card, body, r.seq); err != nil {
		r.failed(ctx, "updating a card", err)
	}

	r.shown, r.updated = body, r.bot.now()
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
			switch {
			case m.Role == orchestration.ChatCoordinator && strings.TrimSpace(m.Text) != "":
				r.finish(ctx, m.Text)
				r.settle(ctx, false)
			case m.Role == orchestration.ChatSystem && m.EventSeq == 0:
				// The model's call failed: what was written stays, with a line that says why it stops there
				// (MemoryOS), and the message is marked as failed (Hermes).
				partial := strings.TrimSpace(r.text.String())
				if partial != "" {
					partial += "\n\n"
				}

				r.finish(ctx, partial+noticeNoReply)
				r.settle(ctx, true)
			}

			r.sent = m.Seq
		}

		if err := r.bot.store.MarkLarkBotSent(ctx, r.chat.LarkChatID, r.sent); err != nil {
			r.failed(ctx, "recording what was sent", err)

			return
		}
	}
}

// finish ends the card being written with its final text, or sends the text
// as a card of its own. A text longer than a card goes on in further cards.
// With no card and no text it does nothing.
func (r *relay) finish(ctx context.Context, text string) {
	card, seq, replyTo := r.card, r.seq, r.replyTo()

	r.card, r.shown, r.status, r.seq = "", "", "", 0
	r.text.Reset()

	if strings.TrimSpace(text) == "" {
		if card == "" {
			return
		}

		text = "(no answer)"
	}

	parts := split(inert(text))
	if r.bot.webURL != "" {
		parts[len(parts)-1] += "\n\n[Open in Hypomnema](" + r.bot.webURL + "/chats/" + r.chat.ChatID + ")"
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	for i, part := range parts {
		if i > 0 || card == "" {
			id, err := r.lark.OpenCard(ctx, r.chat.LarkChatID, replyTo, part)
			if err != nil {
				r.failed(ctx, "sending a reply", err)

				return
			}

			card, seq, replyTo = id, 1, ""
		}

		if err := r.lark.CloseCard(ctx, card, part, seq); err != nil {
			r.failed(ctx, "closing a card", err)
		}
	}
}

func (r *relay) failed(ctx context.Context, doing string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}

	r.bot.logger.WarnContext(ctx, "the Lark bot failed "+doing, slog.String("event", "larkbot.send_failed"),
		slog.String("chat_id", r.chat.ChatID), slog.Any("error", err))
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

// questions sends the owner's open questions as cards and closes the cards
// of questions that ended, on every change of the store.
func (b *Bot) questions(ctx context.Context, lark Lark) {
	wake := b.store.Subscribe(ctx)

	for {
		if err := b.syncQuestions(ctx, lark); err != nil && ctx.Err() == nil {
			b.logger.WarnContext(ctx, "the owner's questions were not brought to Lark",
				slog.String("event", "larkbot.questions_failed"), slog.Any("error", err))
		}

		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
}

func (b *Bot) syncQuestions(ctx context.Context, lark Lark) error {
	bot, err := b.store.LarkBot(ctx)
	if err != nil || !bot.Enabled {
		// A bot that was switched off or removed sends nothing.
		return nil
	}

	open, err := b.store.ListQuestions(ctx, orchestration.QuestionFilter{
		AddressedTo: orchestration.AddressOwner, Status: orchestration.QuestionOpen,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	for _, q := range open {
		if _, err := b.store.LarkQuestionCardOf(ctx, q.ID); !errors.Is(err, orchestration.ErrNotFound) {
			continue
		}

		cardID, messageID, err := lark.SendCard(ctx, bot.OwnerOpenID, questionCard(q))
		if err != nil {
			return fmt.Errorf("sending question %s: %w", q.ID, err)
		}

		if err := b.store.RecordLarkQuestionCard(ctx, orchestration.LarkQuestionCard{
			QuestionID: q.ID, CardID: cardID, MessageID: messageID,
		}); err != nil {
			return err
		}
	}

	cards, err := b.store.OpenLarkQuestionCards(ctx)
	if err != nil {
		return err
	}

	for _, c := range cards {
		q, err := b.store.GetQuestion(ctx, c.QuestionID)
		if err != nil || q.Status == orchestration.QuestionOpen {
			continue
		}

		if err := lark.ReplaceCard(ctx, c.CardID, endedCard(q), c.Seq+1); err != nil {
			return fmt.Errorf("closing the card of question %s: %w", q.ID, err)
		}

		if err := b.store.CloseLarkQuestionCard(ctx, c.QuestionID, c.Seq+1); err != nil {
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

// Press handles a press of a card's button and returns what the person who
// pressed is told. It is called from the connection, which must answer Lark
// within three seconds; an answer is one write.
func (b *Bot) Press(ctx context.Context, p Press) string {
	bot, err := b.store.LarkBot(ctx)
	if err != nil || !bot.Enabled {
		return ""
	}

	if p.Operator != bot.OwnerOpenID {
		return onlyTheOwner
	}

	var option int
	if _, err := fmt.Sscan(p.Value[buttonOption], &option); err != nil || p.Value[buttonQuestion] == "" {
		return ""
	}

	return b.answer(ctx, p.Value[buttonQuestion], orchestration.Answer{Option: &option})
}

// answer records the owner's answer and returns what he is told.
func (b *Bot) answer(ctx context.Context, questionID string, a orchestration.Answer) string {
	if _, err := b.store.AnswerQuestion(ctx, questionID, orchestration.AddressOwner, a); err != nil {
		b.logger.InfoContext(ctx, "an answer from Lark was not taken", slog.String("event", "larkbot.answer_refused"),
			slog.String("question_id", questionID), slog.Any("error", err))

		return "That answer was not taken: " + err.Error()
	}

	return answered
}
