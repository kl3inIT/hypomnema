package diskguard

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

const ScopedSubcommand = "disk-cleanup-exec"

type Scope struct {
	Dir       *os.File
	OnlyRmdir bool
}

func ScopedCommand(ctx context.Context, scopes []Scope, command string, args ...string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	modes := make([]string, 0, len(scopes))
	dirs := make([]*os.File, 0, len(scopes))
	for _, scope := range scopes {
		mode := "all"
		if scope.OnlyRmdir {
			mode = "rmdir"
		}
		modes = append(modes, mode)
		dirs = append(dirs, scope.Dir)
	}
	argv := append([]string{ScopedSubcommand, strings.Join(modes, ","), command}, args...)
	cmd := exec.CommandContext(ctx, exe, argv...) //nolint:gosec // the node executable and its cleanup arguments
	cmd.ExtraFiles = dirs
	return cmd, nil
}
