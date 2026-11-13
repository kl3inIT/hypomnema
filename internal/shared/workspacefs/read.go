package workspacefs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// resolve is the real place of a path inside the folder, with the path made
// clean: NUL, an absolute path and one that climbs out are refused, and so is
// one whose links lead out of the folder (Omnigent's _validate_path and
// _resolve).
func resolve(root, rel string) (place, clean string, err error) {
	if strings.ContainsRune(rel, 0) || strings.Contains(rel, `\`) || strings.HasPrefix(rel, "/") ||
		filepath.IsAbs(rel) || slices.Contains(strings.Split(rel, "/"), "..") {
		return "", "", failure(CodeInvalidPath, "%q is not a path in this folder", rel)
	}

	if clean = path.Clean(rel); clean == "." {
		clean = ""
	}

	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", failure(CodeGone, "the folder %s is not there any more", root)
		}

		return "", "", failure(CodeFailed, "reading the folder: %s", err)
	}

	place, err = filepath.EvalSymlinks(filepath.Join(base, filepath.FromSlash(clean)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", failure(CodeNotFound, "nothing is at %s", clean)
		}

		return "", "", failure(CodeFailed, "reading %s: %s", clean, err)
	}

	if !Inside(base, place) {
		return "", "", failure(CodeInvalidPath, "%s leads out of this folder", clean)
	}

	return place, clean, nil
}

// Inside says whether a real path is the folder base or under it.
func Inside(base, place string) bool {
	rel, err := filepath.Rel(base, place)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

// List is a page of a directory, by name, after the name `after`.
func List(root, rel, after string) (*Listing, error) {
	dir, clean, err := resolve(root, rel)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, failure(CodeFailed, "listing %s: %s", clean, err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	out := &Listing{Entries: []Entry{}}

	for _, e := range entries {
		if after != "" && e.Name() <= after {
			continue
		}

		// An upload not finished is not a file of the folder yet.
		if strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), uploadMark) {
			continue
		}

		if len(out.Entries) == PageEntries {
			out.HasMore = true

			break
		}

		full := filepath.Join(dir, e.Name())

		// A link is listed as what it points to; one that leads nowhere as a file.
		info, err := os.Stat(full)
		if err != nil {
			if info, err = os.Lstat(full); err != nil {
				continue
			}
		}

		entry := Entry{
			Name: e.Name(), Path: path.Join(clean, e.Name()), Type: "file", ModifiedAt: info.ModTime().Unix(),
		}
		if info.IsDir() {
			entry.Type = "directory"
		} else {
			entry.Bytes = info.Size()
		}

		out.Entries = append(out.Entries, entry)
	}

	return out, nil
}

// Read is a file's text, up to MaxRead, cut at a character. A file whose first
// bytes hold a NUL or are not UTF-8 is binary and is not sent. With raw, the
// bytes are sent whatever they are, and a file over MaxRead is too large.
func Read(root, rel string, raw bool) (*File, error) {
	place, clean, err := resolve(root, rel)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(place)
	if err != nil {
		return nil, failure(CodeFailed, "reading %s: %s", clean, err)
	}

	if !info.Mode().IsRegular() {
		return nil, failure(CodeInvalidPath, "%s is not a file", clean)
	}

	f, err := os.Open(place) //nolint:gosec // a path resolve kept inside the folder
	if err != nil {
		return nil, failure(CodeFailed, "reading %s: %s", clean, err)
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, MaxRead+1))
	if err != nil {
		return nil, failure(CodeFailed, "reading %s: %s", clean, err)
	}

	out := &File{Path: clean, Bytes: info.Size()}

	if len(data) <= MaxRead {
		out.SHA256 = sha(data)
	}

	if raw {
		if len(data) > MaxRead {
			return nil, failure(CodeTooLarge, "%s is larger than %d MiB", clean, MaxRead>>20)
		}

		out.Data = data
		out.Binary = binary(data, false)

		return out, nil
	}

	if out.Truncated = len(data) > MaxRead; out.Truncated {
		data = data[:MaxRead]
	}

	if binary(data, out.Truncated) {
		out.Binary, out.Truncated = true, false

		return out, nil
	}

	if out.Truncated {
		data = cut(data)
	}

	out.Content = string(data)

	return out, nil
}

// binary says whether bytes are not text: a NUL in the first 8 KiB, or bytes
// that are not UTF-8. Of bytes that were cut short, a character the cut split
// is not held against them.
func binary(data []byte, cutShort bool) bool {
	head := data
	if len(head) > sniffBytes {
		head = head[:sniffBytes]
	}

	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}

	if cutShort {
		data = cut(data)
	}

	return !utf8.Valid(data)
}

// cut drops a character the end of the bytes cut in two.
func cut(data []byte) []byte {
	for i := 0; i < utf8.UTFMax && i < len(data); i++ {
		r, size := utf8.DecodeLastRune(data[:len(data)-i])
		if r != utf8.RuneError || size > 1 {
			return data[:len(data)-i]
		}
	}

	return data
}

// sha is the SHA-256 of bytes, in hex.
func sha(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}
