package whatsapp

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// A device whatsmeow no longer keeps, because WhatsApp logged it out while the control plane was stopped, opens
// as needing a link rather than failing the start (MemoryOS bots.go, device).
func TestADeviceNoLongerKeptOpensLoggedOut(t *testing.T) {
	t.Parallel()

	svc, err := Open(t.Context(), filepath.Join(t.TempDir(), "whatsapp.db"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	acc, err := svc.Open(t.Context(), "84912345678:11@s.whatsapp.net")
	require.NoError(t, err)
	assert.Equal(t, "+84912345678", acc.Name())
	assert.True(t, acc.State().NeedsLink())
}

// The session's state follows WhatsApp's events as MemoryOS's bridge reports them (bots.go, handle): a drop of a
// connected session comes back by itself, while a logout, a ban or a replacement is not a mere drop.
func TestTheSessionFollowsWhatsAppsEvents(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		events []any
		want   messaging.Status
	}{
		"connected":               {[]any{&events.Connected{}}, messaging.StatusConnected},
		"dropped":                 {[]any{&events.Connected{}, &events.Disconnected{}}, messaging.StatusDisconnected},
		"logged out on the phone": {[]any{&events.Connected{}, &events.LoggedOut{}}, messaging.StatusLoggedOut},
		"replaced, then dropped": {
			[]any{&events.Connected{}, &events.StreamReplaced{}, &events.Disconnected{}}, messaging.StatusReplaced,
		},
		"banned":   {[]any{&events.TemporaryBan{Code: events.TempBanBlockedByUsers, Expire: time.Hour}}, messaging.StatusBanned},
		"outdated": {[]any{&events.ClientOutdated{}}, messaging.StatusOutdated},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			acc := &account{state: messaging.State{Status: messaging.StatusConnecting}}
			for _, e := range tc.events {
				acc.handle(e)
			}

			assert.Equal(t, tc.want, acc.State().Status)
		})
	}
}
