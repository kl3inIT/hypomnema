package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/selfupdate"
)

// Scenario (issue #716): the control socket, next to the database, answers which chats' turns run, to the control
// plane's user alone. A socket left by an earlier run is replaced, and the socket goes when the control plane stops.
func TestTheControlSocketSaysWhichTurnsRun(t *testing.T) {
	// Keep the long temporary path and address the socket relative to it so the Unix address fits.
	work := filepath.Join(t.TempDir(), strings.Repeat("a", 108))
	dir := filepath.Join(work, "data")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	t.Chdir(work)

	path := controlSocket(filepath.Join("data", "hypomnema.db"))
	assert.Equal(t, filepath.Join("data", "control.sock"), path)
	require.NoError(t, os.WriteFile(path, nil, 0o600), "a stale socket")

	ln, err := listenControl(t.Context(), path)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	defer func() {
		cancel()
		<-done
	}()

	// The update is marked inside the coordinator's lock: here, the call records that it went through it.
	var (
		mu     sync.Mutex
		locked bool
	)

	begin := func(mark func() error) error {
		mu.Lock()
		defer mu.Unlock()

		locked = true

		return mark()
	}

	go func() {
		defer close(done)

		serveControl(ctx, ln, func() []string { return []string{"cht_a"} }, func() int { return 2 },
			func() int { return 1 }, begin, slog.New(slog.NewTextHandler(t.Output(), nil)))
	}()

	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
	t.Cleanup(client.CloseIdleConnections)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://control"+selfupdate.PathTurns, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck // read only
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got selfupdate.Turns
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, selfupdate.Turns{Chats: []string{"cht_a"}, ToolCalls: 2, Renders: 1}, got)

	// The update names itself: the control plane writes the marker, through the coordinator's lock.
	body := strings.NewReader(fmt.Sprintf(`{"pid":%d,"start":""}`, os.Getpid()))
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, "http://control"+selfupdate.PathUpdate, body)
	require.NoError(t, err)

	resp, err = client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.True(t, selfupdate.Updating(dir), "the marker names a process that runs")

	mu.Lock()
	assert.True(t, locked)
	mu.Unlock()

	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, "http://control"+selfupdate.PathUpdate,
		strings.NewReader(`{"pid":0}`))
	require.NoError(t, err)

	resp, err = client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode, "an update with no process is refused")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSocket|0o600, info.Mode()&(os.ModeSocket|os.ModePerm))

	cancel()
	<-done

	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist, "the socket goes with the control plane")
}
