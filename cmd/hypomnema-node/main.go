// Command hypomnema-node runs on every machine that does work. It dials out
// to the control plane; nothing connects to it.
//
//	hypomnema-node init --url URL --token-file FILE [--home DIR] [--worktree-root DIR]
//	                        write the configuration (the token is read from FILE)
//	hypomnema-node check [--home DIR]
//	                        say hello once and list the CLIs found
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
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

var errUsage = errors.New("usage: hypomnema-node init --url URL --token-file FILE [--home DIR] " +
	"[--worktree-root DIR] | check [--home DIR] | version")

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
	case "check":
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		home := fs.String("home", "", "the node's home (default ~/.hypomnema-node)")

		if err := fs.Parse(args[1:]); err != nil {
			return errUsage
		}

		dir, err := homeDir(*home)
		if err != nil {
			return err
		}

		return node.Check(ctx, dir, stdout)
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
