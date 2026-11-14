package e2e

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// Scenario: two nodes say hello, one whose configuration says it holds an
// Android emulator and whose user can use /dev/kvm, and a plain one (issue
// #417). The owner's list of nodes shows the first as holding the device
// and able to run it, and the second as holding none.
func TestNodeSaysAtHelloThatItHoldsAnAndroidDevice(t *testing.T) {
	t.Parallel()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.DiscardHandler)
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	nodeapi.New(svc, logger).Register(mux)
	require.NoError(t, clientapi.New(svc, logger, stop).Register(mux))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	hello := func(name, device string, kvm func() bool) {
		_, token, err := svc.AddNode(t.Context(), name, 1)
		require.NoError(t, err)

		state, err := statedb.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), time.Now)
		require.NoError(t, err)
		t.Cleanup(func() { _ = state.Close() })

		_, err = daemon.New(daemon.Config{
			URL: srv.URL, Token: token, WorktreeRoot: "/srv/wt", Version: "test", Device: device, KVM: kvm,
		}, state, newFakeRunner(), logger).Hello(t.Context())
		require.NoError(t, err)
	}

	hello("phone", nodeproto.DeviceAndroid, func() bool { return true })
	hello("build", "", nil)

	_, owner, err := svc.AddToken(t.Context(), "owner", bearertoken.Owner)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+clientapi.BasePath+"/nodes", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+owner)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got struct {
		Data []struct {
			Name   string `json:"name"`
			Device string `json:"device"`
			KVM    *bool  `json:"kvm"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

	byName := map[string]int{}
	for i, n := range got.Data {
		byName[n.Name] = i
	}

	require.Len(t, got.Data, 2)

	phone, build := got.Data[byName["phone"]], got.Data[byName["build"]]
	assert.Equal(t, "android", phone.Device)
	require.NotNil(t, phone.KVM)
	assert.True(t, *phone.KVM)
	assert.Empty(t, build.Device)
	assert.Nil(t, build.KVM)
}
