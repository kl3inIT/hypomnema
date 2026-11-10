// Command hypomnema runs the control plane and the owner's commands.
//
// On the server, with the database (HYPOMNEMA_DATABASE_PATH):
//
//	hypomnema migrate                          bring the database schema up to this version
//	hypomnema serve                            serve the APIs and run the scheduler until SIGINT or SIGTERM
//	hypomnema nodes add <name> [--max N]       register a node; prints its token once
//	hypomnema nodes list
//	hypomnema nodes drain|resume|rm <node>     stop new work on it, take work again, or remove it
//	hypomnema nodes token add <node> <name>    one more token for a node; printed once
//	hypomnema nodes token list <node>
//	hypomnema nodes token revoke <token id>
//	hypomnema machines providers add <name> --kind aws --key vault:<secret> --region R --size S [--max N] [--option name=value]…
//	hypomnema machines providers list | rm <name> | check <name>
//	hypomnema machines create <name> --provider P [--region R] [--size S] [--max N]   a node on a new machine
//	hypomnema accounts add <runtime> <label>   record an account (no secret)
//	hypomnema accounts list
//	hypomnema tokens add <name> [--kind owner|coordinator]   make a client API token; prints it once
//	hypomnema tokens list
//	hypomnema tokens revoke <token id>
//	hypomnema secrets keygen [id]              print a new root key for the vault (HYPOMNEMA_VAULT_KEYS)
//	hypomnema secrets add <name> --from-file FILE|- [--kind generic|oauth_token_set] [--meta key=value]…
//	hypomnema secrets list                     names, kinds, versions and key ids; never a value
//	hypomnema secrets rotate <name> --from-file FILE|-
//	hypomnema secrets rm <name>                refused while a model or machine provider names the secret
//	hypomnema secrets rewrap                   wrap every data key under the active root key
//	hypomnema gateway models apply FILE        declare a model the gateway serves (a YAML document)
//	hypomnema gateway models list | rm <name>
//	hypomnema gateway keys add <name> [--models a,b]   make a gateway key; prints it once
//	hypomnema gateway keys list | revoke <key id>
//	hypomnema gateway usage [--limit N]         the latest requests the gateway served
//
// Anywhere, over the client API (after login):
//
//	hypomnema login --url URL --token-file FILE
//	hypomnema run --repo owner/name [--base B] [--profile P] [--runtime X] [--model M] [--detach] <objective>
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

var errUsage = errors.New("usage: hypomnema migrate | serve | version | " +
	"nodes add|list|drain|resume|rm|token | machines providers|create | accounts add|list | " +
	"tokens add|list|revoke | secrets keygen|add|list|rotate|rm|rewrap | gateway models|keys … | login --url URL --token-file FILE | run --repo owner/name <objective> | " +
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
		err := controlplane.Command(ctx, cfg, args, os.Stdin, stdout)
		if errors.Is(err, controlplane.ErrUsage) {
			return errors.Join(err, errUsage)
		}

		return err
	}
}
