//go:build unix

package clientapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// fakeProgram writes a shell script that stands for one of ffmpeg's programs, by a shell, so no other test's fork
// holds it open for writing when it runs (ETXTBSY), as compose's fakeFFmpeg does.
func fakeProgram(t *testing.T, dir, name, script string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	//nolint:gosec // The test's own shell, writing into its own folder.
	write := exec.CommandContext(t.Context(), "/bin/sh", "-c", `cat > "$1" && chmod 700 "$1"`, "sh", path)
	write.Stdin = strings.NewReader("#!/bin/sh\n" + script)

	out, err := write.CombinedOutput()
	require.NoError(t, err, "%s", out)

	return path
}

// fakeMusicTools are an ffprobe that finds one MP3 stream and an ffmpeg whose measurement of a 30 s track ends with
// end, progress=end or not.
func fakeMusicTools(t *testing.T, end string) ffmpeg.Tools {
	t.Helper()

	dir := t.TempDir()

	return ffmpeg.Tools{
		FFprobe: fakeProgram(t, dir, "ffprobe",
			`printf '{"streams":[{"codec_type":"audio","codec_name":"mp3"}],"format":{"format_name":"mp3"}}'`+"\n"),
		FFmpeg: fakeProgram(t, dir, "ffmpeg",
			"printf 'lavfi.r128.I=-18.000\\nlavfi.r128.true_peak=0.5\\nout_time_us=30000000\\n"+end+"\\n'\n"),
	}
}

// fullDisk is a temporary file on a full disk: its write, or its sync, fails with ENOSPC.
type fullDisk struct {
	*os.File

	failWrite bool
}

func (f fullDisk) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, syscall.ENOSPC
	}

	return f.File.Write(p)
}

func (f fullDisk) Sync() error { return syscall.ENOSPC }

// PR2.3 through the route: a full disk while the track is written or synced, under 400 MB left once it is copied,
// or a database that cannot grow (SQLITE_FULL) answer 507; a measurement cut before progress=end answers 400. None
// leaves a file or a track.
func TestAFullDiskOrDatabaseAnswers507(t *testing.T) {
	t.Parallel()

	tools := func(end string) func(*videos.Config) {
		return func(cfg *videos.Config) {
			made := fakeMusicTools(t, end)
			cfg.Tools = func(context.Context) (ffmpeg.Tools, error) { return made, nil }
		}
	}
	full := func(failWrite bool) func(*videos.Config) {
		return func(cfg *videos.Config) {
			cfg.MusicFS.CreateTemp = func(dir, pattern string) (videos.MusicFile, error) {
				f, err := os.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}

				return fullDisk{File: f, failWrite: failWrite}, nil
			}
		}
	}

	body := []byte("ID3 a track")
	nothingKept := func(t *testing.T, w *musicWorld, name string) {
		t.Helper()

		lib, err := w.videos.Music(t.Context())
		require.NoError(t, err)
		assert.Empty(t, lib.Tracks, name)

		entries, err := os.ReadDir(filepath.Join(w.dir, "music"))
		if err == nil {
			assert.Empty(t, entries, "%s: no file is left", name)
		}
	}

	for name, c := range map[string]struct {
		options []func(*videos.Config)
		status  int
	}{
		"the write fails":          {options: []func(*videos.Config){tools("progress=end"), full(true)}, status: 507},
		"the sync fails":           {options: []func(*videos.Config){tools("progress=end"), full(false)}, status: 507},
		"the measurement is cut":   {options: []func(*videos.Config){tools("progress=continue")}, status: 400},
		"a measurement that works": {options: []func(*videos.Config){tools("progress=end")}, status: 201},
	} {
		w := newMusicWorld(t, c.options...)

		status, _ := w.post(t, "Nhạc", body, octetStream)
		assert.Equal(t, c.status, status, name)

		if c.status != 201 {
			nothingKept(t, w, name)
		}
	}

	// Under 400 MB once the track is copied.
	var reads atomic.Int64

	w := newMusicWorld(t, tools("progress=end"), func(cfg *videos.Config) {
		cfg.FreeBytes = func(string) (uint64, error) {
			if reads.Add(1) == 1 {
				return 1 << 40, nil
			}

			return 400_000_000 - 1, nil
		}
	})

	status, _ := w.post(t, "Nhạc", body, octetStream)
	assert.Equal(t, 507, status, "under 400 MB after the copy")
	nothingKept(t, w, "under 400 MB after the copy")

	// The database cannot grow: rows as large as the upload's fill its pages until one more does not fit.
	w = newMusicWorld(t, tools("progress=end"))
	w.exec("PRAGMA max_page_count = " + strconv.FormatInt(w.pages(), 10))

	for {
		id := platform.NewID("mus")

		err := w.try(`INSERT INTO video_music (id, name, file, media_type, length_ms, bytes, created_at)
			VALUES (?, 'Nhạc', ?, 'audio/mpeg', 30000, 11, 1)`, id, id+".mp3")
		if err != nil {
			require.True(t, sqlite.IsFull(err), err)

			break
		}
	}

	status, _ = w.post(t, "Nhạc", body, octetStream)
	assert.Equal(t, 507, status, "SQLITE_FULL")

	entries, err := os.ReadDir(filepath.Join(w.dir, "music"))
	require.NoError(t, err)
	assert.Empty(t, entries, "the renamed file is removed")
}
