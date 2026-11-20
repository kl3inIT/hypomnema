package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

	if _, err = c.svc.SetConnection(ctx, integration, label, "", name); err != nil {
		return err
	}

	// The token's account is read now, beside, so the first offer that uses the token has it (issue #991).
	c.readIdentity(ctx, integration, label)

	return nil
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
// node user's own logins, as before tokens existed. It never waits for a host:
// the account is read beside, and an offer made before it is known goes
// without one.
func (c connections) Credential(ctx context.Context, host, path string) (nodeapi.HostCredential, bool) {
	conn, ok, err := c.svc.ConnectionFor(ctx, host, path)
	if err != nil || !ok {
		c.identities.without(ctx, "host "+host, "it has no token for this repository")

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

	id := c.identities.of(ctx, c.svc, conn, token, opened.VersionID)
	if !id.Known() {
		c.identities.without(ctx, "connection "+conn.Integration+":"+conn.Label+" "+opened.VersionID,
			"its token's account is not known yet, or its host would not say it")
	}

	return nodeapi.HostCredential{Token: token, Identity: id}, true
}

// readIdentity starts reading the account of a git host Connection's token, beside, unless it is known.
func (c connections) readIdentity(ctx context.Context, integration, label string) {
	if c.identities == nil {
		return
	}

	conn, err := c.svc.ConnectionOf(ctx, integration, label)
	if err != nil {
		return
	}

	opened, err := c.vault.Open(ctx, conn.Secret)
	if err != nil || opened.Payload[vault.KeyValue] == "" {
		return
	}

	c.identities.of(ctx, c.svc, conn, opened.Payload[vault.KeyValue], opened.VersionID)
}

// readIdentities starts reading the account of every git host Connection's token that is not known yet, as the
// control plane starts, so the first offers after an upgrade or a restart have it.
func (c connections) readIdentities(ctx context.Context) {
	hosts, err := c.svc.ListGitHosts(ctx)
	if err != nil {
		return
	}

	for _, h := range hosts {
		for _, label := range h.Connections {
			c.readIdentity(ctx, h.Host, label)
		}
	}
}

// identityTimeout bounds a read of a token's account.
const identityTimeout = 10 * time.Second

// gitIdentities reads the account a git host Connection's token belongs to, as a worker's commits name it (issue
// #991), once a token, and keeps it with the Connection, as Paperclip keeps a GitHub grant's login and id with the
// grant (server/src/services/git-credentials.ts:53, 116-153). A token is a version of the Connection's vault
// secret, named by the version's id, which is never given again: a token replaced, by connecting again, on the
// secret itself or after a disconnect, is read again, as it may be another account's. The read goes the
// Connection's route, as the service proxy's requests do, beside the offers: the node API's loop, which also
// answers heartbeats, never waits for a host. What is offered without an account is said once: the worker then
// commits as the node user's git configuration says, as before.
type gitIdentities struct {
	ways   *connproxy.Transports
	logger *slog.Logger
	// reads holds an *identityRead a token, keyed by its vault secret version's id.
	reads sync.Map
	// said holds what was already logged as going without an account.
	said sync.Map
}

// identityRead is the one read of a token's account; id is set before done is closed.
type identityRead struct {
	done chan struct{}
	id   githost.Identity
}

// of is the account conn's token, of the vault secret version versionID, belongs to; empty while it is not known,
// and then it is read beside, once. g may be nil: then it reads none.
func (g *gitIdentities) of(ctx context.Context, svc *orchestration.Service, conn orchestration.Connection,
	token, versionID string,
) githost.Identity {
	kept := githost.Identity{Name: conn.GitName, Email: conn.GitEmail}
	if kept.Known() && versionID != "" && conn.GitSecretVersionID == versionID {
		return kept
	}

	if g == nil || versionID == "" {
		return githost.Identity{}
	}

	read := &identityRead{done: make(chan struct{})}
	if entry, loaded := g.reads.LoadOrStore(versionID, read); loaded {
		read, _ = entry.(*identityRead)
	} else {
		go g.read(context.WithoutCancel(ctx), svc, conn, token, versionID, read)
	}

	select {
	case <-read.done:
		return read.id
	default:
		return githost.Identity{}
	}
}

// without says once that what key names is offered without an account, and why.
func (g *gitIdentities) without(ctx context.Context, key, why string) {
	if g == nil {
		return
	}

	if _, said := g.said.LoadOrStore(key, true); said {
		return
	}

	g.logger.InfoContext(ctx, "a Dispatch goes without a git identity; its worker commits as its node's git "+
		"configuration says", slog.String("on", key), slog.String("because", why))
}

// read reads the account of token, keeps it with the Connection and closes read.done.
func (g *gitIdentities) read(ctx context.Context, svc *orchestration.Service, conn orchestration.Connection,
	token, versionID string, read *identityRead,
) {
	defer close(read.done)

	ctx, cancel := context.WithTimeout(ctx, identityTimeout)
	defer cancel()

	name := conn.Integration + ":" + conn.Label

	id, err := g.lookup(ctx, svc, conn, token)
	if err != nil {
		// What a host or a proxy answers may repeat the token: it never reaches the log.
		g.logger.WarnContext(ctx, "the account of a git host's token could not be read; its workers commit as "+
			"their node's git configuration says", slog.String("connection", name),
			slog.String("error", strings.ReplaceAll(err.Error(), token, "[token]")))

		return
	}

	read.id = id

	if err := svc.SetConnectionGitIdentity(ctx, conn.Integration, conn.Label, versionID, id.Name, id.Email); err != nil {
		g.logger.WarnContext(ctx, "keeping the account of a git host's token", slog.String("connection", name),
			slog.String("error", err.Error()))
	}
}

func (g *gitIdentities) lookup(ctx context.Context, svc *orchestration.Service, conn orchestration.Connection,
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
