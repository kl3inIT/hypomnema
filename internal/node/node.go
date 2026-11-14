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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/acp"
	"github.com/kl3inIT/hypomnema/internal/node/claude"
	"github.com/kl3inIT/hypomnema/internal/node/codex"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/device"
	"github.com/kl3inIT/hypomnema/internal/node/runner"
	"github.com/kl3inIT/hypomnema/internal/node/skills"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/node/terminal"
	"github.com/kl3inIT/hypomnema/internal/node/transcript"
	"github.com/kl3inIT/hypomnema/internal/node/worktree"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Runtimes the node looks for on its PATH, by CLI name (Orca local-agent-cli-presence.ts).
var knownRuntimes = append([]string{"claude", "codex"}, acp.Runtimes()...)

const versionTimeout = 10 * time.Second

// Config is the node's configuration file.
type Config struct {
	// URL is the control plane's base URL.
	URL string `json:"url"`
	// Token is the node's token, from `hypomnema nodes add`.
	Token string `json:"token"`
	// WorktreeRoot is where the node makes its worktrees.
	WorktreeRoot string `json:"worktree_root"`
	// DisableAutoUpdate keeps the node at its version (Nezha's
	// disable_auto_update). UpdateEveryMinutes sets how often it looks for a
	// newer release (Nezha's self_update_period); zero is a random interval of
	// one to two days.
	DisableAutoUpdate  bool `json:"disable_auto_update,omitempty"`
	UpdateEveryMinutes int  `json:"update_every_minutes,omitempty"`
	// DisableTerminal turns off the terminal the owner opens from the web. It
	// is set only here, on the machine: the control plane cannot turn it back
	// on (Nezha's disable_command_execute).
	DisableTerminal bool `json:"disable_terminal,omitempty"`
	// Device is the device this machine holds: "android" for a machine made
	// to run an Android emulator, which its first-boot script sets with
	// `install --device android`; empty for none (issue #417).
	Device string `json:"device,omitempty"`
	// Accounts maps an account id of the control plane to where its
	// credentials are on this node, for an offer that brings no token of its
	// own. Only the file's path is here, never the token.
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

	return Configure(home, rawURL, strings.TrimSpace(string(raw)), worktreeRoot, "")
}

// Configure writes the configuration with the token given. An existing
// configuration keeps its accounts, and its device unless one is named.
func Configure(home, rawURL, token, worktreeRoot, device string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("the control plane URL %q must be http(s)://host", rawURL)
	}

	if !strings.HasPrefix(token, "hyn_") {
		return errors.New("that does not hold a node token (hyn_…)")
	}

	if device != "" && device != nodeproto.DeviceAndroid {
		return fmt.Errorf("the device %q is not one a node holds; it can be %s", device, nodeproto.DeviceAndroid)
	}

	// An install over a node that is already there changes where it connects and with what, and nothing else:
	// what the machine's owner set in its configuration stays, the worktree root too unless another is named.
	old, oldErr := LoadConfig(home)

	if worktreeRoot == "" && oldErr == nil {
		worktreeRoot = old.WorktreeRoot
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

	cfg := Config{}
	if oldErr == nil {
		cfg = old
	}

	cfg.URL, cfg.Token, cfg.WorktreeRoot = strings.TrimSuffix(rawURL, "/"), token, worktreeRoot
	if device != "" {
		cfg.Device = device
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

// Run runs the daemon with the runtimes' workers until ctx ends.
func Run(ctx context.Context, home string, logger *slog.Logger) error {
	cfg, err := LoadConfig(home)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding the node binary: %w", err)
	}

	runtimes := FindRuntimes(ctx, home, pinnedRuntimes())

	claudePath, codexPath := "", ""
	paths := map[string]string{}

	for _, r := range runtimes {
		paths[r.Runtime] = r.Path

		switch r.Runtime {
		case "claude":
			claudePath = r.Path
		case "codex":
			codexPath = r.Path
		}
	}

	db, err := statedb.Open(ctx, StatePath(home), time.Now)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Closing at shutdown; nothing is left to report it to.

	logger.InfoContext(ctx, "node starting", slog.String("event", "node.starting"), slog.String("url", cfg.URL),
		slog.Int("runtimes", len(runtimes)), slog.Int("accounts", len(cfg.Accounts)))

	// The shells the owner opens from the web end with the node.
	var terminals *terminal.Manager
	if terminal.Supported() && !cfg.DisableTerminal {
		terminals = terminal.New(terminal.Options{})
		defer terminals.Close()
	}

	// The phone of a machine made to hold one (issue #417): installed and started when the control plane asks.
	// It is left running when the node ends, and the next node takes it back.
	var (
		phone     *device.Manager
		phoneSays daemon.Phone
	)

	if cfg.Device == nodeproto.DeviceAndroid {
		phone = device.New(device.Options{Dir: filepath.Join(home, "device"), KVM: CanUseKVM, Logger: logger})
		phoneSays = phone

		go phone.Run(ctx)
	}

	// The agents that speak ACP share one runtime (issue #107).
	acpRuntime := &acp.ACPRuntime{Home: home, Exe: exe, Paths: paths, URL: cfg.URL}

	byRuntime, err := registry(
		entry{"claude", &claude.ClaudeRuntime{
			Home: home, Exe: exe, Claude: claudePath, URL: cfg.URL, Accounts: cfg.Accounts,
		}},
		// A Codex worker's model comes from the control plane's gateway (issue #107).
		entry{"codex", &codex.CodexRuntime{Home: home, Exe: exe, Codex: codexPath, URL: cfg.URL}},
		entry{"omp", acpRuntime}, entry{"opencode", acpRuntime},
	)
	if err != nil {
		return err
	}

	// Workers' transcripts go to the control plane beside their reports, whatever the runtime (issues #43, #157).
	transcripts := &transcript.Forwarder{
		Home: home, Logger: logger,
		Decoders: map[string]func() transcript.Decoder{
			claude.Format: claude.NewDecoder, codex.Format: codex.NewDecoder, acp.Format: acp.NewDecoder,
		},
	}

	all := runners{
		supervisor: &runner.Supervisor{
			Home: home, Exe: exe, URL: cfg.URL, Logger: logger, Worktrees: worktree.Maker{Root: home, Logger: logger},
		},
		byRuntime: byRuntime, transcripts: transcripts, skills: skills.Cache{
			Home: home, URL: cfg.URL, Token: cfg.Token, Client: &http.Client{Timeout: 5 * time.Minute},
		},
	}

	d := daemon.New(daemon.Config{
		URL: cfg.URL, Token: cfg.Token, WorktreeRoot: cfg.WorktreeRoot, Version: platform.Version, Runtimes: runtimes,
		System:      DetectSystem(ctx, cfg.WorktreeRoot),
		Stats:       func(ctx context.Context) *nodeproto.SystemStats { return MeasureSystem(ctx, cfg.WorktreeRoot) },
		Reach:       ReachHost,
		InstallTool: agenttool.ToolInstaller{Home: home}.Install,
		FindRuntimes: func(ctx context.Context, tools []nodeproto.ToolState) []nodeproto.RuntimeInfo {
			found := FindRuntimes(ctx, home, tools)

			paths := map[string]string{}
			for _, r := range found {
				paths[r.Runtime] = r.Path
			}

			acpRuntime.SetPaths(paths)

			return found
		},
		Terminal: terminals,
		Device:   cfg.Device, KVM: CanUseKVM, Phone: phoneSays,
	}, db, all, logger)

	transcripts.Send = d.SendItems

	go transcripts.Forward(ctx)

	// The node updates itself only where ending is a restart: under systemd, which sets INVOCATION_ID.
	updater := Updater{Exe: exe, Version: platform.Version}
	if cfg.DisableAutoUpdate || !updater.CanUpdate() || os.Getenv("INVOCATION_ID") == "" {
		return d.Run(ctx)
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	go func() {
		// An update waits for a terminal, and for the phone while it is installed or started: a restart would cut
		// that short. A phone that runs is left running by the restart and taken back after it.
		busy := func() bool {
			if terminals != nil && terminals.Count() > 0 {
				return true
			}

			if phone == nil {
				return false
			}

			switch phone.Status().State {
			case nodeproto.DeviceInstalling, nodeproto.DeviceStarting:
				return true
			default:
				return false
			}
		}
		every := time.Duration(cfg.UpdateEveryMinutes) * time.Minute

		if err := updater.UpdateLoop(ctx, d.ReleaseURL, busy, every, logger); errors.Is(err, ErrUpdated) {
			cancel(ErrUpdated)
		}
	}()

	err = d.Run(ctx)
	if errors.Is(context.Cause(ctx), ErrUpdated) {
		return ErrUpdated
	}

	return err
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

// FindRuntimes finds the agent CLIs and reads their versions: one the control plane had the node install, under home's tools, else one on
// the PATH (issue #107), so a node with its own copy keeps it until the installed one is there.
func FindRuntimes(ctx context.Context, home string, installed []nodeproto.ToolState) []nodeproto.RuntimeInfo {
	out := []nodeproto.RuntimeInfo{}

	for _, name := range knownRuntimes {
		path, err := exec.LookPath(name)

		for _, t := range installed {
			if t.Name != name || t.Version == "" || t.Error != "" || home == "" {
				continue
			}

			if info, statErr := os.Stat(agenttool.ToolPath(home, name, t.Version)); statErr == nil && info.Mode().IsRegular() {
				path, err = agenttool.ToolPath(home, name, t.Version), nil
			}
		}

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

	runtimes := FindRuntimes(ctx, home, pinnedRuntimes())

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

// pinnedRuntimes are the agent CLIs at the versions this release pins, as the tools the node may have installed
// already: at start the node finds them before the control plane names them again.
func pinnedRuntimes() []nodeproto.ToolState {
	out := make([]nodeproto.ToolState, 0, len(agenttool.Runtimes()))
	for _, d := range agenttool.Runtimes() {
		out = append(out, nodeproto.ToolState{Name: d.Name, Version: d.Version})
	}

	return out
}
