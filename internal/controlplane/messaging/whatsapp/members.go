package whatsapp

import (
	"context"
	"fmt"
	"slices"

	"go.mau.fi/whatsmeow/types"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// Members reads who is in a chat now (issue #1163): a group's participants as WhatsApp lists them, asked each time
// (whatsmeow GetGroupInfo, as groupMode reads a group's addressing in retry.go); a direct chat's other party. Each is
// given by every JID WhatsApp knows him by, his number's and his LID, since a message names its sender by either.
func (a *account) Members(ctx context.Context, chat string) (messaging.Members, error) {
	jid, err := types.ParseJID(chat)
	if err != nil {
		return messaging.Members{}, fmt.Errorf("whatsapp: chat %q: %w", chat, err)
	}

	switch jid.Server {
	case types.GroupServer:
		info, err := a.client.GetGroupInfo(ctx, jid)
		if err != nil {
			return messaging.Members{}, fmt.Errorf("whatsapp: reading the members of group %s: %w", jid, err)
		}

		return groupMembers(info), nil
	case types.DefaultUserServer, types.HiddenUserServer:
		ids := []string{jid.ToNonAD().String()}

		// The other form is known once WhatsApp has told it; a chat by one form alone still names him.
		if alt, err := a.client.Store.GetAltJID(ctx, jid.ToNonAD()); err == nil && !alt.IsEmpty() {
			ids = append(ids, alt.ToNonAD().String())
		}

		return messaging.Members{Name: a.mirror.chatName(ctx, a.mirrorKey(), chat), Direct: true, IDs: ids}, nil
	}

	return messaging.Members{}, fmt.Errorf("whatsapp: chat %s is neither a group nor a person", chat)
}

// groupMembers are a group's participants, each by his JID, his number's JID and his LID, those WhatsApp gives
// (types.GroupParticipant), and the group's name.
func groupMembers(info *types.GroupInfo) messaging.Members {
	m := messaging.Members{Name: info.Name, IDs: []string{}}

	for _, p := range info.Participants {
		for _, jid := range []types.JID{p.JID, p.PhoneNumber, p.LID} {
			if jid.IsEmpty() {
				continue
			}

			if id := jid.ToNonAD().String(); !slices.Contains(m.IDs, id) {
				m.IDs = append(m.IDs, id)
			}
		}
	}

	return m
}
