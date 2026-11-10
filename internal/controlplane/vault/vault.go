// Package vault is the control plane's secret store (issue #66): API keys and
// subscription token sets, encrypted at rest. It is the only package that
// holds the root keys and the only one that reads or writes the secrets and
// secret_versions tables. A value leaves it only through Open, for callers
// inside the control plane; nothing here returns a value to the client API, a
// coordinator or a log.
//
// It follows Omnara's secret manager (internal/secrets, internal/storage):
// versions under one name, a data key per version wrapped by a root key from
// a keyring, the secret's identity as associated data, and a rewrap that
// rotates the root key without touching a value. Credential Sessions per
// Dispatch and the node's proxy are the vault's other half and are not here.
package vault

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Errors callers match with errors.Is.
var (
	// ErrNotConfigured: no keyring is set, so nothing can be sealed or opened.
	ErrNotConfigured = errors.New("the vault is not configured")
	// ErrNotFound: no secret has this name.
	ErrNotFound = errors.New("not found")
	// ErrConflict: a secret with this name exists.
	ErrConflict = errors.New("conflict")
	// ErrInvalid: the name, kind, payload or metadata is wrong.
	ErrInvalid = errors.New("invalid request")
	// ErrInUse: something still refers to the secret.
	ErrInUse = errors.New("the secret is in use")
	// ErrKeyUnknown: the version was sealed under a root key that is not in the keyring.
	ErrKeyUnknown = errors.New("the root key is not in the keyring")
	// ErrTampered: the stored version does not authenticate: it was changed,
	// moved to another secret, or a root key has the right id and the wrong bytes.
	ErrTampered = errors.New("the stored secret does not authenticate")
	// ErrLoginRequired: the provider refused the token set's refresh token for
	// good; the owner logs in again and rotates the secret.
	ErrLoginRequired = errors.New("the login is no longer accepted; log in again")
	// ErrLoginRejected is what a RefreshFunc wraps when the provider refused
	// the refresh token for good (reused, expired, revoked), as opposed to a
	// failure that may pass.
	ErrLoginRejected = errors.New("the provider rejected the refresh token")
	// ErrRefreshFailed: the refresh did not complete and may be tried again;
	// the stored token set is unchanged.
	ErrRefreshFailed = errors.New("the refresh did not complete")
)

// Kind is what a secret holds.
type Kind string

// The kinds, as Omnara's generic and oauth_token_set.
const (
	// KindGeneric is one value under the key "value": an API key, a token.
	KindGeneric Kind = "generic"
	// KindOAuthTokenSet is a refresh token with, optionally, the access and id
	// tokens last issued for it.
	KindOAuthTokenSet Kind = "oauth_token_set" //nolint:gosec // the name of a kind, not a credential
)

// Payload keys.
const (
	KeyValue        = "value"
	KeyAccessToken  = "access_token"
	KeyRefreshToken = "refresh_token"
	KeyIDToken      = "id_token"
)

const (
	// maxValueBytes bounds one payload value, as Omnara's MaxPayloadValueBytes.
	maxValueBytes = 64 * 1024
	// maxMetadataBytes bounds a secret's metadata as stored.
	maxMetadataBytes = 4 * 1024
	// refPrefix marks a reference to a secret where a name of a key's location is expected.
	refPrefix = "vault:"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Payload is a secret's value: named strings, never logged or formatted.
type Payload map[string]string

// String keeps a payload out of logs and error messages.
func (p Payload) String() string { return fmt.Sprintf("payload with %d values", len(p)) }

func (p Payload) keys() []string { return slices.Sorted(maps.Keys(p)) }

// Secret describes a secret without its value.
type Secret struct {
	ID   string
	Name string
	Kind Kind
	// Metadata is not secret: what the owner needs to recognize the secret.
	Metadata map[string]string
	// Version counts from 1 and grows with each rotation.
	Version int
	// KeyID names the root key the current version is sealed under;
	// KeyMissing says that key is not in the keyring, so the secret cannot be
	// opened until it is put back.
	KeyID      string
	KeyMissing bool
	// AccessTokenExpiresAt is when a token set's access token ends, if known.
	AccessTokenExpiresAt *time.Time
	// LoginRequired says the provider refused the refresh token for good.
	LoginRequired bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Opened is a secret's value with what a caller needs to use and refresh it.
type Opened struct {
	Payload Payload
	// Version is the version the payload is; a caller that finds it stale
	// names it to Refresh.
	Version int
	// AccessTokenExpiresAt is when a token set's access token ends, if known.
	AccessTokenExpiresAt *time.Time
	// LoginRequired says the refresh token is dead; the access token may
	// still work until it ends.
	LoginRequired bool
}

// NewSecret is what the owner gives to create a secret.
type NewSecret struct {
	Name     string
	Kind     Kind
	Payload  Payload
	Metadata map[string]string
}

// Ref returns how another record names this secret as the place a key is
// kept, such as a provider's api_key_ref.
func Ref(name string) string { return refPrefix + name }

// Referrers lists what still refers to a secret, by the reference Ref gives.
type Referrers func(ctx context.Context, ref string) ([]string, error)

// Service performs the vault's operations.
type Service struct {
	db        *sql.DB
	clock     platform.Clock
	newID     func(prefix string) string
	keyring   *Keyring
	referrers Referrers

	// refreshing holds the refresh running for each secret in this process,
	// so callers that arrive meanwhile share its result.
	mu         sync.Mutex
	refreshing map[string]*refreshCall
}

// New returns a Service over an opened, migrated database. A nil keyring
// leaves the vault not configured: secrets can be listed and deleted, and
// nothing can be sealed or opened. referrers may be nil.
func New(
	db *sql.DB, clock platform.Clock, newID func(prefix string) string, keyring *Keyring, referrers Referrers,
) *Service {
	return &Service{
		db: db, clock: clock, newID: newID, keyring: keyring, referrers: referrers,
		refreshing: map[string]*refreshCall{},
	}
}

// write runs fn in one transaction, as orchestration does: the connection
// opens every transaction with BEGIN IMMEDIATE (internal/shared/sqlite).
func (s *Service) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	if err := fn(store.New(tx), s.clock.Now().UnixMilli()); err != nil {
		return errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	return nil
}

// Create stores a new secret as its first version.
func (s *Service) Create(ctx context.Context, n NewSecret) (Secret, error) {
	if s.keyring == nil {
		return Secret{}, ErrNotConfigured
	}

	if !namePattern.MatchString(n.Name) {
		return Secret{}, fmt.Errorf("%w: a secret's name is lowercase letters, digits and dashes", ErrInvalid)
	}

	if err := validPayload(n.Kind, n.Payload); err != nil {
		return Secret{}, err
	}

	metadata, err := encodeMetadata(n.Metadata)
	if err != nil {
		return Secret{}, err
	}

	id := identity{SecretID: s.newID("sec"), VersionID: s.newID("secv"), VersionNumber: 1, Kind: n.Kind}

	sealedVersion, err := seal(s.keyring, id, n.Payload)
	if err != nil {
		return Secret{}, err
	}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if _, err := q.GetSecretByName(ctx, n.Name); err == nil {
			return fmt.Errorf("%w: secret %q exists; rotate it to change its value", ErrConflict, n.Name)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reading secret %q: %w", n.Name, err)
		}

		if err := q.InsertSecret(ctx, store.InsertSecretParams{
			ID: id.SecretID, Name: n.Name, Kind: string(n.Kind), Metadata: metadata,
			CurrentVersionID: id.VersionID, Now: now,
		}); err != nil {
			return fmt.Errorf("storing secret %q: %w", n.Name, err)
		}

		return insertVersion(ctx, q, id, sealedVersion, nil, now)
	})
	if err != nil {
		return Secret{}, fmt.Errorf("creating a secret: %w", err)
	}

	return s.Get(ctx, n.Name)
}

// Rotate stores a new value as the secret's next version, which becomes the
// one in use. Earlier versions stay, encrypted. For a token set it is how a
// new login is recorded, and it clears the mark of a login that is required.
func (s *Service) Rotate(ctx context.Context, name string, p Payload) (Secret, error) {
	if s.keyring == nil {
		return Secret{}, ErrNotConfigured
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, current, err := currentVersion(ctx, q, name)
		if err != nil {
			return err
		}

		if err := validPayload(Kind(row.Kind), p); err != nil {
			return err
		}

		id := identity{
			SecretID: row.ID, VersionID: s.newID("secv"), VersionNumber: current.VersionNumber + 1, Kind: Kind(row.Kind),
		}

		sealedVersion, err := seal(s.keyring, id, p)
		if err != nil {
			return err
		}

		if err := insertVersion(ctx, q, id, sealedVersion, nil, now); err != nil {
			return err
		}

		if err := q.SetCurrentSecretVersion(ctx, store.SetCurrentSecretVersionParams{
			CurrentVersionID: id.VersionID, Now: now, ID: row.ID,
		}); err != nil {
			return fmt.Errorf("pointing secret %q at its new version: %w", name, err)
		}

		return nil
	})
	if err != nil {
		return Secret{}, fmt.Errorf("rotating a secret: %w", err)
	}

	return s.Get(ctx, name)
}

// Delete removes a secret and every version of it. It is refused while
// something refers to the secret. The check and the delete are two steps; a
// reference added between them is not seen, which one owner can live with.
func (s *Service) Delete(ctx context.Context, name string) error {
	if s.referrers != nil {
		users, err := s.referrers(ctx, Ref(name))
		if err != nil {
			return fmt.Errorf("finding what uses secret %q: %w", name, err)
		}

		if len(users) > 0 {
			return fmt.Errorf("%w: %q is used by %s", ErrInUse, name, strings.Join(users, ", "))
		}
	}

	return s.write(ctx, func(q *store.Queries, _ int64) error {
		row, err := q.GetSecretByName(ctx, name)
		if err != nil {
			return notFound(err, name)
		}

		if err := q.DeleteSecret(ctx, row.ID); err != nil {
			return fmt.Errorf("deleting secret %q: %w", name, err)
		}

		return nil
	})
}

// Get describes one secret.
func (s *Service) Get(ctx context.Context, name string) (Secret, error) {
	q := store.New(s.db)

	row, current, err := currentVersion(ctx, q, name)
	if err != nil {
		return Secret{}, err
	}

	return s.describe(row, current)
}

// List describes every secret, by name.
func (s *Service) List(ctx context.Context) ([]Secret, error) {
	q := store.New(s.db)

	rows, err := q.ListSecrets(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing secrets: %w", err)
	}

	out := make([]Secret, 0, len(rows))

	for _, row := range rows {
		current, err := q.GetSecretVersion(ctx, row.CurrentVersionID)
		if err != nil {
			return nil, fmt.Errorf("reading the version of secret %q: %w", row.Name, err)
		}

		sec, err := s.describe(row, current)
		if err != nil {
			return nil, err
		}

		out = append(out, sec)
	}

	return out, nil
}

// Open returns a secret's value, for callers inside the control plane that
// must send it to its provider. The caller keeps it out of logs and errors.
func (s *Service) Open(ctx context.Context, name string) (Opened, error) {
	if s.keyring == nil {
		return Opened{}, ErrNotConfigured
	}

	row, current, err := currentVersion(ctx, store.New(s.db), name)
	if err != nil {
		return Opened{}, err
	}

	return s.openVersion(row, current)
}

func (s *Service) openVersion(row store.Secret, current store.SecretVersion) (Opened, error) {
	stored, err := sealedFromRow(current)
	if err != nil {
		return Opened{}, err
	}

	p, err := open(s.keyring, identityOf(current, Kind(row.Kind)), stored)
	if err != nil {
		return Opened{}, fmt.Errorf("opening secret %q: %w", row.Name, err)
	}

	return Opened{
		Payload: p, Version: int(current.VersionNumber),
		AccessTokenExpiresAt: optionalTime(current.AccessTokenExpiresAt), LoginRequired: row.LoginRequiredAt.Valid,
	}, nil
}

// Rewrap wraps the data key of every version under the active root key and
// returns how many it changed. It changes nothing when one version cannot be
// unwrapped, so a missing old key is noticed before it is dropped for good.
func (s *Service) Rewrap(ctx context.Context) (int, error) {
	if s.keyring == nil {
		return 0, ErrNotConfigured
	}

	changed := 0

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		rows, err := q.ListSecretVersions(ctx)
		if err != nil {
			return fmt.Errorf("listing versions: %w", err)
		}

		for _, r := range rows {
			if r.KeyID == s.keyring.active {
				continue
			}

			version := versionOf(r)

			stored, err := sealedFromRow(version)
			if err != nil {
				return err
			}

			next, err := rewrap(s.keyring, identityOf(version, Kind(r.SecretKind)), stored)
			if err != nil {
				return fmt.Errorf("rewrapping version %d of secret %s: %w", r.VersionNumber, r.SecretID, err)
			}

			if err := q.RewrapSecretVersion(ctx, store.RewrapSecretVersionParams{
				KeyID: next.KeyID, EncryptedDek: next.WrappedKey, EncryptedDekNonce: next.WrappedNonce, ID: r.ID,
			}); err != nil {
				return fmt.Errorf("storing the rewrapped key of version %s: %w", r.ID, err)
			}

			changed++
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("rewrapping: %w", err)
	}

	return changed, nil
}

func (s *Service) describe(row store.Secret, current store.SecretVersion) (Secret, error) {
	metadata := map[string]string{}
	if err := json.Unmarshal([]byte(row.Metadata), &metadata); err != nil {
		return Secret{}, fmt.Errorf("decoding the metadata of secret %q: %w", row.Name, err)
	}

	return Secret{
		ID: row.ID, Name: row.Name, Kind: Kind(row.Kind), Metadata: metadata,
		Version: int(current.VersionNumber), KeyID: current.KeyID,
		KeyMissing:           s.keyring != nil && !s.keyring.has(current.KeyID),
		AccessTokenExpiresAt: optionalTime(current.AccessTokenExpiresAt),
		LoginRequired:        row.LoginRequiredAt.Valid,
		CreatedAt:            time.UnixMilli(row.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(row.UpdatedAt).UTC(),
	}, nil
}

func currentVersion(ctx context.Context, q *store.Queries, name string) (store.Secret, store.SecretVersion, error) {
	row, err := q.GetSecretByName(ctx, name)
	if err != nil {
		return store.Secret{}, store.SecretVersion{}, notFound(err, name)
	}

	current, err := q.GetSecretVersion(ctx, row.CurrentVersionID)
	if err != nil {
		return store.Secret{}, store.SecretVersion{}, fmt.Errorf("reading the version of secret %q: %w", name, err)
	}

	return row, current, nil
}

func optionalTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}

	t := time.UnixMilli(v.Int64).UTC()

	return &t
}

// insertVersion stores a sealed version; expires is when a token set's access
// token ends, nil when unknown.
func insertVersion(
	ctx context.Context, q *store.Queries, id identity, s sealed, expires *time.Time, now int64,
) error {
	var expiresAt sql.NullInt64
	if expires != nil {
		expiresAt = sql.NullInt64{Int64: expires.UnixMilli(), Valid: true}
	}

	keys, err := json.Marshal(s.PayloadKeys)
	if err != nil {
		return fmt.Errorf("encoding the payload's key names: %w", err)
	}

	if err := q.InsertSecretVersion(ctx, store.InsertSecretVersionParams{
		ID: id.VersionID, SecretID: id.SecretID, VersionNumber: id.VersionNumber, PayloadKeys: string(keys),
		EncryptionScheme: schemeEnvelopeV1, KeyID: s.KeyID, EncryptedDek: s.WrappedKey,
		EncryptedDekNonce: s.WrappedNonce, Nonce: s.Nonce, Ciphertext: s.Ciphertext,
		AccessTokenExpiresAt: expiresAt, Now: now,
	}); err != nil {
		return fmt.Errorf("storing version %d: %w", id.VersionNumber, err)
	}

	return nil
}

func identityOf(v store.SecretVersion, kind Kind) identity {
	return identity{SecretID: v.SecretID, VersionID: v.ID, VersionNumber: v.VersionNumber, Kind: kind}
}

func sealedFromRow(v store.SecretVersion) (sealed, error) {
	if v.EncryptionScheme != schemeEnvelopeV1 {
		return sealed{}, fmt.Errorf("%w: unknown encryption scheme %q", ErrTampered, v.EncryptionScheme)
	}

	var keys []string
	if err := json.Unmarshal([]byte(v.PayloadKeys), &keys); err != nil {
		return sealed{}, fmt.Errorf("%w: the payload's key names do not decode", ErrTampered)
	}

	return sealed{
		KeyID: v.KeyID, WrappedKey: v.EncryptedDek, WrappedNonce: v.EncryptedDekNonce,
		Nonce: v.Nonce, Ciphertext: v.Ciphertext, PayloadKeys: keys,
	}, nil
}

func versionOf(r store.ListSecretVersionsRow) store.SecretVersion {
	return store.SecretVersion{
		ID: r.ID, SecretID: r.SecretID, VersionNumber: r.VersionNumber, PayloadKeys: r.PayloadKeys,
		EncryptionScheme: r.EncryptionScheme, KeyID: r.KeyID, EncryptedDek: r.EncryptedDek,
		EncryptedDekNonce: r.EncryptedDekNonce, Nonce: r.Nonce, Ciphertext: r.Ciphertext,
		AccessTokenExpiresAt: r.AccessTokenExpiresAt, CreatedAt: r.CreatedAt,
	}
}

// validPayload checks a payload against its kind without ever quoting a value.
func validPayload(kind Kind, p Payload) error {
	var required, allowed []string

	switch kind {
	case KindGeneric:
		required, allowed = []string{KeyValue}, []string{KeyValue}
	case KindOAuthTokenSet:
		required, allowed = []string{KeyRefreshToken}, []string{KeyAccessToken, KeyRefreshToken, KeyIDToken}
	default:
		return fmt.Errorf("%w: kind is %s or %s", ErrInvalid, KindGeneric, KindOAuthTokenSet)
	}

	for _, key := range required {
		if _, ok := p[key]; !ok {
			return fmt.Errorf("%w: a %s secret needs %q", ErrInvalid, kind, key)
		}
	}

	for key, value := range p {
		switch {
		case !slices.Contains(allowed, key):
			return fmt.Errorf("%w: a %s secret has no %q", ErrInvalid, kind, key)
		case value == "":
			return fmt.Errorf("%w: %q is empty", ErrInvalid, key)
		case len(value) > maxValueBytes:
			return fmt.Errorf("%w: %q is over %d bytes", ErrInvalid, key, maxValueBytes)
		}
	}

	return nil
}

func encodeMetadata(m map[string]string) (string, error) {
	if m == nil {
		m = map[string]string{}
	}

	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encoding the metadata: %w", err)
	}

	if len(b) > maxMetadataBytes {
		return "", fmt.Errorf("%w: the metadata is over %d bytes", ErrInvalid, maxMetadataBytes)
	}

	return string(b), nil
}

func notFound(err error, name string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: secret %q", ErrNotFound, name)
	}

	return fmt.Errorf("reading secret %q: %w", name, err)
}
