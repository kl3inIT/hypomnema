package orchestration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

func TestMigrationRepairsInboxConnectionLabels(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	before := migrationVersion(t, "_drop_dispatch_resources.sql")
	db, _ := databaseAt(t, before)
	svc := New(db, platform.SystemClock{}, platform.NewID)

	_, err := svc.SaveServiceConnection(ctx, Connection{
		Integration: "whatsapp", Label: "main", Account: "+84 369 230 426", Secret: "conn-whatsapp-main",
	})
	require.NoError(t, err)

	for id, account := range map[string]string{
		"tdo_phone": "84369230426", "tdo_label": "main", "tdo_unknown": "84911111111",
	} {
		_, err := db.ExecContext(ctx, `INSERT INTO todos (
			id, title, state, source_kind, source_id, source_account, source_ref, created_by_kind, created_at, updated_at
		) VALUES (?, 'Reply to Kai', 'open', 'whatsapp', '8491@s.whatsapp.net', ?,
			'{"kind":"whatsapp_message","ref":"m1"}', 'bot', 1, 1)`, id, account)
		require.NoError(t, err)
	}

	require.NoError(t, Migrate(ctx, db))

	for id, want := range map[string]string{
		"tdo_phone": "main", "tdo_label": "main", "tdo_unknown": "84911111111",
	} {
		item, err := svc.Inbox().Get(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, want, item.Source.Account, id)
	}
}
