package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// A repository's setup script (issue #186): what the repository needs on the
// machine before a worker starts in a new worktree, such as its toolchain. It
// is declared in the repository and run by the node, as Orca runs the
// `scripts.setup` of an `orca.yaml` (src/main/workspace-hooks/hooks.ts) and
// t3code a script of `t3.json` marked `runOnWorktreeCreate`. The worker starts
// after it ends: Orca's `setupAgentStartupPolicy: wait-for-setup`, here always,
// because nobody watches a headless worker that starts before its tools exist.

const (
	// SetupFile is the file at a repository's root that declares the script.
	SetupFile = "hypomnema.yaml"
	// setupTimeout is how long the script may run (Orca: HOOK_TIMEOUT).
	setupTimeout = 120 * time.Second
	// setupOutputKept is how much of the end of the script's output is logged
	// when it fails.
	setupOutputKept = 4 << 10
	// worktreePathEnv tells the script where its worktree is (Orca:
	// ORCA_WORKTREE_PATH, setup-hook-env-vars.ts).
	worktreePathEnv = "HYPOMNEMA_WORKTREE_PATH"
)

// setupFile is the part of SetupFile the node reads.
type setupFile struct {
	Scripts struct {
		// Setup runs after the worktree is created.
		Setup string `yaml:"setup"`
	} `yaml:"scripts"`
}

// setupScript is the script a worktree's repository declares; empty when it
// declares none.
func setupScript(path string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(path, SetupFile)) //nolint:gosec // a file of the worktree the node made
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("reading %s: %w", SetupFile, err)
	}

	var file setupFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return "", fmt.Errorf("reading %s: %w", SetupFile, err)
	}

	return strings.TrimSpace(file.Scripts.Setup), nil
}

// setup runs the repository's setup script in a new worktree and waits for
// it. A script that fails, runs out of time or cannot be read is logged and
// the worktree stands, as in Orca (runtime-local-worktree-setup.ts): the
// worker starts, and can still install what is missing itself.
func (m Maker) setup(ctx context.Context, path string) {
	script, err := setupScript(path)
	if err != nil {
		m.warn(ctx, path, err.Error(), "")

		return
	}

	if script == "" {
		return
	}

	shell, err := setupShell()
	if err != nil {
		m.warn(ctx, path, err.Error(), "")

		return
	}

	timeout := m.SetupTimeout
	if timeout == 0 {
		timeout = setupTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var output bytes.Buffer

	cmd := exec.CommandContext(ctx, shell, "-c", script) //nolint:gosec // the repository's own declared script
	cmd.Dir = path
	cmd.Stdout, cmd.Stderr = &output, &output
	// Git must not wait for a person (Orca: promptGuardShellEnv).
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", worktreePathEnv+"="+path)
	// The script's children end with it when its time is up (Orca: signalProcessTree).
	ownGroup(cmd)
	cmd.WaitDelay = 5 * time.Second

	err = cmd.Run()

	switch {
	case ctx.Err() != nil:
		m.warn(ctx, path, fmt.Sprintf("the setup script did not end within %s", timeout), tail(output.Bytes()))
	case err != nil:
		m.warn(ctx, path, "the setup script failed: "+err.Error(), tail(output.Bytes()))
	}
}

// setupShell is the shell the script runs in: bash, so a script written for it
// works (Orca runs hooks in bash), else sh.
func setupShell() (string, error) {
	for _, name := range []string{"bash", "sh"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}

	return "", errors.New("the setup script was not run: the machine has neither bash nor sh")
}

func (m Maker) warn(ctx context.Context, path, message, output string) {
	if m.Logger == nil {
		return
	}

	m.Logger.WarnContext(context.WithoutCancel(ctx), message,
		slog.String("event", "node.worktree.setup_failed"), slog.String("worktree", path),
		slog.String("output", output))
}

// tail is the end of a script's output.
func tail(output []byte) string {
	if len(output) > setupOutputKept {
		output = output[len(output)-setupOutputKept:]
	}

	return strings.TrimSpace(string(output))
}
