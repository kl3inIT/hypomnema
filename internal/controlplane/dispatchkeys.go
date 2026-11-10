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

	for _, id := range ids {
		dispatch, err := d.svc.GetDispatch(ctx, id)
		if err != nil && !errors.Is(err, orchestration.ErrNotFound) {
			continue
		}

		// A Dispatch that is gone has no worker left to use the key either.
		if err == nil && !dispatch.Settled() {
			continue
		}

		if err := d.gateway.RevokeDispatchKeys(ctx, id); err != nil && ctx.Err() == nil {
			d.logger.ErrorContext(ctx, "revoking a dispatch's gateway keys",
				slog.String("event", "controlplane.dispatch_key_revoke_failed"), slog.String("dispatch_id", id),
				slog.String("error", err.Error()))
		}
	}
}
