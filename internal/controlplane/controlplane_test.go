package controlplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
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

	cfg, err := controlplane.LoadConfig(func(name string) string {
		return map[string]string{"HYPOMNEMA_DATABASE_PATH": path}[name]
	})
	require.NoError(t, err)
	require.NoError(t, controlplane.Migrate(t.Context(), cfg, io.Discard))

	ctx, cancel := context.WithCancel(t.Context())
	addr, served := serve(t, ctx, cfg)

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
			"HYPOMNEMA_HTTP_ADDR":     "127.0.0.1:0",
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

// serve starts the control plane on a port of the system's choosing (HYPOMNEMA_HTTP_ADDR 127.0.0.1:0) and returns
// the address it listens on, read from its "server started" log line, and the channel Serve's end is sent on. A
// port picked first and closed again could be taken by another test before Serve listened on it, and the test
// waited ten seconds on "connection refused" (issue #286); a Serve that ends early fails the test with its error.
func serve(t *testing.T, ctx context.Context, cfg controlplane.Config) (string, <-chan error) {
	t.Helper()

	cfg.HTTPAddr = "127.0.0.1:0"
	log := &startedLog{started: make(chan string, 1)}
	served := make(chan error, 1)

	go func() { served <- controlplane.Serve(ctx, cfg, log) }()

	select {
	case addr := <-log.started:
		return addr, served
	case err := <-served:
		require.FailNow(t, "the control plane ended before it listened", "%v", err)
	case <-time.After(30 * time.Second):
		require.FailNow(t, "the control plane did not start within 30 s")
	}

	return "", served
}

// startedLog reads the address of the control plane's "server started" line among its JSON log lines.
type startedLog struct {
	mu      sync.Mutex
	partial []byte
	started chan string
}

func (l *startedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.partial = append(l.partial, p...)

	for {
		line, rest, ok := bytes.Cut(l.partial, []byte{'\n'})
		if !ok {
			return len(p), nil
		}

		l.partial = rest

		var entry struct {
			Event string `json:"event"`
			Addr  string `json:"addr"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Event == "platform.server.started" {
			select {
			case l.started <- entry.Addr:
			default:
			}
		}
	}
}

// The web search's settings (issue #354): the owner's SearXNG, which must be
// an http(s) address, and the vault secret Exa's key is read from.
func TestLoadConfigReadsTheWebSearchSettings(t *testing.T) {
	t.Parallel()

	load := func(env map[string]string) (controlplane.Config, error) {
		return controlplane.LoadConfig(func(name string) string {
			if name == "HYPOMNEMA_DATABASE_PATH" {
				return filepath.Join(t.TempDir(), "hypomnema.db")
			}

			return env[name]
		})
	}

	cfg, err := load(nil)
	require.NoError(t, err)
	assert.Empty(t, cfg.WebSearchSearXNGURL)
	assert.Equal(t, "web-search-exa", cfg.WebSearchExaSecret)

	cfg, err = load(map[string]string{
		"HYPOMNEMA_WEB_SEARCH_SEARXNG_URL": "http://10.0.0.5:8888/",
		"HYPOMNEMA_WEB_SEARCH_EXA_SECRET":  "exa",
	})
	require.NoError(t, err)
	assert.Equal(t, "http://10.0.0.5:8888", cfg.WebSearchSearXNGURL)
	assert.Equal(t, "exa", cfg.WebSearchExaSecret)

	for _, bad := range []string{"10.0.0.5:8888", "ftp://searx.example", "http://"} {
		_, err := load(map[string]string{"HYPOMNEMA_WEB_SEARCH_SEARXNG_URL": bad})
		require.ErrorContains(t, err, "HYPOMNEMA_WEB_SEARCH_SEARXNG_URL", bad)
	}
}
