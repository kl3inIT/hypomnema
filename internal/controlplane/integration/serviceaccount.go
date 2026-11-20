package integration

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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
//
// The exchange is the JWT bearer grant (RFC 7523) as golang.org/x/oauth2/jwt sends it, written here so that it runs
// on the context of the call that needs the token: a 10-second dry run never waits for an exchange longer than
// itself (review round 1). The writes of a Connection (connect, disconnect, the cap, marking the key refused) are
// made one at a time, under saMu, so the vault's key and the Connection's row always go together.

// BigQueryID is the id of the BigQuery Integration.
const BigQueryID = "bigquery"

// googleTokenURL is where a service account's JWT is exchanged; a key that names another is refused (design v3,
// decision 1).
const googleTokenURL = "https://oauth2.googleapis.com/token" //nolint:gosec // Google's public address, no credential.

// bigqueryScope is the one scope asked: cloud-platform.read-only cannot run jobs.insert, a dry run included (design
// v3, decision 5; Toolbox bigquery.go:780-787).
const bigqueryScope = "https://www.googleapis.com/auth/bigquery"

const (
	// tokenLife is how long a JWT asks its token to last, Google's longest; tokenRenewBefore is how long before its
	// end a token is exchanged again.
	tokenLife        = time.Hour
	tokenRenewBefore = time.Minute
)

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
	Key []byte
	// Settings; a cap of zero keeps the label's cap, or is the default for a new label.
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

	rsa *rsa.PrivateKey
}

// maxKeyBytes bounds a key file: Google's are about 2.3 KB.
const maxKeyBytes = 16 << 10

// parseServiceAccountKey checks a key: a service account's, with its email, an RSA private key, its project and
// Google's token address. Its errors never hold the key.
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

	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		private, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return key, fmt.Errorf("%w: the key's private_key is not an RSA key", ErrInvalid)
		}

		key.rsa = private
	} else if private, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key.rsa = private
	} else {
		return key, fmt.Errorf("%w: the key's private_key does not read as an RSA key", ErrInvalid)
	}

	return key, nil
}

func serviceAccountSecretName(integration, label string) string {
	return "integration-" + integration + "-" + label
}

// saToken is the access token of one key, kept in memory and exchanged again, on the context of the call that
// needs it, shortly before it ends.
type saToken struct {
	key      serviceAccountKey
	tokenURL string
	client   *http.Client
	now      func() time.Time

	mu      sync.Mutex
	access  string
	expires time.Time
}

// Token is the key's access token, exchanged first when there is none or it is about to end.
func (t *saToken) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.access != "" && t.expires.Sub(t.now()) > tokenRenewBefore {
		return t.access, nil
	}

	access, expires, err := t.exchange(ctx)
	if err != nil {
		return "", err
	}

	t.access, t.expires = access, expires

	return access, nil
}

// exchange signs a JWT with the key and exchanges it for an access token. A refusal of the key itself is a
// bigquery.TokenError that is Refused; a 5xx or the network is one that is not.
func (t *saToken) exchange(ctx context.Context) (string, time.Time, error) {
	now := t.now()

	assertion, err := t.sign(now)
	if err != nil {
		return "", time.Time{}, err
	}

	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("building the token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // net/http would repeat the address.
		}

		return "", time.Time{}, &bigquery.TokenError{
			Message: "Google's token endpoint could not be reached: " + bigquery.Redact(err.Error()),
		}
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed; closing adds nothing.

	var body bytes.Buffer
	if _, err := body.ReadFrom(http.MaxBytesReader(nil, resp.Body, maxAnswerBytes)); err != nil {
		return "", time.Time{}, &bigquery.TokenError{Message: "Google's token answer could not be read"}
	}

	if resp.StatusCode != http.StatusOK {
		code := errorCode(body.Bytes())
		refused := resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized

		if refused {
			return "", time.Time{}, &bigquery.TokenError{
				Refused: true, Message: fmt.Sprintf("Google refused the service account's key: %d %s", resp.StatusCode, code),
			}
		}

		return "", time.Time{}, &bigquery.TokenError{
			Message: fmt.Sprintf("Google's token endpoint answered %d %s; try again later", resp.StatusCode, code),
		}
	}

	var answer tokenAnswer
	if json.Unmarshal(body.Bytes(), &answer) != nil || answer.AccessToken == "" {
		return "", time.Time{}, &bigquery.TokenError{Message: "Google's token answer lacks an access token"}
	}

	expires := now.Add(tokenLife)
	if answer.ExpiresIn > 0 {
		expires = now.Add(secondsOf(answer.ExpiresIn))
	}

	return answer.AccessToken, expires, nil
}

// sign makes the JWT of the grant: RS256, the key's id, the account as issuer, the bigquery scope, the token
// address as audience, good for an hour (x/oauth2/jwt and jws, the claims Google documents).
func (t *saToken) sign(now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": t.key.PrivateKeyID})
	if err != nil {
		return "", fmt.Errorf("writing the JWT: %w", err)
	}

	claims, err := json.Marshal(map[string]any{
		"iss": t.key.ClientEmail, "scope": bigqueryScope, "aud": t.tokenURL,
		"iat": now.Unix(), "exp": now.Add(tokenLife).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("writing the JWT: %w", err)
	}

	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))

	signature, err := rsa.SignPKCS1v15(rand.Reader, t.key.rsa, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing the JWT: %w", err)
	}

	return unsigned + "." + enc.EncodeToString(signature), nil
}

// saTransport sends each request with the key's token, got on the request's own context.
type saTransport struct {
	token *saToken
	base  http.RoundTripper
}

func (t saTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	access, err := t.token.Token(req.Context())
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}

		return nil, err
	}

	out := req.Clone(req.Context())
	out.Header.Set("Authorization", "Bearer "+access)

	return t.base.RoundTrip(out)
}

// newToken is the token of a key, kept from then on.
func (s *Service) newToken(key serviceAccountKey) *saToken {
	tokenURL := key.TokenURI
	if s.googleTokenURL != "" {
		tokenURL = s.googleTokenURL
	}

	return &saToken{key: key, tokenURL: tokenURL, client: s.client, now: s.clock.Now}
}

// serviceAccountTokens is the token of a Connection kept in memory, with the secret and its version it was made
// from.
type serviceAccountTokens struct {
	secret  string
	version int
	token   *saToken
}

// bigqueryClient is a client of BigQuery that sends each request with the key's token.
func (s *Service) bigqueryClient(ctx context.Context, connection string, settings bigquery.Settings, token *saToken,
) (*bigquery.Client, error) {
	base := s.client.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	options := []option.ClientOption{
		option.WithHTTPClient(&http.Client{Transport: saTransport{token: token, base: base}}),
	}
	if s.bigqueryEndpoint != "" {
		options = append(options, option.WithEndpoint(s.bigqueryEndpoint))
	}

	return bigquery.New(ctx, bigquery.Config{
		Settings: settings, Connection: connection, Logger: s.logger, Options: options,
	})
}

// errTokenExchange marks a check that failed at its first step, and errKeyRefused one where Google refused the key
// itself, not for a passing reason.
var (
	errTokenExchange = errors.New("the check failed at the token's exchange")
	errKeyRefused    = errors.New("the key was refused")
)

// ConnectServiceAccount checks a service account's key and the settings step by step and records them as a
// Connection under a label: the token's exchange, each dataset's tables, a native table read once, and a dry run
// in the billing project (bigquery.Client.Check). A failing step names itself and stores nothing. A key given
// replaces the one of the label; a Connection that cannot be saved leaves the vault as it was.
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
	raw, keptVersion := sa.Key, 0

	if len(raw) == 0 {
		opened, err := s.vault.Open(ctx, secret)
		if errors.Is(err, vault.ErrNotFound) {
			return Connection{}, fmt.Errorf("%w: give the service account's JSON key (--key-file)", ErrInvalid)
		}

		if err != nil {
			return Connection{}, vaultError(err)
		}

		raw, keptVersion = []byte(opened.Payload[vault.KeyValue]), opened.Version
	}

	key, err := parseServiceAccountKey(raw)
	if err != nil {
		return Connection{}, err
	}

	keepCap := sa.Settings.MaxBytesBilled == 0

	settings, err := sa.Settings.Check(key.ProjectID)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	token := s.newToken(key)
	if err := s.checkServiceAccount(ctx, orchestration.Use(d.ID, label), settings, token); err != nil {
		return Connection{}, err
	}

	s.saMu.Lock()
	defer s.saMu.Unlock()

	previous, hadSettings := s.previousSettings(ctx, d.ID, label)
	if keepCap && hadSettings {
		settings.MaxBytesBilled = previous.MaxBytesBilled
	}

	encoded, err := json.Marshal(settings)
	if err != nil {
		return Connection{}, fmt.Errorf("encoding the settings: %w", err)
	}

	version, undo, err := s.writeKey(ctx, d, label, secret, key, sa.Key, keptVersion)
	if err != nil {
		return Connection{}, err
	}

	saved, err := s.store.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: d.ID, Label: label, Account: key.ClientEmail, Secret: secret, Settings: string(encoded),
	})
	if err != nil {
		undo()

		return Connection{}, err
	}

	// The token the check got serves the Connection's first calls, under the version of its own key.
	s.mu.Lock()
	s.serviceAccounts[accountKey(d.ID, label)] = serviceAccountTokens{secret: secret, version: version, token: token}
	s.mu.Unlock()

	return s.checked(ctx, d, saved), nil
}

// previousSettings are the settings a label has, when it has a Connection.
func (s *Service) previousSettings(ctx context.Context, integration, label string) (bigquery.Settings, bool) {
	conn, err := s.store.ConnectionOf(ctx, integration, label)
	if err != nil {
		return bigquery.Settings{}, false
	}

	settings, err := settingsOf(conn)

	return settings, err == nil
}

// writeKey keeps a key given, creating or rotating the label's secret, and returns the version of the key the
// Connection uses and what undoes the write: the secret removed when it was made here, the old key put back when it
// was replaced. With no key given, the kept one must be the version that was checked. Under saMu.
func (s *Service) writeKey(ctx context.Context, d Definition, label, secret string, key serviceAccountKey,
	given []byte, keptVersion int,
) (int, func(), error) {
	if len(given) == 0 {
		kept, err := s.vault.Get(ctx, secret)
		if err != nil {
			return 0, nil, vaultError(err)
		}

		if kept.Version != keptVersion {
			return 0, nil, fmt.Errorf("%w: the key of %s changed while it was checked; connect again", ErrConflict,
				orchestration.Use(d.ID, label))
		}

		return kept.Version, func() {}, nil
	}

	payload := vault.Payload{vault.KeyValue: string(given)}

	old, err := s.vault.Open(ctx, secret)

	switch {
	case errors.Is(err, vault.ErrNotFound):
		made, err := s.vault.Create(ctx, vault.NewSecret{
			Name: secret, Kind: vault.KindGeneric, Payload: payload,
			Metadata: map[string]string{
				"integration": d.ID, "connection": label, "account": key.ClientEmail,
				"what": "a service account's JSON key",
			},
		})
		if err != nil {
			return 0, nil, vaultError(err)
		}

		return made.Version, func() { s.logged(ctx, "removing a key no Connection uses", s.vault.Delete(ctx, secret)) }, nil
	case err != nil:
		return 0, nil, vaultError(err)
	}

	rotated, err := s.vault.Rotate(ctx, secret, payload)
	if err != nil {
		return 0, nil, vaultError(err)
	}

	return rotated.Version, func() {
		_, err := s.vault.Rotate(ctx, secret, old.Payload)
		s.logged(ctx, "putting the replaced key back", err)
	}, nil
}

// logged writes a failure of a write that cleans up after another, which has its own error to answer.
func (s *Service) logged(ctx context.Context, doing string, err error) {
	if err == nil || s.logger == nil {
		return
	}

	s.logger.ErrorContext(ctx, "a BigQuery Connection's cleanup failed", slog.String("event", "integration.failed"),
		slog.String("doing", doing), slog.String("error", err.Error()))
}

// ErrConflict answers a write that another changed the ground of.
var ErrConflict = orchestration.ErrConflict

// checkServiceAccount runs the connect check: the token first, then BigQuery's steps.
func (s *Service) checkServiceAccount(ctx context.Context, connection string, settings bigquery.Settings,
	token *saToken,
) error {
	if _, err := token.Token(ctx); err != nil {
		var tokenErr *bigquery.TokenError
		if errors.As(err, &tokenErr) && tokenErr.Refused {
			return fmt.Errorf("%w: %w: %w: %s", ErrSignInFailed, errTokenExchange, errKeyRefused, tokenErr.Message)
		}

		return fmt.Errorf("%w: %w: %s", ErrSignInFailed, errTokenExchange, bigquery.Cause(err))
	}

	client, err := s.bigqueryClient(ctx, connection, settings, token)
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck // The client holds no state worth an error.

	if err := client.Check(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	return nil
}

// settingsOf reads a BigQuery Connection's settings.
func settingsOf(conn orchestration.Connection) (bigquery.Settings, error) {
	var settings bigquery.Settings
	if err := json.Unmarshal([]byte(conn.Settings), &settings); err != nil {
		return settings, fmt.Errorf("%w: %s:%s has no settings; connect it again", ErrInvalid, conn.Integration,
			conn.Label)
	}

	return settings, nil
}

// serviceAccount is the token of a Connection, kept or made anew from its key, with the version of that key.
func (s *Service) serviceAccount(ctx context.Context, conn orchestration.Connection) (*saToken, int, error) {
	k := accountKey(conn.Integration, conn.Label)

	described, err := s.vault.Get(ctx, conn.Secret)
	if err != nil {
		return nil, 0, vaultError(err)
	}

	s.mu.Lock()
	kept, ok := s.serviceAccounts[k]
	s.mu.Unlock()

	if ok && kept.secret == conn.Secret && kept.version == described.Version {
		return kept.token, kept.version, nil
	}

	opened, err := s.vault.Open(ctx, conn.Secret)
	if err != nil {
		return nil, 0, vaultError(err)
	}

	key, err := parseServiceAccountKey([]byte(opened.Payload[vault.KeyValue]))
	if err != nil {
		return nil, 0, err
	}

	token := s.newToken(key)

	// The version is the one the key was opened at, never a later read's.
	s.mu.Lock()
	s.serviceAccounts[k] = serviceAccountTokens{secret: conn.Secret, version: opened.Version, token: token}
	s.mu.Unlock()

	return token, opened.Version, nil
}

func (s *Service) dropServiceAccount(integration, label string) {
	s.mu.Lock()
	delete(s.serviceAccounts, accountKey(integration, label))
	s.mu.Unlock()
}

// markRefused records that Google refused a key, when it is still the Connection's: a key replaced meanwhile says
// nothing of the one that replaced it.
func (s *Service) markRefused(ctx context.Context, d Definition, label string, version int) {
	s.saMu.Lock()
	defer s.saMu.Unlock()

	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return
	}

	current, err := s.vault.Get(ctx, conn.Secret)
	if err != nil || current.Version != version {
		return
	}

	s.dropServiceAccount(d.ID, label)
	s.logged(ctx, "marking the key refused", s.store.SetConnectionSignInFailed(ctx, d.ID, label, true))
}

// markWorking clears a Connection's refused mark after a call with its key worked.
func (s *Service) markWorking(ctx context.Context, d Definition, conn orchestration.Connection) {
	if conn.SignInFailed {
		s.logged(ctx, "clearing the refused mark", s.store.SetConnectionSignInFailed(ctx, d.ID, conn.Label, false))
	}
}

// WithBigQuery runs do with a client of a Connection's BigQuery. A key Google refuses marks the Connection as
// needing to be connected again (design v3, decision 4); a passing failure of the exchange marks nothing.
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

	token, version, err := s.serviceAccount(ctx, conn)
	if err != nil {
		return err
	}

	client, err := s.bigqueryClient(ctx, orchestration.Use(d.ID, label), settings, token)
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck // The client holds no state worth an error.

	err = do(client)

	var tokenErr *bigquery.TokenError

	switch {
	case errors.As(err, &tokenErr) && tokenErr.Refused:
		s.markRefused(ctx, d, label, version)

		return fmt.Errorf("%w: %s: %s", ErrSignInRequired, orchestration.Use(d.ID, label), tokenErr.Message)
	case errors.As(err, &tokenErr):
		return fmt.Errorf("%w: %s", orchestration.ErrUnavailable, tokenErr.Message)
	case err == nil:
		s.markWorking(ctx, d, conn)
	}

	return err
}

// SetMaxBytes changes the cap on the bytes one query of a BigQuery Connection may bill (design v3, decision 11).
// It reads and writes the settings under saMu, so a reconnect meanwhile is never overwritten.
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

	s.saMu.Lock()
	defer s.saMu.Unlock()

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
// kept. Only a key Google refuses marks it as needing to be connected again.
func (s *Service) checkServiceAccountAgain(ctx context.Context, d Definition, label string) error {
	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return err
	}

	settings, err := settingsOf(conn)
	if err != nil {
		return err
	}

	token, version, err := s.serviceAccount(ctx, conn)
	if err != nil {
		return err
	}

	err = s.checkServiceAccount(ctx, orchestration.Use(d.ID, label), settings, token)

	switch {
	case errors.Is(err, errKeyRefused):
		s.markRefused(ctx, d, label, version)
	case err == nil:
		s.markWorking(ctx, d, conn)
	}

	return err
}

// forgetServiceAccount removes a Connection, its kept token and its key, under saMu so that a reconnect cannot
// come between. The key still exists at Google until the data owner deletes it there.
func (s *Service) forgetServiceAccount(ctx context.Context, d Definition, conn orchestration.Connection) error {
	s.saMu.Lock()
	defer s.saMu.Unlock()

	if _, err := s.store.RemoveConnection(ctx, d.ID, conn.Label); err != nil {
		return err
	}

	s.dropServiceAccount(d.ID, conn.Label)

	if err := s.vault.Delete(ctx, conn.Secret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return vaultError(err)
	}

	return nil
}

// cleanupServiceAccount finishes a disconnect whose key's removal failed: the label has no Connection, and its key
// is removed when it is still in the vault. It says whether there was one.
func (s *Service) cleanupServiceAccount(ctx context.Context, d Definition, label string) (bool, error) {
	s.saMu.Lock()
	defer s.saMu.Unlock()

	if _, err := s.store.ConnectionOf(ctx, d.ID, label); err == nil {
		return false, nil // Connected again meanwhile: its key is its own.
	}

	secret := serviceAccountSecretName(d.ID, label)

	err := s.vault.Delete(ctx, secret)

	switch {
	case errors.Is(err, vault.ErrNotFound):
		return false, nil
	case err != nil:
		return false, vaultError(err)
	}

	s.dropServiceAccount(d.ID, label)

	return true, nil
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
