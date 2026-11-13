package workspacefs

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// An upload (issue #315), as Orca's staged save (src/shared/file-write/staged-file-save.ts; MIT, read for
// behavior): the file comes in pieces into a temporary file beside its place, which a commit renames into place, so
// that the place never holds part of a file. Each step is its own request and holds no state but the temporary
// file, so the Bot's helper, a process per request, carries it as the node does.
const (
	// MaxUpload is the largest file uploaded (Orca: 50 MiB).
	MaxUpload = 50 << 20
	// MaxChunk is the largest piece sent at once (Orca: 384 KiB), which fits a node's socket message.
	MaxChunk = 384 << 10
	// uploadLeft is how long a temporary file nobody writes is kept (Orca drops a stalled save after 2 minutes).
	uploadLeft = 2 * time.Minute
	uploadMark = ".hypomnema-upload-"
)

// The operations of an upload.
const (
	OpUploadBegin  Op = "upload_begin"
	OpUploadChunk  Op = "upload_chunk"
	OpUploadCommit Op = "upload_commit"
	OpUploadAbort  Op = "upload_abort"
)

var uploadID = regexp.MustCompile(`^[0-9a-f]{24}$`)

// staging is the temporary file of an upload to rel: beside its place, hidden, named by the upload.
func staging(root, rel, upload string) (temp, where, clean string, err error) {
	if !uploadID.MatchString(upload) {
		return "", "", "", failure(CodeInvalidPath, "%q is not an upload", upload)
	}

	where, clean, link, err := place(root, rel)
	if err != nil {
		return "", "", "", err
	}

	if link {
		return "", "", "", failure(CodeInvalidPath, "%s is a link, which is not written through", clean)
	}

	return filepath.Join(filepath.Dir(where), "."+filepath.Base(where)+uploadMark+upload), where, clean, nil
}

// UploadBegin starts an upload to rel and returns its id. Uploads left in the same folder are removed.
func UploadBegin(root, rel string) (*Done, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return nil, failure(CodeFailed, "naming an upload: %s", err)
	}

	upload := hex.EncodeToString(raw)

	temp, _, clean, err := staging(root, rel, upload)
	if err != nil {
		return nil, err
	}

	sweep(filepath.Dir(temp))

	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644) //nolint:gosec // kept inside the folder
	if err != nil {
		return nil, failure(CodeFailed, "starting the upload of %s: %s", clean, err)
	}

	if err := f.Close(); err != nil {
		return nil, failure(CodeFailed, "starting the upload of %s: %s", clean, err)
	}

	return &Done{Path: clean, Upload: upload}, nil
}

// sweep removes the uploads in dir nobody has written for uploadLeft.
func sweep(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if !strings.Contains(e.Name(), uploadMark) || !strings.HasPrefix(e.Name(), ".") {
			continue
		}

		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > uploadLeft {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// UploadChunk writes data at offset, which must be where the upload has got to.
func UploadChunk(root, rel, upload string, offset int64, data []byte) (*Done, error) {
	if len(data) > MaxChunk {
		return nil, failure(CodeTooLarge, "a piece larger than %d KiB", MaxChunk>>10)
	}

	temp, _, clean, err := staging(root, rel, upload)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(temp, os.O_WRONLY, 0) //nolint:gosec // kept inside the folder
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, failure(CodeNotFound, "the upload of %s is not there any more", clean)
		}

		return nil, failure(CodeFailed, "uploading %s: %s", clean, err)
	}
	defer func() { _ = f.Close() }()

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, failure(CodeFailed, "uploading %s: %s", clean, err)
	}

	switch {
	case offset != size:
		return nil, failure(CodeChanged, "the upload of %s is at %d bytes, not %d", clean, size, offset)
	case size+int64(len(data)) > MaxUpload:
		return nil, failure(CodeTooLarge, "a file larger than %d MiB is not uploaded", MaxUpload>>20)
	}

	if _, err := f.Write(data); err != nil {
		return nil, failure(CodeFailed, "uploading %s: %s", clean, err)
	}

	return &Done{Path: clean, Upload: upload}, nil
}

// UploadCommit puts the upload in its place: where nothing is, or, with expect, over the file with that SHA-256.
func UploadCommit(root, rel, upload, expect string) (*Done, error) {
	writes.Lock()
	defer writes.Unlock()

	temp, where, clean, err := staging(root, rel, upload)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(temp) //nolint:gosec // kept inside the folder
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, failure(CodeNotFound, "the upload of %s is not there any more", clean)
		}

		return nil, failure(CodeFailed, "reading the upload of %s: %s", clean, err)
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
			return nil, failure(CodeChanged, "%s changed since it was asked to be replaced", clean)
		}

		mode = info.Mode().Perm()
	case errors.Is(err, fs.ErrNotExist) && expect != "":
		return nil, failure(CodeNotFound, "%s is not there any more", clean)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, failure(CodeFailed, "reading %s: %s", clean, err)
	}

	if err := os.Chmod(temp, mode); err != nil {
		return nil, failure(CodeFailed, "uploading %s: %s", clean, err)
	}

	if err := os.Rename(temp, where); err != nil {
		return nil, failure(CodeFailed, "uploading %s: %s", clean, err)
	}

	sum := sha256.Sum256(data)

	return &Done{Path: clean, SHA256: hex.EncodeToString(sum[:])}, nil
}

// UploadAbort removes an upload that will not be finished.
func UploadAbort(root, rel, upload string) (*Done, error) {
	temp, _, clean, err := staging(root, rel, upload)
	if err != nil {
		return nil, err
	}

	if err := os.Remove(temp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, failure(CodeFailed, "ending the upload of %s: %s", clean, err)
	}

	return &Done{Path: clean}, nil
}
