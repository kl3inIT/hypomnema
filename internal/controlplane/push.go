package controlplane

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/webpush"
)

const pushUsage = "push regenerate-keys"

// push is the owner's commands for Web Push (issue #1047). regenerate-keys makes a new VAPID key pair and deletes
// every subscription in the same step, since a browser subscribed with the old public key takes no push signed with
// the new one, as Coder's `coder server regenerate-vapid-keypair` (coderd/webpush/webpush.go:534). Each device then
// turns notifications on again.
func (o *owner) push(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "regenerate-keys" {
		return fmt.Errorf("%w: %s", ErrUsage, pushUsage)
	}

	if !o.vaultReady {
		return fmt.Errorf("the vault has no root key (HYPOMNEMA_VAULT_KEYS): the VAPID key is kept there")
	}

	// The subscriptions go first: a key made without them deleted would leave devices that take nothing.
	n, err := o.svc.DeleteAllPushSubscriptions(ctx)
	if err != nil {
		return err
	}

	if _, err := webpush.NewKeys(o.vault).Regenerate(ctx); err != nil {
		return err
	}

	o.printf("new VAPID keys made; %d devices removed: turn notifications on again on each\n", n)

	return nil
}
