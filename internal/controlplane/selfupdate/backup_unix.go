//go:build unix

package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
)

// The update's backup of the database and the videos' music library (issue #910, row 6d). The update runs as root
// while the control plane's folder, its data folder, the music in it and the backups are the control plane's user's
// (setup-controlplane.sh: the folder is the user's home's), who may rename or replace any of them by name while the
// update runs. So Install opens the data folder once, as an os.Root, and every step of the backup, the prune and the
// rollback goes through that handle: whatever is renamed into data's place by name later, nothing outside the folder
// it opened is read, written, renamed or removed. The folders the update writes into (the backup being made, a music
// folder) are opened too and written through their own handles; a name is checked to still be the folder opened
// before it is renamed or removed. The music's files are taken only when they are regular files, checked again once
// opened or linked, and what is copied is written under a temporary name and renamed in place whole.

const (
	// partSuffix ends the name of a backup being written.
	partSuffix = ".part"
	// musicBackup is the folder of a backup that holds the videos' music library.
	musicBackup = "videos-music"
	// tmpSuffix ends the name of a file being copied.
	tmpSuffix = ".tmp"
)

// errReplaced reports a folder whose name was given to another while the update wrote into it.
var errReplaced = errors.New("the folder was replaced while the update wrote into it")

// errNotRegular reports a file of the music or the database that is not a regular file, or that changed while it was
// taken.
var errNotRegular = errors.New("not a regular file")

// openData opens the data folder as a root: the folder itself, never a symlink to another.
func openData(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", dir, err)
	}

	if got, err := root.Stat("."); err != nil || !os.SameFile(info, got) {
		_ = root.Close()

		return nil, fmt.Errorf("%s changed while it was opened", dir)
	}

	return root, nil
}

// heldDir opens the folder name of the root and answers it with what it is, refusing what is not a folder or what
// changed between its look and its opening: the update then writes through this handle, not through the name.
func heldDir(root *os.Root, name string) (*os.Root, os.FileInfo, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}

	if !before.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a folder", name)
	}

	held, err := root.OpenRoot(name)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", name, err)
	}

	got, err := held.Stat(".")
	if err != nil || !os.SameFile(before, got) {
		_ = held.Close()

		return nil, nil, fmt.Errorf("%s: %w", name, errReplaced)
	}

	return held, got, nil
}

// isStill says whether name in the root is still the folder held.
func isStill(root *os.Root, name string, held os.FileInfo) bool {
	got, err := root.Lstat(name)

	return err == nil && os.SameFile(held, got)
}

// chownHeld gives a held folder to the control plane's user through its handle.
func chownHeld(held *os.Root, owner ids) error {
	f, err := held.Open(".")
	if err != nil {
		return err
	}

	err = f.Chown(owner.uid, owner.gid)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	return err
}

// musicFolder is the music folder's path in the data folder, as videos.MusicDir names it.
func (o Options) musicFolder() string {
	rel, err := filepath.Rel(filepath.Dir(o.database()), videos.MusicDir(o.database()))
	if err != nil {
		return "videos/music"
	}

	return filepath.ToSlash(rel)
}

// backUp makes the backup of an update in the data folder root, as a pair, the database and the videos' music
// library, published whole as name: into .<name>.part, made here and held, it copies the database and its WAL files
// and hard-links every regular file of the music folder (copying across file systems; an absent folder is backed up
// empty), syncs them, gives them to the control plane's user, and only then renames the folder to name, once its
// name is checked to still be the folder made. Whatever fails before empties the folder made and removes it while it
// still has its name; published says the backup has its name, even when syncing the data folder after failed.
func backUp(o Options, root *os.Root, name string, owner ids) (published bool, err error) {
	part := "." + name + partSuffix

	if err := root.Mkdir(part, 0o700); err != nil {
		return false, fmt.Errorf("making %s: %w", part, err)
	}

	held, info, err := heldDir(root, part)
	if err != nil {
		return false, err
	}
	defer held.Close() //nolint:errcheck // A folder.

	defer func() {
		if !published {
			emptyHeld(held)

			if isStill(root, part, info) {
				_ = root.Remove(part)
			}
		}
	}()

	db := filepath.Base(o.database())

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := o.copyRegular(root, db+suffix, held, db+suffix, owner); err != nil {
			if suffix != "" && errors.Is(err, fs.ErrNotExist) {
				continue
			}

			return false, fmt.Errorf("backing up the database: %w", err)
		}
	}

	if err := held.Mkdir(musicBackup, 0o700); err != nil {
		return false, fmt.Errorf("making %s: %w", musicBackup, err)
	}

	music, _, err := heldDir(held, musicBackup)
	if err != nil {
		return false, err
	}
	defer music.Close() //nolint:errcheck // A folder.

	if err := o.copyMusic(root, o.musicFolder(), part+"/"+musicBackup, music, owner, false); err != nil {
		return false, fmt.Errorf("backing up the videos' music: %w", err)
	}

	for _, d := range []*os.Root{music, held} {
		if err := o.sync(d, "."); err != nil {
			return false, err
		}

		if err := chownHeld(d, owner); err != nil {
			return false, fmt.Errorf("giving the backup to the control plane's user: %w", err)
		}
	}

	if !isStill(root, part, info) {
		return false, fmt.Errorf("%s: %w", part, errReplaced)
	}

	if err := root.Rename(part, name); err != nil {
		return false, fmt.Errorf("publishing the backup %s: %w", name, err)
	}

	if !isStill(root, name, info) {
		return false, fmt.Errorf("%s: %w", name, errReplaced)
	}

	return true, o.sync(root, ".")
}

// emptyHeld removes what a held folder holds.
func emptyHeld(held *os.Root) {
	dir, err := held.Open(".")
	if err != nil {
		return
	}

	entries, _ := dir.ReadDir(-1)
	_ = dir.Close()

	for _, e := range entries {
		_ = held.RemoveAll(e.Name())
	}
}

// pruneBackups removes all but the newest keep `predeploy-*` folders of the data folder root, by when they were
// made, and the `.predeploy-*.part` folders a backup that stopped half-written left, through the root: an entry that
// is not a folder (a symlink put there) is no backup, and is never followed. It runs once an update's backup is
// published, whatever the update's outcome. A music file's bytes go with the last backup that links it.
func pruneBackups(root *os.Root, keep int) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}

	entries, err := dir.ReadDir(-1)
	_ = dir.Close()

	if err != nil {
		return err
	}

	type backup struct {
		name string
		at   time.Time
	}

	var (
		found []backup
		errs  []error
	)

	for _, e := range entries {
		name := e.Name()

		switch {
		case strings.HasPrefix(name, ".predeploy-") && strings.HasSuffix(name, partSuffix):
			errs = append(errs, root.RemoveAll(name))
		case strings.HasPrefix(name, "predeploy-"):
			info, err := root.Lstat(name)
			if err == nil && info.IsDir() {
				found = append(found, backup{name: name, at: info.ModTime()})
			}
		}
	}

	if len(found) > keep {
		slices.SortFunc(found, func(a, b backup) int { return b.at.Compare(a.at) })

		for _, b := range found[keep:] {
			errs = append(errs, root.RemoveAll(b.name))
		}
	}

	return errors.Join(errs...)
}

// restoreDatabase copies the backup's database and WAL files back over the database, through the data folder root;
// a WAL file the backup did not have is removed. A copy needs room for a second database beside the live one: when
// the disk has none (ENOSPC), the backup's file itself is renamed into place within the data folder, as the update
// before the music's backup put the database back without a second copy (review round 3 of #1049); the backup then
// keeps its music without that file.
func (o Options) restoreDatabase(root *os.Root, backup string, owner ids) error {
	db := filepath.Base(o.database())

	for _, suffix := range []string{"", "-wal", "-shm"} {
		saved := backup + "/" + db + suffix

		err := o.copyRegular(root, saved, root, db+suffix, owner)
		if errors.Is(err, fs.ErrNotExist) {
			_ = root.Remove(db + suffix)

			continue
		}

		if errors.Is(err, syscall.ENOSPC) {
			info, lerr := root.Lstat(saved)
			if lerr != nil || !info.Mode().IsRegular() {
				return errors.Join(err, fmt.Errorf("%s: %w", saved, errNotRegular))
			}

			o.printf("no room to copy %s back; moving it into place\n", saved)
			err = root.Rename(saved, db+suffix)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// restoreMusic links every regular file of the backup's music that the music folder lacks back into it, as the
// same file, making the folder when it is gone, through the data folder root.
func (o Options) restoreMusic(root *os.Root, backup string, owner ids) error {
	to := o.musicFolder()
	for _, d := range parents(to) {
		if err := ensureDir(root, d, owner); err != nil {
			return err
		}
	}

	music, _, err := heldDir(root, to)
	if err != nil {
		return err
	}
	defer music.Close() //nolint:errcheck // A folder.

	return o.copyMusic(root, backup+"/"+musicBackup, to, music, owner, true)
}

// parents are a relative path's folders from the top down, the path included.
func parents(p string) []string {
	var out []string
	for ; p != "." && p != "/" && p != ""; p = path.Dir(p) {
		out = append([]string{p}, out...)
	}

	return out
}

// ensureDir makes a folder of the root owned by the control plane's user, or finds it there as a folder.
func ensureDir(root *os.Root, name string, owner ids) error {
	info, err := root.Lstat(name)

	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("%s is not a folder", name)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}

	if err := root.Mkdir(name, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", name, err)
	}

	if err := root.Lchown(name, owner.uid, owner.gid); err != nil {
		return fmt.Errorf("giving %s to the control plane's user: %w", name, err)
	}

	return nil
}

// copyMusic hard-links every regular file of the folder from of the root into the folder to, held as dst, copying
// into dst when they are on two file systems. An absent from is no file; a from, or a folder above it, that is not a
// folder (a symlink) is refused. Anything in from that is not a regular file is left out, and so is a file replaced
// while it was linked; a link that did not land in dst (to was replaced) stops the copy. With missingOnly, a name dst
// already has, of any kind, is left as it is.
func (o Options) copyMusic(root *os.Root, from, to string, dst *os.Root, owner ids, missingOnly bool) error {
	for _, d := range parents(from) {
		info, err := root.Lstat(d)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		if err != nil {
			return err
		}

		if !info.IsDir() {
			return fmt.Errorf("%s is not a folder", d)
		}
	}

	held, err := dst.Stat(".")
	if err != nil {
		return err
	}

	dir, err := root.Open(from)
	if err != nil {
		return err
	}

	entries, err := dir.ReadDir(-1)
	_ = dir.Close()

	if err != nil {
		return fmt.Errorf("reading %s: %w", from, err)
	}

	for _, e := range entries {
		name := e.Name()
		src := from + "/" + name

		info, err := root.Lstat(src)
		if err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			o.printf("leaving %s out: %v\n", src, errNotRegular)

			continue
		}

		if missingOnly {
			if _, err := dst.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
				continue
			}
		}

		err = o.linkIn(root, src, to+"/"+name)
		if err != nil && !errors.Is(err, syscall.EXDEV) && !isStill(root, to, held) {
			return fmt.Errorf("%s: %w", to, errReplaced)
		}

		switch {
		case errors.Is(err, syscall.EXDEV):
			err = o.copyRegular(root, src, dst, name, owner)
		case err == nil:
			got, lerr := dst.Lstat(name)
			if lerr != nil {
				return fmt.Errorf("%s: %w", to, errReplaced)
			}

			// The link is of the file Lstat saw, not of a symlink put in its place meanwhile.
			if !os.SameFile(info, got) {
				_ = dst.Remove(name)

				o.printf("leaving %s out: it changed while it was linked\n", src)

				continue
			}
		}

		if err != nil {
			return fmt.Errorf("putting %s in %s: %w", src, to, err)
		}
	}

	return o.sync(dst, ".")
}

// copyRegular copies the regular file src of the root from to dstName in the root to, under a temporary name first,
// synced, mode 0600 and owned by the control plane's user, then renamed to dstName; a copy cut short never has its
// name.
func (o Options) copyRegular(from *os.Root, src string, to *os.Root, dstName string, owner ids) error {
	info, err := from.Lstat(src)
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: %w", src, errNotRegular)
	}

	in, err := from.Open(src)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // Read only.

	if got, err := in.Stat(); err != nil || !os.SameFile(info, got) {
		return fmt.Errorf("%s changed while it was opened: %w", src, errNotRegular)
	}

	tmp := path.Join(path.Dir(dstName), "."+path.Base(dstName)+tmpSuffix)
	if err := to.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	out, err := to.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}

	copyData := o.copyData
	if copyData == nil {
		copyData = io.Copy
	}

	_, err = copyData(out, in)
	if err == nil {
		err = out.Sync()
	}

	if err == nil {
		err = out.Chown(owner.uid, owner.gid)
	}

	if closeErr := out.Close(); err == nil {
		err = closeErr
	}

	if err == nil {
		err = to.Rename(tmp, dstName)
	}

	if err != nil {
		_ = to.Remove(tmp)

		return fmt.Errorf("writing %s: %w", dstName, err)
	}

	return nil
}

func (o Options) linkIn(root *os.Root, oldname, newname string) error {
	if o.link != nil {
		return o.link(root, oldname, newname)
	}

	return root.Link(oldname, newname)
}

// sync makes a folder of the root's entries durable.
func (o Options) sync(root *os.Root, name string) error {
	if o.syncDir != nil {
		return o.syncDir(root, name)
	}

	f, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("syncing %s: %w", name, err)
	}

	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return fmt.Errorf("syncing %s: %w", name, err)
	}

	return nil
}
