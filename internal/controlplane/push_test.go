package controlplane

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/controlplane/webpush"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario: the owner fears the VAPID key leaked and makes new keys (issue #1047). The command makes another pair
// and removes every subscribed device in the same step, since none takes a push signed with the new key; it prints
// how many, never a key. Without the vault's root key it refuses.
func TestRegeneratingTheVAPIDKeysRemovesEveryDevice(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	path := sqlitetest.Migrated(t, orchestration.Migrate)
	cfg := vaultConfig(t, path, "k1", keygen(t, "k1"))

	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)
	keys := webpush.NewKeys(vault.New(db, platform.SystemClock{}, platform.NewID, cfg.VaultKeys, nil))

	private, before, err := keys.Pair(ctx)
	require.NoError(t, err)

	for _, endpoint := range []string{"https://web.push.apple.com/a", "https://fcm.googleapis.com/fcm/send/b"} {
		_, err := svc.SavePushSubscription(ctx, orchestration.NewPushSubscription{Endpoint: endpoint, P256dh: "k", Auth: "a"})
		require.NoError(t, err)
	}

	var out bytes.Buffer

	run := func(cfg Config, args ...string) error { return Command(ctx, cfg, args, strings.NewReader(""), &out) }

	require.NoError(t, run(cfg, "push", "regenerate-keys"))
	assert.Contains(t, out.String(), "2 devices removed")
	assert.NotContains(t, out.String(), private)

	subs, err := svc.ListPushSubscriptions(ctx)
	require.NoError(t, err)
	assert.Empty(t, subs)

	_, after, err := keys.Pair(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, before, after)

	noVault, err := LoadConfig(func(name string) string {
		return map[string]string{"HYPOMNEMA_DATABASE_PATH": path}[name]
	})
	require.NoError(t, err)
	require.Error(t, run(noVault, "push", "regenerate-keys"))
	require.ErrorIs(t, run(cfg, "push"), ErrUsage)
}
