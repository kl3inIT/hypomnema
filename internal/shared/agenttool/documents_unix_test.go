//go:build unix

package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeUV stands for uv and the venv's Python: `sync` makes the venv of
// UV_PROJECT_ENVIRONMENT, the import check passes unless told otherwise, and
// each call is counted.
type fakeUV struct {
	mu      sync.Mutex
	calls   []string
	syncs   atomic.Int32
	failOn  string
	started chan struct{}
	release chan struct{}
}

func (f *fakeUV) run(_ context.Context, dir string, env []string, name string, args ...string) error {
	call := filepath.Base(name) + " " + strings.Join(args, " ")

	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	if f.failOn != "" && strings.Contains(call, f.failOn) {
		return errors.New("failed as told")
	}

	if len(args) == 0 || args[0] != "sync" {
		return nil
	}

	f.syncs.Add(1)

	if f.started != nil {
		f.started <- struct{}{}
		<-f.release
	}

	venv := ""

	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "UV_PROJECT_ENVIRONMENT="); ok {
			venv = v
		}
	}

	if venv != filepath.Join(dir, "venv") {
		return errors.New("the venv is not the generation's")
	}

	if err := os.MkdirAll(filepath.Join(venv, "bin"), 0o700); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("version_info = 3.14.9\n"), 0o600); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(venv, "bin", "python"), []byte("#!/bin/sh\n"), 0o700) //nolint:gosec // a test's
}

func readSelection(t *testing.T, home string) selection {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(DocumentsRoot(home), "active.json"))
	require.NoError(t, err)

	var sel selection
	require.NoError(t, json.Unmarshal(raw, &sel))

	return sel
}

// Scenario: the coordinator's tools are installed. uv installs the pinned
// Python under the tools and the locked packages into a new generation, the
// skill's imports are checked in it, and active.json selects it. Installed
// again with the same pins, nothing is built.
func TestTheDocumentsEnvironmentIsBuiltOnceForItsPins(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	uv := &fakeUV{}
	docs := Documents{Home: home, UV: "/tools/uv/0.13.8/uv", UVVersion: "0.13.8", run: uv.run}

	venv, err := docs.Ensure(t.Context())
	require.NoError(t, err)

	sel := readSelection(t, home)
	assert.Regexp(t, `^gen-[0-9a-f]{32}$`, sel.Generation)
	assert.Equal(t, filepath.Join(DocumentsRoot(home), sel.Generation, "venv"), venv)
	assert.Equal(t, []string{
		"uv python install --no-config --no-bin 3.14.9",
		"uv sync --no-config --frozen --managed-python --python 3.14.9 --compile-bytecode",
		"python -I -c " + documentsImports,
	}, uv.calls)

	lock, err := os.ReadFile(filepath.Join(DocumentsRoot(home), sel.Generation, "uv.lock"))
	require.NoError(t, err)
	assert.Contains(t, string(lock), "name = \"pypdf\"", "the generation is built from the lock")

	again, err := docs.Ensure(t.Context())
	require.NoError(t, err)
	assert.Equal(t, venv, again)
	assert.Len(t, uv.calls, 3, "the same pins build nothing")
}

// Scenario: two installs run at once (a setup by hand while an update runs
// one). The second waits on the lock and finds the first's generation:
// one build, one generation.
func TestConcurrentInstallsBuildTheDocumentsEnvironmentOnce(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	uv := &fakeUV{started: make(chan struct{}), release: make(chan struct{})}
	docs := Documents{Home: home, UV: "uv", UVVersion: "0.13.8", run: uv.run}

	venvs := make([]string, 2)
	errs := make([]error, 2)

	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() { venvs[i], errs[i] = docs.Ensure(t.Context()) })
	}

	<-uv.started // one build is under way, holding the lock

	select {
	case <-uv.started:
		t.Fatal("a second build started while the first held the lock")
	case <-time.After(200 * time.Millisecond):
	}

	close(uv.release)
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, venvs[0], venvs[1])
	assert.Equal(t, int32(1), uv.syncs.Load())

	generations, err := filepath.Glob(filepath.Join(DocumentsRoot(home), "gen-*"))
	require.NoError(t, err)
	assert.Len(t, generations, 1)
}

// Scenario: a release changes a pin. The install builds a new generation
// and selects it; the generation a running Bot started with stays whole. A
// build that fails selects nothing and leaves nothing of itself: the
// selected generation is the one before.
func TestANewPinIsANewGenerationAndAFailedBuildChangesNothing(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	uv := &fakeUV{}

	old, err := Documents{Home: home, UV: "uv", UVVersion: "0.13.8", run: uv.run}.Ensure(t.Context())
	require.NoError(t, err)

	newer, err := Documents{Home: home, UV: "uv", UVVersion: "0.13.8", Python: "3.14.10", run: uv.run}.
		Ensure(t.Context())
	require.NoError(t, err)

	assert.NotEqual(t, old, newer)
	assert.Equal(t, filepath.Base(filepath.Dir(newer)), readSelection(t, home).Generation)
	assert.FileExists(t, filepath.Join(old, "bin", "python"), "a running Bot keeps its generation's files")

	for _, step := range []string{"python install", "sync", "-I -c"} {
		failing := &fakeUV{failOn: step}

		_, err := Documents{Home: home, UV: "uv", UVVersion: "0.13.9", run: failing.run}.Ensure(t.Context())
		require.Error(t, err, step)

		assert.Equal(t, filepath.Base(filepath.Dir(newer)), readSelection(t, home).Generation,
			"a failed %s selects nothing", step)

		generations, err := filepath.Glob(filepath.Join(DocumentsRoot(home), "gen-*"))
		require.NoError(t, err)
		assert.Len(t, generations, 2, "a failed %s leaves no generation", step)
	}
}

// uv is given no setting of the caller's that picks another project,
// interpreter or cache.
func TestUVIsRunWithoutTheCallersPythonSettings(t *testing.T) {
	t.Parallel()

	got := uvEnvironment([]string{
		"PATH=/usr/bin", "HOME=/home/coordinator", "VIRTUAL_ENV=/x", "PYTHONPATH=/y", "PYTHONHOME=/z",
		"UV_PROJECT_ENVIRONMENT=/w", "UV_INDEX_URL=https://mirror",
	})

	assert.Equal(t, []string{"PATH=/usr/bin", "HOME=/home/coordinator"}, got)
}
