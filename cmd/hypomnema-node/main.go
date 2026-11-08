// Command hypomnema-node runs on every machine that does work. It dials out
// to the control plane; nothing connects to it.
//
//	hypomnema-node init --url URL --token-file FILE [--home DIR] [--worktree-root DIR]
//	                        write the configuration (the token is read from FILE)
//	hypomnema-node check [--home DIR]
//	                        say hello once and list the CLIs found
//	hypomnema-node accounts set <account id> --token-file FILE [--home DIR]
//	                        record where an account's Claude token is on this node
//	hypomnema-node run [--home DIR]
//	                        run the daemon until SIGINT or SIGTERM
//	hypomnema-node service [--home DIR]
//	                        print a systemd user unit for the daemon
//	hypomnema-node shim --dir DIR
//	                        run one worker (started by the daemon)
//	hypomnema-node mcp --dir DIR
//	                        the worker's ask and finish tools over stdio (started by the worker's CLI)
//	hypomnema-node version  print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kl3inIT/hypomnema/internal/node"
	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/node/workertools"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

var errUsage = errors.New("usage: hypomnema-node init --url URL --token-file FILE [--home DIR] " +
	"[--worktree-root DIR] | check | accounts set <account id> --token-file FILE | run | service | version")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)

	stop()

	if err != nil {
		fmt.Fprintln(os.Stderr, "hypomnema-node:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}

	switch args[0] {
	case "version":
		_, err := fmt.Fprintln(stdout, platform.Version)

		return err
	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		url := fs.String("url", "", "the control plane's URL")
		tokenFile := fs.String("token-file", "", "a file holding the node token from `hypomnema nodes add`")
		home := fs.String("home", "", "the node's home (default ~/.hypomnema-node)")
		root := fs.String("worktree-root", "", "where worktrees are made (default <home>/worktrees)")

		if err := fs.Parse(args[1:]); err != nil || *url == "" || *tokenFile == "" {
			return errUsage
		}

		dir, err := homeDir(*home)
		if err != nil {
			return err
		}

		if err := node.Init(dir, *url, *tokenFile, *root); err != nil {
			return err
		}

		_, err = fmt.Fprintf(stdout, "configuration written to %s; the token file can now be deleted\n", dir)

		return err
	case "check", "run", "service":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		home := fs.String("home", "", "the node's home (default ~/.hypomnema-node)")

		if err := fs.Parse(args[1:]); err != nil {
			return errUsage
		}

		dir, err := homeDir(*home)
		if err != nil {
			return err
		}

		switch args[0] {
		case "check":
			return node.Check(ctx, dir, stdout)
		case "run":
			return node.Run(ctx, dir, platform.NewLogger(stdout, platform.LogConfig{}, "hypomnema-node"))
		default:
			exe, err := os.Executable()
			if err != nil {
				return err
			}

			_, err = fmt.Fprint(stdout, node.Unit(exe, dir))

			return err
		}
	case "accounts":
		if len(args) < 3 || args[1] != "set" {
			return errUsage
		}

		fs := flag.NewFlagSet("accounts set", flag.ContinueOnError)
		tokenFile := fs.String("token-file", "", "a private file holding the account's `claude setup-token` token")
		home := fs.String("home", "", "the node's home (default ~/.hypomnema-node)")

		if err := fs.Parse(args[3:]); err != nil || *tokenFile == "" {
			return errUsage
		}

		dir, err := homeDir(*home)
		if err != nil {
			return err
		}

		if err := node.SetAccount(dir, args[2], *tokenFile); err != nil {
			return err
		}

		_, err = fmt.Fprintf(stdout, "account %s uses %s\n", args[2], *tokenFile)

		return err
	case "shim", "mcp":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		dir := fs.String("dir", "", "the worker's directory (shim) or bridge directory (mcp)")

		if err := fs.Parse(args[1:]); err != nil || *dir == "" {
			return errUsage
		}

		if args[0] == "mcp" {
			return workertools.Serve(ctx, os.Stdin, stdout, *dir)
		}

		return shim.Main(ctx, *dir)
	default:
		return errUsage
	}
}

func homeDir(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}

	return node.DefaultHome()
}
