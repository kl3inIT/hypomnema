package telegram

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// Until the owner keeps his application's api_id and api_hash in the vault, no account can connect, and the service
// says the api_id is missing; a value that is no api_id is said as such.
func TestTheAPIIDIsReadFromTheVault(t *testing.T) {
	t.Parallel()

	const (
		ready = iota
		missing
		invalid
	)

	for name, tc := range map[string]struct {
		secrets Secrets
		want    int
	}{
		"none":            {fakeSecrets{}, missing},
		"no hash":         {fakeSecrets{APIIDSecret: "12345"}, missing},
		"hash, no id yet": {fakeSecrets{APIHashSecret: "abc"}, missing},
		"an empty id":     {fakeSecrets{APIIDSecret: " ", APIHashSecret: "abc"}, missing},
		"no vault key":    {notConfigured{}, missing},
		"not a number":    {fakeSecrets{APIIDSecret: "my app", APIHashSecret: "abc"}, invalid},
		"zero":            {fakeSecrets{APIIDSecret: "0", APIHashSecret: "abc"}, invalid},
		"both, as made":   {fakeSecrets{APIIDSecret: " 12345\n", APIHashSecret: "0123456789abcdef"}, ready},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := openService(t, "", tc.secrets).Ready(t.Context())

			switch tc.want {
			case ready:
				require.NoError(t, err)
			case missing:
				require.ErrorIs(t, err, ErrNoAPIID)
				assert.Contains(t, err.Error(), "api_id is missing")
			default:
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrNoAPIID)
			}
		})
	}
}

type notConfigured struct{}

func (notConfigured) Open(context.Context, string) (vault.Opened, error) {
	return vault.Opened{}, vault.ErrNotConfigured
}

// An account whose session is no longer kept opens needing a link, and one kept opens disconnected while the api_id
// is missing, saying so; both still read what was kept.
func TestAnAccountOpensWithoutConnectingWhenItCannot(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "telegram.db")
	svc := openService(t, path, fakeSecrets{})

	gone, err := svc.Open(t.Context(), "42")
	require.NoError(t, err)
	assert.True(t, gone.State().NeedsLink())
	assert.Equal(t, "42", gone.ID())

	require.NoError(t, svc.store.keepSession(t.Context(), "42", []byte(`{}`)))
	require.NoError(t, svc.store.nameSession(t.Context(), "42", "+84912345678"))

	k := keptText(1, "kept before")
	_, err = svc.store.keepMessage(t.Context(), "42", k, fallbackChat(k.chat))
	require.NoError(t, err)

	acc, err := svc.Open(t.Context(), "42")
	require.NoError(t, err)
	t.Cleanup(acc.Close)

	assert.Equal(t, messaging.StatusDisconnected, acc.State().Status)
	assert.Contains(t, acc.State().Reason, "api_id is missing")
	assert.Equal(t, "+84912345678", acc.Name())

	msgs, err := acc.Messages(t.Context(), messaging.Query{})
	require.NoError(t, err)
	assert.Len(t, msgs, 1)

	_, err = svc.Open(t.Context(), "user:42")
	require.Error(t, err, "an account is opened by its user id")
}

// A logout forgets the session and what was kept of the account, after Telegram ended it.
func TestALogoutForgetsTheAccount(t *testing.T) {
	t.Parallel()

	a := testAccount(t)
	ctx := t.Context()
	server := newFakeTelegram()

	require.NoError(t, a.store.keepSession(ctx, a.key, []byte(`{}`)))
	_, err := a.store.keepMessage(ctx, a.key, keptText(1, "hello"), fallbackChat("user:7"))
	require.NoError(t, err)

	a.setClient(server)
	require.NoError(t, a.Logout(ctx))
	assert.True(t, server.loggedOut)

	_, _, err = a.store.session(ctx, a.key)
	require.ErrorIs(t, err, errNoSession)

	chats, err := a.Chats(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, chats)
}
