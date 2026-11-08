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

	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
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
	// Roles are the role defaults, from HYPOMNEMA_ROLES as JSON, for example
	// {"implement": {"runtime": "claude", "model": "opus"}}.
	Roles map[string]orchestration.Role
}

// defaultRoles: an implementer runs on Claude Code unless configured.
const defaultRoles = `{"implement": {"runtime": "claude"}}`

type roleConfig struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

// LoadConfig reads the configuration and reports every problem at once.
func LoadConfig(getenv func(string) string) (Config, error) {
	env := platform.NewEnv(getenv)
	cfg := Config{
		HTTPAddr: env.String(envPrefix+"_HTTP_ADDR", "127.0.0.1:8080"),
		Log:      env.Log(envPrefix),
		Database: sqlite.FromEnv(env, envPrefix),
	}

	roles, rolesErr := parseRoles(env.String(envPrefix+"_ROLES", defaultRoles))
	cfg.Roles = roles

	if err := errors.Join(env.Err(), rolesErr); err != nil {
		return Config{}, fmt.Errorf("configuration: %w", err)
	}

	return cfg, nil
}

func parseRoles(raw string) (map[string]orchestration.Role, error) {
	var in map[string]roleConfig
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, fmt.Errorf("%s_ROLES is not a JSON object of roles: %w", envPrefix, err)
	}

	out := make(map[string]orchestration.Role, len(in))
	for name, r := range in {
		out[name] = orchestration.Role(r)
	}

	return out, nil
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

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID, orchestration.WithRoles(cfg.Roles))

	mux := http.NewServeMux()
	platform.RegisterHealth(mux, sqlite.Ready(db), logger)
	nodeapi.New(svc, logger).Register(mux)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.HTTPAddr, err)
	}

	// The scheduler runs while the server does; if it fails, the server stops.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	loopDone := make(chan struct{})

	go func() {
		defer close(loopDone)

		if err := svc.Loop(ctx); err != nil {
			logger.ErrorContext(ctx, "scheduler stopped",
				slog.String("event", "controlplane.loop_failed"), slog.String("error", err.Error()))
			cancel(fmt.Errorf("scheduler: %w", err))
		}
	}()

	serveErr := platform.Serve(ctx, ln, mux, logger)

	cancel(nil)
	<-loopDone

	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return errors.Join(serveErr, cause)
	}

	return serveErr
}
