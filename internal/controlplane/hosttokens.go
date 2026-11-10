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

// connections keeps the credential of each Connection in the vault and hands
// it out for a Dispatch (issues #44, #103). It is the control plane's own
// wiring between the vault and the two APIs, which may not open a secret
// themselves (issue #66): the client API gives it a token to keep, the node
// API asks it for the token of the repository an offer is on.
//
// Until the vault's credential sessions and the node's proxy exist, the token
// goes to the worker itself, with the offer. That is no less than a worker
// had before, when it used the node user's own logins.
type connections struct {
	svc   *orchestration.Service
	vault *vault.Service
}

// maxTokenBytes bounds a token; GitHub's and GitLab's are far shorter.
const maxTokenBytes = 4096

// secretName is the vault secret that holds a new Connection's credential. A
// secret's name is lower-case letters, digits and dashes, 63 at most, so the
// Integration's other characters become dashes and a long one is cut; the
// Integration and the label are in the secret's metadata.
func secretName(integration, label string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(integration) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}

	host := b.String()
	if room := 63 - len("conn--") - len(label); len(host) > room {
		host = host[:room]
	}

	return "conn-" + host + "-" + label
}

// Connect keeps a token as a Connection of a git host, replacing the token
// the Connection had.
func (c connections) Connect(ctx context.Context, integration, label, token string) error {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > maxTokenBytes || strings.ContainsFunc(token, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return fmt.Errorf("%w: a token is one line without spaces", orchestration.ErrInvalid)
	}

	if !orchestration.ValidConnectionLabel(label) {
		return fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as work", orchestration.ErrInvalid)
	}

	if _, err := c.svc.GitHostOf(ctx, integration); err != nil {
		return err
	}

	// A Connection keeps the secret it has: the token of before Connections is under its old name.
	name := secretName(integration, label)
	if existing, err := c.svc.ConnectionOf(ctx, integration, label); err == nil {
		name = existing.Secret
	} else if !errors.Is(err, orchestration.ErrNotFound) {
		return err
	}

	payload := vault.Payload{vault.KeyValue: token}

	_, err := c.vault.Create(ctx, vault.NewSecret{
		Name: name, Kind: vault.KindGeneric, Payload: payload,
		Metadata: map[string]string{"integration": integration, "connection": label},
	})
	if errors.Is(err, vault.ErrConflict) {
		_, err = c.vault.Rotate(ctx, name, payload)
	}

	if err != nil {
		return vaultError(err)
	}

	_, err = c.svc.SetConnection(ctx, integration, label, "", name)

	return err
}

// Disconnect forgets a Connection and its credential. One that does not
// exist is left as it is.
func (c connections) Disconnect(ctx context.Context, integration, label string) error {
	// The Connection goes first, so a failure in between leaves a secret nothing uses, not a Connection that
	// names a secret that is gone.
	removed, err := c.svc.RemoveConnection(ctx, integration, label)
	if errors.Is(err, orchestration.ErrNotFound) {
		return nil
	}

	if err != nil {
		return err
	}

	if err := c.vault.Delete(ctx, removed.Secret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return vaultError(err)
	}

	return nil
}

// RemoveHost forgets a declared host and its Connections.
func (c connections) RemoveHost(ctx context.Context, host string) error {
	known, err := c.svc.GitHostOf(ctx, host)
	if err != nil {
		return err
	}

	for _, label := range known.Connections {
		if err := c.Disconnect(ctx, host, label); err != nil {
			return err
		}
	}

	return c.svc.RemoveGitHost(ctx, host)
}

// Token returns the token for a Dispatch on a repository, and false when
// there is none to use or it cannot be opened; the worker then falls back to
// the node user's own logins, as before tokens existed.
func (c connections) Token(ctx context.Context, host, path string) (string, bool) {
	conn, ok, err := c.svc.ConnectionFor(ctx, host, path)
	if err != nil || !ok {
		return "", false
	}

	opened, err := c.vault.Open(ctx, conn.Secret)
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
