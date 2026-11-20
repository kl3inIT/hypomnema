package runner

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/node/tempns"
)

func TestWorkerTempIsolationSurvivesRestart(t *testing.T) {
	t.Parallel()
	fixture := newRig(t, "isolated-temp")
	for _, path := range []string{fixture.sup.Exe, fixture.sup.Home, fixture.worktree} {
		if strings.HasPrefix(path, "/tmp/") || strings.HasPrefix(path, "/var/tmp/") {
			t.Skip("set GOTMPDIR and TMPDIR outside the directories bubblewrap covers")
		}
	}
	for _, path := range []*string{&fixture.sup.Home, &fixture.worktree} {
		resolved, err := filepath.EvalSymlinks(filepath.Dir(*path))
		require.NoError(t, err)
		*path = filepath.Join(resolved, filepath.Base(*path))
	}
	fixture.sup.Isolation = tempns.Probe(t.Context(), "")
	if !fixture.sup.Isolation.On() {
		t.Skip(fixture.sup.Isolation.Reason)
	}
	checkAdoptedWorkerCanBeStopped(t, fixture)
}
