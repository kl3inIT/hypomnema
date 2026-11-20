package integration

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"
	"google.golang.org/api/option"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bigquery"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// An Integration whose Connection is a Google service account (KindServiceAccount, issue #976): BigQuery, read
// for the GA4 export. The owner uploads the account's JSON key once, a new key of an account the data owner made
// for this alone, with BigQuery Data Viewer on the GA4 dataset and Job User on the billing project (design v3,
// decisions 1, 3 and 6). The key is checked step by step before anything is kept; it goes to the vault whole, as
// Superset keeps credentials_info (db_engine_specs/bigquery.py:208-218) and Metabase its key file
// (metabase-plugin.yaml:32-37); the control plane signs the JWT and keeps the access token in memory, renewed before
// it ends, as the AWS role's session is kept (aws.go). The key, the JWT and the token never leave the control plane:
// only the owner's recorded read of a vault secret returns the key (docs/decisions/2026-11-10-owner-reads-secret-
// values.md).

// BigQueryID is the id of the BigQuery Integration.
const BigQueryID = "bigquery"

// googleTokenURL is where a service account's JWT is exchanged; a key that names another is refused (design v3,
// decision 1).
const googleTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // Google's public address, no credential.

// bigqueryScope is the one scope asked: cloud-platform.read-only cannot run jobs.insert, a dry run included (design
// v3, decision 5; Toolbox bigquery.go:780-787).
const bigqueryScope = "https://www.googleapis.com/auth/bigquery"

// BigQuery is Google BigQuery read through a service account: one Connection is one key, with its billing project,
// the GA4 export datasets, a location and the cap on the bytes one query may bill.
func BigQuery() Definition {
	return Definition{ID: BigQueryID, Name: "BigQuery", Kind: KindServiceAccount, TokenURL: googleTokenURL}
}

// IsServiceAccount says whether a Connection of the Integration is a Google service account's key.
func (d Definition) IsServiceAccount() bool { return d.Kind == KindServiceAccount }

// ServiceAccount is what the owner gives to connect BigQuery: the account's JSON key, when it is new or replaced,
// and the settings.
type ServiceAccount struct {
	// Key is the JSON key file as Google gives it; empty keeps the key of the label.
	Key      []byte
	Settings bigquery.Settings
}

// serviceAccountKey is what is read of a key; the rest of the file is kept as it came.
type serviceAccountKey struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
}

// maxKeyBytes bounds a key file: Google's are about 2.3 KB.
const maxKeyBytes = 16 << 10

// parseServiceAccountKey checks a key: a service account's, with its email, private key, project and Google's token
// address. Its errors never hold the key.
func parseServiceAccountKey(raw []byte) (serviceAccountKey, error) {
	var key serviceAccountKey

	if len(raw) > maxKeyBytes || json.Unmarshal(raw, &key) != nil {
		return key, fmt.Errorf("%w: the key is the JSON file Google gives for a service account's key", ErrInvalid)
	}

	switch {
	case key.Type != "service_account":
		return key, fmt.Errorf("%w: the key's type is not service_account", ErrInvalid)
	case key.ClientEmail == "" || !strings.Contains(key.ClientEmail, "@"):
		return key, fmt.Errorf("%w: the key names no client_email", ErrInvalid)
	case key.ProjectID == "":
		return key, fmt.Errorf("%w: the key names no project_id", ErrInvalid)
	case key.TokenURI != googleTokenURL:
		return key, fmt.Errorf("%w: the key's token_uri is not %s", ErrInvalid, googleTokenURL)
	}

	block, _ := pem.Decode([]byte(key.PrivateKey))
	if block == nil {
		return key, fmt.Errorf("%w: the key's private_key is not a PEM private key", ErrInvalid)
	}

	if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
		if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return key, fmt.Errorf("%w: the key's private_key does not read as an RSA key", ErrInvalid)
		}
	}

	return key, nil
}

func serviceAccountSecretName(integration, label string) string {
	return "integration-" + integration + "-" + label
}

// serviceAccountTokens is the token of a Connection kept in memory, with the secret it was made from.
type serviceAccountTokens struct {
	secret  string
	version int
	source  oauth2.TokenSource
}

// tokenSource is a key's token, signed and exchanged here and renewed before it ends (x/oauth2/jwt, which keeps
// it in a ReuseTokenSource).
func (s *Service) tokenSource(key serviceAccountKey) oauth2.TokenSource {
	tokenURL := key.TokenURI
	if s.googleTokenURL != "" {
		tokenURL = s.googleTokenURL
	}

	cfg := &jwt.Config{
		Email: key.ClientEmail, PrivateKey: []byte(key.PrivateKey), PrivateKeyID: key.PrivateKeyID,
		Scopes: []string{bigqueryScope}, TokenURL: tokenURL,
	}

	return cfg.TokenSource(context.WithValue(context.Background(), oauth2.HTTPClient, s.client))
}

// bigqueryClient is a client of BigQuery that sends each request with the token of the source.
func (s *Service) bigqueryClient(ctx context.Context, connection string, settings bigquery.Settings,
	source oauth2.TokenSource,
) (*bigquery.Client, error) {
	base := s.client.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	options := []option.ClientOption{
		option.WithHTTPClient(&http.Client{Transport: &oauth2.Transport{Source: source, Base: base}}),
	}
	if s.bigqueryEndpoint != "" {
		options = append(options, option.WithEndpoint(s.bigqueryEndpoint))
	}

	return bigquery.New(ctx, bigquery.Config{
		Settings: settings, Connection: connection, Logger: s.logger, Options: options,
	})
}

// ConnectServiceAccount checks a service account's key and the settings step by step and records them as a
// Connection under a label: the token's exchange, each dataset's tables, a native table read once, and a dry run
// in the billing project (bigquery.Client.Check). A failing step names itself and stores nothing. A key given
// replaces the one of the label.
func (s *Service) ConnectServiceAccount(ctx context.Context, integration, label string, sa ServiceAccount,
) (Connection, error) {
	d, err := s.definition(integration)
	if err != nil {
		return Connection{}, err
	}

	if !d.IsServiceAccount() {
		return Connection{}, fmt.Errorf("%w: %s is not connected with a service account's key", ErrInvalid, d.ID)
	}

	if !orchestration.ValidConnectionLabel(label) {
		return Connection{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as ga4", ErrInvalid)
	}

	secret := serviceAccountSecretName(d.ID, label)
	raw := sa.Key

	if len(raw) == 0 {
		opened, err := s.vault.Open(ctx, secret)
		if errors.Is(err, vault.ErrNotFound) {
			return Connection{}, fmt.Errorf("%w: give the service account's JSON key (--key-file)", ErrInvalid)
		}

		if err != nil {
			return Connection{}, vaultError(err)
		}

		raw = []byte(opened.Payload[vault.KeyValue])
	}

	key, err := parseServiceAccountKey(raw)
	if err != nil {
		return Connection{}, err
	}

	settings, err := sa.Settings.Check(key.ProjectID)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	source := s.tokenSource(key)
	if err := s.checkServiceAccount(ctx, orchestration.Use(d.ID, label), settings, source); err != nil {
		return Connection{}, err
	}

	encoded, err := json.Marshal(settings)
	if err != nil {
		return Connection{}, fmt.Errorf("encoding the settings: %w", err)
	}

	if len(sa.Key) > 0 {
		payload := vault.Payload{vault.KeyValue: string(raw)}

		_, err = s.vault.Create(ctx, vault.NewSecret{
			Name: secret, Kind: vault.KindGeneric, Payload: payload,
			Metadata: map[string]string{
				"integration": d.ID, "connection": label, "account": key.ClientEmail,
				"what": "a service account's JSON key",
			},
		})
		if errors.Is(err, vault.ErrConflict) {
			_, err = s.vault.Rotate(ctx, secret, payload)
		}

		if err != nil {
			return Connection{}, vaultError(err)
		}
	}

	s.dropServiceAccount(d.ID, label)

	saved, err := s.store.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: d.ID, Label: label, Account: key.ClientEmail, Secret: secret, Settings: string(encoded),
	})
	if err != nil {
		return Connection{}, err
	}

	// The token the check got serves the Connection's first calls.
	if kept, err := s.vault.Get(ctx, secret); err == nil {
		s.mu.Lock()
		s.serviceAccounts[accountKey(d.ID, label)] = serviceAccountTokens{
			secret: secret, version: kept.Version, source: source,
		}
		s.mu.Unlock()
	}

	return s.checked(ctx, d, saved), nil
}

// errTokenExchange marks a check that failed at its first step: Google gave no token for the key.
var errTokenExchange = errors.New("the check failed at the token's exchange")

// checkServiceAccount runs the connect check: the token first, then BigQuery's steps.
func (s *Service) checkServiceAccount(ctx context.Context, connection string, settings bigquery.Settings,
	source oauth2.TokenSource,
) error {
	if _, err := source.Token(); err != nil {
		return fmt.Errorf("%w: %w: %s", ErrSignInFailed, errTokenExchange, bigquery.Cause(err))
	}

	client, err := s.bigqueryClient(ctx, connection, settings, source)
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck // The client holds no state worth an error.

	if err := client.Check(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	return nil
}

// SettingsOf reads a BigQuery Connection's settings.
func settingsOf(conn orchestration.Connection) (bigquery.Settings, error) {
	var settings bigquery.Settings
	if err := json.Unmarshal([]byte(conn.Settings), &settings); err != nil {
		return settings, fmt.Errorf("%w: %s:%s has no settings; connect it again", ErrInvalid, conn.Integration,
			conn.Label)
	}

	return settings, nil
}

// serviceAccount is the token of a Connection, kept or made anew from its key.
func (s *Service) serviceAccount(ctx context.Context, conn orchestration.Connection) (oauth2.TokenSource, error) {
	k := accountKey(conn.Integration, conn.Label)

	described, err := s.vault.Get(ctx, conn.Secret)
	if err != nil {
		return nil, vaultError(err)
	}

	s.mu.Lock()
	kept, ok := s.serviceAccounts[k]
	s.mu.Unlock()

	if ok && kept.secret == conn.Secret && kept.version == described.Version {
		return kept.source, nil
	}

	opened, err := s.vault.Open(ctx, conn.Secret)
	if err != nil {
		return nil, vaultError(err)
	}

	key, err := parseServiceAccountKey([]byte(opened.Payload[vault.KeyValue]))
	if err != nil {
		return nil, err
	}

	source := s.tokenSource(key)

	s.mu.Lock()
	s.serviceAccounts[k] = serviceAccountTokens{secret: conn.Secret, version: opened.Version, source: source}
	s.mu.Unlock()

	return source, nil
}

func (s *Service) dropServiceAccount(integration, label string) {
	s.mu.Lock()
	delete(s.serviceAccounts, accountKey(integration, label))
	s.mu.Unlock()
}

// WithBigQuery runs do with a client of a Connection's BigQuery. A token Google refuses marks the Connection as
// needing to be connected again (design v3, decision 4).
func (s *Service) WithBigQuery(ctx context.Context, integration, label string, do func(*bigquery.Client) error,
) error {
	d, err := s.definition(integration)
	if err != nil {
		return err
	}

	if !d.IsServiceAccount() {
		return fmt.Errorf("%w: %s is not read with a service account", ErrInvalid, d.ID)
	}

	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return err
	}

	settings, err := settingsOf(conn)
	if err != nil {
		return err
	}

	source, err := s.serviceAccount(ctx, conn)
	if err != nil {
		return err
	}

	client, err := s.bigqueryClient(ctx, orchestration.Use(d.ID, label), settings, source)
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck // The client holds no state worth an error.

	err = do(client)

	var tokenErr *bigquery.TokenError

	switch {
	case errors.As(err, &tokenErr):
		s.dropServiceAccount(d.ID, label)
		_ = s.store.SetConnectionSignInFailed(ctx, d.ID, label, true)

		return fmt.Errorf("%w: %s: %s", ErrSignInRequired, orchestration.Use(d.ID, label), tokenErr.Message)
	case err == nil && conn.SignInFailed:
		_ = s.store.SetConnectionSignInFailed(ctx, d.ID, label, false)
	}

	return err
}

// SetMaxBytes changes the cap on the bytes one query of a BigQuery Connection may bill (design v3, decision 11).
func (s *Service) SetMaxBytes(ctx context.Context, integration, label string, maxBytes int64) (Connection, error) {
	d, err := s.definition(integration)
	if err != nil {
		return Connection{}, err
	}

	if !d.IsServiceAccount() {
		return Connection{}, fmt.Errorf("%w: %s has no bytes cap", ErrInvalid, d.ID)
	}

	if maxBytes <= 0 {
		return Connection{}, fmt.Errorf("%w: the bytes cap is a positive size, such as 10GiB", ErrInvalid)
	}

	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return Connection{}, err
	}

	settings, err := settingsOf(conn)
	if err != nil {
		return Connection{}, err
	}

	settings.MaxBytesBilled = maxBytes

	encoded, err := json.Marshal(settings)
	if err != nil {
		return Connection{}, fmt.Errorf("encoding the settings: %w", err)
	}

	changed, err := s.store.SetConnectionSettings(ctx, d.ID, label, string(encoded))
	if err != nil {
		return Connection{}, err
	}

	return s.view(ctx, d, changed), nil
}

// checkServiceAccountAgain is the owner's "Check again" of a BigQuery Connection: the connect check with the key
// kept. A token Google refuses marks it as needing to be connected again.
func (s *Service) checkServiceAccountAgain(ctx context.Context, d Definition, label string) error {
	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return err
	}

	settings, err := settingsOf(conn)
	if err != nil {
		return err
	}

	source, err := s.serviceAccount(ctx, conn)
	if err != nil {
		return err
	}

	err = s.checkServiceAccount(ctx, orchestration.Use(d.ID, label), settings, source)
	if errors.Is(err, errTokenExchange) {
		s.dropServiceAccount(d.ID, label)
		_ = s.store.SetConnectionSignInFailed(ctx, d.ID, label, true)
	} else if err == nil && conn.SignInFailed {
		_ = s.store.SetConnectionSignInFailed(ctx, d.ID, label, false)
	}

	return err
}

// forgetServiceAccount removes a Connection, its kept token and its key. The key still exists at Google until
// the data owner deletes it there.
func (s *Service) forgetServiceAccount(ctx context.Context, d Definition, conn orchestration.Connection) error {
	if _, err := s.store.RemoveConnection(ctx, d.ID, conn.Label); err != nil {
		return err
	}

	s.dropServiceAccount(d.ID, conn.Label)

	if err := s.vault.Delete(ctx, conn.Secret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return vaultError(err)
	}

	return nil
}

// WithGoogle sends a service account's token exchange and its BigQuery requests to other addresses than Google's:
// a test's.
func (s *Service) WithGoogle(tokenURL, bigqueryEndpoint string) *Service {
	s.googleTokenURL, s.bigqueryEndpoint = tokenURL, bigqueryEndpoint

	return s
}

// WithLogger gives the service the log its BigQuery jobs are written to.
func (s *Service) WithLogger(logger *slog.Logger) *Service {
	s.logger = logger

	return s
}
