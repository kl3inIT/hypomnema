package controlplane

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// ownerSecrets is how the owner reads and manages the vault through the
// client API (issue #108). It is the control plane's own wiring: the API may
// not import the vault (issue #66), so it hands names and values across this
// type. Only the owner's token reaches it, by the API's policy; an agent has
// no operation that returns a secret's value
// (docs/decisions/2026-11-10-owner-reads-secret-values.md).
type ownerSecrets struct {
	vault     *vault.Service
	referrers vault.Referrers
}

func (o ownerSecrets) info(ctx context.Context, s vault.Secret) (clientapi.SecretInfo, error) {
	var users []string

	if o.referrers != nil {
		var err error

		users, err = o.referrers(ctx, vault.Ref(s.Name))
		if err != nil {
			return clientapi.SecretInfo{}, fmt.Errorf("finding what uses secret %q: %w", s.Name, err)
		}
	}

	return clientapi.SecretInfo{
		Name: s.Name, Kind: string(s.Kind), Version: s.Version, Metadata: s.Metadata,
		KeyMissing: s.KeyMissing, LoginRequired: s.LoginRequired, UsedBy: users,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}, nil
}

// List describes every secret, without a value.
func (o ownerSecrets) List(ctx context.Context) ([]clientapi.SecretInfo, error) {
	secrets, err := o.vault.List(ctx)
	if err != nil {
		return nil, secretError(err)
	}

	out := make([]clientapi.SecretInfo, 0, len(secrets))

	for _, s := range secrets {
		i, err := o.info(ctx, s)
		if err != nil {
			return nil, err
		}

		out = append(out, i)
	}

	return out, nil
}

// Value opens one secret: its values by key, "value" for a generic secret.
func (o ownerSecrets) Value(ctx context.Context, name string) (map[string]string, error) {
	opened, err := o.vault.Open(ctx, name)
	if err != nil {
		return nil, secretError(err)
	}

	return opened.Payload, nil
}

// Put keeps a value under a name: a new generic secret, or a new version of
// one. What uses the secret gets the new value, as Omnara's rotation. A token
// set is not typed over: only a sign-in produces one.
func (o ownerSecrets) Put(ctx context.Context, name, value string) (clientapi.SecretInfo, error) {
	payload := vault.Payload{vault.KeyValue: value}

	existing, err := o.vault.Get(ctx, name)

	var kept vault.Secret

	switch {
	case errors.Is(err, vault.ErrNotFound):
		kept, err = o.vault.Create(ctx, vault.NewSecret{Name: name, Kind: vault.KindGeneric, Payload: payload})
	case err != nil:
		return clientapi.SecretInfo{}, secretError(err)
	case existing.Kind != vault.KindGeneric:
		return clientapi.SecretInfo{}, fmt.Errorf(
			"%w: %q is a token set; sign in again where it is used to replace it", orchestration.ErrInvalid, name)
	default:
		kept, err = o.vault.Rotate(ctx, name, payload)
	}

	if err != nil {
		return clientapi.SecretInfo{}, secretError(err)
	}

	return o.info(ctx, kept)
}

// Delete removes a secret; refused while something uses it.
func (o ownerSecrets) Delete(ctx context.Context, name string) error {
	return secretError(o.vault.Delete(ctx, name))
}

// secretError turns the vault's errors into the ones the API answers with.
// The vault's errors never carry a value.
func secretError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, vault.ErrNotConfigured):
		return fmt.Errorf("%w: the vault has no key; set HYPOMNEMA_VAULT_KEYS (see `hypomnema secrets keygen`)",
			orchestration.ErrUnavailable)
	case errors.Is(err, vault.ErrNotFound):
		return fmt.Errorf("%w: %w", orchestration.ErrNotFound, err)
	case errors.Is(err, vault.ErrInvalid):
		return fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	case errors.Is(err, vault.ErrConflict), errors.Is(err, vault.ErrInUse):
		return fmt.Errorf("%w: %w", orchestration.ErrConflict, err)
	default:
		return err
	}
}
