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
	"syscall"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
)

// The update's backup of the database and the videos' music library (issue #910, row 6d). The update runs as root
// while the data folder, the music in it and the backups are the control plane's user's: every file operation here
// goes through an os.Root of the data folder, so no path, nor a symlink in it, leads out of it, and the music's own
// files are taken only when they are regular files of the music tree, checked again once opened or linked. What is
// written is first written under a temporary name and renamed in place whole.

const (
	// partSuffix ends the name of a backup being written.
	partSuffix = ".part"
	// musicBackup is the folder of a backup that holds the videos' music library.
	musicBackup = "videos-music"
	// tmpSuffix ends the name of a file being copied.
	tmpSuffix = ".tmp"
)

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

// musicFolder is the music folder's path in the data folder, as videos.MusicDir names it.
func (o Options) musicFolder() string {
	rel, err := filepath.Rel(filepath.Dir(o.database()), videos.MusicDir(o.database()))
	if err != nil {
		return "videos/music"
	}

	return filepath.ToSlash(rel)
}

// backUp makes the backup of an update as a pair, the database and the videos' music library, published whole: into
// .predeploy-<stamp>.part it copies the database and its WAL files and hard-links every regular file of the music
// folder (copying across file systems; an absent folder is backed up empty), syncs them, gives the folder to the
// control plane's user, and only then renames it to dir. Whatever fails before the rename removes the .part folder;
// published says the backup has its name, even when syncing the data folder after failed.
func backUp(o Options, dir string, owner ids) (published bool, err error) {
	root, err := openData(filepath.Dir(dir))
	if err != nil {
		return false, err
	}
	defer root.Close() //nolint:errcheck // A folder, read only.

	name := filepath.Base(dir)
	part := "." + name + partSuffix

	if err := root.Mkdir(part, 0o700); err != nil {
		return false, fmt.Errorf("making %s: %w", part, err)
	}

	defer func() {
		if !published {
			_ = root.RemoveAll(part)
		}
	}()

	db := filepath.Base(o.database())

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := o.copyRegular(root, db+suffix, part+"/"+db+suffix, owner); err != nil {
			if suffix != "" && errors.Is(err, fs.ErrNotExist) {
				continue
			}

			return false, fmt.Errorf("backing up the database: %w", err)
		}
	}

	music := part + "/" + musicBackup
	if err := root.Mkdir(music, 0o700); err != nil {
		return false, fmt.Errorf("making %s: %w", music, err)
	}

	if err := o.copyMusic(root, o.musicFolder(), music, owner, false); err != nil {
		return false, fmt.Errorf("backing up the videos' music: %w", err)
	}

	for _, d := range []string{music, part} {
		if err := o.sync(root, d); err != nil {
			return false, err
		}

		if err := root.Lchown(d, owner.uid, owner.gid); err != nil {
			return false, fmt.Errorf("giving %s to the control plane's user: %w", d, err)
		}
	}

	if err := root.Rename(part, name); err != nil {
		return false, fmt.Errorf("publishing the backup %s: %w", dir, err)
	}

	return true, o.sync(root, ".")
}

// restoreMusic links every regular file of the backup's music that the music folder lacks back into it, as the
// same file, making the folder when it is gone.
func restoreMusic(o Options, backup string, owner ids) error {
	root, err := openData(filepath.Dir(backup))
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // A folder, read only.

	to := o.musicFolder()
	for _, d := range parents(to) {
		if err := ensureDir(root, d, owner); err != nil {
			return err
		}
	}

	return o.copyMusic(root, filepath.Base(backup)+"/"+musicBackup, to, owner, true)
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

// copyMusic hard-links every regular file of the folder from into to, both in the root, copying when they are on two
// file systems. An absent from is no file; a from, or a folder above it, that is not a folder (a symlink) is refused.
// Anything in it that is not a regular file is left out, and so is a file replaced while it was linked. With
// missingOnly, a name to already has, of any kind, is left as it is.
func (o Options) copyMusic(root *os.Root, from, to string, owner ids, missingOnly bool) error {
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
		src, dst := from+"/"+e.Name(), to+"/"+e.Name()

		info, err := root.Lstat(src)
		if err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			o.printf("leaving %s out: %v\n", src, errNotRegular)

			continue
		}

		if missingOnly {
			if _, err := root.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
				continue
			}
		}

		err = o.linkIn(root, src, dst)

		switch {
		case errors.Is(err, syscall.EXDEV):
			err = o.copyRegular(root, src, dst, owner)
		case err == nil:
			// The link is of the file Lstat saw, not of a symlink put in its place meanwhile.
			if got, lerr := root.Lstat(dst); lerr != nil || !os.SameFile(info, got) {
				_ = root.Remove(dst)

				o.printf("leaving %s out: it changed while it was linked\n", src)

				continue
			}
		}

		if err != nil {
			return fmt.Errorf("putting %s in %s: %w", src, to, err)
		}
	}

	return o.sync(root, to)
}

// copyRegular copies the regular file src of the root to dst, under a temporary name first, synced, mode 0600 and
// owned by the control plane's user, then renamed to dst; a copy cut short never has dst's name.
func (o Options) copyRegular(root *os.Root, src, dst string, owner ids) error {
	info, err := root.Lstat(src)
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: %w", src, errNotRegular)
	}

	in, err := root.Open(src)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // Read only.

	if got, err := in.Stat(); err != nil || !os.SameFile(info, got) {
		return fmt.Errorf("%s changed while it was opened: %w", src, errNotRegular)
	}

	tmp := path.Join(path.Dir(dst), "."+path.Base(dst)+tmpSuffix)
	if err := root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
		err = root.Rename(tmp, dst)
	}

	if err != nil {
		_ = root.Remove(tmp)

		return fmt.Errorf("writing %s: %w", dst, err)
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
