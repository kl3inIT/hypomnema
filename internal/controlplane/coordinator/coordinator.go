// Package coordinator runs pi, the coordinator on the server (issue #41,
// PR 3): one `pi --mode rpc` per chat, started when the chat has something
// for pi and stopped after it has been idle a while; restarted with the same
// session id, so pi keeps its memory in its own session file while the
// control plane keeps the record (orchestration's chats).
//
// The RPC protocol is pi's (pi docs/rpc.md, rpc-commands.md, json.md): JSONL
// split on LF only, a `response` per command matched by id, a turn over at
// `agent_settled`, `message_end` authoritative. As Omnigent's pi executor,
// pi's own retries are off (spike A, OpenClaw) and it is restarted when it
// exits.
package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Defaults.
const (
	// DefaultIdleAfter is how long pi may sit idle before it is stopped.
	DefaultIdleAfter = 30 * time.Minute
	// tick is the fallback pass when no commit signals.
	tick = 5 * time.Second
	// commandTimeout bounds the wait for a response to one command.
	commandTimeout = 30 * time.Second
	// stopTimeout is how long pi has to exit after its stdin closes.
	stopTimeout = 30 * time.Second
)

// Config says how to start pi for a chat.
type Config struct {
	// Command starts pi in RPC mode for the chat id appended to it, such as
	// [sudo -n -u coordinator /usr/local/bin/hypomnema-pi]. Empty disables
	// the coordinator: chats keep their messages and nothing answers.
	Command []string
	// IdleAfter stops an idle pi; DefaultIdleAfter when zero.
	IdleAfter time.Duration
	// Keys makes the model gateway's key of a chat's pi; nil when pi reaches
	// its model another way.
	Keys Keys
	// Tokens makes the token each pi calls the control plane with (issue
	// #260); nil when pi is given none, as in tests that play pi.
	Tokens Tokens
	// Updating says an update of the control plane runs (issue #716,
	// selfupdate.Updating): no pi is started and an idle pi is given no new
	// turn until it ends; a turn that runs goes on, steers included. Nil: none.
	Updating func() bool
	Logger   *slog.Logger
}

// Tokens makes and ends the token of one pi process (issue #260): bound to
// the chat and the Bot it runs as, so what pi asks the control plane is
// limited to them, whatever its environment says. The launcher reads it as
// the first line of its stdin, before pi's commands: a secret given on
// stdin, never on a command line (`gh auth login --with-token`, `docker
// login --password-stdin`), since sudo, which runs the launcher, passes no
// environment.
type Tokens interface {
	// AddBotToken returns a new token's secret.
	AddBotToken(ctx context.Context, chatID, bot string) (string, error)
	// RevokeBotToken stops it from working.
	RevokeBotToken(secret string)
}

// Keys makes and ends the key a chat's pi calls the model gateway with
// (issue #204), as a Dispatch's worker has one of its own. Omnigent gives
// its pi no credential either: pi asks the process that started it.
type Keys interface {
	// AddChatKey returns a new key's id and its secret, good for the model, or
	// for every model when it is empty.
	AddChatKey(ctx context.Context, chatID, model string) (id, secret string, err error)
	// RevokeKey stops a key from working.
	RevokeKey(ctx context.Context, id string) error
}

// Supervisor starts, feeds and stops pi processes, one per chat.
type Supervisor struct {
	svc *orchestration.Service
	cfg Config

	mu       sync.Mutex
	procs    map[string]*proc
	watchers map[string]map[watcher]struct{}
	kick     chan struct{}
	// stopping says the control plane stops every pi: a turn cut now keeps its mark (cutturns.go).
	stopping atomic.Bool
	// running is Run's context, which ends when the control plane stops (SIGTERM): a pi started for a request, such
	// as a compaction, reads with a context of its own, which does not (cutturns.go exitedMidTurn).
	running atomic.Pointer[context.Context]
}

// New returns a Supervisor; Run starts it.
func New(svc *orchestration.Service, cfg Config) *Supervisor {
	if cfg.IdleAfter <= 0 {
		cfg.IdleAfter = DefaultIdleAfter
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	return &Supervisor{
		svc: svc, cfg: cfg, procs: map[string]*proc{}, watchers: map[string]map[watcher]struct{}{},
		kick: make(chan struct{}, 1),
	}
}

// updating says an update runs, during which no pi starts and no new turn begins.
func (s *Supervisor) updating() bool { return s.cfg.Updating != nil && s.cfg.Updating() }

// BeginUpdate runs mark, which names the update in its marker (selfupdate), under the lock every pi start holds from
// its last look for an update until pi has its token (issue #716): a start that looked before the update began
// finishes first, and every start after it sees the marker. So no pi starts once the update has begun.
func (s *Supervisor) BeginUpdate(mark func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return mark()
}

// errUpdating refuses to start pi while the control plane is updated.
var errUpdating = fmt.Errorf("%w: the control plane is being updated; pi starts again after it",
	orchestration.ErrConflict)

// Enabled reports whether a command to start pi is configured.
func (s *Supervisor) Enabled() bool { return len(s.cfg.Command) > 0 }

// Working reports whether pi is working on a turn of the chat.
func (s *Supervisor) Working(chatID string) bool {
	s.mu.Lock()
	p := s.procs[chatID]
	s.mu.Unlock()

	return p != nil && p.isWorking()
}

// WorkingChats lists the chats whose pi works on a turn, sorted, for `hypomnema update` to wait for (issue #716).
func (s *Supervisor) WorkingChats() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []string

	for id, p := range s.procs {
		if p.isWorking() {
			out = append(out, id)
		}
	}

	slices.Sort(out)

	return out
}

// Idle reports that the chat's pi runs and works on no turn: its last turn
// ended with agent_settled, as against pi having exited, whose proc is
// exited until it is reaped and then gone (issue #656).
func (s *Supervisor) Idle(chatID string) bool {
	s.mu.Lock()
	p := s.procs[chatID]
	s.mu.Unlock()

	return p != nil && !p.exited() && !p.isWorking() && !p.isCompacting()
}

// Stop ends the chat's pi, if one runs, and waits for it to exit (issue #484): the chat was deleted, and its files
// go next, as t3code detaches a deleted thread's provider session before its files are cleaned up. pi's gateway key
// and token end as it exits. A pi that works is asked to stop by its stdin closing, and killed after stopTimeout.
func (s *Supervisor) Stop(chatID string) {
	s.mu.Lock()
	p := s.procs[chatID]
	delete(s.procs, chatID)
	s.mu.Unlock()

	if p != nil {
		p.stop()
	}
}

// Key returns the model gateway's key of the chat's running pi. No pi, or
// one that has ended, has none.
func (s *Supervisor) Key(chatID string) (string, bool) {
	s.mu.Lock()
	p := s.procs[chatID]
	s.mu.Unlock()

	if p == nil || p.exited() {
		return "", false
	}

	key := p.gatewayKey()

	return key.secret, key.secret != ""
}

// Run delivers what chats have for pi after every commit and every tick,
// until ctx ends; then it stops every pi.
func (s *Supervisor) Run(ctx context.Context) error {
	if !s.Enabled() {
		<-ctx.Done()

		return nil
	}

	wake := s.svc.Subscribe(ctx)
	t := time.NewTicker(tick)

	s.running.Store(&ctx)
	s.continueCutTurns(ctx)

	defer t.Stop()
	defer s.stopAll()

	for {
		s.pass(ctx)

		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-s.kick:
		case <-t.C:
		}
	}
}

// pass wakes chats for new events, delivers what is waiting, and stops pi
// processes idle for too long.
func (s *Supervisor) pass(ctx context.Context) {
	if _, err := s.svc.WakeChats(ctx); err != nil && ctx.Err() == nil {
		s.cfg.Logger.ErrorContext(ctx, "waking chats failed", slog.String("event", "coordinator.wake_failed"),
			slog.Any("error", err))
	}

	chats, err := s.svc.ChatsWithUndelivered(ctx)
	if err == nil {
		var queued []string

		queued, err = s.svc.ChatsWithQueue(ctx)
		for _, id := range queued {
			if !slices.Contains(chats, id) {
				chats = append(chats, id)
			}
		}
	}

	if err != nil {
		if ctx.Err() == nil {
			s.cfg.Logger.ErrorContext(ctx, "listing chats failed", slog.Any("error", err))
		}

		return
	}

	for _, id := range chats {
		if err := s.deliver(ctx, id); err != nil && ctx.Err() == nil {
			s.cfg.Logger.ErrorContext(ctx, "delivering to pi failed", slog.String("event", "coordinator.deliver_failed"),
				slog.String("chat_id", id), slog.Any("error", err))
		}
	}

	s.reapIdle()
}

// deliver gives pi what the chat has for it and marks it delivered once pi accepted it. While pi works, only the
// owner messages sent now go, as a steer, and the rest waits for the turn to settle, as pi's own follow-ups did
// (rpc-commands.md: a followUp "is delivered only when agent stops"). When pi is idle the chat's queue moves into
// the chat first (issue #497), so it goes with the wake-ups as one prompt.
func (s *Supervisor) deliver(ctx context.Context, chatID string) error {
	working := s.Working(chatID)

	// While the control plane is updated, what is written waits for the update to end, except what the owner sends a
	// turn that runs (issue #716, as Hermes's update drain refuses new turns and lets the running ones finish).
	if !working && s.updating() {
		return nil
	}

	if !working {
		if _, err := s.svc.TakeChatQueue(ctx, chatID); err != nil {
			return err
		}
	}

	msgs, err := s.svc.Undelivered(ctx, chatID)
	if err != nil {
		return err
	}

	if working {
		msgs = ownerOnly(msgs)
	}

	if len(msgs) == 0 {
		return nil
	}

	p, err := s.procFor(ctx, chatID)
	if err != nil {
		return err
	}

	// A compaction the owner asked for runs as a turn of its own; what was written meanwhile waits for its end.
	if p.isCompacting() {
		return nil
	}

	if err := s.applyModel(ctx, p); err != nil {
		return err
	}

	if err := p.send(ctx, promptText(msgs)); err != nil {
		return err
	}

	if working {
		seqs := make([]int64, 0, len(msgs))
		for _, m := range msgs {
			seqs = append(seqs, m.Seq)
		}

		return s.svc.MarkDeliveredSeqs(ctx, chatID, seqs)
	}

	return s.svc.MarkDelivered(ctx, chatID, msgs[len(msgs)-1].Seq)
}

// ownerOnly keeps the owner messages: what is sent while pi works.
func ownerOnly(msgs []orchestration.ChatMessage) []orchestration.ChatMessage {
	out := msgs[:0:0]

	for _, m := range msgs {
		if m.Role == orchestration.ChatOwner {
			out = append(out, m)
		}
	}

	return out
}

// promptText joins the owner's messages and the wake-ups into one turn.
// Wake-ups say they come from the control plane, so pi follows the skill's
// rule for them: act, then end the turn. The line that wakes a chat to
// continue a turn a restart cut is given as what it asks (cutturns.go).
func promptText(msgs []orchestration.ChatMessage) string {
	var owner, wakes []string

	resumed := false

	for _, m := range msgs {
		switch {
		case m.Role == orchestration.ChatOwner:
			owner = append(owner, ownerText(m))
		case continued(m):
			resumed = true
		default:
			wakes = append(wakes, "- "+m.Text)
		}
	}

	var b strings.Builder

	if resumed {
		b.WriteString("Message from the Hypomnema control plane: " + continuedPrompt + "\n")
	}

	if len(wakes) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}

		b.WriteString("Message from the Hypomnema control plane: these need you.\n")
		b.WriteString(strings.Join(wakes, "\n"))
		b.WriteString("\nAct on them, then end your turn; the control plane wakes you again.\n")
	}

	if len(owner) > 0 {
		if b.Len() > 0 {
			b.WriteString("\nThe owner also wrote:\n")
		}

		b.WriteString(strings.Join(owner, "\n\n"))
	}

	return b.String()
}

// ownerText is an owner message with a line for each file attached to it (issue #343), as OpenMausBot gives a
// file by its path (pathForFile) and Orca gives its agent the paths of a session's attachments: pi reads them
// with its own tools. The path is relative to the Bot's folder, which is pi's working directory in its box
// (deploy/coordinator/hypomnema-pi binds the folder and starts pi there with --chdir), and pi's tools resolve a
// relative path against it.
func ownerText(m orchestration.ChatMessage) string {
	if len(m.Attachments) == 0 {
		return m.Text
	}

	var b strings.Builder

	if text := strings.TrimSpace(m.Text); text != "" {
		b.WriteString(m.Text)
		b.WriteString("\n\n")
	}

	b.WriteString("The owner attached these files, in your working directory:")

	for _, a := range m.Attachments {
		kind := a.Type
		if kind == "" {
			kind = "file"
		}

		fmt.Fprintf(&b, "\n- %s (%s, %d bytes)", a.Path, kind, a.Size)
	}

	return b.String()
}

// procFor returns the chat's running pi, starting one if needed.
func (s *Supervisor) procFor(ctx context.Context, chatID string) (*proc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if p := s.procs[chatID]; p != nil && !p.exited() {
		return p, nil
	}

	if s.updating() {
		return nil, errUpdating
	}

	args, bot, model, err := s.botArgs(ctx, chatID)
	if err != nil {
		return nil, err
	}

	var key gatewayKey

	if s.cfg.Keys != nil {
		key.id, key.secret, err = s.cfg.Keys.AddChatKey(ctx, chatID, model.Model)
		if err != nil {
			return nil, fmt.Errorf("making the gateway key of chat %s: %w", chatID, err)
		}
	}

	if s.cfg.Tokens != nil {
		key.token, err = s.cfg.Tokens.AddBotToken(ctx, chatID, bot)
		if err != nil {
			s.revoke(ctx, chatID, key)

			return nil, fmt.Errorf("making the token of chat %s's pi: %w", chatID, err)
		}
	}

	// An update that began while the key and the token were made is seen here, before pi starts (issue #716). From
	// this look until pi has its token s.mu is held, which BeginUpdate takes to name the update: it cannot begin in
	// between.
	if s.updating() {
		s.revoke(ctx, chatID, key)

		return nil, errUpdating
	}

	p, err := start(ctx, s, chatID, args, key)
	if err != nil {
		s.revoke(ctx, chatID, key)

		return nil, fmt.Errorf("starting pi for chat %s: %w", chatID, err)
	}

	// One that did not begin through BeginUpdate (a control plane whose control socket did not answer the update,
	// which then names itself) and began while pi started: the pi is stopped before it is given anything.
	if s.updating() {
		p.stop()

		return nil, errUpdating
	}

	p.model = model
	s.procs[chatID] = p

	return p, nil
}

// botArgs are the launcher's flags for the chat's Bot: its id, by which the
// launcher finds its directory, persona and memory, whether the chat is a
// Lark group's, and the chat's model (model.go): the provider (an id in pi's
// models.json), the model and the effort. The model is returned too: the
// chat's gateway key is good for it; and the Bot's id, which pi's token is
// bound to.
func (s *Supervisor) botArgs(ctx context.Context, chatID string,
) ([]string, string, orchestration.ChatModel, error) {
	none := orchestration.ChatModel{}

	id, m, err := s.chatModel(ctx, chatID)
	if err != nil {
		return nil, "", none, err
	}

	args := []string{"--bot", id}

	// The chat of a Lark group is not given the Bot's memory, which holds what the owner's chats taught it, and
	// is told who writes there (issue #246; OpenClaw leaves MEMORY.md out of a group session's prompt).
	group, err := s.svc.Channels().IsChannelGroupChat(ctx, chatID)
	if err != nil {
		return nil, "", none, err
	}

	if group {
		args = append(args, "--group")
	}

	if m.Model != "" {
		if m.Provider != "" {
			args = append(args, "--provider", m.Provider)
		}

		args = append(args, "--model", m.Model)
	}

	if m.Effort != "" {
		args = append(args, "--thinking", piLevel(m.Effort))
	}

	return args, id, m, nil
}

// revoke ends a pi's gateway key and its token. The pi is gone or never
// started, so the caller's context may be over: both are ended all the same.
func (s *Supervisor) revoke(ctx context.Context, chatID string, key gatewayKey) {
	if s.cfg.Tokens != nil && key.token != "" {
		s.cfg.Tokens.RevokeBotToken(key.token)
	}

	if s.cfg.Keys == nil || key.id == "" {
		return
	}

	if err := s.cfg.Keys.RevokeKey(context.WithoutCancel(ctx), key.id); err != nil {
		s.cfg.Logger.ErrorContext(ctx, "revoking pi's gateway key failed",
			slog.String("event", "coordinator.key_revoke_failed"), slog.String("chat_id", chatID),
			slog.String("key_id", key.id), slog.Any("error", err))
	}
}

// settled is called by a proc when a turn ends or it exits, so waiting
// messages go out at once.
func (s *Supervisor) settled() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Supervisor) reapIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, p := range s.procs {
		switch {
		case p.exited():
			delete(s.procs, id)
		case p.idleFor() > s.cfg.IdleAfter:
			go p.stop()

			delete(s.procs, id)
		}
	}
}

func (s *Supervisor) stopAll() {
	s.stopping.Store(true)

	s.mu.Lock()
	procs := s.procs
	s.procs = map[string]*proc{}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Go(p.stop)
	}

	wg.Wait()
}

// ErrDisabled means no command to start pi is configured.
var ErrDisabled = errors.New("the coordinator is not configured (HYPOMNEMA_COORDINATOR_COMMAND)")
