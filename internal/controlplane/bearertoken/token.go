// Package bearertoken makes and checks the client API's tokens (issue #41).
// The format follows Omnara's internal/bearertoken (Apache-2.0): a prefix that
// names the kind and version, 43 Base62 characters of secret (256 bits), and a
// 6-character CRC32 checksum, so a mistyped or foreign token is refused before
// the database is read. Only a token's SHA-256 is stored.
package bearertoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
)

// Kind is what a token may do.
type Kind string

// Kinds.
const (
	// Owner may do everything.
	Owner Kind = "owner"
	// Coordinator may change its own Runs and read the rest.
	Coordinator Kind = "coordinator"
	// Gateway is a key for the model gateway (issue #20), not for the client
	// API: it calls models and nothing else.
	Gateway Kind = "gateway"
)

// Kinds lists every kind.
var Kinds = []Kind{Owner, Coordinator, Gateway}

// ClientKinds lists the kinds the client API accepts.
var ClientKinds = []Kind{Owner, Coordinator}

const (
	alphabet       = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	secretLength   = 43
	checksumLength = 6
	separator      = '_'
	// Bytes from this value up are skipped, so every character is equally likely.
	unbiasedLimit = byte(256 - 256%len(alphabet))
)

// ErrInvalid means the string is not a well-formed token.
var ErrInvalid = errors.New("invalid bearer token")

func prefix(k Kind) string { return "hyp_" + string(k) + "_v1_" }

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}

	return false
}

// Generate returns a new token of kind k.
func Generate(k Kind) (string, error) {
	return generate(k, rand.Reader)
}

func generate(k Kind, source io.Reader) (string, error) {
	if !k.Valid() {
		return "", fmt.Errorf("%w: unknown kind %q", ErrInvalid, k)
	}

	secret, err := randomBase62(source, secretLength)
	if err != nil {
		return "", fmt.Errorf("making a %s token: %w", k, err)
	}

	body := prefix(k) + secret

	return body + string(separator) + checksum(body), nil
}

// Parse checks a token's form and checksum and returns its kind.
func Parse(token string) (Kind, error) {
	for _, k := range Kinds {
		p := prefix(k)
		if !strings.HasPrefix(token, p) {
			continue
		}

		if len(token) != len(p)+secretLength+1+checksumLength {
			return "", fmt.Errorf("%w: wrong length", ErrInvalid)
		}

		sep := len(p) + secretLength
		body, sum := token[:sep], token[sep+1:]

		switch {
		case token[sep] != separator:
			return "", fmt.Errorf("%w: no checksum separator", ErrInvalid)
		case !base62(token[len(p):sep]) || !base62(sum):
			return "", fmt.Errorf("%w: not Base62", ErrInvalid)
		case sum != checksum(body):
			return "", fmt.Errorf("%w: checksum mismatch", ErrInvalid)
		}

		return k, nil
	}

	return "", fmt.Errorf("%w: unknown prefix", ErrInvalid)
}

// Hash is what the database keeps of a token: its SHA-256, in hex.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

func base62(s string) bool {
	for i := range len(s) {
		if !strings.ContainsRune(alphabet, rune(s[i])) {
			return false
		}
	}

	return true
}

func randomBase62(source io.Reader, n int) (string, error) {
	out := make([]byte, 0, n)

	var buf [64]byte

	for len(out) < n {
		read, err := source.Read(buf[:])

		for _, b := range buf[:read] {
			if b >= unbiasedLimit || len(out) == n {
				continue
			}

			out = append(out, alphabet[int(b)%len(alphabet)])
		}

		if len(out) == n {
			break
		}

		if err != nil {
			return "", fmt.Errorf("reading random bytes: %w", err)
		}

		if read == 0 {
			return "", io.ErrNoProgress
		}
	}

	return string(out), nil
}

func checksum(body string) string {
	v := crc32.ChecksumIEEE([]byte(body))

	var out [checksumLength]byte

	for i := checksumLength - 1; i >= 0; i-- {
		out[i] = alphabet[v%uint32(len(alphabet))]
		v /= uint32(len(alphabet))
	}

	return string(out[:])
}
