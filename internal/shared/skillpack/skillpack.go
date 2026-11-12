// Package skillpack reads a skill in the Agent Skills format (agentskills.io):
// a folder with a SKILL.md whose frontmatter names it and says what it is
// for, and the scripts and references beside it (issue #21).
//
// A skill is kept whole and known by a digest of its files, as Omnara's
// skill revisions (archive_digest, migrations/000009_skills.sql) and
// OpenClaw's library revisions, whose hash covers each file's path, content
// and executable flag and nothing else (docs/tools/skills.md, "Revisions and
// session selection"): the same folder zipped again, or in another order,
// has the same digest. It is in shared because the node unpacks and checks
// what the control plane keeps.
package skillpack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Limits. An archive is at most Omnara's upload limit (25 MB, docs/tools/
// skills.mdx); what it unpacks to and its number of files are bounded too, so
// a small archive cannot fill memory.
const (
	MaxArchive  = 25 << 20
	MaxUnpacked = 64 << 20
	MaxFiles    = 2000
)

// Limits of the frontmatter, the standard's as pi checks them
// (packages/coding-agent/src/core/skills.ts, MAX_NAME_LENGTH and
// MAX_DESCRIPTION_LENGTH).
const (
	MaxName        = 64
	MaxDescription = 1024
)

// Manifest is the file every skill has at its root.
const Manifest = "SKILL.md"

// ErrInvalid is what is wrong with an archive or a skill in it.
var ErrInvalid = errors.New("invalid skill")

// namePattern is the standard's: lowercase letters, digits and single
// hyphens, not at either end.
var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// File is one file of a skill, its path relative to the skill's folder.
type File struct {
	Path string
	Exec bool
	Data []byte
}

// Skill is a skill read from an archive, its files sorted by path.
type Skill struct {
	Name        string
	Description string
	Files       []File
	// Digest is the SHA-256 of the files, in hex.
	Digest string
}

// Size is what the skill's files hold, in bytes.
func (s Skill) Size() int64 {
	var n int64
	for _, f := range s.Files {
		n += int64(len(f.Data))
	}

	return n
}

// FromArchive reads a skill from a zip or a gzipped tar. Its SKILL.md is at
// the archive's root, or in the one folder everything is in, as a folder
// zipped whole is.
func FromArchive(data []byte) (Skill, error) {
	if len(data) > MaxArchive {
		return Skill{}, fmt.Errorf("%w: the archive is over %d MB", ErrInvalid, MaxArchive>>20)
	}

	var (
		files []File
		err   error
	)

	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		files, err = readZip(data)
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		files, err = readTarGz(bytes.NewReader(data), func(p string) (string, bool) { return p, true })
	default:
		return Skill{}, fmt.Errorf("%w: the archive is neither a zip nor a gzipped tar", ErrInvalid)
	}

	if err != nil {
		return Skill{}, err
	}

	return build(rootOf(files))
}

// FromTarball reads the skill in folder dir of a repository's gzipped tar,
// as GitHub serves one: every path is under one top folder named after the
// commit, which is dropped. dir empty is the repository's root.
func FromTarball(r io.Reader, dir string) (Skill, error) {
	dir = strings.Trim(dir, "/")
	if dir != "" {
		clean, err := cleanPath(dir)
		if err != nil {
			return Skill{}, err
		}

		dir = clean + "/"
	}

	files, err := readTarGz(r, func(p string) (string, bool) {
		_, rest, ok := strings.Cut(p, "/")
		if !ok || !strings.HasPrefix(rest, dir) {
			return "", false
		}

		return strings.TrimPrefix(rest, dir), true
	})
	if err != nil {
		return Skill{}, err
	}

	if len(files) == 0 {
		return Skill{}, fmt.Errorf("%w: the repository has no folder %q", ErrInvalid, strings.TrimSuffix(dir, "/"))
	}

	return build(files)
}

// Archive is the skill as a gzipped tar that does not depend on how it was
// given: files in order of path, no times, no owners, modes 0644 or 0755.
func (s Skill) Archive() ([]byte, error) {
	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, f := range s.Files {
		mode := int64(0o644)
		if f.Exec {
			mode = 0o755
		}

		h := &tar.Header{
			Typeflag: tar.TypeReg, Name: f.Path, Mode: mode, Size: int64(len(f.Data)),
			ModTime: time.Unix(0, 0), Format: tar.FormatPAX,
		}

		if err := tw.WriteHeader(h); err != nil {
			return nil, fmt.Errorf("writing %s: %w", f.Path, err)
		}

		if _, err := tw.Write(f.Data); err != nil {
			return nil, fmt.Errorf("writing %s: %w", f.Path, err)
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing the archive: %w", err)
	}

	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("closing the archive: %w", err)
	}

	return buf.Bytes(), nil
}

// digest hashes each file's path, executable flag, length and content, in
// order of path.
func digest(files []File) string {
	h := sha256.New()

	var n [8]byte

	for _, f := range files {
		h.Write([]byte(f.Path))
		h.Write([]byte{0})

		if f.Exec {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}

		binary.BigEndian.PutUint64(n[:], uint64(len(f.Data)))
		h.Write(n[:])
		h.Write(f.Data)
	}

	return hex.EncodeToString(h.Sum(nil))
}

func build(files []File) (Skill, error) {
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })

	for i := 1; i < len(files); i++ {
		if files[i].Path == files[i-1].Path {
			return Skill{}, fmt.Errorf("%w: %s is in the archive twice", ErrInvalid, files[i].Path)
		}
	}

	i, found := slices.BinarySearchFunc(files, Manifest, func(f File, p string) int { return strings.Compare(f.Path, p) })
	if !found {
		return Skill{}, fmt.Errorf("%w: no %s at the skill's root", ErrInvalid, Manifest)
	}

	name, description, err := frontmatter(files[i].Data)
	if err != nil {
		return Skill{}, err
	}

	return Skill{Name: name, Description: description, Files: files, Digest: digest(files)}, nil
}

// frontmatter reads the name and description of a SKILL.md.
func frontmatter(manifest []byte) (name, description string, err error) {
	text := strings.ReplaceAll(string(manifest), "\r\n", "\n")

	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return "", "", fmt.Errorf("%w: %s does not start with a frontmatter (---)", ErrInvalid, Manifest)
	}

	head, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return "", "", fmt.Errorf("%w: the frontmatter of %s does not end (---)", ErrInvalid, Manifest)
	}

	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}

	if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
		return "", "", fmt.Errorf("%w: the frontmatter of %s: %w", ErrInvalid, Manifest, err)
	}

	name, description = strings.TrimSpace(fm.Name), strings.TrimSpace(fm.Description)

	switch {
	case name == "":
		return "", "", fmt.Errorf("%w: the frontmatter of %s has no name", ErrInvalid, Manifest)
	case len(name) > MaxName || !namePattern.MatchString(name):
		return "", "", fmt.Errorf("%w: the name %q is not lowercase letters, digits and single hyphens, "+
			"at most %d", ErrInvalid, name, MaxName)
	case description == "":
		return "", "", fmt.Errorf("%w: the frontmatter of %s has no description", ErrInvalid, Manifest)
	case utf8.RuneCountInString(description) > MaxDescription:
		return "", "", fmt.Errorf("%w: the description is over %d characters", ErrInvalid, MaxDescription)
	}

	return name, description, nil
}

// rootOf drops the one folder every file is in when the manifest is not at
// the root.
func rootOf(files []File) []File {
	top := ""

	for _, f := range files {
		if f.Path == Manifest {
			return files
		}

		dir, _, ok := strings.Cut(f.Path, "/")
		if !ok || (top != "" && dir != top) {
			return files
		}

		top = dir
	}

	out := make([]File, len(files))
	for i, f := range files {
		out[i] = File{Path: strings.TrimPrefix(f.Path, top+"/"), Exec: f.Exec, Data: f.Data}
	}

	return out
}

// cleanPath refuses a path that is absolute, climbs out or is not plain.
func cleanPath(p string) (string, error) {
	if p == "" || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: the path %q is not a plain relative path", ErrInvalid, p)
	}

	clean := path.Clean(p)
	if clean != strings.TrimSuffix(p, "/") || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: the path %q is not a plain relative path", ErrInvalid, p)
	}

	return clean, nil
}

// macJunk is what macOS's Finder adds to a zip beside the folder.
func macJunk(p string) bool {
	return strings.HasPrefix(p, "__MACOSX/") || path.Base(p) == ".DS_Store"
}

// counter keeps an archive's files within the limits.
type counter struct {
	files int
	bytes int64
}

func (c *counter) add(size int64) error {
	c.files++
	c.bytes += size

	switch {
	case c.files > MaxFiles:
		return fmt.Errorf("%w: more than %d files", ErrInvalid, MaxFiles)
	case size < 0 || c.bytes > MaxUnpacked:
		return fmt.Errorf("%w: the files are over %d MB", ErrInvalid, MaxUnpacked>>20)
	}

	return nil
}

// readAll reads what a header says a file holds, and no more.
func readAll(r io.Reader, size int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) != size {
		return nil, fmt.Errorf("%w: a file is not the size its header says", ErrInvalid)
	}

	return data, nil
}

func readZip(data []byte) ([]File, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the zip: %w", ErrInvalid, err)
	}

	var (
		out []File
		c   counter
	)

	for _, zf := range zr.File {
		mode := zf.Mode()

		switch {
		case mode.IsDir() || macJunk(zf.Name):
			continue
		case !mode.IsRegular():
			return nil, fmt.Errorf("%w: %s is not a plain file (links are refused)", ErrInvalid, zf.Name)
		}

		p, err := cleanPath(zf.Name)
		if err != nil {
			return nil, err
		}

		if err := c.add(int64(zf.UncompressedSize64)); err != nil { //nolint:gosec // checked against the limit
			return nil, err
		}

		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: opening %s: %w", ErrInvalid, p, err)
		}

		b, err := readAll(rc, int64(zf.UncompressedSize64)) //nolint:gosec // checked against the limit
		_ = rc.Close()

		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", p, err)
		}

		out = append(out, File{Path: p, Exec: mode.Perm()&0o111 != 0, Data: b})
	}

	return out, nil
}

// readTarGz reads the regular files that keep names, under the name keep
// gives them. A link or a device anywhere it keeps is refused.
func readTarGz(r io.Reader, keep func(string) (string, bool)) ([]File, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: reading the gzip: %w", ErrInvalid, err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)

	var (
		out []File
		c   counter
	)

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}

		if err != nil {
			return nil, fmt.Errorf("%w: reading the tar: %w", ErrInvalid, err)
		}

		name, ok := keep(strings.TrimPrefix(h.Name, "./"))
		if !ok || name == "" || macJunk(name) {
			continue
		}

		switch h.Typeflag {
		case tar.TypeDir, tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("%w: %s is not a plain file (links are refused)", ErrInvalid, name)
		}

		p, err := cleanPath(name)
		if err != nil {
			return nil, err
		}

		if err := c.add(h.Size); err != nil {
			return nil, err
		}

		b, err := readAll(tr, h.Size)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", p, err)
		}

		out = append(out, File{Path: p, Exec: h.Mode&0o111 != 0, Data: b})
	}
}
