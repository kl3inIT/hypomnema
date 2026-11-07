// Package postgres opens the PostgreSQL pool of the control plane and of the
// vault, each with its own database. The node never links it: the dependency
// check in the Makefile refuses pgx in the node binary.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Config is how to reach one database.
type Config struct {
	URL            string
	MaxConns       int32
	AcquireTimeout time.Duration
}

// FromEnv reads prefix_DATABASE_URL and prefix_DATABASE_MAX_CONNS.
func FromEnv(env *platform.Env, prefix string) Config {
	return Config{
		URL:            env.Required(prefix + "_DATABASE_URL"),
		MaxConns:       int32(env.Int(prefix+"_DATABASE_MAX_CONNS", 10, 1, 100)), //nolint:gosec // bounded to 100 above.
		AcquireTimeout: 5 * time.Second,
	}
}

// Open opens the pool with an explicit size and checks that the database
// answers.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing the database URL: %w", err)
	}

	pcfg.MaxConns = cfg.MaxConns

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("opening the database pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.AcquireTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()

		return nil, fmt.Errorf("reaching the database: %w", err)
	}

	return pool, nil
}
