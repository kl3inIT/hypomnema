package vault

import (
	"encoding/json"
	"fmt"
	"slices"
)

// schemeEnvelopeV1 names the encryption of a secret version, as Omnara's
// aes-256-gcm-envelope-v1: the payload under a data key of its own, the data
// key under a root key, both with AES-256-GCM and the same associated data.
const schemeEnvelopeV1 = "aes-256-gcm-envelope-v1"

// identity is what a sealed version is bound to. A version moved to another
// secret, another version number or another kind does not open.
type identity struct {
	SecretID      string
	VersionID     string
	VersionNumber int64
	Kind          Kind
}

// sealed is one encrypted version as it is stored.
type sealed struct {
	KeyID        string
	WrappedKey   []byte
	WrappedNonce []byte
	Nonce        []byte
	Ciphertext   []byte
	PayloadKeys  []string
}

// associatedData is a persisted wire format: changing a field, a tag or their
// order makes every stored secret unreadable. A test pins the exact bytes.
func associatedData(id identity, payloadKeys []string) ([]byte, error) {
	keys := slices.Clone(payloadKeys)
	slices.Sort(keys)

	b, err := json.Marshal(struct {
		SchemaVersion int      `json:"schema_version"`
		SecretID      string   `json:"secret_id"`
		VersionID     string   `json:"version_id"`
		VersionNumber int64    `json:"version_number"`
		Kind          Kind     `json:"kind"`
		PayloadKeys   []string `json:"payload_keys"`
	}{1, id.SecretID, id.VersionID, id.VersionNumber, id.Kind, keys})
	if err != nil {
		return nil, fmt.Errorf("encoding the associated data: %w", err)
	}

	return b, nil
}

// seal encrypts a validated payload for one version.
func seal(k *Keyring, id identity, p Payload) (sealed, error) {
	keys := p.keys()

	associated, err := associatedData(id, keys)
	if err != nil {
		return sealed{}, err
	}

	body, err := json.Marshal(p)
	if err != nil {
		return sealed{}, fmt.Errorf("encoding the payload: %w", err)
	}

	dataKey := randomBytes(dataKeySize)

	aead, err := newGCM(dataKey)
	if err != nil {
		return sealed{}, err
	}

	keyID, wrapped, wrappedNonce, err := k.wrap(dataKey, associated)
	if err != nil {
		return sealed{}, err
	}

	nonce := randomBytes(nonceSize)

	return sealed{
		KeyID: keyID, WrappedKey: wrapped, WrappedNonce: wrappedNonce,
		Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, body, associated), PayloadKeys: keys,
	}, nil
}

// open decrypts a version and checks that it holds what its envelope says.
func open(k *Keyring, id identity, s sealed) (Payload, error) {
	associated, err := associatedData(id, s.PayloadKeys)
	if err != nil {
		return nil, err
	}

	dataKey, err := k.unwrap(s.KeyID, s.WrappedKey, s.WrappedNonce, associated)
	if err != nil {
		return nil, err
	}

	aead, err := newGCM(dataKey)
	if err != nil {
		return nil, err
	}

	if len(s.Nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("%w: the payload's nonce has the wrong size", ErrTampered)
	}

	body, err := aead.Open(nil, s.Nonce, s.Ciphertext, associated)
	if err != nil {
		return nil, fmt.Errorf("%w: the payload does not open", ErrTampered)
	}

	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("%w: the payload is not a map of strings", ErrTampered)
	}

	want := slices.Clone(s.PayloadKeys)
	slices.Sort(want)

	if !slices.Equal(p.keys(), want) {
		return nil, fmt.Errorf("%w: the payload's keys are not the envelope's", ErrTampered)
	}

	return p, nil
}

// rewrap wraps a version's data key under the active root key. The payload's
// ciphertext is not touched, so no secret value is decrypted.
func rewrap(k *Keyring, id identity, s sealed) (sealed, error) {
	associated, err := associatedData(id, s.PayloadKeys)
	if err != nil {
		return sealed{}, err
	}

	dataKey, err := k.unwrap(s.KeyID, s.WrappedKey, s.WrappedNonce, associated)
	if err != nil {
		return sealed{}, err
	}

	s.KeyID, s.WrappedKey, s.WrappedNonce, err = k.wrap(dataKey, associated)

	return s, err
}
