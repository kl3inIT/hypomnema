// Command hypomnema-vault runs the vault.
//
//	hypomnema-vault serve     serve until SIGINT or SIGTERM
//	hypomnema-vault version   print the version
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/vault"
)

var errUsage = errors.New("usage: hypomnema-vault serve | hypomnema-vault version")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout)

	stop()

	if err != nil {
		fmt.Fprintln(os.Stderr, "hypomnema-vault:", err)
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
		cfg, err := vault.LoadConfig(getenv)
		if err != nil {
			return err
		}

		return vault.Serve(ctx, cfg, stdout)
	default:
		return errUsage
	}
}
