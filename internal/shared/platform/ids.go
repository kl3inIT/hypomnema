package platform

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewID returns prefix + "_" + 26 random base32 characters (130 bits), such
// as "run_k3x…". The prefix says what the id names when it shows up in a log.
func NewID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms.

	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(b))
}
