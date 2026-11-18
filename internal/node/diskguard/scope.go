package diskguard

import (
	"context"
	"os"
	"os/exec"
	"strconv"
)

const ScopedSubcommand = "disk-cleanup-exec"

func ScopedCommand(ctx context.Context, dirs []*os.File, command string, args ...string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	argv := append([]string{ScopedSubcommand, strconv.Itoa(len(dirs)), command}, args...)
	cmd := exec.CommandContext(ctx, exe, argv...) //nolint:gosec // the node executable and its cleanup arguments
	cmd.ExtraFiles = dirs
	return cmd, nil
}
