package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientcli"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The `tool` command (issue #103): what an agent on this machine runs an
// Integration's tool through, and how the tools are put there.
//
//	hypomnema tool install --dir DIR     install every tool, its script and nothing else under DIR
//	hypomnema tool <name> [args...]      run a tool with a fresh token, as the logged-in agent
//
// The second form is what the script named after a tool runs. The agent's
// credential is the token `hypomnema login` saved: the coordinator's.
func tool(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: tool install --dir DIR | tool <name> [args...]", errUsage)
	}

	if args[0] == "install" {
		return toolInstall(ctx, args[1:], stdout)
	}

	home, err := clientcli.DefaultHome()
	if err != nil {
		return err
	}

	cfg, err := clientcli.LoadConfig(home)
	if err != nil {
		return fmt.Errorf("%s is run as the agent logged in on this machine: %w", args[0], err)
	}

	launcher := agenttool.FromEnv()
	launcher.URL, launcher.Key = cfg.URL, cfg.Token

	return launcher.Run(ctx, args[0], args[1:])
}

// toolInstall puts every tool that has a release for this system under dir,
// with the script agents call it by. It is run by the user the agent runs as,
// so that the agent can read what is installed.
func toolInstall(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("tool install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	dir := fs.String("dir", "", "the directory to install under")

	if err := fs.Parse(args); err != nil || *dir == "" || fs.NArg() != 0 {
		return fmt.Errorf("%w: tool install --dir DIR", errUsage)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	var integrations []string

	for _, def := range agenttool.Catalog() {
		release, ok := def.For(runtime.GOOS, runtime.GOARCH)
		if !ok {
			_, _ = fmt.Fprintf(stdout, "%s has no release for %s/%s; skipped\n", def.Name, runtime.GOOS, runtime.GOARCH)

			continue
		}

		if err := (agenttool.ToolInstaller{Home: *dir}).Install(ctx, release); err != nil {
			return err
		}

		integrations = append(integrations, def.Integration)

		_, _ = fmt.Fprintf(stdout, "%s %s is installed\n", def.Name, def.Version)
	}

	place := agenttool.Place{Home: *dir, Exe: exe}
	if err := place.Prepare(integrations); err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "put %s first on the agent's PATH and set %s=%s\n", place.Bin(), agenttool.EnvTools, *dir)

	return err
}

// exitCode is the code a tool run through the launcher ended with, so that
// the agent sees the tool's own failure and not this binary's.
func exitCode(err error) (int, bool) {
	var exit *agenttool.ExitError
	if errors.As(err, &exit) {
		return exit.Code, true
	}

	return 0, false
}
