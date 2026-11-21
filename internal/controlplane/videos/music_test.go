package videos

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The music library (issue #910, PR 2's checks 1-9).

// countingReader counts the reads of a body.
type countingReader struct {
	r     io.Reader
	reads atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads.Add(1)

	return c.r.Read(p)
}

// addTrack puts a track in the library as an upload leaves it: its file in the music folder and its row.
func (r *rig) addTrack(t *testing.T, name string, lengthMs int64) string {
	t.Helper()

	id := platform.NewID("mus")
	require.NoError(t, os.MkdirAll(r.svc.musicDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(r.svc.musicDir, id+".mp3"), bytes.Repeat([]byte{1}, 1000), 0o600))
	require.NoError(t, store.New(r.svc.db).InsertMusic(t.Context(), store.InsertMusicParams{
		ID: id, Name: name, File: id + ".mp3", MediaType: "audio/mpeg", LengthMs: lengthMs, Bytes: 1000, Now: 1,
	}))

	return id
}

// useTools has the rig's Service use these programs as its ffmpeg.
func (r *rig) useTools(tools ffmpeg.Tools) {
	r.svc.cfg.Tools = func(context.Context) (ffmpeg.Tools, error) { return tools, nil }
}

// musicIn is a music section as sent.
func musicIn(track string, gain ...float64) *LookInput {
	in := &MusicInput{Track: &track}
	if len(gain) > 0 {
		in.GainDB = &gain[0]
	}

	return &LookInput{Music: in}
}

// makeWith makes a revision of the rig's take as the owner, with the look sent.
func (r *rig) makeWith(t *testing.T, look *LookInput) (Video, Revision, error) {
	t.Helper()

	return r.svc.Make(t.Context(), MakeRequest{Take: takeID, By: Owner, ByName: Owner, Look: look})
}

// The probe's streams decide a track's type (row 6b): one audio stream, a cover aside, of the five kinds.
func TestATrackIsTypedByItsStreams(t *testing.T) {
	t.Parallel()

	audio := func(codec string) compose.Stream { return compose.Stream{Type: "audio", Codec: codec} }
	const mov = "mov,mp4,m4a,3gp,3g2,mj2"

	cover := compose.Stream{Type: "video", Codec: "mjpeg", AttachedPicture: true}

	for name, c := range map[string]struct {
		probe     compose.Probe
		mediaType string
		ext       string
	}{
		"mp3":            {compose.Probe{Format: "mp3", Streams: []compose.Stream{audio("mp3")}}, "audio/mpeg", "mp3"},
		"mp3 with cover": {compose.Probe{Format: "mp3", Streams: []compose.Stream{audio("mp3"), cover}}, "audio/mpeg", "mp3"},
		"m4a":            {compose.Probe{Format: mov, Brand: "M4A", Streams: []compose.Stream{audio("aac")}}, "audio/mp4", "m4a"},
		"mp4":            {compose.Probe{Format: mov, Brand: "isom", Streams: []compose.Stream{audio("aac")}}, "audio/mp4", "m4a"},
		"m4a with chapters": {compose.Probe{Format: mov, Brand: "M4A",
			Streams: []compose.Stream{audio("aac"), {Type: "data", Codec: "bin_data"}}}, "audio/mp4", "m4a"},
		"QuickTime": {probe: compose.Probe{Format: mov, Brand: "qt", Streams: []compose.Stream{audio("aac")}}},
		"no brand":  {probe: compose.Probe{Format: mov, Streams: []compose.Stream{audio("aac")}}},
		"wav":       {compose.Probe{Format: "wav", Streams: []compose.Stream{audio("pcm_s16le")}}, "audio/wav", "wav"},
		"flac":      {compose.Probe{Format: "flac", Streams: []compose.Stream{audio("flac")}}, "audio/flac", "flac"},
		"vorbis":    {compose.Probe{Format: "ogg", Streams: []compose.Stream{audio("vorbis")}}, "audio/ogg", "ogg"},
		"opus":      {compose.Probe{Format: "ogg", Streams: []compose.Stream{audio("opus")}}, "audio/ogg", "ogg"},
		"alac":      {probe: compose.Probe{Format: mov, Brand: "M4A", Streams: []compose.Stream{audio("alac")}}},
		"two audio streams": {probe: compose.Probe{Format: mov, Brand: "M4A",
			Streams: []compose.Stream{audio("aac"), audio("aac")}}},
		"a video": {probe: compose.Probe{Format: mov, Brand: "isom",
			Streams: []compose.Stream{audio("aac"), {Type: "video", Codec: "h264"}}}},
		"no audio":   {probe: compose.Probe{Format: "mp3"}},
		"mp3 in wav": {probe: compose.Probe{Format: "wav", Streams: []compose.Stream{audio("mp3")}}},
	} {
		mediaType, ext, err := musicType(c.probe)
		if c.mediaType == "" {
			require.ErrorIs(t, err, ErrInvalid, name)

			continue
		}

		require.NoError(t, err, name)
		assert.Equal(t, c.mediaType, mediaType, name)
		assert.Equal(t, c.ext, ext, name)
	}
}

// PR2.1, the service's part: an upload while another is received, one past the quota and one the disk cannot hold
// are refused before a byte of the body is read.
func TestAnUploadIsRefusedBeforeItsBodyIsRead(t *testing.T) {
	t.Parallel()

	t.Run("quota", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		require.NoError(t, os.MkdirAll(r.svc.musicDir, 0o700))
		// A sparse file: the library is full without taking the disk.
		require.NoError(t, os.WriteFile(filepath.Join(r.svc.musicDir, "full.mp3"), nil, 0o600))
		require.NoError(t, os.Truncate(filepath.Join(r.svc.musicDir, "full.mp3"), MusicQuotaBytes-MaxMusicBytes+1))

		body := &countingReader{r: bytes.NewReader(make([]byte, 10))}
		_, err := r.svc.UploadMusic(t.Context(), "Nhạc", MaxMusicBytes, body)
		require.ErrorIs(t, err, ErrDiskLow)
		assert.Zero(t, body.reads.Load())

		_, err = r.svc.UploadMusic(t.Context(), "Nhạc", MaxMusicBytes-1, failingReader{})
		require.NotErrorIs(t, err, ErrDiskLow, "up to the quota is taken")
	})

	t.Run("disk", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		r.free = minFreeBytes + MaxMusicBytes - 1

		body := &countingReader{r: bytes.NewReader(make([]byte, 10))}
		_, err := r.svc.UploadMusic(t.Context(), "Nhạc", MaxMusicBytes, body)
		require.ErrorIs(t, err, ErrDiskLow)
		assert.Zero(t, body.reads.Load())
	})

	t.Run("busy", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		first, feed := io.Pipe()
		done := make(chan error, 1)

		go func() {
			_, err := r.svc.UploadMusic(t.Context(), "Một", MaxMusicBytes, first)
			done <- err
		}()

		// The first upload is reading its body.
		_, err := feed.Write([]byte("ID3"))
		require.NoError(t, err)

		body := &countingReader{r: bytes.NewReader(make([]byte, 10))}
		_, err = r.svc.UploadMusic(t.Context(), "Hai", MaxMusicBytes, body)
		require.ErrorIs(t, err, ErrMusicBusy)
		assert.Zero(t, body.reads.Load())

		require.NoError(t, feed.CloseWithError(io.ErrUnexpectedEOF))
		require.Error(t, <-done)
	})
}

// failingReader fails its first read, as a client that left does.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// fullFile is a temporary file on a full disk: its write, or its sync, fails with ENOSPC.
type fullFile struct {
	*os.File

	failWrite bool
}

func (f fullFile) Write(p []byte) (int, error) {
	if f.failWrite {
		return 0, syscall.ENOSPC
	}

	return f.File.Write(p)
}

func (f fullFile) Sync() error { return syscall.ENOSPC }

// partFiles are the files a receive left in the music folder.
func (r *rig) musicFiles(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(r.svc.musicDir)
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	return names
}

// PR2.3: a full disk while the track is received, at its write or at its sync, or after the copy, is 507's error,
// and leaves no .part.
func TestAFullDiskWhileReceivingLeavesNothing(t *testing.T) {
	t.Parallel()

	for _, failWrite := range []bool{true, false} {
		r := newRig(t)
		r.svc.fs.CreateTemp = func(dir, pattern string) (MusicFile, error) {
			f, err := os.CreateTemp(dir, pattern)
			if err != nil {
				return nil, err
			}

			return fullFile{File: f, failWrite: failWrite}, nil
		}

		_, err := r.svc.UploadMusic(t.Context(), "Nhạc", 4, bytes.NewReader([]byte("ID3x")))
		require.ErrorIs(t, err, ErrDiskLow, "write fails: %v", failWrite)
		assert.Empty(t, r.musicFiles(t), "no .part is left")
	}

	r := newRig(t)
	calls := 0
	r.svc.cfg.FreeBytes = func(string) (uint64, error) {
		calls++
		if calls == 1 {
			return 1 << 40, nil
		}

		return minFreeBytes - 1, nil
	}

	_, err := r.svc.UploadMusic(t.Context(), "Nhạc", 4, bytes.NewReader([]byte("ID3x")))
	require.ErrorIs(t, err, ErrDiskLow, "under 400 MB once the track is copied")
	assert.Empty(t, r.musicFiles(t))
}

// A free disk that cannot be read is no room (review of #1049): before the body is read, and once it is copied.
func TestAnUnknownFreeDiskIsNoRoom(t *testing.T) {
	t.Parallel()

	for failAt := 1; failAt <= 2; failAt++ {
		r := newRig(t)
		calls := 0
		r.svc.cfg.FreeBytes = func(string) (uint64, error) {
			calls++
			if calls == failAt {
				return 0, errors.New("statfs: input/output error")
			}

			return 1 << 40, nil
		}

		body := &countingReader{r: bytes.NewReader([]byte("ID3x"))}
		_, err := r.svc.UploadMusic(t.Context(), "Nhạc", 4, body)
		require.ErrorIs(t, err, ErrDiskLow, "the read that fails: %d", failAt)
		assert.Empty(t, r.musicFiles(t))

		if failAt == 1 {
			assert.Zero(t, body.reads.Load(), "the body is not read")
		}
	}
}

// PR2.4: at start, the music folder loses what a crash left half-written and the files with no track, before New
// returns, so before the API and the runner; a track whose file is gone is listed missing. A file swept keeps its
// bytes where another link holds them, as a backup does.
func TestTheSweepAtStartRemovesWhatHasNoTrack(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	kept := r.addTrack(t, "Giữ", 30_000)
	gone := r.addTrack(t, "Mất", 30_000)
	require.NoError(t, os.Remove(filepath.Join(r.svc.musicDir, gone+".mp3")))

	stray := filepath.Join(r.svc.musicDir, "mus_x.123.part")
	orphan := filepath.Join(r.svc.musicDir, "mus_newer.mp3")
	backup := filepath.Join(t.TempDir(), "mus_newer.mp3")

	require.NoError(t, os.WriteFile(stray, []byte("half"), 0o600))
	require.NoError(t, os.WriteFile(orphan, []byte("a newer track"), 0o600))
	require.NoError(t, os.Link(orphan, backup))

	svc, err := New(r.svc.cfg)
	require.NoError(t, err)

	assert.NoFileExists(t, stray)
	assert.NoFileExists(t, orphan)
	assert.FileExists(t, filepath.Join(r.svc.musicDir, kept+".mp3"))

	raw, err := os.ReadFile(backup) //nolint:gosec // The test's own file.
	require.NoError(t, err)
	assert.Equal(t, "a newer track", string(raw), "the backup's link keeps the bytes")

	lib, err := svc.Music(t.Context())
	require.NoError(t, err)
	require.Len(t, lib.Tracks, 2)

	missing := map[string]bool{}
	for _, tr := range lib.Tracks {
		missing[tr.ID] = tr.Missing
	}

	assert.Equal(t, map[string]bool{kept: false, gone: true}, missing)
	assert.Equal(t, int64(1000), lib.UsedBytes)
	assert.Equal(t, int64(MusicQuotaBytes), lib.QuotaBytes)
}

// PR2.6: a track named by a revision queued or being made is not deleted; the default's music becomes none only when
// it names the deleted track; a track only made revisions name is deleted, their looks keeping its name and length.
func TestDeletingATrack(t *testing.T) {
	t.Parallel()

	t.Run("a revision being made has it", func(t *testing.T) {
		t.Parallel()

		for _, state := range []string{Queued, Voicing, Composing, Captioning} {
			r := newRig(t)
			a := r.addTrack(t, "A", 30_000)

			v, _, err := r.makeWith(t, musicIn(a))
			require.NoError(t, err)
			_, err = r.svc.db.ExecContext(t.Context(), "UPDATE video_revisions SET state = ? WHERE video_id = ?", state, v.ID)
			require.NoError(t, err)

			require.ErrorIs(t, r.svc.DeleteMusic(t.Context(), a), ErrConflict, state)
			assert.FileExists(t, filepath.Join(r.svc.musicDir, a+".mp3"), state)
		}
	})

	t.Run("the default", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		a, b := r.addTrack(t, "A", 30_000), r.addTrack(t, "B", 40_000)

		_, _, err := r.makeWith(t, musicIn(a, -3))
		require.NoError(t, err)
		r.runAll(t)
		require.Equal(t, a, r.savedDefault(t).Music.Track, "the owner's make saves the music as the default")

		require.NoError(t, r.svc.DeleteMusic(t.Context(), b))
		require.Equal(t, a, r.savedDefault(t).Music.Track, "deleting another track leaves the default")

		v, rev := r.make(t, "")
		require.NotNil(t, rev.Look.Music)
		assert.Equal(t, a, rev.Look.Music.Track, "the next make still has A")
		r.runAll(t)

		used, err := r.svc.Music(t.Context())
		require.NoError(t, err)

		require.NoError(t, r.svc.DeleteMusic(t.Context(), a))
		assert.Nil(t, r.savedDefault(t).Music, "deleting the default's track leaves no music")
		assert.NoFileExists(t, filepath.Join(r.svc.musicDir, a+".mp3"))

		after, err := r.svc.Music(t.Context())
		require.NoError(t, err)
		assert.Less(t, after.UsedBytes, used.UsedBytes, "used_bytes falls")
		assert.Empty(t, after.Tracks)

		made := r.revision(t, v.ID, rev.Rev)
		require.Equal(t, Review, made.State)
		assert.Equal(t, &MusicLook{Track: a, Name: "A", LengthMs: 30_000, GainDB: -3}, made.Look.Music,
			"a made revision keeps the name and length")

		require.ErrorIs(t, r.svc.DeleteMusic(t.Context(), a), ErrNotFound)
	})
}

// PR2.7: a music section needs its track, through a make and a look change; a track with its gain is stored, the
// other sections kept.
func TestAMusicSectionNeedsItsTrack(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	a := r.addTrack(t, "A", 30_000)
	gain := -6.0

	_, _, err := r.makeWith(t, &LookInput{Music: &MusicInput{GainDB: &gain}})
	require.ErrorIs(t, err, ErrInvalid, "a make")

	v, rev, err := r.makeWith(t, musicIn(a, -6))
	require.NoError(t, err)
	assert.Equal(t, &MusicLook{Track: a, Name: "A", LengthMs: 30_000, GainDB: -6}, rev.Look.Music)
	r.runAll(t)

	_, _, err = r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{Look: &LookInput{Music: &MusicInput{GainDB: &gain}},
		RequestID: "k1"})
	require.ErrorIs(t, err, ErrInvalid, "a look change")

	before := r.revision(t, v.ID, 1).Look

	_, rev2, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{Look: musicIn(a, -9.5), RequestID: "k2"})
	require.NoError(t, err)
	assert.Equal(t, &MusicLook{Track: a, Name: "A", LengthMs: 30_000, GainDB: -9.5}, rev2.Look.Music)
	assert.Equal(t, before.Captions, rev2.Look.Captions, "the captions are kept")
	assert.Equal(t, before.Frame, rev2.Look.Frame, "the frame is kept")

	_, _, err = r.makeWith(t, musicIn(a, 11))
	require.ErrorIs(t, err, ErrInvalid, "a gain past +10 dB")

	_, none, err := r.makeWith(t, musicIn("none"))
	require.NoError(t, err)
	assert.Nil(t, none.Look.Music, "none is no music")
	assert.Nil(t, r.savedDefault(t).Music)
}

// The runner lays the look's track under the voice, at its gain, from the library's file with its decoded length.
func TestTheRunnerLaysTheTrackUnderTheVoice(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	a := r.addTrack(t, "A", 30_000)

	_, _, err := r.makeWith(t, musicIn(a, 4))
	require.NoError(t, err)
	_, _, err = r.makeWith(t, musicIn("none"))
	require.NoError(t, err)
	r.runAll(t)

	require.Len(t, r.maker.inputs, 2)
	got := r.maker.inputs[0].Music
	require.NotNil(t, got)
	assert.Equal(t, compose.Music{File: filepath.Join(r.svc.musicDir, a+".mp3"), LengthMs: 30_000, GainDB: 4}, *got)
	assert.NotNil(t, r.maker.inputs[0].FreeBytes, "the bed's disk check reads the Service's free disk")
	assert.Nil(t, r.maker.inputs[1].Music, "none lays nothing")
}

// PR2.8: a deleted track sent is refused; one taken from the default or the source falls back to none, with the
// track it was and a warning once made; a note alone queues nothing.
func TestAGoneTrackAtQueueTime(t *testing.T) {
	t.Parallel()

	t.Run("sent", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		a := r.addTrack(t, "A", 30_000)
		require.NoError(t, r.svc.DeleteMusic(t.Context(), a))

		_, _, err := r.makeWith(t, musicIn(a))
		require.ErrorIs(t, err, ErrConflict)

		b := r.addTrack(t, "B", 30_000)
		v, _, err := r.makeWith(t, musicIn(b))
		require.NoError(t, err)
		r.runAll(t)

		_, _, err = r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{Look: musicIn(a), RequestID: "k"})
		require.ErrorIs(t, err, ErrConflict, "a look change")
	})

	fellBack := func(t *testing.T, r *rig, rev Revision, track string) {
		t.Helper()

		require.NotNil(t, rev.Look.Music)
		assert.Equal(t, &MusicLook{Track: musicNone, FellBackFrom: &MusicSnapshot{Track: track, Name: "A",
			LengthMs: 30_000}}, rev.Look.Music)
	}

	t.Run("from the default", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		a := r.addTrack(t, "A", 30_000)
		d := legacyLook
		d.Music = &MusicLook{Track: a, Name: "A", LengthMs: 30_000}
		r.setDefault(t, d)
		// The track is gone without the default knowing, as after a restore.
		_, err := r.svc.db.ExecContext(t.Context(), "DELETE FROM video_music")
		require.NoError(t, err)

		v, rev := r.make(t, "")
		fellBack(t, r, rev, a)
		r.runAll(t)

		made := r.revision(t, v.ID, 1)
		require.Equal(t, Review, made.State, made.FailedCause)
		assert.Contains(t, made.Warnings, `the music "A" was deleted, so the video has no music`)
		assert.Nil(t, r.maker.inputs[0].Music)
	})

	t.Run("a retry of a revision that has it", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		a := r.addTrack(t, "A", 30_000)

		// The revision fails with the track still named, and the track is deleted after.
		r.maker.fail = "compose"
		v, _, err := r.makeWith(t, musicIn(a))
		require.NoError(t, err)
		r.runAll(t)
		r.maker.fail = ""

		failed := r.revision(t, v.ID, 1)
		require.Equal(t, Failed, failed.State)
		require.Equal(t, a, failed.Look.Music.Track, "the failed revision still names the track")
		require.NoError(t, r.svc.DeleteMusic(t.Context(), a))

		_, retried, err := r.svc.Retry(t.Context(), v.ID, 1, Owner, "r")
		require.NoError(t, err)
		fellBack(t, r, retried, a)
		r.runAll(t)

		made := r.revision(t, v.ID, retried.Rev)
		require.Equal(t, Review, made.State, made.FailedCause)
		assert.Contains(t, made.Warnings, `the music "A" was deleted, so the video has no music`)
	})

	t.Run("from the source", func(t *testing.T) {
		t.Parallel()

		r := newRig(t)
		a := r.addTrack(t, "A", 30_000)

		v, _, err := r.makeWith(t, musicIn(a))
		require.NoError(t, err)
		r.runAll(t)
		require.NoError(t, r.svc.DeleteMusic(t.Context(), a))

		// A look change without music.
		_, rev, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{Look: &LookInput{}, RequestID: "k"})
		require.NoError(t, err)
		fellBack(t, r, rev, a)

		// A retry of a failed revision.
		r.maker.fail = "compose"
		r.runAll(t)
		r.maker.fail = ""
		require.Equal(t, Failed, r.revision(t, v.ID, rev.Rev).State)

		_, retried, err := r.svc.Retry(t.Context(), v.ID, rev.Rev, Owner, "r")
		require.NoError(t, err)
		fellBack(t, r, retried, a)
		r.runAll(t)

		// Changes asked with lines, and a note alone.
		n := len(r.revisionsOf(t, v.ID))
		_, err = r.svc.RequestChanges(t.Context(), v.ID, 1, ChangeRequest{Note: "chậm lại"})
		require.NoError(t, err)
		assert.Len(t, r.revisionsOf(t, v.ID), n, "a note alone queues nothing")

		_, err = r.svc.RequestChanges(t.Context(), v.ID, 1, ChangeRequest{Lines: []string{"Một", "Hai"}})
		require.NoError(t, err)

		revs := r.revisionsOf(t, v.ID)
		require.Len(t, revs, n+1)
		fellBack(t, r, revs[len(revs)-1], a)
	})
}

func (r *rig) revisionsOf(t *testing.T, id string) []Revision {
	t.Helper()

	v, err := r.svc.Get(t.Context(), id)
	require.NoError(t, err)

	return v.Revisions
}

// PR2.8: a make racing a track's delete never queues the deleted track: either the make has it and the delete is
// refused, or the delete goes first and the make is refused.
func TestAMakeRacingADeleteNeverQueuesAGoneTrack(t *testing.T) {
	t.Parallel()

	r := newRig(t)

	for range 20 {
		a := r.addTrack(t, "A", 30_000)

		var (
			wg                 sync.WaitGroup
			makeErr, deleteErr error
		)

		wg.Go(func() { _, _, makeErr = r.makeWith(t, musicIn(a)) })
		wg.Go(func() { deleteErr = r.svc.DeleteMusic(t.Context(), a) })
		wg.Wait()

		if makeErr == nil {
			require.ErrorIs(t, deleteErr, ErrConflict)
		} else {
			require.ErrorIs(t, makeErr, ErrConflict)
			require.NoError(t, deleteErr)
		}

		var queued int
		require.NoError(t, r.svc.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM video_revisions r
			WHERE json_extract(r.look, '$.music.track') = ? AND NOT EXISTS (SELECT 1 FROM video_music m WHERE m.id = ?)`,
			a, a).Scan(&queued))
		assert.Zero(t, queued)

		r.runAll(t)
	}
}

// PR2.9: a look change of the music alone speaks and hears nothing with the voice cache full; a clip removed is
// spoken again (and heard again with captions); its word times alone removed are heard again.
func TestAMusicChangeReusesTheVoice(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	a := r.addTrack(t, "A", 30_000)

	v, _ := r.make(t, "")
	r.runAll(t)

	spoken := func() int {
		r.speech.mu.Lock()
		defer r.speech.mu.Unlock()

		n := 0
		for _, c := range r.speech.spoken {
			n += c
		}

		return n
	}
	heard := func() int {
		r.speech.mu.Lock()
		defer r.speech.mu.Unlock()

		return r.speech.heard
	}

	change := func(key string) {
		t.Helper()

		_, _, err := r.svc.ChangeLook(t.Context(), v.ID, 1, LookChange{Look: musicIn(a, -2), RequestID: key})
		require.NoError(t, err)
		r.runAll(t)
	}

	clips, err := filepath.Glob(filepath.Join(r.dir, voiceDir, "*."+clipFormat))
	require.NoError(t, err)
	require.Len(t, clips, 2)

	s0, h0 := spoken(), heard()
	change("full")
	assert.Equal(t, s0, spoken(), "no line is spoken")
	assert.Equal(t, h0, heard(), "no clip is heard")

	require.NoError(t, os.Remove(clips[0]))
	require.NoError(t, os.Remove(clips[0]+wordsSuffix))
	change("clip")
	assert.Equal(t, s0+1, spoken(), "the removed clip is spoken")
	assert.Equal(t, h0+1, heard(), "and heard, with captions")

	require.NoError(t, os.Remove(clips[1]+wordsSuffix))
	change("words")
	assert.Equal(t, s0+1, spoken(), "nothing is spoken")
	assert.Equal(t, h0+2, heard(), "the clip whose word times were removed is heard")

	var states []string
	for _, rev := range r.revisionsOf(t, v.ID) {
		states = append(states, rev.State)
	}

	assert.Equal(t, []string{Review, Review, Review, Review}, states)
}

// A track's file is served by its id, as the type it was uploaded as; an id not in the library is not found.
func TestATracksAudioIsItsFile(t *testing.T) {
	t.Parallel()

	r := newRig(t)
	a := r.addTrack(t, "A", 30_000)

	path, kind, err := r.svc.MusicAudio(t.Context(), a)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(r.svc.musicDir, a+".mp3"), path)
	assert.Equal(t, "audio/mpeg", kind)

	for _, id := range []string{"mus_x", "../a", platform.NewID("mus")} {
		_, _, err := r.svc.MusicAudio(t.Context(), id)
		require.ErrorIs(t, err, ErrNotFound, id)
	}

	require.NoError(t, os.Remove(path))

	_, _, err = r.svc.MusicAudio(t.Context(), a)
	require.ErrorIs(t, err, ErrNotFound, "a missing file")
}
