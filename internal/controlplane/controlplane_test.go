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

// The profiler (issue #586) is off unless asked for, and is taken on a loopback address only: it shows the process's
// memory and command line, so it is never reachable from another machine.
func TestLoadConfigTakesTheProfilerOnLoopbackOnly(t *testing.T) {
	t.Parallel()

	load := func(addr string) (controlplane.Config, error) {
		return controlplane.LoadConfig(func(name string) string {
			return map[string]string{
				"HYPOMNEMA_DATABASE_PATH": filepath.Join(t.TempDir(), "hypomnema.db"),
				"HYPOMNEMA_PPROF_ADDR":    addr,
			}[name]
		})
	}

	cfg, err := load("")
	require.NoError(t, err)
	assert.Empty(t, cfg.PprofAddr, "off by default")

	for _, ok := range []string{"127.0.0.1:6060", "[::1]:6060"} {
		cfg, err = load(ok)
		require.NoError(t, err, ok)
		assert.Equal(t, ok, cfg.PprofAddr)
	}

	for _, bad := range []string{":6060", "0.0.0.0:6060", "[::]:6060", "10.0.0.5:6060", "localhost:6060", "127.0.0.1"} {
		_, err = load(bad)
		require.ErrorContains(t, err, "HYPOMNEMA_PPROF_ADDR", bad)
	}
}

// The profiler answers on its own address, and the control plane's server does not serve it.
func TestServeServesTheProfilerOnItsOwnListener(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "hypomnema.db")

	cfg, err := controlplane.LoadConfig(func(name string) string {
		return map[string]string{"HYPOMNEMA_DATABASE_PATH": path, "HYPOMNEMA_PPROF_ADDR": "127.0.0.1:0"}[name]
	})
	require.NoError(t, err)
	require.NoError(t, controlplane.Migrate(t.Context(), cfg, io.Discard))

	cfg.HTTPAddr = "127.0.0.1:0"
	cfg.FFmpegOff = true
	log := &startedLog{started: make(chan string, 1), profiler: make(chan string, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)

	go func() { served <- controlplane.Serve(ctx, cfg, log) }()

	var addr, profiler string

	for addr == "" || profiler == "" {
		select {
		case addr = <-log.started:
		case profiler = <-log.profiler:
		case err := <-served:
			require.FailNow(t, "the control plane ended before it listened", "%v", err)
		case <-time.After(30 * time.Second):
			require.FailNow(t, "the control plane or its profiler did not start within 30 s")
		}
	}

	get := func(base string) (int, string) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			"http://"+base+"/debug/pprof/goroutine?debug=1", nil)
		require.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)

		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		return resp.StatusCode, string(body)
	}

	code, body := get(profiler)
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "goroutine profile:")

	assert.NotEqual(t, addr, profiler)
	_, body = get(addr)
	assert.NotContains(t, body, "goroutine profile:", "the control plane's own server has no profiler")

	cancel()
	require.NoError(t, <-served)
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

// The command that removes a deleted chat's files (issue #484) is a JSON array, as the other commands are; anything
// else is refused when the control plane starts.
func TestLoadConfigReadsTheCommandForADeletedChatsFiles(t *testing.T) {
	t.Parallel()

	load := func(command string) (controlplane.Config, error) {
		return controlplane.LoadConfig(func(name string) string {
			return map[string]string{
				"HYPOMNEMA_DATABASE_PATH":   filepath.Join(t.TempDir(), "hypomnema.db"),
				"HYPOMNEMA_CHAT_RM_COMMAND": command,
			}[name]
		})
	}

	cfg, err := load(`["sudo","-n","-u","coordinator","/usr/local/bin/hypomnema-chat-rm"]`)
	require.NoError(t, err)
	assert.Equal(t, []string{"sudo", "-n", "-u", "coordinator", "/usr/local/bin/hypomnema-chat-rm"}, cfg.ChatRmCommand)

	cfg, err = load("")
	require.NoError(t, err)
	assert.Empty(t, cfg.ChatRmCommand)

	_, err = load("sudo hypomnema-chat-rm")
	require.ErrorContains(t, err, "HYPOMNEMA_CHAT_RM_COMMAND is not a JSON array of strings")
}

// serve starts the control plane on a port of the system's choosing (HYPOMNEMA_HTTP_ADDR 127.0.0.1:0) and returns
// the address it listens on, read from its "server started" log line, and the channel Serve's end is sent on. A
// port picked first and closed again could be taken by another test before Serve listened on it, and the test
// waited ten seconds on "connection refused" (issue #286); a Serve that ends early fails the test with its error.
func serve(t *testing.T, ctx context.Context, cfg controlplane.Config) (string, <-chan error) {
	t.Helper()

	cfg.HTTPAddr = "127.0.0.1:0"
	cfg.FFmpegOff = true // No test downloads ffmpeg.
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

// startedLog reads the address of the control plane's "server started" line among its JSON log lines, and that of
// its profiler's when profiler is not nil.
type startedLog struct {
	mu       sync.Mutex
	partial  []byte
	started  chan string
	profiler chan string
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
		if json.Unmarshal(line, &entry) != nil {
			continue
		}

		to := map[string]chan string{
			"platform.server.started": l.started, "controlplane.pprof.started": l.profiler,
		}[entry.Event]
		select {
		case to <- entry.Addr:
		default:
		}
	}
}
