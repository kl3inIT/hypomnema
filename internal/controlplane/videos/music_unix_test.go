//go:build unix

package videos

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// fakeScript writes a shell script that stands for one of ffmpeg's programs. A shell writes it, not this process,
// so no other test's fork holds it open for writing when it runs (ETXTBSY), as compose's fakeFFmpeg does.
func fakeScript(t *testing.T, dir, name, script string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	//nolint:gosec // The test's own shell, writing into its own folder.
	write := exec.CommandContext(t.Context(), "/bin/sh", "-c", `cat > "$1" && chmod 700 "$1"`, "sh", path)
	write.Stdin = strings.NewReader("#!/bin/sh\n" + script)

	out, err := write.CombinedOutput()
	require.NoError(t, err, "%s", out)

	return path
}

// fakeTools are an ffprobe that finds one MP3 stream and an ffmpeg whose measurement prints measure.
func fakeTools(t *testing.T, measure string) ffmpeg.Tools {
	t.Helper()

	dir := t.TempDir()

	return ffmpeg.Tools{
		FFprobe: fakeScript(t, dir, "ffprobe",
			`printf '{"streams":[{"codec_type":"audio","codec_name":"mp3"}],"format":{"format_name":"mp3"}}'`+"\n"),
		FFmpeg: fakeScript(t, dir, "ffmpeg", measure),
	}
}

// measured is what a real ffmpeg prints measuring a 30 s track.
const measured = "printf 'lavfi.r128.I=-18.000\\nlavfi.r128.true_peak=0.5\\nout_time_us=30000000\\n'\n"

// PR2.3 and PR2.4: a database that cannot grow (SQLite's SQLITE_FULL, here past its max_page_count) is 507's error,
// and the track's renamed file is removed; nothing is kept. The upload goes through once the database has room.
func TestAFullDatabaseKeepsNoTrack(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.useTools(fakeTools(t, measured+"printf 'progress=end\\n'\n"))

	var pages int64
	require.NoError(t, r.svc.db.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&pages))
	_, err := r.svc.db.ExecContext(t.Context(), "PRAGMA max_page_count = "+strconv.FormatInt(pages, 10))
	require.NoError(t, err)

	// Rows as large as the upload's fill the pages the database has, until one more does not fit.
	name := "Nhạc nền"
	for {
		id := platform.NewID("mus")

		err := store.New(r.svc.db).InsertMusic(t.Context(), store.InsertMusicParams{
			ID: id, Name: name, File: id + ".mp3", MediaType: "audio/mpeg", LengthMs: 30_000, Bytes: 4, Now: 1,
		})
		if err != nil {
			require.True(t, sqlite.IsFull(err), err)

			break
		}
	}

	_, err = r.svc.UploadMusic(t.Context(), name, 4, bytes.NewReader([]byte("ID3x")))
	require.ErrorIs(t, err, ErrDiskLow)
	assert.Empty(t, r.musicFiles(t), "the renamed file is removed")

	_, err = r.svc.db.ExecContext(t.Context(), "PRAGMA max_page_count = 1073741823")
	require.NoError(t, err)

	track, err := r.svc.UploadMusic(t.Context(), name, 4, bytes.NewReader([]byte("ID3x")))
	require.NoError(t, err)
	assert.Equal(t, int64(30_000), track.LengthMs, "the decoded length")
	assert.Equal(t, "audio/mpeg", track.MediaType)
	assert.FileExists(t, filepath.Join(r.svc.musicDir, track.ID+".mp3"))
}

// PR2.3: a measurement whose output is cut before ffmpeg's progress=end is a track that does not decode: 400's
// error, and nothing is kept.
func TestACutMeasurementKeepsNoTrack(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	r.useTools(fakeTools(t, measured+"printf 'progress=continue\\n'\n"))

	_, err := r.svc.UploadMusic(t.Context(), "Nhạc", 4, bytes.NewReader([]byte("ID3x")))
	require.ErrorIs(t, err, ErrInvalid)
	assert.Empty(t, r.musicFiles(t))

	lib, err := r.svc.Music(t.Context())
	require.NoError(t, err)
	assert.Empty(t, lib.Tracks)
}
