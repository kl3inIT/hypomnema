package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// hostTokens keeps the token of each git host in the vault and hands it out
// for a Dispatch (issue #44). It is the control plane's own wiring between
// the vault and the two APIs, which may not open a secret themselves
// (issue #66): the client API gives it a token to keep, the node API asks it
// for the token of the host an offer is on.
//
// Until the vault's credential sessions and the node's proxy exist, the token
// goes to the worker itself, with the offer. That is no less than a worker
// had before, when it used the node user's own logins.
type hostTokens struct {
	svc   *orchestration.Service
	vault *vault.Service
}

// maxTokenBytes bounds a token; GitHub's and GitLab's are far shorter.
const maxTokenBytes = 4096

// secretName is the vault secret that holds a host's token. A secret's name
// is lower-case letters, digits and dashes, so the host's other characters
// become dashes; the host itself is in the secret's metadata.
func secretName(host string) string {
	var b strings.Builder

	b.WriteString("git-host-")

	for _, r := range strings.ToLower(host) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}

	name := b.String()
	if len(name) > 63 {
		name = name[:63]
	}

	return name
}

// Set keeps a token for a host, replacing the one it had.
func (h hostTokens) Set(ctx context.Context, host, token string) error {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > maxTokenBytes || strings.ContainsFunc(token, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return fmt.Errorf("%w: a token is one line without spaces", orchestration.ErrInvalid)
	}

	if _, err := h.svc.GitHostOf(ctx, host); err != nil {
		return err
	}

	name := secretName(host)
	payload := vault.Payload{vault.KeyValue: token}

	_, err := h.vault.Create(ctx, vault.NewSecret{
		Name: name, Kind: vault.KindGeneric, Payload: payload, Metadata: map[string]string{"git_host": host},
	})
	if errors.Is(err, vault.ErrConflict) {
		_, err = h.vault.Rotate(ctx, name, payload)
	}

	if err != nil {
		return vaultError(err)
	}

	return h.svc.SetGitHostTokenSecret(ctx, host, name)
}

// Clear forgets a host's token. A host that has none is left as it is.
func (h hostTokens) Clear(ctx context.Context, host string) error {
	known, err := h.svc.GitHostOf(ctx, host)
	if err != nil {
		return err
	}

	if !known.HasToken() {
		return nil
	}

	// The host stops naming the secret first, so a failure in between leaves a secret nothing uses, not a host
	// that names a secret that is gone.
	if err := h.svc.SetGitHostTokenSecret(ctx, host, ""); err != nil {
		return err
	}

	if err := h.vault.Delete(ctx, known.TokenSecret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return vaultError(err)
	}

	return nil
}

// Remove forgets a declared host and its token.
func (h hostTokens) Remove(ctx context.Context, host string) error {
	if err := h.Clear(ctx, host); err != nil {
		return err
	}

	return h.svc.RemoveGitHost(ctx, host)
}

// Token returns a host's token for a Dispatch, and false when the host has
// none or it cannot be opened; the worker then falls back to the node user's
// own logins, as before tokens existed.
func (h hostTokens) Token(ctx context.Context, host string) (string, bool) {
	known, err := h.svc.GitHostOf(ctx, host)
	if err != nil || !known.HasToken() {
		return "", false
	}

	opened, err := h.vault.Open(ctx, known.TokenSecret)
	if err != nil {
		return "", false
	}

	token := opened.Payload[vault.KeyValue]

	return token, token != ""
}

// vaultError turns the vault's errors into the ones the APIs answer with,
// without a secret's value: the vault's errors never carry one.
func vaultError(err error) error {
	switch {
	case errors.Is(err, vault.ErrNotConfigured):
		return fmt.Errorf("%w: the vault has no key, so no token can be kept; set HYPOMNEMA_VAULT_KEYS "+
			"(see `hypomnema secrets keygen`)", orchestration.ErrUnavailable)
	case errors.Is(err, vault.ErrInvalid):
		return fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	default:
		return fmt.Errorf("keeping the token: %w", err)
	}
}
