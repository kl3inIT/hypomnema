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
	"strings"
	"sync"
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
	Logger    *slog.Logger
}

// Supervisor starts, feeds and stops pi processes, one per chat.
type Supervisor struct {
	svc *orchestration.Service
	cfg Config

	mu       sync.Mutex
	procs    map[string]*proc
	watchers map[string]map[watcher]struct{}
	kick     chan struct{}
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

// Enabled reports whether a command to start pi is configured.
func (s *Supervisor) Enabled() bool { return len(s.cfg.Command) > 0 }

// Working reports whether pi is working on a turn of the chat.
func (s *Supervisor) Working(chatID string) bool {
	s.mu.Lock()
	p := s.procs[chatID]
	s.mu.Unlock()

	return p != nil && p.isWorking()
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

// deliver gives pi the chat's waiting messages as one prompt, or as a
// follow-up while it works, and marks them delivered once pi accepted them.
func (s *Supervisor) deliver(ctx context.Context, chatID string) error {
	msgs, err := s.svc.Undelivered(ctx, chatID)
	if err != nil || len(msgs) == 0 {
		return err
	}

	p, err := s.procFor(ctx, chatID)
	if err != nil {
		return err
	}

	if err := p.send(ctx, promptText(msgs)); err != nil {
		return err
	}

	return s.svc.MarkDelivered(ctx, chatID, msgs[len(msgs)-1].Seq)
}

// promptText joins the owner's messages and the wake-ups into one turn.
// Wake-ups say they come from the control plane, so pi follows the skill's
// rule for them: act, then end the turn.
func promptText(msgs []orchestration.ChatMessage) string {
	var owner, wakes []string

	for _, m := range msgs {
		if m.Role == orchestration.ChatOwner {
			owner = append(owner, m.Text)
		} else {
			wakes = append(wakes, "- "+m.Text)
		}
	}

	var b strings.Builder

	if len(wakes) > 0 {
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

// procFor returns the chat's running pi, starting one if needed.
func (s *Supervisor) procFor(ctx context.Context, chatID string) (*proc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if p := s.procs[chatID]; p != nil && !p.exited() {
		return p, nil
	}

	args, err := s.modelArgs(ctx)
	if err != nil {
		return nil, err
	}

	p, err := start(ctx, s, chatID, args)
	if err != nil {
		return nil, fmt.Errorf("starting pi for chat %s: %w", chatID, err)
	}

	s.procs[chatID] = p

	return p, nil
}

// CoordinatorProfile is the profile pi runs with.
const CoordinatorProfile = "coordinator"

// modelArgs are pi's flags for the coordinator profile's current model:
// the provider (an id in pi's models.json), the model and the effort.
func (s *Supervisor) modelArgs(ctx context.Context) ([]string, error) {
	p, err := s.svc.GetProfile(ctx, CoordinatorProfile)
	if err != nil {
		return nil, fmt.Errorf("reading the %s profile: %w", CoordinatorProfile, err)
	}

	var args []string

	if p.Model != "" {
		if p.Provider != "" {
			args = append(args, "--provider", p.Provider)
		}

		args = append(args, "--model", p.Model)
	}

	if p.Effort != "" {
		args = append(args, "--thinking", p.Effort)
	}

	return args, nil
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
