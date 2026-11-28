package telegram

// The session's state from Telegram's errors (issue #1184, the design's row of states): gotd classifies a lost
// authorization (telegram/account_status.go:66-91, AuthLostKindOf; IsAccountBanned :93-98) and reports a frozen
// account with its dates (FreezeState :143-158).

import (
	"fmt"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tgerr"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// stateOf is the state an error leaves the session in: banned, logged out (the session was ended from another device,
// expired, or the account deleted: it must be linked again), replaced (AUTH_KEY_DUPLICATED, the same key used by
// another connection), or merely disconnected, to be connected again, for anything else.
func stateOf(err error) messaging.State {
	reason := ""
	if rpc, ok := tgerr.As(err); ok {
		reason = rpc.Type
	}

	if tgerr.Is(err, "AUTH_KEY_DUPLICATED") {
		return messaging.State{Status: messaging.StatusReplaced, Reason: reason}
	}

	if telegram.IsAccountBanned(err) {
		return messaging.State{Status: messaging.StatusBanned, Reason: reason}
	}

	switch telegram.AuthLostKindOf(err) {
	case telegram.AuthLostBanned:
		return messaging.State{Status: messaging.StatusBanned, Reason: reason}
	case telegram.AuthLostDeleted, telegram.AuthLostRevoked, telegram.AuthLostUnregistered, telegram.AuthLostExpired,
		telegram.AuthLostKeyInvalid:
		return messaging.State{Status: messaging.StatusLoggedOut, Reason: reason}
	case telegram.AuthLostUnknown:
	}

	if err == nil {
		return messaging.State{Status: messaging.StatusDisconnected}
	}

	return messaging.State{Status: messaging.StatusDisconnected, Reason: err.Error()}
}

// frozenState is a frozen account: it stays signed in and reads, but Telegram refuses most of what it asks, and
// deletes it at Until unless the owner appeals. messaging.State has no status of its own for it: it is shown as a
// ban with that end date and the appeal's link (inference: gotd's FreezeState gives the dates, not a status).
func frozenState(st telegram.FreezeState) messaging.State {
	reason := "frozen by Telegram"
	if !st.Since.IsZero() {
		reason += " since " + st.Since.UTC().Format("2006-01-02")
	}

	if st.AppealURL != "" {
		reason = fmt.Sprintf("%s; appeal at %s", reason, st.AppealURL)
	}

	return messaging.State{Status: messaging.StatusBanned, Reason: reason, Until: st.Until.UTC()}
}

// terminal says the session cannot go on by itself: it is connected again only after a new link (a logout, a
// replacement) or never while it lasts (a ban). A frozen account is not: it still reads.
func terminal(err error) bool {
	return telegram.IsAuthLost(err) || telegram.IsAccountBanned(err)
}
