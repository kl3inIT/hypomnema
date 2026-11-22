// Package webpush sends Web Push notifications to the owner's browsers (issue #1047): what waits on him reaches the
// desktop browser and the iPhone Home Screen app when no tab of the app is open. It signs with VAPID keys kept in the
// vault and encrypts with SherClockHolmes/webpush-go, the library of the owner's Second Bite
// (server/internal/notification/push_sender.go:11) and Coder (coderd/webpush/webpush.go:17).
package webpush

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"

	"github.com/SherClockHolmes/webpush-go"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// keySecret is the vault secret that holds the VAPID private key, under "value".
const keySecret = "web-push-vapid-key" //nolint:gosec // the name of a vault secret, not a credential

// Keys is the control plane's one VAPID key pair, made on first use and kept in the vault, as Coder makes its pair
// once and keeps it in its database (coderd/webpush/webpush.go:144-162), Chatwoot in its own
// (lib/vapid_service.rb) and OpenClaw in its state (resolveVapidKeys); the vault instead of a column because the
// control plane's secrets live there (ARCHITECTURE.md, the vault). Only the private half is kept: the public half
// is read from it, as gitSSHKey reads its public key from its private one.
type Keys struct {
	vault *vault.Service
	// mu keeps two first uses from each making a pair.
	mu sync.Mutex
}

// NewKeys returns the key pair kept in a vault.
func NewKeys(v *vault.Service) *Keys { return &Keys{vault: v} }

// Pair is the private key and the public key, both in the unpadded base64url form browsers and webpush-go read,
// made and kept when there is none.
func (k *Keys) Pair(ctx context.Context) (private, public string, err error) {
	private, err = k.open(ctx)
	if errors.Is(err, vault.ErrNotFound) {
		private, err = k.make(ctx)
	}

	if err != nil {
		return "", "", err
	}

	public, err = publicOf(private)

	return private, public, err
}

// Replacement makes a new pair and returns the vault's write that keeps it in place of the one kept, for the
// transaction that deletes every subscription (orchestration.Service.ReplacePushKeys): a browser subscribed with the
// old public key takes no push signed with the new one, as Coder's RegenerateVAPIDKeys
// (coderd/webpush/webpush.go:534).
func (k *Keys) Replacement() (vault.TxStep, error) {
	private, _, err := newPair()
	if err != nil {
		return nil, err
	}

	return k.vault.PutStep(newSecret(private)), nil
}

func (k *Keys) open(ctx context.Context) (string, error) {
	opened, err := k.vault.Open(ctx, keySecret)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			return "", err
		}

		return "", fmt.Errorf("opening the VAPID key: %w", err)
	}

	return opened.Payload[vault.KeyValue], nil
}

// make makes a pair and keeps it, unless another call made one meanwhile.
func (k *Keys) make(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if private, err := k.open(ctx); !errors.Is(err, vault.ErrNotFound) {
		return private, err
	}

	private, _, err := newPair()
	if err != nil {
		return "", err
	}

	if err := k.create(ctx, private); err != nil {
		return "", fmt.Errorf("keeping the VAPID key: %w", err)
	}

	return private, nil
}

func (k *Keys) create(ctx context.Context, private string) error {
	_, err := k.vault.Create(ctx, newSecret(private))

	return err
}

func newSecret(private string) vault.NewSecret {
	return vault.NewSecret{
		Name: keySecret, Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: private},
		Metadata: map[string]string{"purpose": "the VAPID key Web Push notifications are signed with (issue #1047)"},
	}
}

func newPair() (private, public string, err error) {
	private, public, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", fmt.Errorf("making a VAPID key: %w", err)
	}

	return private, public, nil
}

// publicOf is the public key of a private one, the P-256 point in its uncompressed form a browser's
// applicationServerKey takes.
func publicOf(private string) (string, error) {
	d, err := base64.RawURLEncoding.DecodeString(private)
	if err != nil {
		return "", errors.New("the VAPID key kept is not base64url")
	}

	key, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return "", errors.New("the VAPID key kept is not a P-256 key")
	}

	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}
