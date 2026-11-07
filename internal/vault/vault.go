// Package vault assembles the vault: its configuration, its own database and
// its HTTP server. Keys, secrets and sessions are packages under this tree,
// added by a later issue. Nothing outside this tree may import it.
package vault

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/postgres"
)

const (
	binary    = "hypomnema-vault"
	envPrefix = "HYPOMNEMA_VAULT"
)

// Config is the vault's configuration, read from the environment.
type Config struct {
	HTTPAddr string
	Log      platform.LogConfig
	Database postgres.Config
}

// LoadConfig reads the configuration and reports every problem at once.
func LoadConfig(getenv func(string) string) (Config, error) {
	env := platform.NewEnv(getenv)
	cfg := Config{
		HTTPAddr: env.String(envPrefix+"_HTTP_ADDR", "127.0.0.1:8090"),
		Log:      env.Log(envPrefix),
		Database: postgres.FromEnv(env, envPrefix),
	}

	if err := env.Err(); err != nil {
		return Config{}, fmt.Errorf("configuration: %w", err)
	}

	return cfg, nil
}

// Serve runs the vault until ctx is canceled.
func Serve(ctx context.Context, cfg Config, stdout io.Writer) error {
	logger := platform.NewLogger(stdout, cfg.Log, binary)

	pool, err := postgres.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	mux := http.NewServeMux()
	platform.RegisterHealth(mux, pool.Ping, logger)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.HTTPAddr, err)
	}

	return platform.Serve(ctx, ln, mux, logger)
}
