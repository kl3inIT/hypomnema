package controlplane

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// diskAlertChats is what telling the owner of a node's disk reads and writes: the main Bot, whose main chat he is
// told in, and that chat.
type diskAlertChats interface {
	GetBot(ctx context.Context, id string) (orchestration.Bot, error)
	TellChat(ctx context.Context, chatID, text, routineID, runID string) error
}

// diskAlertOwner sends him a card through the main Bot's Lark Channels (channels.Channels.TellOwner).
type diskAlertOwner interface {
	TellOwner(ctx context.Context, bot, chatID, title, text string)
}

// tellDiskAlert tells the owner that a node is still out of disk after trimming its caches, or that it has room
// again (issue #1017): a line in the main Bot's main chat and a card through that Bot's Lark Channels, the way a
// failing Routine is told (routines.Runner.alert), as Coder notifies a workspace's owner that it is out of disk
// (coderd/agentapi/resources_monitoring.go:248-252).
func tellDiskAlert(chats diskAlertChats, owner diskAlertOwner, logger *slog.Logger,
) func(ctx context.Context, alert orchestration.DiskAlert) {
	return func(ctx context.Context, alert orchestration.DiskAlert) {
		title, text := diskAlertText(alert)
		logger.WarnContext(ctx, title, slog.String("event", "controlplane.disk_alert"),
			slog.String("node_id", alert.NodeID), slog.Bool("recovered", alert.Recovered),
			slog.Int64("free_bytes", alert.FreeBytes), slog.Int64("floor_bytes", alert.FloorBytes))

		chatID := ""
		if bot, err := chats.GetBot(ctx, orchestration.DefaultBotID); err == nil {
			chatID = bot.MainChat
		}

		if chatID != "" {
			if err := chats.TellChat(ctx, chatID, text, "", ""); err != nil {
				logger.WarnContext(ctx, "the main chat was not told of a node's disk",
					slog.String("event", "controlplane.disk_alert_failed"), slog.Any("error", err))
			}
		}

		if owner != nil {
			owner.TellOwner(ctx, orchestration.DefaultBotID, chatID, title, text)
		}
	}
}

func diskAlertText(alert orchestration.DiskAlert) (title, text string) {
	if alert.Recovered {
		return "A node has disk space again", fmt.Sprintf("Node %s has %s free on its worktree volume again, at or "+
			"above its floor of %s: it takes new work.", alert.Node, gib(alert.FreeBytes), gib(alert.FloorBytes))
	}

	return "A node is out of disk", fmt.Sprintf("Node %s has %s free on its worktree volume, below its floor of %s, "+
		"after trimming the caches it manages: it takes no new work, and Tasks wait with nodes_low_disk. Free "+
		"space on it.", alert.Node, gib(alert.FreeBytes), gib(alert.FloorBytes))
}

func gib(n int64) string {
	return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
}

// tellListeningBroken tells the owner that a social listening source broke (issue #1290), as tellDiskAlert tells him
// a node is out of disk: a line in the main Bot's main chat and a card through that Bot's Lark Channels. The listening
// service calls it once until the source works again (postiz informAboutRefreshError, called only by the refresh
// that flagged the channel, refresh.integration.service.ts:148-166).
func tellListeningBroken(chats diskAlertChats, owner diskAlertOwner, logger *slog.Logger,
) func(ctx context.Context, b listening.Broken) {
	return func(ctx context.Context, b listening.Broken) {
		if b.Source != "" { // API only: built in the next commit.
			return
		}

		title := listening.Label(b.Source) + " stopped working for social listening"
		text := fmt.Sprintf("%s could not be read (%s: %s). %s Settings → Gateway → Social listening shows it.",
			listening.Label(b.Source), b.Code, b.Detail, b.Fix)

		logger.WarnContext(ctx, title, slog.String("event", "controlplane.listening_broken"),
			slog.String("source", b.Source), slog.String("code", string(b.Code)))

		chatID := ""
		if bot, err := chats.GetBot(ctx, orchestration.DefaultBotID); err == nil {
			chatID = bot.MainChat
		}

		if chatID != "" {
			if err := chats.TellChat(ctx, chatID, text, "", ""); err != nil {
				logger.WarnContext(ctx, "the main chat was not told of a broken listening source",
					slog.String("event", "controlplane.listening_broken_failed"), slog.Any("error", err))
			}
		}

		if owner != nil {
			owner.TellOwner(ctx, orchestration.DefaultBotID, chatID, title, text)
		}
	}
}
