package telegram

import (
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tgerr"
	"github.com/stretchr/testify/assert"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// The design's row of states (issue #1184), from gotd's AuthLostKindOf: banned is Banned; a session ended elsewhere,
// unregistered, expired or of a deleted account must be linked again; AUTH_KEY_DUPLICATED is another connection with
// the same key; anything else is a drop gotd recovers from.
func TestTheSessionsStateFollowsTelegramsErrors(t *testing.T) {
	t.Parallel()

	rpc := func(code int, kind string) error { return &tgerr.Error{Code: code, Type: kind} }

	for name, tc := range map[string]struct {
		err      error
		status   messaging.Status
		terminal bool
	}{
		"banned":             {rpc(401, "USER_DEACTIVATED_BAN"), messaging.StatusBanned, true},
		"number banned":      {rpc(400, "PHONE_NUMBER_BANNED"), messaging.StatusBanned, true},
		"revoked":            {rpc(401, "SESSION_REVOKED"), messaging.StatusLoggedOut, true},
		"unregistered":       {rpc(401, "AUTH_KEY_UNREGISTERED"), messaging.StatusLoggedOut, true},
		"expired":            {rpc(401, "SESSION_EXPIRED"), messaging.StatusLoggedOut, true},
		"deleted":            {rpc(401, "USER_DEACTIVATED"), messaging.StatusLoggedOut, true},
		"key dropped":        {rpc(401, "AUTH_KEY_DROP_DUPLICATED"), messaging.StatusLoggedOut, true},
		"replaced":           {rpc(406, "AUTH_KEY_DUPLICATED"), messaging.StatusReplaced, true},
		"lost, as Run says":  {&telegram.AuthLostError{Err: rpc(401, "SESSION_REVOKED")}, messaging.StatusLoggedOut, true},
		"a network drop":     {errors.New("connection reset"), messaging.StatusDisconnected, false},
		"a flood wait error": {rpc(420, "FLOOD_WAIT_30"), messaging.StatusDisconnected, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.status, stateOf(tc.err).Status)
			assert.Equal(t, tc.terminal, terminal(tc.err))
		})
	}
}

// A frozen account is shown with the date Telegram deletes it unless the owner appeals, and the appeal's link; it
// keeps reading, so it is not an end of the session.
func TestAFrozenAccountIsShownWithItsEndDate(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	until := since.AddDate(0, 1, 0)

	st := frozenState(telegram.FreezeState{Since: since, Until: until, AppealURL: "https://t.me/spambot"})
	assert.Equal(t, messaging.StatusBanned, st.Status)
	assert.Equal(t, until, st.Until)
	assert.Contains(t, st.Reason, "frozen")
	assert.Contains(t, st.Reason, "https://t.me/spambot")

	a := testAccount(t)
	a.froze(telegram.FreezeState{Since: since, Until: until})
	a.connected()
	assert.Equal(t, messaging.StatusBanned, a.State().Status, "a frozen account stays shown frozen when it connects")
	assert.Equal(t, until, a.State().Until)
}

// A drop is shown until gotd connects the session back, and a session that never connected is not shown open by a
// connection alone: it is open once the updates manager starts.
func TestADroppedSessionComesBack(t *testing.T) {
	t.Parallel()

	a := testAccount(t)

	a.reconnected()
	assert.Equal(t, messaging.StatusConnecting, a.State().Status)

	a.setClient(newFakeTelegram())
	a.connected()
	a.dropped()
	assert.Equal(t, messaging.StatusDisconnected, a.State().Status)

	a.reconnected()
	assert.Equal(t, messaging.StatusConnected, a.State().Status)

	a.setState(messaging.State{Status: messaging.StatusLoggedOut})
	a.dropped()
	a.reconnected()
	assert.Equal(t, messaging.StatusLoggedOut, a.State().Status, "a logged out session is not merely dropped")
}
