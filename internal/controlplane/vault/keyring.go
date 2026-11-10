package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	rootKeySize = 32
	dataKeySize = 32
	nonceSize   = 12
)

var keyIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Keyring holds the root keys, each under an id, and says which one wraps new
// data keys. It follows Omnara's LocalKeyWrapper (internal/secrets/secrets.go).
// Its keys are never printed: it has no exported field and formats as a count.
type Keyring struct {
	active string
	keys   map[string][]byte
}

// ParseKeyring reads the keyring from its two settings: keys is a
// comma-separated list of id:base64 entries of 32 bytes each, active the id
// new data keys are wrapped with. Both empty means no vault (a nil Keyring);
// one without the other is an error, as Omnara's configuration.
func ParseKeyring(keys, active string) (*Keyring, error) {
	keys, active = strings.TrimSpace(keys), strings.TrimSpace(active)

	switch {
	case keys == "" && active == "":
		return nil, nil //nolint:nilnil // No keyring is a valid configuration: the vault is not set up.
	case keys == "" || active == "":
		return nil, errors.New("the vault's keys and its active key id are set together or not at all")
	}

	k := &Keyring{active: active, keys: map[string][]byte{}}

	for entry := range strings.SplitSeq(keys, ",") {
		id, encoded, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok || !keyIDPattern.MatchString(id) {
			return nil, errors.New("a vault key is id:base64, its id lowercase letters, digits and dashes")
		}

		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(key) != rootKeySize {
			return nil, fmt.Errorf("vault key %q is not %d bytes of base64", id, rootKeySize)
		}

		if _, dup := k.keys[id]; dup {
			return nil, fmt.Errorf("vault key id %q is listed twice", id)
		}

		for other, material := range k.keys {
			if subtle.ConstantTimeCompare(material, key) == 1 {
				return nil, fmt.Errorf("vault keys %q and %q are the same key", other, id)
			}
		}

		k.keys[id] = key
	}

	if _, ok := k.keys[active]; !ok {
		return nil, fmt.Errorf("the active vault key %q is not among the keys", active)
	}

	return k, nil
}

// GenerateKey returns a new root key as an id:base64 entry for the keyring.
func GenerateKey(id string) (string, error) {
	if !keyIDPattern.MatchString(id) {
		return "", fmt.Errorf("%w: a key id is lowercase letters, digits and dashes", ErrInvalid)
	}

	return id + ":" + base64.StdEncoding.EncodeToString(randomBytes(rootKeySize)), nil
}

// String keeps a keyring out of logs and error messages.
func (k *Keyring) String() string {
	if k == nil {
		return "no vault keys"
	}

	return fmt.Sprintf("%d vault keys, active %s", len(k.keys), k.active)
}

func (k *Keyring) has(id string) bool {
	_, ok := k.keys[id]

	return ok
}

// wrap encrypts a data key under the active root key.
func (k *Keyring) wrap(dataKey, associated []byte) (keyID string, wrapped, nonce []byte, err error) {
	aead, err := newGCM(k.keys[k.active])
	if err != nil {
		return "", nil, nil, err
	}

	nonce = randomBytes(nonceSize)

	return k.active, aead.Seal(nil, nonce, dataKey, associated), nonce, nil
}

// unwrap decrypts a data key with the root key it names.
func (k *Keyring) unwrap(keyID string, wrapped, nonce, associated []byte) ([]byte, error) {
	root, ok := k.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrKeyUnknown, keyID)
	}

	aead, err := newGCM(root)
	if err != nil {
		return nil, err
	}

	if len(nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("%w: the data key's nonce has the wrong size", ErrTampered)
	}

	dataKey, err := aead.Open(nil, nonce, wrapped, associated)
	if err != nil || len(dataKey) != dataKeySize {
		return nil, fmt.Errorf("%w: the data key does not open under key %q", ErrTampered, keyID)
	}

	return dataKey, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating the cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating AES-GCM: %w", err)
	}

	return aead, nil
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms.

	return b
}
