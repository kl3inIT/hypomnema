// Package controlplane assembles the control plane: its configuration, its
// SQLite database, its HTTP server with the node API, the scheduler loop, and
// the owner's commands. The parts that make it useful (orchestration, vault,
// coordinator, channels, policy) are packages under this tree.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
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
}

// LoadConfig reads the configuration and reports every problem at once.
func LoadConfig(getenv func(string) string) (Config, error) {
	env := platform.NewEnv(getenv)
	cfg := Config{
		HTTPAddr: env.String(envPrefix+"_HTTP_ADDR", "127.0.0.1:8080"),
		Log:      env.Log(envPrefix),
		Database: sqlite.FromEnv(env, envPrefix),
	}

	if err := env.Err(); err != nil {
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
	nodeapi.New(svc, logger).Register(mux)

	if err := clientapi.New(svc, logger, ctx.Done()).Register(mux); err != nil {
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

	serveErr := platform.Serve(ctx, ln, mux, logger)

	cancel(nil)
	<-loopDone

	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return errors.Join(serveErr, cause)
	}

	return serveErr
}
