package controlplane

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
)

// chatFilesWithin bounds the removal of one chat's files.
const chatFilesWithin = 30 * time.Second

// chatFiles removes what pi kept for a deleted chat on this machine (issue #484), its run/ folder and its sessions,
// by running `hypomnema-chat-rm` with the chat's id as the coordinator: the control plane's user cannot write the
// coordinator's home (deploy/coordinator/hypomnema-chat-rm).
type chatFiles struct {
	command []string
}

// newChatFiles is how the client API removes a deleted chat's files, or nil when HYPOMNEMA_CHAT_RM_COMMAND is not
// set.
func newChatFiles(command []string) clientapi.ChatFiles {
	if len(command) == 0 {
		return nil
	}

	return chatFiles{command: command}
}

func (c chatFiles) Remove(ctx context.Context, chatID string) error {
	ctx, cancel := context.WithTimeout(ctx, chatFilesWithin)
	defer cancel()

	args := append(append([]string{}, c.command[1:]...), chatID)
	cmd := exec.CommandContext(ctx, c.command[0], args...) //nolint:gosec // the owner's own command

	var stderr bytes.Buffer
	cmd.Stderr = &limited{w: &stderr, left: 4 << 10}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("removing chat %s's files: %w: %s", chatID, err, strings.TrimSpace(stderr.String()))
	}

	return nil
}
