// Command hypomnema runs the control plane.
//
//	hypomnema serve     serve until SIGINT or SIGTERM
//	hypomnema version   print the version
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kl3inIT/hypomnema/internal/controlplane"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

var errUsage = errors.New("usage: hypomnema serve | hypomnema version")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout)

	stop()

	if err != nil {
		fmt.Fprintln(os.Stderr, "hypomnema:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	if len(args) != 1 {
		return errUsage
	}

	switch args[0] {
	case "version":
		_, err := fmt.Fprintln(stdout, platform.Version)

		return err
	case "serve":
		cfg, err := controlplane.LoadConfig(getenv)
		if err != nil {
			return err
		}

		return controlplane.Serve(ctx, cfg, stdout)
	default:
		return errUsage
	}
}
