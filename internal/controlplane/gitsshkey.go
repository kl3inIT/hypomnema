package controlplane

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// gitSSHKeySecret is the vault secret that holds the control plane's SSH key
// for git: its private half in OpenSSH's form, under the key "value".
const gitSSHKeySecret = "git-ssh-key" //nolint:gosec // the name of a vault secret, not a credential

// gitSSHKeyComment ends the public key, so the owner recognizes it among the
// keys of his account on the git host.
const gitSSHKeyComment = "hypomnema"

// gitSSHKey is the one SSH key pair nodes clone with over SSH (issue #625),
// made on first use and kept in the vault, as Coder makes each user's key
// (coderd/gitsshkey/gitsshkey.go, ed25519 by default) and keeps it in its
// database. The owner adds its public half to the git host once; a worker's
// helper asks the agent API for the private half on each git call (agentapi,
// gitssh.go), so no node keeps it. Deleting the secret from the vault makes
// the next use make a new pair.
type gitSSHKey struct {
	vault *vault.Service
	// mu keeps two first uses from each making a pair.
	mu sync.Mutex
}

// PublicKey is the public half in the authorized_keys form.
func (k *gitSSHKey) PublicKey(ctx context.Context) (string, error) {
	_, public, err := k.Pair(ctx)

	return public, err
}

// Pair is the key's private half in OpenSSH's form and its public half, made
// and kept in the vault when there is none.
func (k *gitSSHKey) Pair(ctx context.Context) (private, public string, err error) {
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

func (k *gitSSHKey) open(ctx context.Context) (string, error) {
	opened, err := k.vault.Open(ctx, gitSSHKeySecret)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			return "", err
		}

		return "", sshKeyError(err)
	}

	return opened.Payload[vault.KeyValue], nil
}

// make makes a pair and keeps it, unless another call made one meanwhile.
func (k *gitSSHKey) make(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if private, err := k.open(ctx); !errors.Is(err, vault.ErrNotFound) {
		return private, err
	}

	private, err := newSSHKey()
	if err != nil {
		return "", err
	}

	_, err = k.vault.Create(ctx, vault.NewSecret{
		Name: gitSSHKeySecret, Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: private},
		Metadata: map[string]string{"purpose": "the SSH key nodes clone repositories with (issue #625)"},
	})
	if err != nil {
		return "", sshKeyError(err)
	}

	return private, nil
}

// newSSHKey makes an ed25519 key in OpenSSH's private key form, as Coder's
// ed25519KeyGen does; x/crypto/ssh now writes that form itself.
func newSSHKey() (string, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("making an SSH key: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(key, gitSSHKeyComment)
	if err != nil {
		return "", fmt.Errorf("writing the SSH key: %w", err)
	}

	return string(pem.EncodeToMemory(block)), nil
}

// publicOf is the public half of a private key, in the authorized_keys form
// with the comment: it is read from the private half, so it is kept nowhere
// else.
func publicOf(private string) (string, error) {
	signer, err := ssh.ParsePrivateKey([]byte(private))
	if err != nil {
		return "", fmt.Errorf("reading the SSH key: %w", err)
	}

	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " " + gitSSHKeyComment, nil
}

// sshKeyError says what the owner can do when the vault cannot keep the key.
func sshKeyError(err error) error {
	if errors.Is(err, vault.ErrNotConfigured) {
		return fmt.Errorf("%w: the vault has no key, so no SSH key can be kept; set HYPOMNEMA_VAULT_KEYS "+
			"(see `hypomnema secrets keygen`)", orchestration.ErrUnavailable)
	}

	return fmt.Errorf("the SSH key for git: %w", err)
}
