package bearertoken

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateThenParse(t *testing.T) {
	t.Parallel()

	for _, k := range Kinds {
		token, err := Generate(k)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(token, "hyp_"+string(k)+"_v1_"), token)

		got, err := Parse(token)
		require.NoError(t, err)
		assert.Equal(t, k, got)
	}
}

func TestParseRefusesDamagedTokens(t *testing.T) {
	t.Parallel()

	token, err := Generate(Owner)
	require.NoError(t, err)

	flip := func(i int) string {
		b := []byte(token)
		if b[i] == 'a' {
			b[i] = 'b'
		} else {
			b[i] = 'a'
		}

		return string(b)
	}

	for name, bad := range map[string]string{
		"empty":          "",
		"node token":     "hyn_abc",
		"unknown kind":   strings.Replace(token, "hyp_owner_", "hyp_admin_", 1),
		"short":          token[:len(token)-1],
		"secret changed": flip(len("hyp_owner_v1_") + 3),
		"sum changed":    flip(len(token) - 1),
		"not base62":     token[:len(token)-1] + "-",
	} {
		_, err := Parse(bad)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
}

// Bytes at or above the unbiased limit are skipped, so each character is
// equally likely; a source that runs dry is an error, not a short token.
func TestRandomBase62SkipsBiasedBytesAndFailsOnShortSource(t *testing.T) {
	t.Parallel()

	got, err := randomBase62(bytes.NewReader([]byte{255, 0, 249, 61, 1}), 3)
	require.NoError(t, err)
	assert.Equal(t, "0z1", got)

	_, err = randomBase62(bytes.NewReader([]byte{1, 2}), 3)
	require.Error(t, err)
}
