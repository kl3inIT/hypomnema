//go:build linux

package diskguard

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGoCacheTrimStopsAtTargetAndKeepsNewAndUnknownFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bucket := filepath.Join(root, "01")
	require.NoError(t, os.Mkdir(bucket, 0o700))
	now := time.Now()
	var paths []string
	for index := range goCacheBatch + 1 {
		path := filepath.Join(bucket, fmt.Sprintf("01%062x-d", index))
		date := now.Add(-goCacheAge - time.Duration(goCacheBatch+1-index)*time.Hour)
		require.NoError(t, os.WriteFile(path, nil, 0o600))
		require.NoError(t, os.Chtimes(path, date, date))
		paths = append(paths, path)
	}
	fresh := filepath.Join(bucket, fmt.Sprintf("01%062x-a", 0))
	unknown := filepath.Join(bucket, "README")
	require.NoError(t, os.WriteFile(fresh, nil, 0o600))
	require.NoError(t, os.WriteFile(unknown, nil, 0o600))
	old := now.Add(-goCacheAge - time.Hour)
	require.NoError(t, os.Chtimes(unknown, old, old))
	fuzz := filepath.Join(root, "fuzz")
	require.NoError(t, os.Mkdir(fuzz, 0o700))
	checks := 0
	require.NoError(t, trimGo(t.Context(), root, root, now.Add(-goCacheAge), func() bool {
		checks++
		return checks <= 2
	}))
	assert.NoFileExists(t, paths[0])
	assert.NoFileExists(t, paths[goCacheBatch-1])
	assert.FileExists(t, paths[goCacheBatch])
	assert.FileExists(t, fresh)
	assert.FileExists(t, unknown)
	assert.DirExists(t, fuzz)
}

func TestGoCacheOnAnotherMountIsNeverTrimmed(t *testing.T) {
	t.Parallel()
	volume := t.TempDir()
	if !RealDir("/dev/shm") || SameVolume(volume, "/dev/shm") {
		t.Skip("a separate /dev/shm mount is needed")
	}
	require.NoError(t, trimGo(t.Context(), volume, "/dev/shm", time.Now(), func() bool {
		t.Error("a cache on a different mount must not be inspected or trimmed")
		return false
	}))
}
