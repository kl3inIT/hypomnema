package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Connections (issue #103). An Integration is a service Hypomnema works with;
// today each git host is one, named by its host. A Connection is one account
// of an Integration that the owner authorized, under a label he chose. Its
// credential is in the vault; this package holds only the secret's name.
//
// "Account" already means a runtime's subscription (Account), so an
// authorized account of a service is called a Connection.

// DefaultConnection is the label of a host's Connection made without one, and
// of the token a host had before Connections existed.
const DefaultConnection = "default"

// Connection is one authorized account of an Integration.
type Connection struct {
	Integration string
	Label       string
	// Account is the account as the service names it, when known.
	Account string
	// Secret names the vault secret that holds the credential. The
	// credential itself is never here.
	Secret string
	// Services are the services of the Integration this Connection was
	// granted, each to read or to read and write ("gmail:read", "calendar");
	// empty for a git host.
	Services []string
	// Address is where the service is, for an Integration whose Connections
	// each have their own: a self-hosted Superset. Empty otherwise.
	Address string
	// Version is what the service said it runs when the Connection was made.
	Version string
	// AppID is the id of the app the Connection is, for an Integration that
	// is an app (Lark, issue #311); its secret is the vault secret Secret
	// names. Not a secret. Empty otherwise.
	AppID string
	// AppLabel is the app Connection of the same Integration a user Connection signs in with (issue #468): the
	// owner signed in to the service as himself through that app, and AppID is its app's id when he did. Empty
	// for every other Connection.
	AppLabel string
	// SignInFailed says the service refused the account the last time the
	// control plane signed in.
	SignInFailed bool
	// Chats says the coordinator may use the Connection in chats.
	Chats bool
	// Proxy is the proxy its requests to its service go through (issue #850): socks5://, socks5h://, http:// or
	// https:// with a host and a port. Empty goes direct.
	Proxy string
	// DialAddress is where its requests are dialed instead of their host's (issue #850), as curl's --resolve:
	// the Host header and the TLS name stay the host's. A temporary way round a name that does not resolve. Empty
	// dials the host.
	DialAddress string
	// Access is what the account can really do, action by action, as it was last read at the service (issue
	// #567); empty until it was read once.
	Access ConnectionAccess
	// GitName and GitEmail are the account a git host Connection's token belongs to, as a worker's commits name it
	// (issue #991), read from the host with the token of the vault secret version GitSecretVersionID; empty until
	// read.
	GitName            string
	GitEmail           string
	GitSecretVersionID string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// The access of an action (issue #567): what the account can do at the service, beside the owner's answer for it.
// lark-cli's `auth check` answers granted and missing (cmd/auth/check.go); unknown is what could not be read, which
// lark-cli's pre-flight lets through (cmd/service/service.go, checkServiceScopes).
const (
	AccessGranted = "granted"
	AccessMissing = "missing"
	AccessUnknown = "unknown"
)

// ActionAccess is whether the account can do one action, and why, in a few words.
type ActionAccess struct {
	Access string `json:"access"`
	Reason string `json:"reason,omitempty"`
}

// ConnectionAccess is what the account can do, action by action, and when it was read.
type ConnectionAccess struct {
	CheckedAt time.Time               `json:"checked_at"`
	Actions   map[string]ActionAccess `json:"actions"`
}

// Of is an action's access; unknown when it was not read.
func (a ConnectionAccess) Of(action string) ActionAccess {
	if got, ok := a.Actions[action]; ok {
		return got
	}

	return ActionAccess{Access: AccessUnknown}
}

// Missing counts the actions the account cannot do.
func (a ConnectionAccess) Missing() int {
	n := 0

	for _, got := range a.Actions {
		if got.Access == AccessMissing {
			n++
		}
	}

	return n
}

var connectionLabelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// ValidConnectionLabel says whether a label is one the owner can give.
func ValidConnectionLabel(label string) bool { return connectionLabelPattern.MatchString(label) }

// User says the Connection is the owner's own account: a sign-in through an app (issue #468), or his own
// messaging account linked as a device (issue #638). It acts as the owner, so only the owner's token and a Bot that
// may use it, on the owner's turn, may use it (issue #618).
func (c Connection) User() bool {
	return c.AppLabel != "" || slices.Contains(ownAccountIntegrations, c.Integration)
}

// ownAccountIntegrations are the Integrations every Connection of which is the owner's own account: those of kind
// device (integration.KindDevice), named here as scopedIntegration names Superset (botaccess.go).
var ownAccountIntegrations = []string{"whatsapp", "zalo"}

// SetConnection records a Connection and the secret that holds its
// credential, or changes the secret and account of an existing one. The
// Integration must be a git host a Run can be on.
func (s *Service) SetConnection(ctx context.Context, integration, label, account, secret string,
) (Connection, error) {
	if !ValidConnectionLabel(label) {
		return Connection{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as work", ErrInvalid)
	}

	if secret == "" {
		return Connection{}, fmt.Errorf("%w: a connection needs its secret", ErrInvalid)
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := gitHostOf(ctx, q, integration); err != nil {
			return err
		}

		return q.UpsertConnection(ctx, store.UpsertConnectionParams{
			Integration: integration, Label: label, Account: nullString(account), Secret: secret, Now: now,
		})
	})
	if err != nil {
		return Connection{}, fmt.Errorf("recording connection %s:%s: %w", integration, label, err)
	}

	return s.ConnectionOf(ctx, integration, label)
}

// SaveServiceConnection records a Connection of an Integration that is not a
// git host, with the services it was granted, or replaces the one under that
// label. The caller knows the Integration: this package holds no catalog.
func (s *Service) SaveServiceConnection(ctx context.Context, c Connection) (Connection, error) {
	if !ValidConnectionLabel(c.Label) {
		return Connection{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as work", ErrInvalid)
	}

	if c.Integration == "" || c.Secret == "" {
		return Connection{}, fmt.Errorf("%w: a connection needs its integration and its secret", ErrInvalid)
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.UpsertConnection(ctx, store.UpsertConnectionParams{
			Integration: c.Integration, Label: c.Label, Account: nullString(c.Account), Secret: c.Secret,
			Services: nullString(strings.Join(c.Services, ",")), Address: nullString(c.Address),
			Version: nullString(c.Version), AppID: nullString(c.AppID), AppLabel: nullString(c.AppLabel), Now: now,
		})
	})
	if err != nil {
		return Connection{}, fmt.Errorf("recording connection %s:%s: %w", c.Integration, c.Label, err)
	}

	return s.ConnectionOf(ctx, c.Integration, c.Label)
}

// SetConnectionSignInFailed records whether the service refused a
// Connection's account the last time the control plane signed in.
func (s *Service) SetConnectionSignInFailed(ctx context.Context, integration, label string, failed bool) error {
	flag := int64(0)
	if failed {
		flag = 1
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		_, err := q.SetConnectionSignInFailed(ctx, store.SetConnectionSignInFailedParams{
			Integration: integration, Label: label, Failed: flag, Now: now,
		})

		return err
	})
	if err != nil {
		return fmt.Errorf("recording the sign-in of %s:%s: %w", integration, label, err)
	}

	return nil
}

// SetConnectionAccess keeps what a Connection's account can do, as it was just read at the service.
func (s *Service) SetConnectionAccess(ctx context.Context, integration, label string, a ConnectionAccess) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encoding the access of %s:%s: %w", integration, label, err)
	}

	err = s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.SetConnectionAccess(ctx, store.SetConnectionAccessParams{
			Integration: integration, Label: label, Access: nullString(string(raw)),
		})
		if err == nil && n == 0 {
			return fmt.Errorf("%w: %s has no connection %s", ErrNotFound, integration, label)
		}

		return err
	})
	if err != nil {
		return fmt.Errorf("recording the access of %s:%s: %w", integration, label, err)
	}

	return nil
}

// SetConnectionGitIdentity keeps the account a git host Connection's token belongs to (issue #991), read with the
// token of the vault secret version versionID. Nothing is kept once versionID is no longer the current version of
// the Connection's secret: a newer token's account is never overwritten.
func (s *Service) SetConnectionGitIdentity(ctx context.Context, integration, label, versionID, name, email string,
) error {
	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		_, err := q.SetConnectionGitIdentity(ctx, store.SetConnectionGitIdentityParams{
			GitName: nullString(name), GitEmail: nullString(email), GitSecretVersionID: nullString(versionID),
			Integration: integration, Label: label,
		})

		return err
	})
	if err != nil {
		return fmt.Errorf("recording the git identity of %s:%s: %w", integration, label, err)
	}

	return nil
}

// ConnectionsOf returns an Integration's Connections, oldest first.
func (s *Service) ConnectionsOf(ctx context.Context, integration string) ([]Connection, error) {
	rows, err := s.read().ListConnectionsOf(ctx, integration)
	if err != nil {
		return nil, fmt.Errorf("listing the connections of %s: %w", integration, err)
	}

	out := make([]Connection, 0, len(rows))
	for _, r := range rows {
		out = append(out, connectionFromRow(r))
	}

	return out, nil
}

// IntegrationClient is the OAuth client an Integration signs in with. Its
// secret is in the vault; this holds only the secret's name.
type IntegrationClient struct {
	Integration string
	ClientID    string
	Secret      string
	UpdatedAt   time.Time
}

// SetIntegrationClient records an Integration's OAuth client, replacing the
// one it had.
func (s *Service) SetIntegrationClient(ctx context.Context, integration, clientID, secret string) error {
	if integration == "" || clientID == "" || secret == "" {
		return fmt.Errorf("%w: a client needs its integration, its id and its secret", ErrInvalid)
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.UpsertIntegrationClient(ctx, store.UpsertIntegrationClientParams{
			Integration: integration, ClientID: clientID, Secret: secret, Now: now,
		})
	})
	if err != nil {
		return fmt.Errorf("recording the client of %s: %w", integration, err)
	}

	return nil
}

// IntegrationClientOf returns an Integration's OAuth client; ErrNotFound
// when none was set.
func (s *Service) IntegrationClientOf(ctx context.Context, integration string) (IntegrationClient, error) {
	row, err := s.read().GetIntegrationClient(ctx, integration)
	if errors.Is(err, sql.ErrNoRows) {
		return IntegrationClient{}, fmt.Errorf("%w: %s has no OAuth client", ErrNotFound, integration)
	}

	if err != nil {
		return IntegrationClient{}, fmt.Errorf("reading the client of %s: %w", integration, err)
	}

	return IntegrationClient{
		Integration: row.Integration, ClientID: row.ClientID, Secret: row.Secret, UpdatedAt: millis(row.UpdatedAt),
	}, nil
}

// ConnectionOf returns one Connection; ErrNotFound when there is none.
func (s *Service) ConnectionOf(ctx context.Context, integration, label string) (Connection, error) {
	row, err := s.read().GetConnection(ctx, store.GetConnectionParams{Integration: integration, Label: label})
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, fmt.Errorf("%w: %s has no connection %s", ErrNotFound, integration, label)
	}

	if err != nil {
		return Connection{}, fmt.Errorf("reading connection %s:%s: %w", integration, label, err)
	}

	return connectionFromRow(row), nil
}

// ListConnections returns every Connection, by Integration and then by age.
func (s *Service) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := s.read().ListConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing connections: %w", err)
	}

	out := make([]Connection, 0, len(rows))
	for _, r := range rows {
		out = append(out, connectionFromRow(r))
	}

	return out, nil
}

// RemoveConnection forgets a Connection and returns it, so its secret can be
// deleted. It is refused while a registered repository names it.
func (s *Service) RemoveConnection(ctx context.Context, integration, label string) (Connection, error) {
	var removed Connection

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		row, err := q.GetConnection(ctx, store.GetConnectionParams{Integration: integration, Label: label})
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s has no connection %s", ErrNotFound, integration, label)
		}

		if err != nil {
			return fmt.Errorf("reading connection %s:%s: %w", integration, label, err)
		}

		repos, err := q.CountReposUsingConnection(ctx, store.CountReposUsingConnectionParams{
			Host: integration, Connection: nullString(label),
		})
		if err != nil {
			return fmt.Errorf("counting the repositories that use %s:%s: %w", integration, label, err)
		}

		if repos > 0 {
			return fmt.Errorf("%w: %d registered repositories use %s:%s", ErrConflict, repos, integration, label)
		}

		// A role a machine provider takes its key from (issue #331) stays while the provider does.
		providers, err := q.ListMachineProviderNamesByKeyRef(ctx, Use(integration, label))
		if err != nil {
			return fmt.Errorf("reading the machine providers that use %s:%s: %w", integration, label, err)
		}

		if len(providers) > 0 {
			return fmt.Errorf("%w: machine provider %s takes its key from %s:%s; give it another key first",
				ErrConflict, strings.Join(providers, ", "), integration, label)
		}

		_, err = q.DeleteConnection(ctx, store.DeleteConnectionParams{Integration: integration, Label: label})
		if err != nil {
			return fmt.Errorf("removing connection %s:%s: %w", integration, label, err)
		}

		// An app a user Connection signs in with stays while it does (issue #468): its secret refreshes the
		// person's token and revokes it.
		users, err := q.ListUserConnectionsOfApp(ctx, store.ListUserConnectionsOfAppParams{
			Integration: integration, AppLabel: nullString(label),
		})
		if err != nil {
			return fmt.Errorf("reading the user connections that sign in with %s:%s: %w", integration, label, err)
		}

		if len(users) > 0 {
			return fmt.Errorf("%w: %s signs in with the app of %s:%s; disconnect it first", ErrConflict,
				integration+":"+strings.Join(users, ", "+integration+":"), integration, label)
		}

		// The owner's answers for its actions go with it: an account connected again under the label starts
		// from the defaults.
		if err := q.DeleteActionPoliciesOfConnection(ctx, store.DeleteActionPoliciesOfConnectionParams{
			Integration: integration, Label: label,
		}); err != nil {
			return fmt.Errorf("removing the action policies of %s:%s: %w", integration, label, err)
		}

		removed = connectionFromRow(row)

		return nil
	})

	return removed, err
}

// ConnectionFor returns the Connection work on a repository uses: the one a
// registered repository names, else the host's only one. With several and
// none named there is no answer: nothing is guessed.
func (s *Service) ConnectionFor(ctx context.Context, host, path string) (Connection, bool, error) {
	q := s.read()

	if path != "" {
		named, err := q.ConnectionOfRepo(ctx, store.ConnectionOfRepoParams{Host: host, Path: path})
		if err == nil && named.Valid {
			row, err := q.GetConnection(ctx, store.GetConnectionParams{Integration: host, Label: named.String})
			if err == nil {
				return connectionFromRow(row), true, nil
			}

			if !errors.Is(err, sql.ErrNoRows) {
				return Connection{}, false, fmt.Errorf("reading connection %s:%s: %w", host, named.String, err)
			}
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Connection{}, false, fmt.Errorf("reading the connection of %s/%s: %w", host, path, err)
		}
	}

	rows, err := q.ListConnectionsOf(ctx, host)
	if err != nil {
		return Connection{}, false, fmt.Errorf("listing the connections of %s: %w", host, err)
	}

	if len(rows) != 1 {
		return Connection{}, false, nil
	}

	return connectionFromRow(rows[0]), true, nil
}

// ConnectionsUsingSecret names the Connections whose credential is a vault
// secret, so the vault can refuse to delete it and the owner can see what it
// is for.
func (s *Service) ConnectionsUsingSecret(ctx context.Context, secret string) ([]string, error) {
	rows, err := s.read().ListConnectionsBySecret(ctx, secret)
	if err != nil {
		return nil, fmt.Errorf("listing the connections that use %q: %w", secret, err)
	}

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, "connection "+r.Integration+":"+r.Label)
	}

	clients, err := s.read().ListIntegrationClientsBySecret(ctx, secret)
	if err != nil {
		return nil, fmt.Errorf("listing the clients that use %q: %w", secret, err)
	}

	for _, c := range clients {
		out = append(out, "the OAuth client of "+c.Integration)
	}

	return out, nil
}

// SecretRead is one time a secret's value was read: by which token, and
// when. The value is never part of it.
type SecretRead struct {
	Secret string
	// TokenID is the token that read it, or "host" for the command run on
	// the control plane host.
	TokenID string
	At      time.Time
}

// RecordSecretRead writes that a token read a secret's value (issue #108),
// as Infisical records a `get-secret` audit event. It is kept apart from the
// event log, which every token reads.
func (s *Service) RecordSecretRead(ctx context.Context, secret, tokenID string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		return q.InsertSecretRead(ctx, store.InsertSecretReadParams{Secret: secret, TokenID: tokenID, Now: now})
	})
}

// SecretReads returns the latest reads of a secret's value, newest first.
func (s *Service) SecretReads(ctx context.Context, secret string, limit int) ([]SecretRead, error) {
	rows, err := s.read().ListSecretReads(ctx, store.ListSecretReadsParams{Secret: secret, Lim: int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("listing the reads of secret %q: %w", secret, err)
	}

	out := make([]SecretRead, 0, len(rows))
	for _, r := range rows {
		out = append(out, SecretRead{Secret: r.Secret, TokenID: r.TokenID, At: millis(r.CreatedAt)})
	}

	return out, nil
}

func connectionFromRow(r store.Connection) Connection {
	var services []string
	if r.Services.String != "" {
		services = strings.Split(r.Services.String, ",")
	}

	// What cannot be read counts as never read: every action is then unknown.
	var access ConnectionAccess
	if r.Access.Valid {
		_ = json.Unmarshal([]byte(r.Access.String), &access)
	}

	return Connection{
		Integration: r.Integration, Label: r.Label, Account: r.Account.String, Secret: r.Secret, Services: services,
		Address: r.Address.String, Version: r.Version.String, AppID: r.AppID.String, AppLabel: r.AppLabel.String,
		SignInFailed:       r.SignInFailed != 0,
		Chats:              r.Chats != 0,
		Proxy:              r.Proxy.String,
		DialAddress:        r.DialAddress.String,
		Access:             access,
		GitName:            r.GitName.String,
		GitEmail:           r.GitEmail.String,
		GitSecretVersionID: r.GitSecretVersionID.String,
		CreatedAt:          millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt),
	}
}
