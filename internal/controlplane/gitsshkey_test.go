package controlplane

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// Scenario: the owner asks for the SSH key the nodes clone with (issue #625)
// the first time. It is made then, an ed25519 key, and kept in the vault,
// sealed; each later read, however many come at once, gives the same pair.
// The public half is the private half's, with the comment that names
// Hypomnema on the git host's list of keys.
func TestTheSSHKeyIsMadeOnceAndKeptInTheVault(t *testing.T) {
	t.Parallel()

	tokens, _, path := tokensWorld(t, true)
	key := &gitSSHKey{vault: tokens.vault}
	ctx := t.Context()

	var (
		wg   sync.WaitGroup
		pubs [4]string
	)

	for i := range pubs {
		wg.Go(func() {
			public, err := key.PublicKey(ctx)
			assert.NoError(t, err)

			pubs[i] = public
		})
	}

	wg.Wait()

	for _, public := range pubs {
		assert.Equal(t, pubs[0], public, "one pair, however many asked first")
	}

	assert.True(t, strings.HasPrefix(pubs[0], "ssh-ed25519 "), pubs[0])
	assert.True(t, strings.HasSuffix(pubs[0], " hypomnema"), pubs[0])

	private, public, err := key.Pair(ctx)
	require.NoError(t, err)
	assert.Equal(t, pubs[0], public)

	signer, err := ssh.ParsePrivateKey([]byte(private))
	require.NoError(t, err, "the private half is in OpenSSH's form, which ssh reads")

	authorized, _, _, _, err := ssh.ParseAuthorizedKey([]byte(public))
	require.NoError(t, err)
	assert.Equal(t, signer.PublicKey().Marshal(), authorized.Marshal(), "the public half is the private half's")

	secret, err := tokens.vault.Get(ctx, gitSSHKeySecret)
	require.NoError(t, err)
	assert.Equal(t, vault.KindGeneric, secret.Kind)
	assert.Equal(t, 1, secret.Version)

	raw, err := os.ReadFile(path) //nolint:gosec // the test's own database
	require.NoError(t, err)
	assert.NotContains(t, string(raw), strings.Split(private, "\n")[1], "the database holds the key sealed")
}

// Scenario: a vault that was never set up keeps no key, and the owner is told
// how to set it up.
func TestNoSSHKeyWithoutAVault(t *testing.T) {
	t.Parallel()

	tokens, _, _ := tokensWorld(t, false)
	key := &gitSSHKey{vault: tokens.vault}

	_, err := key.PublicKey(t.Context())
	require.ErrorIs(t, err, orchestration.ErrUnavailable)
	assert.ErrorContains(t, err, "HYPOMNEMA_VAULT_KEYS")
}
