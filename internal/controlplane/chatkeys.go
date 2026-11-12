package controlplane

import (
	"context"
	"errors"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
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

// RevokeKey ends a pi's key. One already ended is no error: a pi that fails
// as it starts is stopped, and both its end and its start end the key.
func (c chatKeys) RevokeKey(ctx context.Context, id string) error {
	err := c.gateway.RevokeKey(ctx, id)
	if errors.Is(err, modelgateway.ErrConflict) || errors.Is(err, modelgateway.ErrNotFound) {
		return nil
	}

	return err
}
