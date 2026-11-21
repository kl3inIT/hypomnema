package controlplane

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

type diskAlertRecorder struct {
	mainChat string
	lines    []string
	cards    []string
}

func (r *diskAlertRecorder) GetBot(_ context.Context, id string) (orchestration.Bot, error) {
	return orchestration.Bot{ID: id, MainChat: r.mainChat}, nil
}

func (r *diskAlertRecorder) TellChat(_ context.Context, chatID, text, _, _ string) error {
	r.lines = append(r.lines, chatID+": "+text)
	return nil
}

func (r *diskAlertRecorder) TellOwner(_ context.Context, bot, chatID, title, _ string) {
	r.cards = append(r.cards, bot+" "+chatID+" "+title)
}

func TestDiskAlertTellsTheMainChatAndTheMainBotsChannels(t *testing.T) {
	t.Parallel()
	r := &diskAlertRecorder{mainChat: "chat_main"}
	tell := tellDiskAlert(r, r, slog.New(slog.DiscardHandler))

	tell(t.Context(), orchestration.DiskAlert{Node: "stc-hy-app", FreeBytes: 4 << 30, FloorBytes: 5 << 30})
	tell(t.Context(), orchestration.DiskAlert{Node: "stc-hy-app", Recovered: true, FreeBytes: 30 << 30,
		FloorBytes: 5 << 30})

	require.Len(t, r.lines, 2)
	assert.Contains(t, r.lines[0], "chat_main: Node stc-hy-app has 4.0 GiB free")
	assert.Contains(t, r.lines[0], "below its floor of 5.0 GiB")
	assert.Contains(t, r.lines[1], "has 30.0 GiB free on its worktree volume again")
	assert.Equal(t, []string{
		orchestration.DefaultBotID + " chat_main A node is out of disk",
		orchestration.DefaultBotID + " chat_main A node has disk space again",
	}, r.cards)

	r = &diskAlertRecorder{}
	tellDiskAlert(r, r, slog.New(slog.DiscardHandler))(t.Context(), orchestration.DiskAlert{Node: "n"})
	assert.Empty(t, r.lines, "a main Bot without a main chat is told through its Channels only")
	assert.Len(t, r.cards, 1)
}
