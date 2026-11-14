package coordinator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// A chat's model and effort (issue #369) are its own, as a t3code thread's, an
// Orca session's and an Omnigent session's are: what the chat chose, and its
// Bot's for what it did not. A pi that starts is given them as flags; a pi that
// runs takes a change before the chat's next message, without restarting, with
// pi's RPC commands set_model and set_thinking_level (pi
// docs/rpc-commands.md, Model and Thinking).

// chatModel is the name of the chat's Bot and the model and effort the chat
// runs with.
func (s *Supervisor) chatModel(ctx context.Context, chatID string) (string, orchestration.ChatModel, error) {
	chat, err := s.svc.GetChat(ctx, chatID)
	if err != nil {
		return "", orchestration.ChatModel{}, err
	}

	b, err := s.svc.GetBot(ctx, chat.Bot)
	if err != nil {
		return "", orchestration.ChatModel{}, fmt.Errorf("reading the bot of chat %s: %w", chatID, err)
	}

	m := orchestration.ChatModel{Provider: b.Provider, Model: b.Model, Effort: b.Effort}
	if chat.Choice.Model != "" {
		m.Provider, m.Model = chat.Choice.Provider, chat.Choice.Model
	}

	if chat.Choice.Effort != "" {
		m.Effort = chat.Choice.Effort
	}

	return b.Name, m, nil
}

// piLevel is an effort as pi names its thinking level: the gateway's none is
// pi's off (clientcli/pimodels.go); the others have the same names.
func piLevel(effort string) string {
	if effort == "none" {
		return "off"
	}

	return effort
}

// applyModel gives a running pi the chat's model and effort when they changed
// since it started, before the prompt that follows. While a turn runs, the
// change waits for the next prompt that starts one: the turn's requests keep
// the model and the key they began with. The chat's gateway key is good for
// one model, so a new model gets a new key first; pi asks for its key at each
// request (`hypomnema model-key`). pi's own default, a model no chat or Bot
// names, is had only at start. A pi that refuses a change is stopped, so that
// the next pass starts one with the chat's choice as its flags.
func (s *Supervisor) applyModel(ctx context.Context, p *proc) error {
	if p.isWorking() {
		return nil
	}

	_, want, err := s.chatModel(ctx, p.chatID)
	if err != nil {
		return err
	}

	p.mu.Lock()
	have := p.model
	p.mu.Unlock()

	if want == have {
		return nil
	}

	if err := s.setModel(ctx, p, have, want); err != nil {
		s.mu.Lock()
		if s.procs[p.chatID] == p {
			delete(s.procs, p.chatID)
		}
		s.mu.Unlock()

		go p.stop()

		return fmt.Errorf("giving pi of chat %s its model: %w", p.chatID, err)
	}

	p.mu.Lock()
	p.model = want
	p.mu.Unlock()

	s.cfg.Logger.InfoContext(ctx, "pi's model changed", slog.String("event", "coordinator.model_changed"),
		slog.String("chat_id", p.chatID), slog.String("model", want.Model), slog.String("effort", want.Effort))

	return nil
}

// setModel sends pi the commands that take it from have to want.
func (s *Supervisor) setModel(ctx context.Context, p *proc, have, want orchestration.ChatModel) error {
	moved := want.Model != "" && (want.Model != have.Model || want.Provider != have.Provider)

	if moved {
		if s.cfg.Keys != nil {
			id, secret, err := s.cfg.Keys.AddChatKey(ctx, p.chatID, want.Model)
			if err != nil {
				return fmt.Errorf("making the gateway key for %s: %w", want.Model, err)
			}

			p.mu.Lock()
			p.keys.id, p.keys.secret = id, secret
			p.mu.Unlock()
		}

		if _, err := p.call(ctx, map[string]any{
			"type": "set_model", "provider": want.Provider, "modelId": want.Model,
		}); err != nil {
			return err
		}
	}

	// pi fits its level to a model it switches to, so the chosen one is given again after a switch.
	if want.Effort != "" && (moved || want.Effort != have.Effort) {
		if _, err := p.call(ctx, map[string]any{"type": "set_thinking_level", "level": piLevel(want.Effort)}); err != nil {
			return err
		}
	}

	return nil
}
