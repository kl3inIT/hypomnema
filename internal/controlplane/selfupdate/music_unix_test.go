//go:build unix

package selfupdate_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/selfupdate"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
)

// The update's backup of the videos' music library (issue #910, PR 2's check 5), through Install with a fake run.

// musicDir is the music folder of an installed control plane, as videos.MusicDir names it.
func musicDir(o selfupdate.Options) string { return filepath.Join(o.Home, "data", "videos", "music") }

// oldBackups makes n backups of earlier updates, the oldest first, an hour ago; the oldest links track.
func oldBackups(t *testing.T, o selfupdate.Options, n int, track string) {
	t.Helper()

	old := time.Now().Add(-time.Hour)

	for i := range n {
		dir := filepath.Join(o.Home, "data", fmt.Sprintf("predeploy-old-%d", i))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "videos-music"), 0o700))

		if i == 0 && track != "" {
			require.NoError(t, os.Link(track, filepath.Join(dir, "videos-music", filepath.Base(track))))
		}

		at := old.Add(time.Duration(i) * time.Minute)
		require.NoError(t, os.Chtimes(dir, at, at))
	}
}

// backups are the names of the backups in data, and whether a half-written one is left.
func backups(t *testing.T, o selfupdate.Options) ([]string, bool) {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(o.Home, "data"))
	require.NoError(t, err)

	var names []string

	part := false

	for _, e := range entries {
		switch {
		case strings.HasPrefix(e.Name(), "predeploy-"):
			names = append(names, e.Name())
		case strings.HasPrefix(e.Name(), ".predeploy-"):
			part = true
		}
	}

	return names, part
}

// ownBackup is the backup the update made: the one not made by oldBackups.
func ownBackup(t *testing.T, names []string) string {
	t.Helper()

	var own []string

	for _, n := range names {
		if !strings.HasPrefix(n, "predeploy-old-") {
			own = append(own, n)
		}
	}

	require.Len(t, own, 1, "one backup of this update, among %v", names)

	return own[0]
}

func track(t *testing.T, o selfupdate.Options, name, content string) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(musicDir(o), 0o700))
	path := filepath.Join(musicDir(o), name)
	write(t, path, content)

	return path
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()

	ia, err := os.Stat(a)
	require.NoError(t, err)
	ib, err := os.Stat(b)
	require.NoError(t, err)

	return os.SameFile(ia, ib)
}

// Scenario: an update backs up the music with the database: each track a hard link to the same file, in a backup
// published whole. A first update, with no music folder yet, backs up an empty one.
func TestTheBackupHoldsTheMusicAsLinks(t *testing.T) {
	t.Parallel()

	for _, withMusic := range []bool{true, false} {
		o := installed(t)
		o.Ready = ready(t, http.StatusNoContent)
		from := filepath.Join(o.State, "hypomnema-v0.9.5")
		write(t, from, "the new control plane")
		o.Run = (&commands{}).run

		live := ""
		if withMusic {
			live = track(t, o, "mus_a.mp3", "a track")
		}

		require.NoError(t, selfupdate.Install(t.Context(), o, from))

		names, part := backups(t, o)
		require.Len(t, names, 1)
		assert.False(t, part, "no half-written backup is left")

		dir := filepath.Join(o.Home, "data", names[0])
		assert.Equal(t, "the old database", read(t, filepath.Join(dir, "hypomnema.db")))

		entries, err := os.ReadDir(filepath.Join(dir, "videos-music"))
		require.NoError(t, err, "the music is backed up, empty or not")

		if !withMusic {
			assert.Empty(t, entries)

			continue
		}

		require.Len(t, entries, 1)
		assert.True(t, sameFile(t, live, filepath.Join(dir, "videos-music", "mus_a.mp3")), "a link, not a copy")
	}
}

// Scenario: with five backups already there, an update that rolls back, one that fails to put its binary in place
// and one whose coordinator fails to be set up each leave exactly five backups, the newest their own. The oldest is
// removed with the links it held; the track it linked stays in the music folder.
func TestEveryOutcomeKeepsFiveBackups(t *testing.T) {
	t.Parallel()

	for name, setUp := range map[string]func(o *selfupdate.Options, cmds *commands){
		"rolled back": func(o *selfupdate.Options, _ *commands) {
			o.Ready = ready(t, http.StatusServiceUnavailable)
			o.ReadyWithin = time.Second
		},
		"the binary not placed": func(o *selfupdate.Options, _ *commands) {
			require.NoError(t, os.MkdirAll(filepath.Join(o.Home, "bin", "hypomnema.new"), 0o700))
		},
		"the coordinator not set up": func(_ *selfupdate.Options, cmds *commands) {
			cmds.fail = map[string]error{"sh ": errors.New("setup-coordinator.sh failed")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			o := installed(t)
			o.Ready = ready(t, http.StatusNoContent)
			from := filepath.Join(o.State, "hypomnema-v0.9.5")
			write(t, from, "the new control plane")

			cmds := &commands{}
			setUp(&o, cmds)
			o.Run = cmds.run

			live := track(t, o, "mus_a.mp3", "a track")
			oldBackups(t, o, 5, live)

			require.Error(t, selfupdate.Install(t.Context(), o, from))

			names, part := backups(t, o)
			assert.Len(t, names, 5, "%v", names)
			assert.False(t, part)
			ownBackup(t, names)
			assert.NotContains(t, names, "predeploy-old-0", "the oldest is removed")
			assert.Equal(t, "a track", read(t, live), "the track it linked stays")
		})
	}
}

// Scenario: the disk fills while the backup is written. No backup carries the official name, nothing half-written
// is left, the five backups there are kept, and the control plane is started again.
func TestABackupThatFailsLeavesNothing(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.Ready = ready(t, http.StatusNoContent)
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")
	cmds := &commands{}
	o.Run = cmds.run
	o = selfupdate.WithCopyData(o, func(io.Writer, io.Reader) (int64, error) { return 0, syscall.ENOSPC })

	track(t, o, "mus_a.mp3", "a track")
	oldBackups(t, o, 5, "")

	err := selfupdate.Install(t.Context(), o, from)
	require.ErrorIs(t, err, syscall.ENOSPC)

	names, part := backups(t, o)
	assert.False(t, part, "no .part is left")
	assert.Len(t, names, 5)

	for _, n := range names {
		assert.True(t, strings.HasPrefix(n, "predeploy-old-"), n)
	}

	ran := cmds.lines()
	assert.Equal(t, []string{"systemctl stop hypomnema-cp.service", "systemctl start hypomnema-cp.service"}, ran)
	assert.Equal(t, "the old control plane", read(t, filepath.Join(o.Home, "bin", "hypomnema")))
}

// Scenario: a track is deleted after the backup (its row and its file), and the new release does not become ready.
// The rollback puts the database back, and the track's file back as the same file the backup links.
func TestARollbackPutsADeletedTrackBack(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.Ready = ready(t, http.StatusServiceUnavailable)
	o.ReadyWithin = time.Second
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")

	live := track(t, o, "mus_a.mp3", "a track")
	kept := track(t, o, "mus_b.mp3", "another track")

	cmds := &commands{}
	o.Run = func(ctx context.Context, name string, args ...string) error {
		if name == "runuser" {
			write(t, filepath.Join(o.Home, "data", "hypomnema.db"), "the database without track a")
			require.NoError(t, os.Remove(live))
		}

		return cmds.run(ctx, name, args...)
	}

	require.ErrorContains(t, selfupdate.Install(t.Context(), o, from), "did not become ready")

	assert.Equal(t, "the old database", read(t, filepath.Join(o.Home, "data", "hypomnema.db")))
	assert.Equal(t, "a track", read(t, live), "the track is back")

	names, _ := backups(t, o)
	require.Len(t, names, 1)
	backup := filepath.Join(o.Home, "data", names[0], "videos-music")
	assert.True(t, sameFile(t, live, filepath.Join(backup, "mus_a.mp3")), "as the same file the backup links")
	assert.True(t, sameFile(t, kept, filepath.Join(backup, "mus_b.mp3")), "a track still there is left as it is")
}

// secret is a file outside the data folder only root may read, as the updater could; it must never reach a backup
// or the music folder.
func secret(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "shadow")
	write(t, path, "root's secret")

	return path
}

// noSecretIn says that no file under dir holds the secret's bytes.
func noSecretIn(t *testing.T, dir string) {
	t.Helper()

	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return err
		}

		assert.NotEqual(t, "root's secret", read(t, p), "%s holds the secret", p)

		return nil
	}))
}

// exdev fails every hard link as two file systems would, so the files are copied.
func exdev(*os.Root, string, string) error { return &os.LinkError{Op: "link", Err: syscall.EXDEV} }

// Scenario (review of #1049): the music folder, the control plane's user's, holds symlinks to a file outside it and
// to a track in it, and a folder. The backup holds the regular track alone, linked, or copied across file systems;
// no symlink is followed, and nothing from outside the tree is copied.
func TestTheBackupTakesOnlyTheMusicsRegularFiles(t *testing.T) {
	t.Parallel()

	for _, across := range []bool{false, true} {
		o := installed(t)
		o.Ready = ready(t, http.StatusNoContent)
		from := filepath.Join(o.State, "hypomnema-v0.9.5")
		write(t, from, "the new control plane")
		o.Run = (&commands{}).run

		if across {
			o = selfupdate.WithLink(o, exdev)
		}

		live := track(t, o, "mus_a.mp3", "a track")
		hidden := secret(t)
		require.NoError(t, os.Symlink(hidden, filepath.Join(musicDir(o), "mus_out.mp3")))
		require.NoError(t, os.Symlink("mus_a.mp3", filepath.Join(musicDir(o), "mus_in.mp3")))
		require.NoError(t, os.Mkdir(filepath.Join(musicDir(o), "mus_dir.mp3"), 0o700))

		require.NoError(t, selfupdate.Install(t.Context(), o, from))

		names, part := backups(t, o)
		require.Len(t, names, 1)
		assert.False(t, part)

		backup := filepath.Join(o.Home, "data", names[0], "videos-music")
		entries, err := os.ReadDir(backup)
		require.NoError(t, err)

		var got []string
		for _, e := range entries {
			got = append(got, e.Name())
		}

		assert.Equal(t, []string{"mus_a.mp3"}, got, "across file systems: %v", across)
		assert.Equal(t, "a track", read(t, filepath.Join(backup, "mus_a.mp3")))
		assert.Equal(t, !across, sameFile(t, live, filepath.Join(backup, "mus_a.mp3")), "linked, or copied")
		noSecretIn(t, filepath.Join(o.Home, "data"))
	}
}

// Scenario (review of #1049): the music folder is a symlink to a folder outside the data folder. The backup is
// refused, the control plane started again, and nothing of that folder is backed up.
func TestABackupRefusesAMusicFolderThatIsASymlink(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.Ready = ready(t, http.StatusNoContent)
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")
	cmds := &commands{}
	o.Run = cmds.run

	outside := filepath.Dir(secret(t))
	require.NoError(t, os.MkdirAll(filepath.Join(o.Home, "data", "videos"), 0o700))
	require.NoError(t, os.Symlink(outside, musicDir(o)))

	require.Error(t, selfupdate.Install(t.Context(), o, from))

	names, part := backups(t, o)
	assert.Empty(t, names)
	assert.False(t, part)
	assert.Equal(t, []string{"systemctl stop hypomnema-cp.service", "systemctl start hypomnema-cp.service"},
		cmds.lines())
	noSecretIn(t, filepath.Join(o.Home, "data"))
}

// Scenario (review of #1049): after the backup, the control plane's user puts symlinks in the backup's
// videos-music/, one to a file outside, and a symlink in the music folder at a deleted track's name. The rollback
// puts back the deleted track it can, follows no symlink, writes through none, and copies nothing from outside.
func TestARollbackFollowsNoSymlink(t *testing.T) {
	t.Parallel()

	for _, across := range []bool{false, true} {
		o := installed(t)
		o.Ready = ready(t, http.StatusServiceUnavailable)
		o.ReadyWithin = time.Second
		from := filepath.Join(o.State, "hypomnema-v0.9.5")
		write(t, from, "the new control plane")

		live := track(t, o, "mus_a.mp3", "a track")
		planted := track(t, o, "mus_b.mp3", "another track")
		hidden := secret(t)

		cmds := &commands{}
		o.Run = func(ctx context.Context, name string, args ...string) error {
			if name == "runuser" {
				names, _ := backups(t, o)
				backup := filepath.Join(o.Home, "data", names[0], "videos-music")

				require.NoError(t, os.Remove(live))
				require.NoError(t, os.Remove(planted))
				require.NoError(t, os.Symlink(hidden, planted))
				require.NoError(t, os.Symlink(hidden, filepath.Join(backup, "mus_evil.mp3")))
			}

			return cmds.run(ctx, name, args...)
		}

		if across {
			o = selfupdate.WithLink(o, exdev)
		}

		require.ErrorContains(t, selfupdate.Install(t.Context(), o, from), "did not become ready")

		assert.Equal(t, "a track", read(t, live), "the deleted track is back")
		assert.NoFileExists(t, filepath.Join(musicDir(o), "mus_evil.mp3"), "a symlink of the backup is left out")

		target, err := os.Readlink(planted)
		require.NoError(t, err, "the symlink at a track's name is left as it is")
		assert.Equal(t, hidden, target)
		assert.Equal(t, "root's secret", read(t, hidden), "nothing is written through it")
		noSecretIn(t, musicDir(o))

		ran := cmds.lines()
		assert.Equal(t, "systemctl start hypomnema-cp.service", ran[len(ran)-1])
	}
}

// Scenario (review of #1049): the rollback copies a deleted track across file systems and the copy is cut short. No
// file has the track's name, no temporary file is left, and the control plane is started again anyway.
func TestACutCopyOnRollbackLeavesNoTrack(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.Ready = ready(t, http.StatusServiceUnavailable)
	o.ReadyWithin = time.Second
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")

	live := track(t, o, "mus_a.mp3", "a track of some length")

	var rollingBack atomic.Bool

	cmds := &commands{}
	o.Run = func(ctx context.Context, name string, args ...string) error {
		if name == "runuser" {
			require.NoError(t, os.Remove(live))
			rollingBack.Store(true)
		}

		return cmds.run(ctx, name, args...)
	}
	o = selfupdate.WithLink(o, exdev)
	o = selfupdate.WithCopyData(o, func(dst io.Writer, src io.Reader) (int64, error) {
		if !rollingBack.Load() {
			return io.Copy(dst, src)
		}

		n, _ := io.CopyN(dst, src, 4)

		return n, syscall.ENOSPC
	})

	err := selfupdate.Install(t.Context(), o, from)
	require.ErrorContains(t, err, "music")

	assert.NoFileExists(t, live, "a cut copy never has the track's name")
	entries, err := os.ReadDir(musicDir(o))
	require.NoError(t, err)
	assert.Empty(t, entries, "no temporary file is left")

	ran := cmds.lines()
	assert.Equal(t, "systemctl start hypomnema-cp.service", ran[len(ran)-1], "started again")
}

// Scenario (review of #1049): syncing the data folder fails once the backup has its name. The update stops and
// starts the control plane again, and still keeps no more than five backups, its own among them.
func TestAFailedSyncAfterPublishingStillPrunes(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.Ready = ready(t, http.StatusNoContent)
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")
	cmds := &commands{}
	o.Run = cmds.run
	o = selfupdate.WithSyncDir(o, func(root *os.Root, name string) error {
		if name == "." {
			return syscall.EIO
		}

		f, err := root.Open(name)
		if err != nil {
			return err
		}

		defer f.Close() //nolint:errcheck // Read only.

		return f.Sync()
	})

	oldBackups(t, o, 5, "")

	require.ErrorIs(t, selfupdate.Install(t.Context(), o, from), syscall.EIO)

	names, _ := backups(t, o)
	assert.Len(t, names, 5, "%v", names)
	ownBackup(t, names)

	ran := cmds.lines()
	assert.Equal(t, "systemctl start hypomnema-cp.service", ran[len(ran)-1])
}

// PR2.5 with the control plane's own database and library: a track is deleted through videos.Service after the
// backup, its row and its file, and the new release does not become ready. After the rollback the library lists the
// track again, its file the very file the backup links, and the sweep at start keeps it.
func TestARollbackBringsBackATrackDeletedByTheLibrary(t *testing.T) {
	t.Parallel()

	o := installed(t)
	o.Ready = ready(t, http.StatusServiceUnavailable)
	o.ReadyWithin = time.Second
	from := filepath.Join(o.State, "hypomnema-v0.9.5")
	write(t, from, "the new control plane")

	data := filepath.Join(o.Home, "data")
	migrated, err := os.ReadFile(sqlitetest.Migrated(t, orchestration.Migrate))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(data, "hypomnema.db"), migrated, 0o600))

	library := func(fn func(svc *videos.Service, exec func(string, ...any))) {
		db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(data, "hypomnema.db")})
		require.NoError(t, err)

		defer db.Close() //nolint:errcheck // A test's database.

		svc, err := videos.New(videos.Config{DB: db, Dir: filepath.Join(data, "videos")})
		require.NoError(t, err)

		fn(svc, func(q string, args ...any) {
			_, err := db.ExecContext(t.Context(), q, args...)
			require.NoError(t, err)
		})
	}

	id := platform.NewID("mus")

	library(func(_ *videos.Service, exec func(string, ...any)) {
		track(t, o, id+".mp3", "a track")
		exec(`INSERT INTO video_music (id, name, file, media_type, length_ms, bytes, created_at)
			VALUES (?, 'Nền', ?, 'audio/mpeg', 30000, 7, 1)`, id, id+".mp3")
	})

	cmds := &commands{}
	o.Run = func(ctx context.Context, name string, args ...string) error {
		if name == "runuser" {
			library(func(svc *videos.Service, _ func(string, ...any)) {
				require.NoError(t, svc.DeleteMusic(ctx, id))
			})
			require.NoFileExists(t, filepath.Join(musicDir(o), id+".mp3"), "the delete removed the file")
		}

		return cmds.run(ctx, name, args...)
	}

	require.ErrorContains(t, selfupdate.Install(t.Context(), o, from), "did not become ready")

	names, _ := backups(t, o)
	require.Len(t, names, 1)

	library(func(svc *videos.Service, _ func(string, ...any)) {
		lib, err := svc.Music(t.Context())
		require.NoError(t, err)
		require.Len(t, lib.Tracks, 1, "the row is back")
		assert.Equal(t, id, lib.Tracks[0].ID)
		assert.False(t, lib.Tracks[0].Missing, "and its file")
	})

	assert.True(t, sameFile(t, filepath.Join(musicDir(o), id+".mp3"),
		filepath.Join(data, names[0], "videos-music", id+".mp3")), "the very file the backup links")
}
