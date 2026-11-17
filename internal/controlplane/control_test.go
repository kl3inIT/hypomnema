package controlplane

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

	go func() {
		defer close(done)

		serveControl(ctx, path, func() []string { return []string{"cht_a"} }, slog.New(slog.DiscardHandler))
	}()

	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}
	t.Cleanup(client.CloseIdleConnections)

	var got selfupdate.Turns

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

	assert.Equal(t, []string{"cht_a"}, got.Chats)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSocket|0o600, info.Mode()&(os.ModeSocket|os.ModePerm))

	cancel()
	<-done

	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist, "the socket goes with the control plane")
}
