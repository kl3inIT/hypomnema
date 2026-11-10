package vault

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The associated data is a stored wire format: every sealed version
// authenticates against these exact bytes. A change here makes every stored
// secret unreadable, so the bytes are pinned.
func TestAssociatedDataBytesArePinned(t *testing.T) {
	t.Parallel()

	got, err := associatedData(
		identity{SecretID: "sec_1", VersionID: "secv_2", VersionNumber: 3, Kind: KindOAuthTokenSet},
		[]string{"refresh_token", "access_token"},
	)
	require.NoError(t, err)

	//nolint:testifylint // the exact bytes are the contract: JSONEq would accept another field order
	assert.Equal(t, `{"schema_version":1,"secret_id":"sec_1","version_id":"secv_2","version_number":3,`+
		`"kind":"oauth_token_set","payload_keys":["access_token","refresh_token"]}`, string(got))
}
