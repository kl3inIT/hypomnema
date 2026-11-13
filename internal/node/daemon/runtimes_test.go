package daemon

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Scenario: the node starts with omp on its PATH and no opencode. The control
// plane has it install both; once they are installed, the heartbeat says the
// runtimes it now has, and only then: a heartbeat with the same tools says
// nothing more, and a hello after it names the new runtimes (issue #107).
func TestTheNodeSaysTheRuntimesItInstalled(t *testing.T) {
	t.Parallel()

	start := []nodeproto.RuntimeInfo{{Runtime: "omp", Path: "/usr/bin/omp", Version: "18.0.0"}}
	installed := []nodeproto.RuntimeInfo{
		{Runtime: "omp", Path: "/home/n/tools/omp/18.18.6/omp", Version: "18.18.6"},
		{Runtime: "opencode", Path: "/home/n/tools/opencode/2.2.7/opencode", Version: "2.2.7"},
	}

	finds := 0
	d := New(Config{
		Runtimes: start,
		FindRuntimes: func(_ context.Context, tools []nodeproto.ToolState) []nodeproto.RuntimeInfo {
			finds++

			if len(tools) == 2 && tools[0].Version != "" && tools[1].Version != "" {
				return installed
			}

			return start
		},
	}, nil, nil, nil)

	assert.Nil(t, d.changedRuntimes(t.Context(), nil), "before the tools are tried, nothing is said")

	failing := []nodeproto.ToolState{{Name: "omp", Version: "18.18.6"}, {Name: "opencode", Error: "status 503"}}
	assert.Nil(t, d.changedRuntimes(t.Context(), &failing), "the runtimes did not change")

	done := []nodeproto.ToolState{{Name: "omp", Version: "18.18.6"}, {Name: "opencode", Version: "2.2.7"}}
	said := d.changedRuntimes(t.Context(), &done)
	require.NotNil(t, said)
	assert.Equal(t, installed, *said)

	assert.Nil(t, d.changedRuntimes(t.Context(), &done), "the same tools: nothing is said again")
	assert.Equal(t, 2, finds, "nor looked for again")
	assert.Equal(t, installed, d.currentRuntimes(), "a hello after it names them")
}
