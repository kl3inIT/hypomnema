package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kl3inIT/hypomnema/internal/controlplane/connproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// connections keeps the credential of each Connection in the vault and hands
// it out for a Dispatch (issues #44, #103). It is the control plane's own
// wiring between the vault and the two APIs, which may not open a secret
// themselves (issue #66): the client API gives it a token to keep, the node
// API asks it for the token of the repository an offer is on.
//
// The token goes to the worker itself, with the offer, as OpenHands and
// Omnara deliver credentials: there is no proxy on the node (docs/decisions/2026-11-13-worker-credentials-in-the-environment.md).
type connections struct {
	svc   *orchestration.Service
	vault *vault.Service
	// identities reads the account each token belongs to (issue #991); nil reads none, and offers carry none.
	identities *gitIdentities
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

// Credential returns the token for a Dispatch on a repository, with the
// account it belongs to when that is known (issue #991), and false when there
// is no token to use or it cannot be opened; the worker then falls back to the
// node user's own logins, as before tokens existed.
func (c connections) Credential(ctx context.Context, host, path string) (nodeapi.HostCredential, bool) {
	conn, ok, err := c.svc.ConnectionFor(ctx, host, path)
	if err != nil || !ok {
		return nodeapi.HostCredential{}, false
	}

	opened, err := c.vault.Open(ctx, conn.Secret)
	if err != nil {
		return nodeapi.HostCredential{}, false
	}

	token := opened.Payload[vault.KeyValue]
	if token == "" {
		return nodeapi.HostCredential{}, false
	}

	return nodeapi.HostCredential{Token: token, Identity: c.identities.of(ctx, c.svc, conn, token, opened.Version)}, true
}

// identityTimeout bounds the one read of a token's account; an offer waits for it once.
const identityTimeout = 10 * time.Second

// gitIdentities reads the account a git host Connection's token belongs to, as a worker's commits name it (issue
// #991), once a token, and keeps it with the Connection, as Paperclip keeps a GitHub grant's login and id with the
// grant (server/src/services/git-credentials.ts:53, 116-153). A token is a version of the Connection's vault
// secret: a token replaced, by connecting again or on the secret itself, is read again, as it may be another
// account's. The read goes the Connection's route, as the service
// proxy's requests do. An account that cannot be read is said once and the offers go without one: the worker then
// commits as the node user's git configuration says, as before.
type gitIdentities struct {
	ways   *connproxy.Transports
	logger *slog.Logger
	// reads holds an *identityRead a token, keyed by the Connection, its secret and the secret's version.
	reads sync.Map
}

// identityRead is the one read of a token's account; offers that ask meanwhile wait for it.
type identityRead struct {
	once sync.Once
	id   githost.Identity
}

// of is the account conn's token, of version version of its secret, belongs to; empty when it is not known. g may
// be nil: then it reads none.
func (g *gitIdentities) of(ctx context.Context, svc *orchestration.Service, conn orchestration.Connection,
	token string, version int,
) githost.Identity {
	kept := githost.Identity{Name: conn.GitName, Email: conn.GitEmail}
	if kept.Known() && conn.GitSecretVersion == version {
		return kept
	}

	if g == nil {
		return githost.Identity{}
	}

	key := conn.Integration + "\x00" + conn.Label + "\x00" + conn.Secret + "\x00" + strconv.Itoa(version)
	entry, _ := g.reads.LoadOrStore(key, &identityRead{})
	read, _ := entry.(*identityRead)

	read.once.Do(func() {
		// One session ending does not spoil the read for the others.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), identityTimeout)
		defer cancel()

		id, err := g.read(ctx, svc, conn, token)
		if err != nil {
			g.logger.WarnContext(ctx, "the account of a git host's token is not known; its workers commit as their "+
				"node's git configuration says", slog.String("connection", conn.Integration+":"+conn.Label),
				slog.String("error", err.Error()))

			return
		}

		read.id = id

		err = svc.SetConnectionGitIdentity(ctx, conn.Integration, conn.Label, conn.Secret, version, id.Name, id.Email)
		if err != nil {
			g.logger.WarnContext(ctx, "keeping the account of a git host's token",
				slog.String("connection", conn.Integration+":"+conn.Label), slog.String("error", err.Error()))
		}
	})

	return read.id
}

func (g *gitIdentities) read(ctx context.Context, svc *orchestration.Service, conn orchestration.Connection,
	token string,
) (githost.Identity, error) {
	kind, ok := svc.KindOfHost(ctx, conn.Integration)
	if !ok {
		return githost.Identity{}, fmt.Errorf("%s is not a known git host", conn.Integration)
	}

	way, err := g.ways.For(connproxy.Route{Proxy: conn.Proxy, Dial: conn.DialAddress})
	if err != nil {
		return githost.Identity{}, err
	}

	return githost.ReadIdentity(ctx, &http.Client{Transport: way}, kind, conn.Integration, token)
}

// Of returns the token of a git host's Connection by its label, for the service proxy (issue #685): a Bot given
// github.com:default reads GitHub with the token workers use. The proxy adds it to a request; no agent holds it.
func (c connections) Of(ctx context.Context, host, label string) (string, error) {
	conn, err := c.svc.ConnectionOf(ctx, host, label)
	if err != nil {
		return "", err
	}

	opened, err := c.vault.Open(ctx, conn.Secret)
	if err != nil {
		return "", vaultError(err)
	}

	token := opened.Payload[vault.KeyValue]
	if token == "" {
		return "", fmt.Errorf("%w: %s:%s has no token", orchestration.ErrNotFound, host, label)
	}

	return token, nil
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
