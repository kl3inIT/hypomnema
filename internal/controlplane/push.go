package controlplane

import (
	"context"
	"fmt"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/controlplane/webpush"
)

const pushUsage = "push regenerate-keys"

// push is the owner's commands for Web Push (issue #1047). regenerate-keys makes a new VAPID key pair and deletes
// every subscription in the same step, since a browser subscribed with the old public key takes no push signed with
// the new one, as Coder's `coder server regenerate-vapid-keypair` (coderd/webpush/webpush.go:534). Each device then
// turns notifications on again.
func (o *owner) push(ctx context.Context, args []string) error {
	if !slices.Equal(args, []string{"regenerate-keys"}) {
		return fmt.Errorf("%w: %s", ErrUsage, pushUsage)
	}

	if !o.vaultReady {
		return fmt.Errorf("the vault has no root key (HYPOMNEMA_VAULT_KEYS): the VAPID key is kept there")
	}

	keep, err := webpush.NewKeys(o.vault).Replacement()
	if err != nil {
		return err
	}

	// One transaction: the devices are forgotten only with the new key kept.
	n, err := o.svc.ReplacePushKeys(ctx, keep)
	if err != nil {
		return err
	}

	o.printf("new VAPID keys made; %d devices removed: turn notifications on again on each\n", n)

	return nil
}
