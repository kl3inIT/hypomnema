// Package artifactpack is what an Artifact's files are (issue #402): the rules a Bot's file or folder is read by,
// how it goes to the control plane, and what kind of thing it is, found from its bytes and its name. The command
// `hypomnema artifact show` reads it in the Bot's directory and the control plane checks it again, so both use one
// set of rules.
//
// The rules are OpenMausBot's for a page (server/pages/bundle.ts, packages/phone-protocol/src/app.ts and
// published-page.ts, Apache-2.0): one HTML file at most 256 KB, a folder with index.html at its top and its other
// files beside it, at most 5 MB and 200 files, hidden files and package folders left out, a link that leads out of
// the folder never followed, and only the kinds of files a page loads. Any other single file is an Artifact too
// (issue #402, scope widened by the owner on 2026-11-14), shown by its kind as Onyx's preview variants are: a PDF,
// an image, or a file to download.
package artifactpack

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits, OpenMausBot's (APP_LIMITS.htmlBytes and title, PAGE_MAX_BYTES, PAGE_MAX_FILES).
const (
	// MaxHTML bounds the page itself: one HTML file, or a folder's index.html.
	MaxHTML = 256 << 10
	// MaxTotal bounds all the files of an Artifact.
	MaxTotal = 5 << 20
	// MaxFiles bounds a folder's files.
	MaxFiles = 200
	// MaxTitle bounds a title, in characters.
	MaxTitle = 80
	// MaxBody bounds the archive that carries the files: theirs and a tar header and padding each.
	MaxBody = MaxTotal + (MaxFiles+2)*1024
	// maxName bounds one segment of a path.
	maxName = 100
	// maxDepth is how deep a folder is read (OpenMausBot reads nine levels below the top).
	maxDepth = 9
)

// Entry is a folder page's own file.
const Entry = "index.html"

// Type is what an Artifact is shown as.
type Type string

// The types an Artifact has.
const (
	TypeHTML  Type = "html"
	TypePDF   Type = "pdf"
	TypeImage Type = "image"
	TypeFile  Type = "file"
)

// ErrInvalid is what is wrong with an Artifact's files.
var ErrInvalid = errors.New("invalid artifact")

// ErrTooLarge is an Artifact over a limit.
var ErrTooLarge = errors.New("artifact too large")

// File is one file of an Artifact, its path relative to the folder, with "/" between segments.
type File struct {
	Path string
	Data []byte
}

// Pack is an Artifact's files as checked: sorted by path, with what it is.
type Pack struct {
	Files []File
	Type  Type
	// Entry is the file shown: index.html of a folder, or the one file.
	Entry string
	// Digest is the SHA-256 of the files' paths and contents: the same files have the same digest.
	Digest string
	Size   int64
}

// pageTypes are the files a page's folder may carry and the type each is served as (OpenMausBot's
// PAGE_FILE_TYPES, without its videos, which it does not store either).
var pageTypes = map[string]string{
	"html": "text/html; charset=utf-8", "htm": "text/html; charset=utf-8", "css": "text/css; charset=utf-8",
	"js": "text/javascript; charset=utf-8", "mjs": "text/javascript; charset=utf-8",
	"json": "application/json; charset=utf-8", "txt": "text/plain; charset=utf-8", "csv": "text/csv; charset=utf-8",
	"tsv": "text/tab-separated-values; charset=utf-8", "ndjson": "application/x-ndjson; charset=utf-8",
	"md": "text/markdown; charset=utf-8", "geojson": "application/geo+json",
	"topojson": "application/json; charset=utf-8", "parquet": "application/vnd.apache.parquet",
	"arrow": "application/vnd.apache.arrow.file", "svg": "image/svg+xml", "png": "image/png", "jpg": "image/jpeg",
	"jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp", "avif": "image/avif", "ico": "image/x-icon",
	"woff": "font/woff", "woff2": "font/woff2", "ttf": "font/ttf", "otf": "font/otf",
}

func ext(name string) string { return strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")) }

// PageFile says whether a folder page may carry a file of this name.
func PageFile(name string) bool {
	_, ok := pageTypes[ext(name)]

	return ok
}

// isHTML says whether a single file is a page: an .html or .htm name, and text.
func isHTML(f File) bool {
	e := ext(f.Path)

	return (e == "html" || e == "htm") && utf8.Valid(f.Data)
}

// imageType is the media type of an image a browser shows, from its bytes; SVG, which is text, also by its name.
func imageType(f File) string {
	if t := http.DetectContentType(f.Data); strings.HasPrefix(t, "image/") {
		return t
	}

	if len(f.Data) >= 12 && string(f.Data[4:12]) == "ftypavif" {
		return "image/avif"
	}

	if ext(f.Path) == "svg" && utf8.Valid(f.Data) && bytes.Contains(f.Data, []byte("<svg")) {
		return "image/svg+xml"
	}

	return ""
}

// isPDF is a PDF by its signature, as the Lark relay tells one (larkbot/media.go).
func isPDF(f File) bool { return bytes.HasPrefix(f.Data, []byte("%PDF-")) }

// MediaType is the type a file of a pack is served as: a page's file by its name, a single file by its bytes.
func MediaType(p Pack, f File) string {
	if p.Type == TypeHTML {
		if t, ok := pageTypes[ext(f.Path)]; ok {
			return t
		}
	}

	switch {
	case isPDF(f):
		return "application/pdf"
	case imageType(f) != "":
		return imageType(f)
	default:
		return "application/octet-stream"
	}
}

// CheckName refuses a segment of a path a pack cannot hold: empty, "." or "..", hidden, a separator, a control
// character, or longer than 100 characters (OpenMausBot's pageName, widened to any letter, since a file the owner
// downloads keeps its own name).
func CheckName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return fmt.Errorf("%w: %q is not a file name", ErrInvalid, name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("%w: %s is hidden", ErrInvalid, name)
	case !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxName:
		return fmt.Errorf("%w: %q is not a name of at most %d characters", ErrInvalid, name, maxName)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%w: %q holds a separator", ErrInvalid, name)
	}

	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %q holds a control character", ErrInvalid, name)
		}
	}

	return nil
}

// CheckPath refuses a path a pack cannot hold: each segment by CheckName, and at most ten levels.
func CheckPath(p string) error {
	parts := strings.Split(p, "/")
	if len(parts) > maxDepth+1 {
		return fmt.Errorf("%w: %s is nested too deep", ErrInvalid, p)
	}

	for _, part := range parts {
		if err := CheckName(part); err != nil {
			return err
		}
	}

	return nil
}

// CheckTitle refuses a title that is empty, not one line, or longer than 80 characters.
func CheckTitle(title string) error {
	switch {
	case strings.TrimSpace(title) == "":
		return fmt.Errorf("%w: an artifact needs a title", ErrInvalid)
	case !utf8.ValidString(title) || utf8.RuneCountInString(title) > MaxTitle:
		return fmt.Errorf("%w: a title is at most %d characters", ErrInvalid, MaxTitle)
	case strings.ContainsAny(title, "\r\n"):
		return fmt.Errorf("%w: a title is one line", ErrInvalid)
	}

	return nil
}

// Check makes a pack of files: one file of any kind, or a folder with index.html at its top whose files a page
// loads. It refuses what is over a limit or named as a pack cannot hold.
func Check(files []File) (Pack, error) {
	if len(files) == 0 {
		return Pack{}, fmt.Errorf("%w: no file", ErrInvalid)
	}

	if len(files) > MaxFiles {
		return Pack{}, fmt.Errorf("%w: %d files; a folder holds at most %d", ErrTooLarge, len(files), MaxFiles)
	}

	files = slices.Clone(files)
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })

	var size int64

	for i, f := range files {
		if err := CheckPath(f.Path); err != nil {
			return Pack{}, err
		}

		if i > 0 && files[i-1].Path == f.Path {
			return Pack{}, fmt.Errorf("%w: %s is given twice", ErrInvalid, f.Path)
		}

		size += int64(len(f.Data))
	}

	if size > MaxTotal {
		return Pack{}, fmt.Errorf("%w: the files come to %d bytes; an artifact is at most %d (5 MB)",
			ErrTooLarge, size, MaxTotal)
	}

	p := Pack{Files: files, Size: size, Digest: digest(files)}

	if len(files) == 1 && !strings.Contains(files[0].Path, "/") {
		f := files[0]

		p.Entry = f.Path

		switch {
		case isHTML(f):
			p.Type = TypeHTML
		case isPDF(f):
			p.Type = TypePDF
		case imageType(f) != "":
			p.Type = TypeImage
		default:
			p.Type = TypeFile
		}
	} else {
		p.Type, p.Entry = TypeHTML, Entry

		i := slices.IndexFunc(files, func(f File) bool { return f.Path == Entry })
		if i < 0 {
			return Pack{}, fmt.Errorf("%w: a folder needs an index.html at its top; name the main page index.html",
				ErrInvalid)
		}

		for _, f := range files {
			if !PageFile(f.Path) {
				return Pack{}, fmt.Errorf("%w: a page's folder cannot carry %s; it carries pages, styles, scripts, "+
					"data, images and fonts", ErrInvalid, f.Path)
			}
		}
	}

	if p.Type == TypeHTML {
		entry := files[slices.IndexFunc(files, func(f File) bool { return f.Path == p.Entry })]
		if len(entry.Data) > MaxHTML {
			return Pack{}, fmt.Errorf("%w: %s is %d bytes; a page is at most %d (256 KB): move its data into files "+
				"beside it", ErrTooLarge, entry.Path, len(entry.Data), MaxHTML)
		}
	}

	return p, nil
}

// digest is the SHA-256 of each file's path and the SHA-256 of its bytes, in order.
func digest(files []File) string {
	h := sha256.New()

	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		_, _ = fmt.Fprintf(h, "%s\x00%s\n", f.Path, hex.EncodeToString(sum[:]))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// Tar writes files as the archive the control plane takes: a tar of regular files, no modes or times of the
// machine's.
func Tar(files []File) ([]byte, error) {
	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: f.Path, Mode: 0o644, Size: int64(len(f.Data)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0),
			Format: tar.FormatPAX,
		}); err != nil {
			return nil, fmt.Errorf("packing %s: %w", f.Path, err)
		}

		if _, err := tw.Write(f.Data); err != nil {
			return nil, fmt.Errorf("packing %s: %w", f.Path, err)
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("packing the files: %w", err)
	}

	return buf.Bytes(), nil
}

// FromTar reads the files of an archive Tar wrote, and checks them. Anything but a regular file, and more files or
// bytes than an Artifact may hold, is refused before it is read whole.
func FromTar(data []byte) (Pack, error) {
	tr := tar.NewReader(bytes.NewReader(data))

	var (
		files []File
		size  int64
	)

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return Pack{}, fmt.Errorf("%w: the archive cannot be read: %w", ErrInvalid, err)
		}

		if h.Typeflag != tar.TypeReg {
			return Pack{}, fmt.Errorf("%w: %s is not a file", ErrInvalid, h.Name)
		}

		if len(files) == MaxFiles {
			return Pack{}, fmt.Errorf("%w: a folder holds at most %d files", ErrTooLarge, MaxFiles)
		}

		size += h.Size
		if h.Size < 0 || size > MaxTotal {
			return Pack{}, fmt.Errorf("%w: an artifact is at most %d bytes (5 MB)", ErrTooLarge, MaxTotal)
		}

		body, err := io.ReadAll(io.LimitReader(tr, h.Size))
		if err != nil {
			return Pack{}, fmt.Errorf("%w: reading %s: %w", ErrInvalid, h.Name, err)
		}

		files = append(files, File{Path: h.Name, Data: body})
	}

	return Check(files)
}
