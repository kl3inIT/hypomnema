// Package node assembles the node: its configuration file, the CLIs it
// finds, and its commands. The configuration lives in the node's home
// (default ~/.hypomnema-node) as config.json, mode 0600 in a 0700
// directory, as Omnara's daemon.json (internal/omnarad/daemon_config.go at
// 0ef1711); it holds the node's token and nothing else secret.
package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/claude"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/terminal"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Runtimes the node looks for on its PATH, by CLI name (Orca local-agent-cli-presence.ts).
var knownRuntimes = []string{"claude", "codex"}

const versionTimeout = 10 * time.Second

// Config is the node's configuration file.
type Config struct {
	// URL is the control plane's base URL.
	URL string `json:"url"`
	// Token is the node's token, from `hypomnema nodes add`.
	Token string `json:"token"`
	// WorktreeRoot is where the node makes its worktrees.
	WorktreeRoot string `json:"worktree_root"`
	// DisableTerminal turns off the terminal the owner opens from the web. It
	// is set only here, on the machine: the control plane cannot turn it back
	// on (Nezha's disable_command_execute).
	DisableTerminal bool `json:"disable_terminal,omitempty"`
	// Accounts maps an account id of the control plane to where its
	// credentials are on this node, until the vault exists. Only the file's
	// path is here, never the token.
	Accounts map[string]claude.Account `json:"accounts,omitempty"`
}

// DefaultHome is ~/.hypomnema-node.
func DefaultHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the home directory: %w", err)
	}

	return filepath.Join(home, ".hypomnema-node"), nil
}

func configPath(home string) string { return filepath.Join(home, "config.json") }

// StatePath is the node's state database in its home.
func StatePath(home string) string { return filepath.Join(home, "state.db") }

// Init writes the configuration. The token is read from tokenFile, so it
// never appears on a command line or in shell history.
func Init(home, rawURL, tokenFile, worktreeRoot string) error {
	raw, err := os.ReadFile(tokenFile) //nolint:gosec // the owner names the file holding the token
	if err != nil {
		return fmt.Errorf("reading the token file: %w", err)
	}

	return Configure(home, rawURL, strings.TrimSpace(string(raw)), worktreeRoot)
}

// Configure writes the configuration with the token given. An existing
// configuration keeps its accounts.
func Configure(home, rawURL, token, worktreeRoot string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("the control plane URL %q must be http(s)://host", rawURL)
	}

	if !strings.HasPrefix(token, "hyn_") {
		return errors.New("that does not hold a node token (hyn_…)")
	}

	if worktreeRoot == "" {
		worktreeRoot = filepath.Join(home, "worktrees")
	}

	if !filepath.IsAbs(worktreeRoot) {
		return fmt.Errorf("the worktree root %q must be absolute", worktreeRoot)
	}

	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", home, err)
	}

	if err := os.Chmod(home, 0o700); err != nil { //nolint:gosec // a directory needs its execute bit; 0700 is owner-only
		return fmt.Errorf("restricting %s: %w", home, err)
	}

	cfg := Config{URL: strings.TrimSuffix(rawURL, "/"), Token: token, WorktreeRoot: worktreeRoot}
	if old, err := LoadConfig(home); err == nil {
		cfg.Accounts = old.Accounts
	}

	return saveConfig(home, cfg)
}

func saveConfig(home string, cfg Config) error {
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the configuration: %w", err)
	}

	// Written whole and renamed into place, so a crash never leaves half a file.
	tmp := configPath(home) + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing the configuration: %w", err)
	}

	if err := os.Rename(tmp, configPath(home)); err != nil {
		return fmt.Errorf("saving the configuration: %w", err)
	}

	return nil
}

// SetAccount records where the credentials of a control plane account are on
// this node. The file must be private to the node's user.
func SetAccount(home, accountID, tokenFile string) error {
	cfg, err := LoadConfig(home)
	if err != nil {
		return err
	}

	if !strings.HasPrefix(accountID, "acc_") {
		return fmt.Errorf("%q is not an account id (acc_…, from `hypomnema accounts list`)", accountID)
	}

	abs, err := filepath.Abs(tokenFile)
	if err != nil {
		return err
	}

	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("reading the token file: %w", err)
	}

	if info.Mode().Perm()&0o077 != 0 && runtimeIsUnix() {
		return fmt.Errorf("%s can be read by others; chmod 600 it first", abs)
	}

	if cfg.Accounts == nil {
		cfg.Accounts = map[string]claude.Account{}
	}

	cfg.Accounts[accountID] = claude.Account{TokenFile: abs}

	return saveConfig(home, cfg)
}

// Run runs the daemon with the Claude runner until ctx ends.
func Run(ctx context.Context, home string, logger *slog.Logger) error {
	cfg, err := LoadConfig(home)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding the node binary: %w", err)
	}

	runtimes := DetectRuntimes(ctx)

	claudePath := ""
	for _, r := range runtimes {
		if r.Runtime == "claude" {
			claudePath = r.Path
		}
	}

	db, err := statedb.Open(ctx, StatePath(home), time.Now)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Closing at shutdown; nothing is left to report it to.

	runner := &claude.Runner{
		Home: home, Exe: exe, Claude: claudePath, Accounts: cfg.Accounts, Logger: logger,
		Worktrees: worktree.Maker{Root: home},
	}

	logger.InfoContext(ctx, "node starting", slog.String("event", "node.starting"), slog.String("url", cfg.URL),
		slog.Int("runtimes", len(runtimes)), slog.Int("accounts", len(cfg.Accounts)))

	// The shells the owner opens from the web end with the node.
	var terminals *terminal.Manager
	if terminal.Supported() && !cfg.DisableTerminal {
		terminals = terminal.New(terminal.Options{})
		defer terminals.Close()
	}

	d := daemon.New(daemon.Config{
		URL: cfg.URL, Token: cfg.Token, WorktreeRoot: cfg.WorktreeRoot, Version: platform.Version, Runtimes: runtimes,
		System:   DetectSystem(ctx, cfg.WorktreeRoot),
		Stats:    func(ctx context.Context) *nodeproto.SystemStats { return MeasureSystem(ctx, cfg.WorktreeRoot) },
		Reach:    ReachHost,
		Terminal: terminals,
	}, db, runner, logger)

	// Workers' transcripts go to the control plane beside their reports (issue #43).
	runner.Send = d.SendItems

	go runner.Forward(ctx)

	return d.Run(ctx)
}

// Unit is a systemd user unit for the daemon. KillMode=process keeps workers
// alive when the daemon restarts (Omnara service_systemd.go; spike 1a).
func Unit(exe, home string) string {
	return fmt.Sprintf(`[Unit]
Description=Hypomnema node
After=network-online.target

[Service]
Type=exec
ExecStart=%s run --home %s
Restart=on-failure
RestartSec=3
KillMode=process
TimeoutStopSec=30s

[Install]
WantedBy=default.target
`, exe, home)
}

// LoadConfig reads the configuration from home.
func LoadConfig(home string) (Config, error) {
	raw, err := os.ReadFile(configPath(home))
	if err != nil {
		return Config{}, fmt.Errorf("reading the configuration (run `hypomnema-node init` first): %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("decoding %s: %w", configPath(home), err)
	}

	if cfg.URL == "" || cfg.Token == "" || cfg.WorktreeRoot == "" {
		return Config{}, fmt.Errorf("%s needs url, token and worktree_root", configPath(home))
	}

	return cfg, nil
}

// DetectRuntimes finds the known CLIs on PATH and reads their versions.
func DetectRuntimes(ctx context.Context) []nodeproto.RuntimeInfo {
	out := []nodeproto.RuntimeInfo{}

	for _, name := range knownRuntimes {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}

		out = append(out, nodeproto.RuntimeInfo{Runtime: name, Path: path, Version: cliVersion(ctx, path)})
	}

	return out
}

func cliVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()

	var stdout bytes.Buffer

	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return ""
	}

	line, _, _ := strings.Cut(strings.TrimSpace(stdout.String()), "\n")

	return strings.TrimSpace(line)
}

// Check says hello once with the configuration and prints what the control
// plane answered, to confirm an install.
func Check(ctx context.Context, home string, stdout io.Writer) error {
	cfg, err := LoadConfig(home)
	if err != nil {
		return err
	}

	db, err := statedb.Open(ctx, StatePath(home), time.Now)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Closing at exit; nothing is left to report it to.

	runtimes := DetectRuntimes(ctx)

	d := daemon.New(daemon.Config{
		URL: cfg.URL, Token: cfg.Token, WorktreeRoot: cfg.WorktreeRoot, Version: platform.Version, Runtimes: runtimes,
		System: DetectSystem(ctx, cfg.WorktreeRoot),
	}, db, nil, slog.New(slog.DiscardHandler))

	nodeID, err := d.Hello(ctx)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "connected to %s as %s\n", cfg.URL, nodeID)

	for id, a := range cfg.Accounts {
		_, _ = fmt.Fprintf(stdout, "  account %s: %s\n", id, a.TokenFile)
	}

	for _, r := range runtimes {
		_, _ = fmt.Fprintf(stdout, "  %s %s (%s)\n", r.Runtime, r.Version, r.Path)
	}

	if len(runtimes) == 0 {
		_, _ = fmt.Fprintln(stdout, "  no CLI found on PATH: install claude or codex")
	}

	return nil
}

func runtimeIsUnix() bool { return os.PathSeparator == '/' }
