package controlplane

import (
	"context"
	"errors"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// chatKeys gives the coordinator's supervisor the model gateway's key of a
// chat's pi (issue #204): made as pi starts for the chat, ended when that pi
// ends, as dispatchKeys does for a worker.
type chatKeys struct {
	gateway *modelgateway.Service
}

// AddChatKey returns a new key's id and secret for the chat's pi.
func (c chatKeys) AddChatKey(ctx context.Context, chatID, model string) (string, string, error) {
	k, secret, err := c.gateway.AddChatKey(ctx, chatID, model)

	return k.ID, secret, err
}

// botTokens gives the coordinator's supervisor the token of each pi it
// starts (issue #260), made under the coordinator token pi's runs belong to.
type botTokens struct {
	svc *orchestration.Service
	// coordinator names the coordinator token: the Runs a pi creates are its.
	coordinator string
}

// AddBotToken makes the token of a pi starting for the chat as the Bot.
func (b botTokens) AddBotToken(ctx context.Context, chatID, bot string) (string, error) {
	return b.svc.AddBotToken(ctx, b.coordinator, chatID, bot)
}

// AddRoutineToken makes the token of a Routine's script starting (issue
// #211), as a pi's is made.
func (b botTokens) AddRoutineToken(ctx context.Context, routineID string) (string, error) {
	return b.svc.AddRoutineToken(ctx, b.coordinator, routineID)
}

// RevokeBotToken ends a pi's token, or a Routine's script's.
func (b botTokens) RevokeBotToken(secret string) { b.svc.RevokeBotToken(secret) }

// RevokeKey ends a pi's key. One already ended is no error: a pi that fails
// as it starts is stopped, and both its end and its start end the key.
func (c chatKeys) RevokeKey(ctx context.Context, id string) error {
	err := c.gateway.RevokeKey(ctx, id)
	if errors.Is(err, modelgateway.ErrConflict) || errors.Is(err, modelgateway.ErrNotFound) {
		return nil
	}

	return err
}
