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
	t.Parallel()

	dir := t.TempDir()
	path := controlSocket(filepath.Join(dir, "hypomnema.db"))
	assert.Equal(t, filepath.Join(dir, "control.sock"), path)
	require.NoError(t, os.WriteFile(path, nil, 0o600), "a stale socket")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

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

		serveControl(ctx, path, func() []string { return []string{"cht_a"} }, func() int { return 2 }, begin,
			slog.New(slog.DiscardHandler))
	}()

	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
	t.Cleanup(client.CloseIdleConnections)

	var got struct {
		selfupdate.Turns
		Renders int `json:"renders"`
	}

	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://control"+selfupdate.PathTurns, nil)
		if err != nil {
			return false
		}

		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close() //nolint:errcheck // read only

		return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&got) == nil
	}, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, selfupdate.Turns{Chats: []string{"cht_a"}, ToolCalls: 2}, got.Turns)
	assert.Equal(t, 1, got.Renders)

	// The update names itself: the control plane writes the marker, through the coordinator's lock.
	body := strings.NewReader(fmt.Sprintf(`{"pid":%d,"start":""}`, os.Getpid()))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://control"+selfupdate.PathUpdate, body)
	require.NoError(t, err)

	resp, err := client.Do(req)
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
