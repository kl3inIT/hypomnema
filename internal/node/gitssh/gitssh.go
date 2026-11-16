// Package gitssh is the ssh git runs on a node for a repository cloned over
// SSH (issue #625): `hypomnema-node gitssh`, set as GIT_SSH_COMMAND, asks the
// agent API for the control plane's key with the Dispatch's own key, writes
// it to a file only its user reads, runs ssh with it and removes the file. So
// the node keeps no key, as Coder's `coder gitssh` (cli/gitssh.go), which its
// agent sets as GIT_SSH_COMMAND (agent/agent.go), fetches the key per use.
//
// Unlike Coder's, the user's own keys are not tried (IdentitiesOnly): a node
// clones with the control plane's key only. The host is trusted under the
// keys the owner confirmed when he registered the repository, which come with
// the private key and go into a known_hosts file of this call alone
// (StrictHostKeyChecking=yes): a host is never trusted on first use, so a
// first connection cannot be intercepted and handed the key.
package gitssh

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Subcommand is the node binary's subcommand that runs the helper.
const Subcommand = "gitssh"

// hostAlias is the name the host's keys are written under in the call's
// known_hosts file and that ssh looks them up by (HostKeyAlias), whatever the
// address's host and port: the file holds this one repository's host.
const hostAlias = "hypomnema-git-host"

// Env is what git's environment gains to clone over SSH with the control
// plane's key: GIT_SSH_COMMAND naming the helper, and the agent API's address
// and the Dispatch's key the helper asks it with. Git runs GIT_SSH_COMMAND
// through the shell, so each path is quoted; on Windows git takes forward
// slashes, as Coder's agent writes its path. GIT_SSH_VARIANT tells git the
// helper takes OpenSSH's options: git would otherwise probe it with -G,
// fetching the key once more, and without the probe give no port.
func Env(exe, url, key string) []string {
	command := quote(filepath.ToSlash(exe)) + " " + Subcommand + " --"

	return []string{
		"GIT_SSH_COMMAND=" + command, "GIT_SSH_VARIANT=ssh", agenttool.EnvURL + "=" + url, agenttool.EnvKey + "=" + key,
	}
}

// quote quotes s for a POSIX shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Helper runs ssh for git with the control plane's key.
type Helper struct {
	// URL is the control plane's address; Key the Dispatch's own key.
	URL    string
	Key    string
	Client *http.Client
	// SSH runs ssh with args; nil runs the ssh on PATH with the helper's
	// standard streams, which carry git's protocol.
	SSH    func(ctx context.Context, args []string) error
	Stderr io.Writer
}

// Main runs the helper as `hypomnema-node gitssh -- <ssh arguments>`, with
// the address and the key from the environment.
func Main(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet(Subcommand, flag.ContinueOnError)

	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		return errors.New("usage: hypomnema-node gitssh -- <ssh arguments>")
	}

	h := Helper{
		URL: os.Getenv(agenttool.EnvURL), Key: os.Getenv(agenttool.EnvKey),
		Client: &http.Client{Timeout: time.Minute}, Stderr: os.Stderr,
	}

	return h.Run(ctx, fs.Args())
}

// Run runs ssh with args, the control plane's key and the host's keys, which
// are on disk only while ssh runs. Without the host's keys it runs nothing. ssh's own exit code is returned as an agenttool.ExitError,
// so git sees it.
func (h Helper) Run(ctx context.Context, args []string) error {
	if h.URL == "" || h.Key == "" {
		return fmt.Errorf("the control plane's address or this worker's key is missing (%s, %s)",
			agenttool.EnvURL, agenttool.EnvKey)
	}

	key, err := h.fetch(ctx)
	if err != nil {
		return err
	}

	var known strings.Builder

	for line := range strings.SplitSeq(key.HostKeys, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			known.WriteString(hostAlias + " " + line + "\n")
		}
	}

	if known.Len() == 0 {
		return errors.New("the repository's SSH host key is not known, and a host is not trusted on first use; " +
			"the owner sets it with `hypomnema repos set <name> --ssh-host-key`")
	}

	file, err := writeFile("hypomnema-gitssh-*", key.PrivateKey)
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(file) }()

	knownHosts, err := writeFile("hypomnema-known-hosts-*", known.String())
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(knownHosts) }()

	sshArgs := append([]string{
		"-i", file, "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "HostKeyAlias=" + hostAlias, "-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=" + os.DevNull,
	}, args...)

	run := h.SSH
	if run == nil {
		run = runSSH
	}

	err = run(ctx, sshArgs)

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// 255 is ssh's own failure, such as the host refusing the key: say which key to add, as Coder's does.
		if exit.ExitCode() == 255 && h.Stderr != nil {
			_, _ = fmt.Fprintf(h.Stderr, "hypomnema: ssh signs in with the control plane's key; the git host "+
				"must have this public key (`hypomnema git ssh-key`):\n%s\n", strings.TrimSpace(key.PublicKey))
		}

		return &agenttool.ExitError{Code: exit.ExitCode()}
	}

	return err
}

// fetch asks the agent API for the key.
func (h Helper) fetch(ctx context.Context) (agenttool.GitSSHKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.URL, "/")+agenttool.PathGitSSHKey, nil)
	if err != nil {
		return agenttool.GitSSHKey{}, err
	}

	req.Header.Set("Authorization", "Bearer "+h.Key)

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return agenttool.GitSSHKey{}, fmt.Errorf("asking the control plane for the SSH key: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		var refused struct {
			Message string `json:"message"`
		}

		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&refused)

		return agenttool.GitSSHKey{}, fmt.Errorf("the control plane gave no SSH key (%d): %s", resp.StatusCode,
			refused.Message)
	}

	var key agenttool.GitSSHKey
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&key); err != nil || key.PrivateKey == "" {
		return agenttool.GitSSHKey{}, errors.New("the control plane's answer holds no SSH key")
	}

	return key, nil
}

// writeFile writes content to a new file only this user reads.
func writeFile(pattern, content string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("making the key's file: %w", err)
	}

	name := f.Name()

	err = f.Chmod(0o600)
	if err == nil {
		_, err = f.WriteString(content)
	}

	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		_ = os.Remove(name)

		return "", fmt.Errorf("writing the key's file: %w", err)
	}

	return name, nil
}

func runSSH(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, "ssh", args...) //nolint:gosec // ssh with the options above and what git passed
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	return cmd.Run()
}
