//go:build integration

package controlplane_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/kl3inIT/hypomnema/internal/controlplane"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The control plane started against a real PostgreSQL reports ready, and
// stops cleanly when told to.
func TestServeIsReadyWithItsDatabase(t *testing.T) {
	t.Parallel()

	pg, err := tcpostgres.Run(t.Context(), "postgres:18-alpine",
		tcpostgres.WithDatabase("hypomnema"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, pg)
	require.NoError(t, err)

	url, err := pg.ConnectionString(t.Context(), "sslmode=disable")
	require.NoError(t, err)

	addr := freeAddr(t)
	cfg, err := controlplane.LoadConfig(func(name string) string {
		return map[string]string{
			"HYPOMNEMA_DATABASE_URL": url,
			"HYPOMNEMA_HTTP_ADDR":    addr,
		}[name]
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)

	go func() { served <- controlplane.Serve(ctx, cfg, io.Discard) }()

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+platform.PathReady, nil)
		require.NoError(c, err)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(c, err)
		_ = resp.Body.Close()
		assert.Equal(c, http.StatusNoContent, resp.StatusCode)
	}, 10*time.Second, 100*time.Millisecond)

	cancel()
	require.NoError(t, <-served)
}

func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	return addr
}
