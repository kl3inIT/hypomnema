// Command hypomnema runs the control plane and the owner's commands.
//
// On the server, with the database (HYPOMNEMA_DATABASE_PATH):
//
//	hypomnema migrate                          bring the database schema up to this version
//	hypomnema serve                            serve the APIs and run the scheduler until SIGINT or SIGTERM
//	hypomnema nodes add <name> [--max N]       register a node; prints its token once
//	hypomnema nodes list
//	hypomnema accounts add <runtime> <label>   record an account (no secret)
//	hypomnema accounts list
//	hypomnema tokens add <name> [--kind owner|coordinator]   make a client API token; prints it once
//	hypomnema tokens list
//	hypomnema tokens revoke <token id>
//
// Anywhere, over the client API (after login):
//
//	hypomnema login --url URL --token-file FILE
//	hypomnema run --repo owner/name [--base B] [--role R] [--runtime X] [--model M] [--detach] <objective>
//	hypomnema runs create|list|show|finish|cancel|wait …
//	hypomnema tasks create|show|cancel|reopen …
//	hypomnema questions [show|forward] …, hypomnema answer <question> <answer>
//	hypomnema whoami
//	hypomnema skills get|install …              the coordinator skill (no login needed)
//
// `hypomnema <command> --help` lists a command's flags; every one takes --json.
//
//	hypomnema version                          print the version
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/kl3inIT/hypomnema/internal/controlplane"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientcli"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

var errUsage = errors.New("usage: hypomnema migrate | serve | version | nodes add|list | accounts add|list | " +
	"tokens add|list|revoke | login --url URL --token-file FILE | run --repo owner/name <objective> | " +
	"runs … | tasks … | questions … | whoami | skills get|install | " +
	"answer <question> <answer> | questions")

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

	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintf(stdout, "%s\n\nover the client API (after login):\n%s\n\n"+
			"`hypomnema <command> --help` lists a command's flags.\n", errUsage, clientcli.Usage())

		return err
	}

	if args[0] == "version" {
		if len(args) != 1 {
			return errUsage
		}

		_, err := fmt.Fprintln(stdout, platform.Version)

		return err
	}

	if slices.Contains(clientcli.Commands(), args[0]) {
		home, err := clientcli.DefaultHome()
		if err != nil {
			return err
		}

		return clientcli.Command(ctx, home, args, os.Stdin, stdout)
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
