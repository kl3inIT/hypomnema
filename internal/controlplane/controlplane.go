// Package controlplane assembles the control plane: its configuration, its
// SQLite database, its HTTP server with the node API, the scheduler loop, and
// the owner's commands. The parts that make it useful (orchestration, vault,
// coordinator, channels, policy) are packages under this tree.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/controlplane/webui"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/web"
)

const (
	binary    = "hypomnema"
	envPrefix = "HYPOMNEMA"
)

// Config is the control plane's configuration, read from the environment.
type Config struct {
	HTTPAddr string
	Log      platform.LogConfig
	Database sqlite.Config
	// CoordinatorCommand starts pi for a chat id appended to it, from
	// HYPOMNEMA_COORDINATOR_COMMAND as a JSON array; empty: no coordinator.
	CoordinatorCommand []string
	// VaultKeys holds the vault's root keys, from HYPOMNEMA_VAULT_KEYS (a
	// comma-separated list of id:base64 entries) and
	// HYPOMNEMA_VAULT_ACTIVE_KEY_ID; nil: the vault is not configured.
	VaultKeys *vault.Keyring
	// PublicURL is the control plane's URL as nodes reach it, from
	// HYPOMNEMA_PUBLIC_URL; empty: the install script names the host it was
	// asked at.
	PublicURL string
	// NodeReleaseURL is where the install script downloads the node from,
	// from HYPOMNEMA_NODE_RELEASE_URL; empty: the latest GitHub release.
	NodeReleaseURL string
	// CodexIssuer is the authorization server Codex accounts log in at, from
	// HYPOMNEMA_CODEX_ISSUER; empty: OpenAI's. Tests name their own.
	CodexIssuer string
	// CodexBaseURL is the backend the gateway serves a Codex subscription
	// from, from HYPOMNEMA_CODEX_BASE_URL; empty: ChatGPT's. Tests name their own.
	CodexBaseURL string
	// AntigravityBaseURL is the Cloud Code host the gateway serves an
	// Antigravity subscription from, from HYPOMNEMA_ANTIGRAVITY_BASE_URL;
	// empty: Google's daily host. Tests name their own.
	AntigravityBaseURL string
	// Antigravity configures the Antigravity login: the OAuth secret of the
	// IDE's client, from HYPOMNEMA_ANTIGRAVITY_CLIENT_SECRET, which is never
	// in the source; empty: no Antigravity account can be logged in or
	// refreshed. Tests set its addresses.
	Antigravity subscription.AntigravityOptions
}

// LoadConfig reads the configuration and reports every problem at once.
func LoadConfig(getenv func(string) string) (Config, error) {
	env := platform.NewEnv(getenv)
	cfg := Config{
		HTTPAddr: env.String(envPrefix+"_HTTP_ADDR", "127.0.0.1:8080"),
		Log:      env.Log(envPrefix),
		Database: sqlite.FromEnv(env, envPrefix),

		PublicURL:          strings.TrimSuffix(env.String(envPrefix+"_PUBLIC_URL", ""), "/"),
		NodeReleaseURL:     env.String(envPrefix+"_NODE_RELEASE_URL", ""),
		CodexIssuer:        env.String(envPrefix+"_CODEX_ISSUER", ""),
		CodexBaseURL:       env.String(envPrefix+"_CODEX_BASE_URL", ""),
		AntigravityBaseURL: env.String(envPrefix+"_ANTIGRAVITY_BASE_URL", ""),
		Antigravity: subscription.AntigravityOptions{
			ClientSecret: env.String(envPrefix+"_ANTIGRAVITY_CLIENT_SECRET", ""),
		},
	}

	var cmdErr error
	if raw := env.String(envPrefix+"_COORDINATOR_COMMAND", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.CoordinatorCommand); err != nil || len(cfg.CoordinatorCommand) == 0 {
			cmdErr = fmt.Errorf("%s_COORDINATOR_COMMAND is not a JSON array of strings", envPrefix)
		}
	}

	keys, keysErr := vault.ParseKeyring(getenv(envPrefix+"_VAULT_KEYS"), getenv(envPrefix+"_VAULT_ACTIVE_KEY_ID"))
	cfg.VaultKeys = keys

	if err := errors.Join(env.Err(), cmdErr, keysErr); err != nil {
		return Config{}, fmt.Errorf("configuration: %w", err)
	}

	return cfg, nil
}

// Migrate brings the database schema up to this binary's version.
func Migrate(ctx context.Context, cfg Config, stdout io.Writer) error {
	logger := platform.NewLogger(stdout, cfg.Log, binary)

	db, err := sqlite.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Closing at exit; nothing is left to report it to.

	if err := orchestration.Migrate(ctx, db); err != nil {
		return err
	}

	logger.InfoContext(ctx, "database migrated", slog.String("path", cfg.Database.Path))

	return nil
}

// Serve runs the control plane until ctx is canceled. It refuses a database
// whose schema is not this binary's; `hypomnema migrate` fixes that.
func Serve(ctx context.Context, cfg Config, stdout io.Writer) error {
	logger := platform.NewLogger(stdout, cfg.Log, binary)

	db, err := sqlite.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Closing at shutdown; nothing is left to report it to.

	if err := orchestration.CheckSchema(ctx, db); err != nil {
		return err
	}

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	// The scheduler runs while the server does; if it fails, the server stops.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	mux := http.NewServeMux()
	platform.RegisterHealth(mux, sqlite.Ready(db), logger)
	secrets := vault.New(db, platform.SystemClock{}, platform.NewID, cfg.VaultKeys, secretReferrers(svc))
	tokens := connections{svc: svc, vault: secrets}
	machineMgr := machineManager(svc, secrets, cfg.PublicURL, logger)

	nodeapi.New(svc, logger).WithHostTokens(tokens.Token).Register(mux)
	mux.Handle("GET "+nodeapi.PathInstallScript, nodeapi.InstallScript(cfg.PublicURL, cfg.NodeReleaseURL))

	subscriptions := newSubscriptions(cfg, svc, secrets, logger)
	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, gatewayProviders(svc),
		gatewaySecrets(secrets))
	modelgateway.NewServer(gateway, logger, modelgateway.Config{
		Subscriptions: gatewayAccounts{subs: subscriptions}, CodexBaseURL: cfg.CodexBaseURL,
		AntigravityBaseURL: cfg.AntigravityBaseURL,
	}).Register(mux)

	sup := coordinator.New(svc, coordinator.Config{Command: cfg.CoordinatorCommand, Logger: logger})

	api := clientapi.New(svc, logger, ctx.Done()).WithCoordinator(sup).WithHostTokens(tokens).
		WithMachines(machineMgr)
	if err := api.Register(mux); err != nil {
		return err
	}

	if assets := web.Dist(); webui.Available(assets) {
		mux.Handle("/", webui.Handler(assets))
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.HTTPAddr, err)
	}

	loopDone := make(chan struct{})

	go func() {
		defer close(loopDone)

		if err := svc.Loop(ctx); err != nil {
			logger.ErrorContext(ctx, "scheduler stopped",
				slog.String("event", "controlplane.loop_failed"), slog.String("error", err.Error()))
			cancel(fmt.Errorf("scheduler: %w", err))
		}
	}()

	machinesDone := make(chan struct{})

	go func() {
		defer close(machinesDone)

		machineMgr.Loop(ctx)
	}()

	// Tokens of the accounts logged in here are replaced before they end.
	refreshDone := make(chan struct{})

	go func() {
		defer close(refreshDone)

		subscriptions.Loop(ctx)
	}()

	supDone := make(chan struct{})

	go func() {
		defer close(supDone)

		_ = sup.Run(ctx)
	}()

	serveErr := platform.Serve(ctx, ln, mux, logger)

	cancel(nil)
	<-loopDone
	<-machinesDone
	<-refreshDone
	<-supDone

	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return errors.Join(serveErr, cause)
	}

	return serveErr
}
