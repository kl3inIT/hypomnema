package platform

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// idLength is the length of an id's random part: 16 bytes in base32.
const idLength = 26

// NewID returns prefix + "_" + 26 random base32 characters (130 bits), such
// as "run_k3x…". The prefix says what the id names when it shows up in a log.
func NewID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms.

	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(b))
}

// IsID reports whether id has the shape NewID(prefix) gives: the prefix, "_",
// and 26 characters of its alphabet. A client that chooses an id before the
// thing exists (issue #357) is held to it.
func IsID(prefix, id string) bool {
	rest, ok := strings.CutPrefix(id, prefix+"_")
	if !ok || len(rest) != idLength {
		return false
	}

	for _, c := range rest {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}

	return true
}
