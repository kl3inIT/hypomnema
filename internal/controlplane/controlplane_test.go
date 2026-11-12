package controlplane_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The control plane started on a fresh path creates its database, reports
// ready, and stops cleanly when told to.
func TestServeIsReadyWithItsDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hypomnema.db")
	addr := freeAddr(t)

	cfg, err := controlplane.LoadConfig(func(name string) string {
		return map[string]string{
			"HYPOMNEMA_DATABASE_PATH": path,
			"HYPOMNEMA_HTTP_ADDR":     addr,
		}[name]
	})
	require.NoError(t, err)
	require.NoError(t, controlplane.Migrate(t.Context(), cfg, io.Discard))

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
	}, 10*time.Second, 50*time.Millisecond)

	_, err = os.Stat(path)
	require.NoError(t, err)

	cancel()
	require.NoError(t, <-served)
}

// Serving an unmigrated database is refused with the command that fixes it.
func TestServeRefusesAnUnmigratedDatabase(t *testing.T) {
	t.Parallel()

	cfg, err := controlplane.LoadConfig(func(name string) string {
		return map[string]string{
			"HYPOMNEMA_DATABASE_PATH": filepath.Join(t.TempDir(), "hypomnema.db"),
			"HYPOMNEMA_HTTP_ADDR":     freeAddr(t),
		}[name]
	})
	require.NoError(t, err)

	err = controlplane.Serve(t.Context(), cfg, io.Discard)
	require.ErrorIs(t, err, orchestration.ErrSchemaOutdated)
	require.ErrorContains(t, err, "hypomnema migrate")
}

func TestLoadConfigRequiresTheDatabasePath(t *testing.T) {
	t.Parallel()

	_, err := controlplane.LoadConfig(func(string) string { return "" })
	require.ErrorContains(t, err, "HYPOMNEMA_DATABASE_PATH")
}

// The command that runs a script Routine is a JSON array, as the one that
// starts pi is; anything else is refused when the control plane starts.
func TestLoadConfigReadsTheCommandForScriptRoutines(t *testing.T) {
	t.Parallel()

	load := func(command string) (controlplane.Config, error) {
		return controlplane.LoadConfig(func(name string) string {
			return map[string]string{
				"HYPOMNEMA_DATABASE_PATH":   filepath.Join(t.TempDir(), "hypomnema.db"),
				"HYPOMNEMA_ROUTINE_COMMAND": command,
			}[name]
		})
	}

	cfg, err := load(`["sudo","-n","-u","coordinator","/usr/local/bin/hypomnema-routine"]`)
	require.NoError(t, err)
	assert.Equal(t, []string{"sudo", "-n", "-u", "coordinator", "/usr/local/bin/hypomnema-routine"}, cfg.RoutineCommand)

	cfg, err = load("")
	require.NoError(t, err)
	assert.Empty(t, cfg.RoutineCommand)

	for _, bad := range []string{"sudo -n -u coordinator hypomnema-routine", "[]", `{"command":"sh"}`} {
		_, err = load(bad)
		require.ErrorContains(t, err, "HYPOMNEMA_ROUTINE_COMMAND is not a JSON array of strings", bad)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	return addr
}
