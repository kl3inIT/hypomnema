package workspacefs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// The changes (issue #297), after Orca's file methods (src/main/runtime/rpc/methods/files/files-mutation-methods.ts,
// src/main/ipc/filesystem/local-disk-write-precondition.ts; MIT, read for behavior): a write replaces a file only
// while it is still the text it was read as, compared where the file is and under a lock, and lands whole, through
// a file beside it renamed over it; a file or folder is made only where nothing is (Orca's `wx`); a delete is for
// good, there being no Trash on a node. A link is never written through: the place of a new or changed name is its
// folder's real place, and the name itself must not be a link.

// writes keeps one change at a time in this process, so a compare and the write after it are not split by another
// of ours (Orca locks per path in its own process too). Agents writing the folder are not held by it.
var writes sync.Mutex

// place is where a name inside the folder would be: its folder resolved, which must be there and inside. The name
// itself is not followed; link says it is a link. The folder itself is not a name.
func place(root, rel string) (where, clean string, link bool, err error) {
	if strings.ContainsRune(rel, 0) || strings.Contains(rel, `\`) || strings.HasPrefix(rel, "/") ||
		filepath.IsAbs(rel) {
		return "", "", false, failure(CodeInvalidPath, "%q is not a path in this folder", rel)
	}

	if clean = path.Clean(rel); clean == "." || clean == "" {
		return "", "", false, failure(CodeInvalidPath, "the folder itself cannot be changed")
	}

	dir, name := path.Split(clean)

	parent, _, err := resolve(root, strings.TrimSuffix(dir, "/"))
	if err != nil {
		return "", "", false, err
	}

	if name == ".." || name == "." {
		return "", "", false, failure(CodeInvalidPath, "%q is not a path in this folder", rel)
	}

	where = filepath.Join(parent, name)
	info, err := os.Lstat(where)

	return where, clean, err == nil && info.Mode()&fs.ModeSymlink != 0, nil
}

// Write puts content in the file at rel. With expect, the file must be there and still have that SHA-256; without,
// nothing may be there. The text lands whole or not at all.
func Write(root, rel, content, expect string) (*Done, error) {
	if len(content) > MaxWrite {
		return nil, failure(CodeTooLarge, "a file larger than %d MiB is not written from here", MaxWrite>>20)
	}

	writes.Lock()
	defer writes.Unlock()

	where, clean, link, err := place(root, rel)
	if err != nil {
		return nil, err
	}

	if link {
		return nil, failure(CodeInvalidPath, "%s is a link, which is not written through", clean)
	}

	mode := os.FileMode(0o644)

	info, err := os.Stat(where)

	switch {
	case err == nil && !info.Mode().IsRegular():
		return nil, failure(CodeInvalidPath, "%s is not a file", clean)
	case err == nil && expect == "":
		return nil, failure(CodeExists, "%s is already there", clean)
	case err == nil:
		now, err := os.ReadFile(where) //nolint:gosec // a path place kept inside the folder
		if err != nil {
			return nil, failure(CodeFailed, "reading %s: %s", clean, err)
		}

		if sha(now) != expect {
			return nil, failure(CodeChanged, "%s changed since it was opened", clean)
		}

		mode = info.Mode().Perm()
	case errors.Is(err, fs.ErrNotExist) && expect != "":
		return nil, failure(CodeNotFound, "%s is not there any more", clean)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, failure(CodeFailed, "reading %s: %s", clean, err)
	}

	if err := landWhole(where, []byte(content), mode); err != nil {
		return nil, failure(CodeFailed, "writing %s: %s", clean, err)
	}

	return &Done{Path: clean, SHA256: sha([]byte(content))}, nil
}

// landWhole writes data to a file beside where and renames it over where.
func landWhole(where string, data []byte, mode os.FileMode) error {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return err
	}

	temp := filepath.Join(filepath.Dir(where), "."+filepath.Base(where)+".hypomnema-"+hex.EncodeToString(raw))

	if err := os.WriteFile(temp, data, mode); err != nil {
		return err
	}

	// WriteFile's mode is cut by the umask; the file keeps the one it had.
	if err := os.Chmod(temp, mode); err != nil {
		_ = os.Remove(temp)

		return err
	}

	if err := os.Rename(temp, where); err != nil {
		_ = os.Remove(temp)

		return err
	}

	return nil
}

// Mkdir makes a folder at rel, where nothing is.
func Mkdir(root, rel string) (*Done, error) {
	writes.Lock()
	defer writes.Unlock()

	where, clean, _, err := place(root, rel)
	if err != nil {
		return nil, err
	}

	if err := os.Mkdir(where, 0o755); err != nil { //nolint:gosec // a working folder, as git makes them
		if errors.Is(err, fs.ErrExist) {
			return nil, failure(CodeExists, "%s is already there", clean)
		}

		return nil, failure(CodeFailed, "making %s: %s", clean, err)
	}

	return &Done{Path: clean}, nil
}

// Rename moves rel to to, where nothing is.
func Rename(root, rel, to string) (*Done, error) {
	writes.Lock()
	defer writes.Unlock()

	from, clean, _, err := place(root, rel)
	if err != nil {
		return nil, err
	}

	target, cleanTo, _, err := place(root, to)
	if err != nil {
		return nil, err
	}

	if _, err := os.Lstat(from); err != nil {
		return nil, failure(CodeNotFound, "nothing is at %s", clean)
	}

	if _, err := os.Lstat(target); err == nil {
		return nil, failure(CodeExists, "%s is already there", cleanTo)
	}

	if cleanTo == clean || strings.HasPrefix(cleanTo, clean+"/") {
		return nil, failure(CodeInvalidPath, "%s cannot be moved into itself", clean)
	}

	if err := os.Rename(from, target); err != nil {
		return nil, failure(CodeFailed, "moving %s: %s", clean, err)
	}

	return &Done{Path: cleanTo}, nil
}

// Delete deletes the file or folder at rel, for good. A link is deleted, not what it points to.
func Delete(root, rel string) (*Done, error) {
	writes.Lock()
	defer writes.Unlock()

	where, clean, _, err := place(root, rel)
	if err != nil {
		return nil, err
	}

	if _, err := os.Lstat(where); err != nil {
		return nil, failure(CodeNotFound, "nothing is at %s", clean)
	}

	if err := os.RemoveAll(where); err != nil {
		return nil, failure(CodeFailed, "deleting %s: %s", clean, err)
	}

	return &Done{Path: clean}, nil
}
