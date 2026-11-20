package vault

import (
	"context"
	"database/sql"
	"errors"
)

// Copies seals the value of a credential another package keeps only as its hash, so the owner can read it again
// (issue #996): a share link's token, as an owner-made gateway key's value is sealed (modelgateway key_values.go).
// It is the vault's side of orchestration.ShareVault, given by the control plane's wiring.
type Copies struct{ s *Service }

// Copies returns the vault's copies of credentials.
func (s *Service) Copies() Copies { return Copies{s: s} }

// SealTx keeps value as the first version of a new generic secret named name, in the record's transaction tx, and
// returns the version's id; "" when the vault is not configured, as a gateway key is then kept without its value.
func (c Copies) SealTx(ctx context.Context, tx *sql.Tx, name, value string, now int64) (string, error) {
	versionID, err := c.s.CreateTx(ctx, tx, NewSecret{Name: name, Kind: KindGeneric, Payload: Payload{KeyValue: value}},
		now)
	if errors.Is(err, ErrNotConfigured) {
		return "", nil
	}

	return versionID, err
}

// Open returns the value kept under a version; "" when the vault cannot open it here: it is not configured, or the
// root key it was sealed under is not in the keyring. A version that does not authenticate is an error.
func (c Copies) Open(ctx context.Context, versionID string) (string, error) {
	opened, err := c.s.OpenVersion(ctx, versionID)
	if errors.Is(err, ErrNotConfigured) || errors.Is(err, ErrKeyUnknown) {
		return "", nil
	}

	if err != nil {
		return "", err
	}

	return opened.Payload[KeyValue], nil
}

// DeleteTx removes the secret of a version in the transaction tx that unlinks it.
func (Copies) DeleteTx(ctx context.Context, tx *sql.Tx, versionID string) error {
	return DeleteByVersionTx(ctx, tx, versionID)
}
