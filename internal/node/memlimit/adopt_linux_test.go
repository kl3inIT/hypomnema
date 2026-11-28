package memlimit

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCgroups is a host whose processes fork while they are moved: each listing of the worker's tree finds the
// process the last one forked, until every process is in a frozen cgroup, where none forks.
type fakeCgroups struct {
	in      map[int]string // the cgroup of each process
	frozen  map[string]bool
	forks   []int // the processes forked one after another, each by the one before it
	parent  map[int]int
	calls   []string
	started []int
}

func (f *fakeCgroups) ops() cgroupOps {
	return cgroupOps{
		run: func(_ context.Context, argv ...string) ([]byte, error) {
			// StartTransientUnit moves the processes it lists into the scope.
			at := slices.Index(argv, "au")
			n, _ := strconv.Atoi(argv[at+1])
			for _, raw := range argv[at+2 : at+2+n] {
				pid, _ := strconv.Atoi(raw)
				f.started = append(f.started, pid)
				f.in[pid] = scopeDir
			}
			f.calls = append(f.calls, "start")

			return nil, nil
		},
		cgroupOf: func(pid int) (string, error) { return f.in[pid], nil },
		moveInto: func(dir string, pid int) error {
			f.in[pid] = dir
			f.calls = append(f.calls, "move "+strconv.Itoa(pid))

			return nil
		},
		freeze: func(dir string, frozen bool) error {
			f.frozen[dir] = frozen
			f.calls = append(f.calls, "freeze "+strconv.FormatBool(frozen))

			return nil
		},
	}
}

// tree lists the worker's processes; right after it, a process outside a frozen cgroup forks the next one, which
// the listing missed.
func (f *fakeCgroups) tree() []int {
	var out []int
	for _, pid := range []int{100, 101, 102, 103, 104, 105} {
		if _, ok := f.in[pid]; ok {
			out = append(out, pid)
		}
	}

	if len(f.forks) > 0 {
		next := f.forks[0]
		if parent := f.parent[next]; !f.frozen[f.in[parent]] {
			f.forks = f.forks[1:]
			f.in[next] = f.in[parent]
		}
	}

	return out
}

const (
	daemonDir = "/sys/fs/cgroup/user.slice/user-1003.slice/user@1003.service/app.slice/hypomnema-node.service"
	scopeDir  = "/sys/fs/cgroup/user.slice/user-1003.slice/user@1003.service/hypomnema.slice/" +
		"hypomnema-workers.slice/hypomnema-worker-dsp_1.scope"
)

// Scenario (review round 2's repro): while an adopted worker is moved, a process forked after the first listing
// forks again after the second, before it is moved itself. Every process ends in the scope, the scope is frozen
// while the stragglers are moved and thawed after, and none is left in the daemon's cgroup (issue #1313 row 1).
func TestAdoptMovesWhatTheWorkerForksDuringTheMove(t *testing.T) {
	t.Parallel()

	f := &fakeCgroups{
		in:     map[int]string{100: daemonDir, 101: daemonDir, 102: daemonDir},
		frozen: map[string]bool{},
		forks:  []int{103, 104, 105},
		parent: map[int]int{103: 102, 104: 103, 105: 104},
	}

	l := Limit{SystemdRun: "systemd-run", Systemctl: "systemctl", Busctl: "busctl"}
	dir, err := l.adopt(t.Context(), f.ops(), "dsp_1", 0, f.tree)
	require.NoError(t, err)
	assert.Equal(t, scopeDir, dir)

	require.Len(t, f.in, 6, "every fork happened")
	for pid, in := range f.in {
		assert.Equal(t, scopeDir, in, "process %d is in the worker's scope", pid)
	}
	assert.Equal(t, []int{100, 101, 102}, f.started, "the scope was made with the first listing")

	assert.Equal(t, "start", f.calls[0])
	assert.Equal(t, "freeze true", f.calls[1], "the scope is frozen before the stragglers are moved")
	assert.Equal(t, "freeze false", f.calls[len(f.calls)-1], "and thawed after")
	assert.False(t, f.frozen[scopeDir])
}

// Scenario (review round 3's repro): an outside parent forks a child and exits between the listing and the read of
// its cgroup. Its read fails; that is no "nothing outside": adoption lists again, finds the child the shim reaped
// under it, and moves it before it reports done (issue #1313 row 1).
func TestAdoptCountsAProcessItCouldNotReadAsUnsettled(t *testing.T) {
	t.Parallel()

	f := &fakeCgroups{in: map[int]string{100: daemonDir, 101: daemonDir}, frozen: map[string]bool{}}
	ops := f.ops()
	ops.wait = time.Second

	listed := 0
	tree := func() []int {
		listed++
		switch listed {
		case 1:
			return []int{100, 101}
		case 2:
			// 102 forked 106 after the scope was made, then exited before its cgroup was read.
			f.in[106] = daemonDir

			return []int{100, 101, 102}
		default:
			return []int{100, 101, 106}
		}
	}

	read := ops.cgroupOf
	ops.cgroupOf = func(pid int) (string, error) {
		if pid == 102 {
			return "", os.ErrNotExist
		}

		return read(pid)
	}

	l := Limit{SystemdRun: "systemd-run", Systemctl: "systemctl", Busctl: "busctl"}
	dir, err := l.adopt(t.Context(), ops, "dsp_1", 0, tree)
	require.NoError(t, err)
	assert.Equal(t, scopeDir, dir)
	assert.Equal(t, scopeDir, f.in[106], "the child of the process that could not be read is moved")
	assert.GreaterOrEqual(t, listed, 4, "done only after a listing that read every process inside")
}

// Scenario (review round 3's repro): every listing finds a process outside that cannot be moved. Past its bound,
// adoption fails, with the scope thawed, rather than reporting done.
func TestAdoptFailsPastItsBound(t *testing.T) {
	t.Parallel()

	f := &fakeCgroups{in: map[int]string{100: daemonDir, 101: daemonDir}, frozen: map[string]bool{}}
	ops := f.ops()
	ops.wait = 100 * time.Millisecond
	ops.moveInto = func(string, int) error { return nil } // the move never takes

	tree := func() []int { return []int{100, 101, 107} }
	f.in[107] = daemonDir

	l := Limit{SystemdRun: "systemd-run", Systemctl: "systemctl", Busctl: "busctl"}
	_, err := l.adopt(t.Context(), ops, "dsp_1", 0, tree)
	require.ErrorContains(t, err, "still outside")
	assert.False(t, f.frozen[scopeDir], "the scope is thawed")
}

// Scenario (review round 3's repro): the thaw cannot be written. Adoption fails with it, rather than leaving a frozen
// worker behind a success.
func TestAdoptReturnsAThawError(t *testing.T) {
	t.Parallel()

	f := &fakeCgroups{in: map[int]string{100: daemonDir}, frozen: map[string]bool{}}
	ops := f.ops()
	ops.wait = time.Second
	freeze := ops.freeze
	ops.freeze = func(dir string, frozen bool) error {
		if !frozen {
			return errors.New("permission denied")
		}

		return freeze(dir, frozen)
	}

	l := Limit{SystemdRun: "systemd-run", Systemctl: "systemctl", Busctl: "busctl"}
	_, err := l.adopt(t.Context(), ops, "dsp_1", 0, func() []int { return []int{100} })
	require.ErrorContains(t, err, "thawing")
}

// Scenario (review round 3's repro): a daemon froze a worker's scope and died before it thawed it. The next daemon
// finds the shim already in the slice: it thaws the scope before it returns.
func TestAdoptThawsAScopeItFindsFrozen(t *testing.T) {
	t.Parallel()

	f := &fakeCgroups{in: map[int]string{100: scopeDir, 101: scopeDir}, frozen: map[string]bool{scopeDir: true}}
	ops := f.ops()
	ops.wait = time.Second

	l := Limit{SystemdRun: "systemd-run", Systemctl: "systemctl", Busctl: "busctl"}
	dir, err := l.adopt(t.Context(), ops, "dsp_1", 0, func() []int { return []int{100, 101} })
	require.NoError(t, err)
	assert.Equal(t, scopeDir, dir)
	assert.False(t, f.frozen[scopeDir], "the scope found frozen is thawed")
	assert.NotContains(t, f.calls, "start", "no new scope is made")
}
