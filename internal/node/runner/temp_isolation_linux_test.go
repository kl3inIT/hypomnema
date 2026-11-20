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
	g := newRig(t, "isolated-temp")
	for _, path := range []string{g.sup.Exe, g.sup.Home, g.worktree} {
		if strings.HasPrefix(path, "/tmp/") || strings.HasPrefix(path, "/var/tmp/") {
			t.Skip("set GOTMPDIR and TMPDIR outside the directories bubblewrap covers")
		}
	}
	for _, path := range []*string{&g.sup.Home, &g.worktree} {
		resolved, err := filepath.EvalSymlinks(filepath.Dir(*path))
		require.NoError(t, err)
		*path = filepath.Join(resolved, filepath.Base(*path))
	}
	g.sup.Isolation = tempns.Probe(t.Context(), "")
	if !g.sup.Isolation.On() {
		t.Skip(g.sup.Isolation.Reason)
	}
	checkAdoptedWorkerCanBeStopped(t, g)
}
