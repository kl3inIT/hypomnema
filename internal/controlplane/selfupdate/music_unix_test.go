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
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/selfupdate"
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
