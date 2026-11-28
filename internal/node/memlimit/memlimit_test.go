package memlimit

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Scenario: the control plane sends a node's allocatable memory; the workers' slice gets it as MemoryMax, 90% of it
// as MemoryHigh and no swap. No ceiling, or one of 0, leaves the slice unlimited, as before (issue #1313 rows 2, 3).
func TestSlicePropertiesAreTheCeilingNinetyPercentAndNoSwap(t *testing.T) {
	t.Parallel()

	// aioffice-app's 33652387840 bytes, less a reserve of 4 GiB and the 1 GiB floor.
	assert.Equal(t, []string{"MemoryMax=28283678720", "MemoryHigh=25455310848", "MemorySwapMax=0"},
		SliceProperties(33652387840-5<<30))
	assert.Equal(t, []string{"MemoryMax=536870912", "MemoryHigh=483183820", "MemorySwapMax=0"},
		SliceProperties(512<<20))
	assert.Equal(t, []string{"MemoryMax=100", "MemoryHigh=90", "MemorySwapMax=0"}, SliceProperties(100))
	assert.Equal(t, []string{"MemoryMax=infinity", "MemoryHigh=infinity", "MemorySwapMax=infinity"},
		SliceProperties(0))
	assert.Equal(t, []string{"MemoryMax=infinity", "MemoryHigh=infinity", "MemorySwapMax=infinity"},
		SliceProperties(-1))
}

// Scenario: the shim reads its scope's memory.events when its worker exits; the oom_kill line says how many of its
// processes the kernel killed for memory (issue #1313 row 4).
func TestOOMKillsReadsMemoryEvents(t *testing.T) {
	t.Parallel()

	n, err := OOMKills([]byte("low 0\nhigh 4813\nmax 35\noom 1\noom_kill 1\noom_group_kill 0\n"))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	n, err = OOMKills([]byte("low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n"))
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	_, err = OOMKills([]byte("low 0\nhigh 0\n"))
	require.Error(t, err, "a file without oom_kill is no count")

	_, err = OOMKills([]byte("oom_kill x\n"))
	require.Error(t, err)
}

// Scenario: a worker runs in a scope of its own inside the workers' slice, which keeps going after the kernel kills
// one of its processes; a profile's limits.memory becomes the scope's MemoryMax, and none is set without one
// (issue #1313 rows 1, 4, 5). With the limit off the worker runs as it did.
func TestScopeRunsTheWorkerInTheSlice(t *testing.T) {
	t.Parallel()

	on := Limit{SystemdRun: "/usr/bin/systemd-run", Systemctl: "/usr/bin/systemctl", OOMPolicy: true}

	assert.Equal(t, []string{
		"/usr/bin/systemd-run", "--user", "--scope", "--quiet", "--collect", "--slice=hypomnema-workers.slice",
		"--unit=hypomnema-worker-dsp_1.scope", "--property=OOMPolicy=continue", "--",
	}, on.Scope("dsp_1", 0))
	assert.Equal(t, []string{
		"/usr/bin/systemd-run", "--user", "--scope", "--quiet", "--collect", "--slice=hypomnema-workers.slice",
		"--unit=hypomnema-worker-dsp_1.scope", "--property=OOMPolicy=continue", "--property=MemoryMax=4294967296", "--",
	}, on.Scope("dsp_1", 4<<30))

	older := on
	older.OOMPolicy = false
	assert.NotContains(t, older.Scope("dsp_1", 0), "--property=OOMPolicy=continue")

	assert.Nil(t, Limit{Reason: "no systemd"}.Scope("dsp_1", 4<<30))
}

// Scenario: the node says at hello whether its workers' memory is limited, and why not (issue #1313 row 6).
func TestHelloSaysWhyMemoryIsNotLimited(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &nodeproto.MemoryLimit{On: true}, Limit{SystemdRun: "/usr/bin/systemd-run"}.Hello())
	assert.Equal(t, &nodeproto.MemoryLimit{Reason: "no systemd"}, Limit{Reason: "no systemd"}.Hello())
}

// Scenario: the ceiling comes at hello and with every heartbeat's acknowledgement; the slice is set when it changes,
// not at each heartbeat, and one that failed is set again at the next. A node without the limit runs nothing.
func TestCeilingIsAppliedWhenItChanges(t *testing.T) {
	t.Parallel()

	var calls [][]string

	fail := false
	c := &Ceiling{
		Limit: Limit{SystemdRun: "systemd-run", Systemctl: "systemctl"},
		Run: func(_ context.Context, argv ...string) ([]byte, error) {
			calls = append(calls, argv)
			if fail {
				return []byte("Failed to connect to bus"), errors.New("exit status 1")
			}

			return nil, nil
		},
	}

	c.Apply(t.Context(), 8<<30)
	c.Apply(t.Context(), 8<<30)
	require.Len(t, calls, 1)
	assert.Equal(t, []string{
		"systemctl", "--user", "set-property", "hypomnema-workers.slice",
		"MemoryMax=8589934592", "MemoryHigh=7730941132", "MemorySwapMax=0",
	}, calls[0])

	fail = true
	c.Apply(t.Context(), 6<<30)
	require.Len(t, calls, 2)

	fail = false
	c.Apply(t.Context(), 6<<30)
	require.Len(t, calls, 3, "a ceiling that failed is tried again")

	c.Apply(t.Context(), 0)
	require.Len(t, calls, 4)
	assert.Equal(t, "MemoryMax=infinity", calls[3][4])

	off := &Ceiling{Limit: Limit{Reason: "no systemd"}, Run: func(context.Context, ...string) ([]byte, error) {
		t.Fatal("a node without the limit sets nothing")

		return nil, nil
	}}
	off.Apply(t.Context(), 8<<30)
}
