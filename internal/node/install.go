package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
)

// Installing the node on a machine (issue #69, step 1b), after Omnara's
// `omnarad install` (internal/omnarad): the binary copies itself into the
// node's home, takes the token from the environment or a prompt, writes the
// configuration, says hello once, and installs and starts a systemd user
// unit. The install script (internal/controlplane/nodeapi/install.sh.tmpl)
// downloads and verifies the binary, then runs this.

// TokenEnv names the environment variable an install reads the node token
// from, for a machine with no terminal.
const TokenEnv = "HYPOMNEMA_NODE_TOKEN"

const (
	unitName       = "hypomnema-node.service"
	serviceTimeout = 30 * time.Second
)

// InstallOptions says where and how to install.
type InstallOptions struct {
	Home         string
	URL          string
	WorktreeRoot string
	// Exe is the binary to install: the one that runs.
	Exe string
	// Token is the node token; empty: asked for on the terminal.
	Token string
	// NoStart leaves the service alone: the binary and the configuration only.
	NoStart bool
}

// Install puts the node on this machine and starts it. It can be run again:
// the binary and the configuration are replaced and the service restarted.
func Install(ctx context.Context, o InstallOptions, stdout io.Writer) error {
	token := strings.TrimSpace(o.Token)
	if token == "" {
		var err error
		if token, err = promptToken(); err != nil {
			return err
		}
	}

	if err := Configure(o.Home, o.URL, token, o.WorktreeRoot); err != nil {
		return err
	}

	exe, err := installBinary(o.Home, o.Exe)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "installed %s\n", exe)

	// Before the service starts: a wrong URL or token is reported here, not in a log.
	if err := Check(ctx, o.Home, stdout); err != nil {
		return fmt.Errorf("the node is installed but cannot connect: %w", err)
	}

	if o.NoStart {
		return nil
	}

	return startService(ctx, exe, o.Home, stdout)
}

// installBinary copies the running binary to <home>/bin and returns the copy.
func installBinary(home, source string) (string, error) {
	name := "hypomnema-node"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	dir := filepath.Join(home, "bin")
	target := filepath.Join(dir, name)

	if same, err := sameFile(source, target); err == nil && same {
		return target, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("making %s: %w", dir, err)
	}

	in, err := os.Open(source) //nolint:gosec // the running binary
	if err != nil {
		return "", fmt.Errorf("reading the node binary: %w", err)
	}
	defer in.Close() //nolint:errcheck // Read only.

	// Written beside the target and renamed over it, so a running node keeps its old file until it restarts.
	tmp := target + ".new"

	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700) //nolint:gosec // an executable, owner-only
	if err != nil {
		return "", fmt.Errorf("writing %s: %w", tmp, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()

		return "", fmt.Errorf("copying the node binary: %w", err)
	}

	if err := out.Close(); err != nil {
		return "", fmt.Errorf("writing %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, target); err != nil {
		return "", fmt.Errorf("installing %s: %w", target, err)
	}

	return target, nil
}

func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}

	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}

	return os.SameFile(ai, bi), nil
}

// startService installs the systemd user unit and (re)starts it. Where there
// is no systemd user manager it says how to run the node by hand.
func startService(ctx context.Context, exe, home string, stdout io.Writer) error {
	systemctl, err := exec.LookPath("systemctl")
	if runtime.GOOS != "linux" || err != nil {
		_, _ = fmt.Fprintf(stdout, "no systemd here: run the node with `%s run --home %s`\n", exe, home)

		return nil
	}

	config, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("finding the user's configuration directory: %w", err)
	}

	dir := filepath.Join(config, "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // systemd's own directory; units are not secret
		return fmt.Errorf("making %s: %w", dir, err)
	}

	unit := filepath.Join(dir, unitName)
	if err := os.WriteFile(unit, []byte(Unit(exe, home)), 0o644); err != nil { //nolint:gosec // a unit holds no secret
		return fmt.Errorf("writing %s: %w", unit, err)
	}

	for _, args := range [][]string{{"daemon-reload"}, {"enable", unitName}, {"restart", unitName}} {
		if out, err := command(ctx, systemctl, append([]string{"--user"}, args...)...); err != nil {
			return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, out)
		}
	}

	_, _ = fmt.Fprintf(stdout, "service %s started (%s)\n", unitName, unit)
	warnWithoutLinger(ctx, stdout)

	return nil
}

// warnWithoutLinger says what to run when the user's services stop at logout
// (Omnara's ensureSystemdLinger warns the same way).
func warnWithoutLinger(ctx context.Context, stdout io.Writer) {
	user := os.Getenv("USER")
	if user == "" {
		return
	}

	loginctl, err := exec.LookPath("loginctl")
	if err != nil {
		return
	}

	out, err := command(ctx, loginctl, "show-user", user, "--property=Linger", "--value")
	if err == nil && strings.TrimSpace(out) == "yes" {
		return
	}

	_, _ = fmt.Fprintf(stdout, "warning: this user's services stop when it logs out; run "+
		"`sudo loginctl enable-linger %s` to keep the node running and start it at boot\n", user)
}

func command(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, serviceTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // fixed commands of this file

	return strings.TrimSpace(string(out)), err
}

// promptToken asks for the node token on the terminal without echoing it. The
// install script's own input is the script, so the terminal is opened by name.
func promptToken() (string, error) {
	missing := errors.New("no node token: set " + TokenEnv + " or run this from a terminal")

	name := "/dev/tty"
	if runtime.GOOS == "windows" {
		name = "CONIN$"
	}

	tty, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return "", missing
	}
	defer tty.Close() //nolint:errcheck // A terminal; nothing to flush.

	fd := int(tty.Fd()) //nolint:gosec // a file descriptor fits an int
	if !term.IsTerminal(fd) {
		return "", missing
	}

	_, _ = fmt.Fprint(tty, "Node token (from `hypomnema nodes add`): ")

	raw, err := term.ReadPassword(fd)

	_, _ = fmt.Fprintln(tty)

	if err != nil {
		return "", fmt.Errorf("reading the node token: %w", err)
	}

	if strings.TrimSpace(string(raw)) == "" {
		return "", missing
	}

	return strings.TrimSpace(string(raw)), nil
}
