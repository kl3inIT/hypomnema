// Command hypomnema runs the control plane and the owner's commands.
//
//	hypomnema migrate                          bring the database schema up to this version
//	hypomnema serve                            serve the node API and run the scheduler until SIGINT or SIGTERM
//	hypomnema version                          print the version
//	hypomnema nodes add <name> [--max N]       register a node; prints its token once
//	hypomnema nodes list
//	hypomnema accounts add <runtime> <label>   record an account (no secret)
//	hypomnema accounts list
//	hypomnema run --repo owner/name [--base B] [--role R] [--runtime X] [--model M] [--detach] <objective>
//	hypomnema answer <question> <number or text>
//	hypomnema questions                        list open questions to the owner
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

var errUsage = errors.New("usage: hypomnema migrate | serve | version | nodes add|list | accounts add|list | " +
	"run --repo owner/name <objective> | answer <question> <answer> | questions")

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
	if len(args) == 0 {
		return errUsage
	}

	if args[0] == "version" {
		if len(args) != 1 {
			return errUsage
		}

		_, err := fmt.Fprintln(stdout, platform.Version)

		return err
	}

	cfg, err := controlplane.LoadConfig(getenv)
	if err != nil {
		return err
	}

	switch args[0] {
	case "migrate", "serve":
		if len(args) != 1 {
			return errUsage
		}

		if args[0] == "migrate" {
			return controlplane.Migrate(ctx, cfg, stdout)
		}

		return controlplane.Serve(ctx, cfg, stdout)
	default:
		err := controlplane.Command(ctx, cfg, args, stdout)
		if errors.Is(err, controlplane.ErrUsage) {
			return errors.Join(err, errUsage)
		}

		return err
	}
}
