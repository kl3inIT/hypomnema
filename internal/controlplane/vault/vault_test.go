package vault_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

const apiKey = "sk-live-0123456789abcdef-this-must-never-be-stored-in-the-clear"

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) }

func newDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	return db
}

// newKey returns a keyring entry for a new root key.
func newKey(t *testing.T, id string) string {
	t.Helper()

	entry, err := vault.GenerateKey(id)
	require.NoError(t, err)

	return entry
}

func keyring(t *testing.T, active string, entries ...string) *vault.Keyring {
	t.Helper()

	k, err := vault.ParseKeyring(strings.Join(entries, ","), active)
	require.NoError(t, err)

	return k
}

// ids are unique across the services of one test binary, since several
// services may share a database.
var ids atomic.Int64

func newVault(db *sql.DB, k *vault.Keyring, referrers vault.Referrers) *vault.Service {
	return vault.New(db, fixedClock{}, func(prefix string) string {
		return fmt.Sprintf("%s_%04d", prefix, ids.Add(1))
	}, k, referrers)
}

func generic(value string) vault.NewSecret {
	return vault.NewSecret{Name: "router", Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: value}}
}

// Scenario: the owner stores an API key. The value comes back only through
// Open, and no column of the database holds it in the clear.
func TestASecretIsStoredEncryptedAndOpensToItsValue(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)

	s, err := v.Create(t.Context(), vault.NewSecret{
		Name: "router", Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: apiKey},
		Metadata: map[string]string{"owner": "me"},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, s.Version)
	assert.Equal(t, "k1", s.KeyID)
	assert.False(t, s.KeyMissing)
	assert.Equal(t, map[string]string{"owner": "me"}, s.Metadata)

	p, err := v.Open(t.Context(), "router")
	require.NoError(t, err)
	assert.Equal(t, apiKey, p.Payload[vault.KeyValue])

	for _, table := range []string{"secrets", "secret_versions"} {
		assert.NotContains(t, dump(t, db, table), apiKey, "table %s", table)
	}

	assert.NotContains(t, fmt.Sprintf("%v %s %+v", p, p.Payload, p), apiKey, "a payload must not format as its value")
}

// dump returns every column of every row of a table as text.
func dump(t *testing.T, db *sql.DB, table string) string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), "SELECT * FROM "+table) //nolint:gosec // a fixed table name
	require.NoError(t, err)

	defer rows.Close() //nolint:errcheck // a test read

	cols, err := rows.Columns()
	require.NoError(t, err)

	var out strings.Builder

	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))

		for i := range values {
			ptrs[i] = &values[i]
		}

		require.NoError(t, rows.Scan(ptrs...))

		for _, value := range values {
			if b, ok := value.([]byte); ok {
				out.Write(b)
			} else {
				fmt.Fprint(&out, value)
			}

			out.WriteByte('\n')
		}
	}

	require.NoError(t, rows.Err())

	return out.String()
}

// Scenario: someone edits the database. A changed ciphertext, wrapped key or
// nonce, and a version moved to another secret, version number or kind, all
// fail to open instead of yielding a value.
func TestAChangedOrMovedVersionDoesNotOpen(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"ciphertext":      `UPDATE secret_versions SET ciphertext = zeroblob(length(ciphertext)) WHERE secret_id = :a`,
		"wrapped key":     `UPDATE secret_versions SET encrypted_dek = zeroblob(48) WHERE secret_id = :a`,
		"wrapped nonce":   `UPDATE secret_versions SET encrypted_dek_nonce = zeroblob(12) WHERE secret_id = :a`,
		"payload nonce":   `UPDATE secret_versions SET nonce = zeroblob(12) WHERE secret_id = :a`,
		"version number":  `UPDATE secret_versions SET version_number = 7 WHERE secret_id = :a`,
		"kind":            `UPDATE secrets SET kind = 'oauth_token_set' WHERE id = :a`,
		"payload key set": `UPDATE secret_versions SET payload_keys = '["refresh_token"]' WHERE secret_id = :a`,
		// The other secret's whole envelope, under this secret's identity.
		"another secret's version": `UPDATE secret_versions SET
			(encrypted_dek, encrypted_dek_nonce, nonce, ciphertext) =
			(SELECT encrypted_dek, encrypted_dek_nonce, nonce, ciphertext FROM secret_versions WHERE secret_id = :b)
			WHERE secret_id = :a`,
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db := newDB(t)
			v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)

			a, err := v.Create(t.Context(), generic(apiKey))
			require.NoError(t, err)

			other := generic("another value")
			other.Name = "other"
			b, err := v.Create(t.Context(), other)
			require.NoError(t, err)

			_, err = db.ExecContext(t.Context(), change, sql.Named("a", a.ID), sql.Named("b", b.ID))
			require.NoError(t, err)

			p, err := v.Open(t.Context(), "router")
			require.ErrorIs(t, err, vault.ErrTampered)
			assert.Empty(t, p.Payload)
			assert.NotContains(t, err.Error(), apiKey)
		})
	}
}

// Scenario: the owner rotates a key. The new value is the one in use, the
// version grows, and a value that does not fit the secret's kind is refused
// without changing anything.
func TestRotateMakesANewVersionTheOneInUse(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)

	_, err := v.Create(t.Context(), generic("first"))
	require.NoError(t, err)

	s, err := v.Rotate(t.Context(), "router", vault.Payload{vault.KeyValue: "second"})
	require.NoError(t, err)
	assert.Equal(t, 2, s.Version)

	_, err = v.Rotate(t.Context(), "router", vault.Payload{vault.KeyRefreshToken: "not a generic secret"})
	require.ErrorIs(t, err, vault.ErrInvalid)

	_, err = v.Rotate(t.Context(), "missing", vault.Payload{vault.KeyValue: "x"})
	require.ErrorIs(t, err, vault.ErrNotFound)

	p, err := v.Open(t.Context(), "router")
	require.NoError(t, err)
	assert.Equal(t, "second", p.Payload[vault.KeyValue])

	listed, err := v.List(t.Context())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, 2, listed[0].Version)
}

// Scenario: the owner replaces the root key. Rewrap moves every version,
// earlier ones included, under the new key without changing a ciphertext,
// after which the old key can be dropped.
func TestRewrapMovesEveryVersionUnderTheActiveKey(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	k1, k2 := newKey(t, "k1"), newKey(t, "k2")

	old := newVault(db, keyring(t, "k1", k1), nil)
	_, err := old.Create(t.Context(), generic("first"))
	require.NoError(t, err)
	_, err = old.Rotate(t.Context(), "router", vault.Payload{vault.KeyValue: apiKey})
	require.NoError(t, err)

	before := column(t, db, "ciphertext")

	both := newVault(db, keyring(t, "k2", k1, k2), nil)
	n, err := both.Rewrap(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	assert.Equal(t, before, column(t, db, "ciphertext"), "rewrap must not re-encrypt a value")
	assert.Equal(t, []string{"k2", "k2"}, column(t, db, "key_id"))

	again, err := both.Rewrap(t.Context())
	require.NoError(t, err)
	assert.Zero(t, again)

	onlyNew := newVault(db, keyring(t, "k2", k2), nil)
	p, err := onlyNew.Open(t.Context(), "router")
	require.NoError(t, err)
	assert.Equal(t, apiKey, p.Payload[vault.KeyValue])
}

func column(t *testing.T, db *sql.DB, name string) []string {
	t.Helper()

	//nolint:gosec // a fixed column name
	rows, err := db.QueryContext(t.Context(), "SELECT "+name+" FROM secret_versions ORDER BY version_number")
	require.NoError(t, err)

	defer rows.Close() //nolint:errcheck // a test read

	var out []string

	for rows.Next() {
		var b []byte

		require.NoError(t, rows.Scan(&b))
		out = append(out, string(b))
	}

	require.NoError(t, rows.Err())

	return out
}

// Scenario: the old root key was removed before rewrapping. The secret is
// reported as unopenable, Open says which key is missing, and Rewrap changes
// nothing, so putting the key back recovers everything.
func TestAMissingRootKeyIsReportedAndNothingIsRewritten(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	k1, k2 := newKey(t, "k1"), newKey(t, "k2")

	_, err := newVault(db, keyring(t, "k1", k1), nil).Create(t.Context(), generic(apiKey))
	require.NoError(t, err)

	before := dump(t, db, "secret_versions")
	onlyNew := newVault(db, keyring(t, "k2", k2), nil)

	_, err = onlyNew.Open(t.Context(), "router")
	require.ErrorIs(t, err, vault.ErrKeyUnknown)
	assert.Contains(t, err.Error(), `"k1"`)

	_, err = onlyNew.Rewrap(t.Context())
	require.ErrorIs(t, err, vault.ErrKeyUnknown)

	listed, err := onlyNew.List(t.Context())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.True(t, listed[0].KeyMissing)

	assert.Equal(t, before, dump(t, db, "secret_versions"))

	p, err := newVault(db, keyring(t, "k2", k1, k2), nil).Open(t.Context(), "router")
	require.NoError(t, err)
	assert.Equal(t, apiKey, p.Payload[vault.KeyValue])
}

// Scenario: a root key has the right id and the wrong bytes (a mistyped
// environment). The secret does not open, and the error does not blame a
// missing key.
func TestAWrongKeyUnderTheRightIDDoesNotOpen(t *testing.T) {
	t.Parallel()

	db := newDB(t)

	_, err := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil).Create(t.Context(), generic(apiKey))
	require.NoError(t, err)

	_, err = newVault(db, keyring(t, "k1", newKey(t, "k1")), nil).Open(t.Context(), "router")
	require.ErrorIs(t, err, vault.ErrTampered)
}

// Scenario: the control plane runs without vault keys. Secrets can still be
// listed and deleted, and nothing is sealed or opened: there is no path that
// stores a value in the clear.
func TestWithoutKeysNothingIsSealedOrOpened(t *testing.T) {
	t.Parallel()

	db := newDB(t)

	_, err := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil).Create(t.Context(), generic(apiKey))
	require.NoError(t, err)

	v := newVault(db, nil, nil)

	_, err = v.Create(t.Context(), vault.NewSecret{
		Name: "second", Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: "x"},
	})
	require.ErrorIs(t, err, vault.ErrNotConfigured)

	_, err = v.Rotate(t.Context(), "router", vault.Payload{vault.KeyValue: "x"})
	require.ErrorIs(t, err, vault.ErrNotConfigured)

	_, err = v.Open(t.Context(), "router")
	require.ErrorIs(t, err, vault.ErrNotConfigured)

	_, err = v.Rewrap(t.Context())
	require.ErrorIs(t, err, vault.ErrNotConfigured)

	listed, err := v.List(t.Context())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "router", listed[0].Name)

	require.NoError(t, v.Delete(t.Context(), "router"))
}

// Scenario: a provider still names the secret. Deleting it is refused and
// says who uses it; once nothing does, the secret and its versions go.
func TestDeleteIsRefusedWhileTheSecretIsUsed(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	users := []string{"provider router"}

	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), func(_ context.Context, ref string) ([]string, error) {
		if ref != "vault:router" {
			return nil, nil
		}

		return users, nil
	})

	_, err := v.Create(t.Context(), generic(apiKey))
	require.NoError(t, err)
	_, err = v.Rotate(t.Context(), "router", vault.Payload{vault.KeyValue: "second"})
	require.NoError(t, err)

	err = v.Delete(t.Context(), "router")
	require.ErrorIs(t, err, vault.ErrInUse)
	assert.Contains(t, err.Error(), "provider router")

	users = nil

	require.NoError(t, v.Delete(t.Context(), "router"))
	assert.Empty(t, dump(t, db, "secret_versions"))

	_, err = v.Get(t.Context(), "router")
	require.ErrorIs(t, err, vault.ErrNotFound)
	require.ErrorIs(t, v.Delete(t.Context(), "router"), vault.ErrNotFound)
}

// Scenario: what the owner gives is wrong. Each case is refused with
// ErrInvalid or ErrConflict, and no error message repeats a value.
func TestCreateRefusesWhatDoesNotFit(t *testing.T) {
	t.Parallel()

	db := newDB(t)
	v := newVault(db, keyring(t, "k1", newKey(t, "k1")), nil)

	_, err := v.Create(t.Context(), generic("first"))
	require.NoError(t, err)

	tokens := vault.Payload{vault.KeyRefreshToken: apiKey, vault.KeyAccessToken: "access"}
	_, err = v.Create(t.Context(), vault.NewSecret{Name: "codex", Kind: vault.KindOAuthTokenSet, Payload: tokens})
	require.NoError(t, err)

	cases := map[string]struct {
		in   vault.NewSecret
		want error
	}{
		"a name already taken": {generic(apiKey), vault.ErrConflict},
		"a name with a slash": {
			vault.NewSecret{Name: "a/b", Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: apiKey}},
			vault.ErrInvalid,
		},
		"an unknown kind": {
			vault.NewSecret{Name: "x", Kind: "aws", Payload: vault.Payload{vault.KeyValue: apiKey}}, vault.ErrInvalid,
		},
		"a generic secret without its value": {
			vault.NewSecret{Name: "x", Kind: vault.KindGeneric, Payload: vault.Payload{}}, vault.ErrInvalid,
		},
		"a generic secret with an extra entry": {
			vault.NewSecret{
				Name: "x", Kind: vault.KindGeneric,
				Payload: vault.Payload{vault.KeyValue: apiKey, vault.KeyRefreshToken: apiKey},
			},
			vault.ErrInvalid,
		},
		"a token set without its refresh token": {
			vault.NewSecret{
				Name: "x", Kind: vault.KindOAuthTokenSet, Payload: vault.Payload{vault.KeyAccessToken: apiKey},
			},
			vault.ErrInvalid,
		},
		"an empty value": {
			vault.NewSecret{Name: "x", Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: ""}},
			vault.ErrInvalid,
		},
		"a value over the limit": {
			vault.NewSecret{
				Name: "x", Kind: vault.KindGeneric,
				Payload: vault.Payload{vault.KeyValue: strings.Repeat(apiKey, 2000)},
			},
			vault.ErrInvalid,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := v.Create(t.Context(), c.in)
			require.ErrorIs(t, err, c.want)
			assert.NotContains(t, err.Error(), apiKey)
		})
	}
}

// The keyring's two settings come together, name 32-byte keys once each, and
// the active id is one of them.
func TestParseKeyring(t *testing.T) {
	t.Parallel()

	k1 := newKey(t, "k1")
	material := strings.TrimPrefix(k1, "k1:")

	none, err := vault.ParseKeyring(" ", "")
	require.NoError(t, err)
	assert.Nil(t, none)

	bad := map[string][2]string{
		"keys without an active id": {k1, ""},
		"an active id without keys": {"", "k1"},
		"an active id not listed":   {k1, "k2"},
		"an entry without an id":    {material, "k1"},
		"a key that is not base64":  {"k1:not base64!", "k1"},
		"a key of the wrong size":   {"k1:c2hvcnQ=", "k1"},
		"an id listed twice":        {k1 + "," + newKey(t, "k1"), "k1"},
		"one key under two ids":     {k1 + ",k2:" + material, "k1"},
	}

	for name, c := range bad {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			k, err := vault.ParseKeyring(c[0], c[1])
			require.Error(t, err)
			assert.Nil(t, k)
			assert.NotContains(t, err.Error(), material)
		})
	}

	k, err := vault.ParseKeyring(k1+" , "+newKey(t, "k2"), "k2")
	require.NoError(t, err)
	assert.NotContains(t, fmt.Sprintf("%v %+v", k, k), material, "a keyring must not format as its keys")
}
