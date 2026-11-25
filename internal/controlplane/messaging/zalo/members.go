package zalo

import (
	"context"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// membersWait bounds reading a group's members from Zalo.
const membersWait = 30 * time.Second

// Members reads who is in a chat now (issue #1163): a group's members as Zalo gives them, asked each time (bridge.mjs,
// members; zca-js getGroupInfo, as OpenClaw's zalouser lists them); a direct chat's other party, whose id is the
// chat's. A chat not kept is not known to be either.
func (a *account) Members(ctx context.Context, chat string) (messaging.Members, error) {
	switch a.svc.mirror.chatKind(ctx, a.uid, chat) {
	case "dm":
		return messaging.Members{Name: a.svc.mirror.chatName(ctx, a.uid, chat), Direct: true, IDs: []string{chat}}, nil
	case "":
		return messaging.Members{}, fmt.Errorf("%w: no Zalo chat %s is kept; list them first",
			orchestration.ErrNotFound, chat)
	}

	b := a.current()
	if b == nil {
		return messaging.Members{}, errNotConnected
	}

	ctx, cancel := context.WithTimeout(ctx, membersWait)
	defer cancel()

	var got struct {
		Name string   `json:"name"`
		IDs  []string `json:"ids"`
	}
	if err := b.call(ctx, "members", map[string]any{"group": chat}, &got); err != nil {
		return messaging.Members{}, fmt.Errorf("zalo: reading the members of group %s: %w", chat, err)
	}

	return messaging.Members{Name: got.Name, IDs: got.IDs}, nil
}
