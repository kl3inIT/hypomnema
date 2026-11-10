package controlplane

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// dispatchKeysEvery is how often the keys of settled Dispatches are looked
// for when nothing signals a change.
const dispatchKeysEvery = time.Minute

// dispatchKeys makes the model gateway's key for a Dispatch whose model the
// gateway serves, and revokes it when the Dispatch settles (issue #107). The
// key is made as the offer is sent, as the git host's token is read then, and
// only its hash is kept.
type dispatchKeys struct {
	gateway *modelgateway.Service
	svc     *orchestration.Service
	logger  *slog.Logger
}

// AgentKey returns a new key to the agent API for a Dispatch, good for the
// Connections its Run may use (issue #103).
func (d dispatchKeys) AgentKey(ctx context.Context, dispatchID string, uses []string) (string, bool) {
	secret, err := d.svc.AddAgentSession(ctx, dispatchID, uses)
	if err != nil {
		d.logger.ErrorContext(ctx, "making a dispatch's agent key",
			slog.String("event", "controlplane.agent_key_failed"), slog.String("dispatch_id", dispatchID),
			slog.String("error", err.Error()))

		return "", false
	}

	return secret, true
}

// Key returns a new key for a Dispatch, good for its model only.
func (d dispatchKeys) Key(ctx context.Context, dispatchID, model string) (string, bool) {
	secret, err := d.gateway.AddDispatchKey(ctx, dispatchID, model)
	if err != nil {
		d.logger.ErrorContext(ctx, "making a dispatch's gateway key",
			slog.String("event", "controlplane.dispatch_key_failed"), slog.String("dispatch_id", dispatchID),
			slog.String("error", err.Error()))

		return "", false
	}

	return secret, true
}

// Loop revokes the keys of Dispatches that settled, whenever orchestration
// commits and every dispatchKeysEvery, until ctx ends.
func (d dispatchKeys) Loop(ctx context.Context) {
	wake := d.svc.Subscribe(ctx)
	tick := time.NewTicker(dispatchKeysEvery)

	defer tick.Stop()

	for {
		d.sweep(ctx)

		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-tick.C:
		}
	}
}

func (d dispatchKeys) sweep(ctx context.Context) {
	ids, err := d.gateway.DispatchesWithKeys(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.ErrorContext(ctx, "listing dispatches with gateway keys",
				slog.String("event", "controlplane.dispatch_keys_failed"), slog.String("error", err.Error()))
		}

		return
	}

	d.sweepAgentKeys(ctx)

	for _, id := range ids {
		if !d.ended(ctx, id) {
			continue
		}

		if err := d.gateway.RevokeDispatchKeys(ctx, id); err != nil && ctx.Err() == nil {
			d.logger.ErrorContext(ctx, "revoking a dispatch's gateway keys",
				slog.String("event", "controlplane.dispatch_key_revoke_failed"), slog.String("dispatch_id", id),
				slog.String("error", err.Error()))
		}
	}
}

// ended reports whether a Dispatch settled or is gone: either way no worker
// is left to use its keys.
func (d dispatchKeys) ended(ctx context.Context, id string) bool {
	dispatch, err := d.svc.GetDispatch(ctx, id)
	if err != nil {
		return errors.Is(err, orchestration.ErrNotFound)
	}

	return dispatch.Settled()
}

// sweepAgentKeys revokes the agent API keys of Dispatches that ended.
func (d dispatchKeys) sweepAgentKeys(ctx context.Context) {
	ids, err := d.svc.DispatchesWithAgentSessions(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.logger.ErrorContext(ctx, "listing dispatches with agent keys",
				slog.String("event", "controlplane.agent_keys_failed"), slog.String("error", err.Error()))
		}

		return
	}

	for _, id := range ids {
		if !d.ended(ctx, id) {
			continue
		}

		if err := d.svc.RevokeAgentSessions(ctx, id); err != nil && ctx.Err() == nil {
			d.logger.ErrorContext(ctx, "revoking a dispatch's agent keys",
				slog.String("event", "controlplane.agent_key_revoke_failed"), slog.String("dispatch_id", id),
				slog.String("error", err.Error()))
		}
	}
}
