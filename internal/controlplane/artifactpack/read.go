package artifactpack

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Skipped is a file or folder (its path ends in "/") a folder page leaves out, and why.
type Skipped struct {
	Path string
	Why  string
}

// skippedFolders are never read: packages and caches (OpenMausBot's SKIPPED_FOLDERS).
var skippedFolders = []string{"node_modules", "__pycache__"}

// Read reads what `hypomnema artifact show --path` names, relative to dir (the Bot's directory): one file, or a
// folder with index.html at its top. It must be inside dir. A folder's hidden files, package folders, files a page
// does not load, badly named ones and links that lead out of the folder are left out and named, as OpenMausBot's
// readPageBundle does; over a limit it stops with an error rather than read on.
func Read(dir, name string) ([]File, []Skipped, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading your directory: %w", err)
	}

	target := name
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}

	resolved, err := filepath.EvalSymlinks(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("%w: nothing is at %s; save it in your directory and give its path", ErrInvalid,
			name)
	}

	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", name, err)
	}

	if !inside(root, resolved) {
		return nil, nil, fmt.Errorf("%w: only a file or folder in your directory (%s) can be shown", ErrInvalid, root)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", name, err)
	}

	if !info.IsDir() {
		data, err := readFile(resolved, MaxTotal)
		if err != nil {
			return nil, nil, err
		}

		return []File{{Path: filepath.Base(resolved), Data: data}}, nil, nil
	}

	r := folderReader{top: resolved}
	if err := r.walk(resolved, "", 0); err != nil {
		return nil, nil, err
	}

	if !slices.ContainsFunc(r.files, func(f File) bool { return f.Path == Entry }) {
		return nil, nil, fmt.Errorf("%w: %s has no index.html at its top; name the main page index.html", ErrInvalid,
			name)
	}

	return r.files, r.skipped, nil
}

type folderReader struct {
	top     string
	files   []File
	skipped []Skipped
	size    int64
}

func (r *folderReader) walk(dir, rel string, depth int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}

	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		p := strings.TrimPrefix(rel+"/"+name, "/")

		if strings.HasPrefix(name, ".") {
			r.skip(p, e.IsDir(), "hidden")

			continue
		}

		// Every entry is resolved, not only links: one that leads out of the folder is not followed.
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil || !inside(r.top, resolved) {
			r.skip(p, false, "a link out of the folder")

			continue
		}

		info, err := os.Stat(resolved)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, err)
		}

		if info.IsDir() {
			switch {
			case slices.Contains(skippedFolders, name):
				r.skip(p, true, "packages")
			case depth >= maxDepth:
				r.skip(p, true, "nested too deep")
			case CheckName(name) != nil:
				r.skip(p, true, "a name a page cannot hold")
			default:
				if err := r.walk(resolved, p, depth+1); err != nil {
					return err
				}
			}

			continue
		}

		if !info.Mode().IsRegular() {
			continue
		}

		if !PageFile(name) {
			r.skip(p, false, "not a kind of file a page loads")

			continue
		}

		if CheckName(name) != nil {
			r.skip(p, false, "a name a page cannot hold")

			continue
		}

		if len(r.files) == MaxFiles {
			return fmt.Errorf("%w: the folder has more than %d files; show a smaller folder", ErrTooLarge, MaxFiles)
		}

		r.size += info.Size()
		if r.size > MaxTotal {
			return fmt.Errorf("%w: the folder's files come to more than %d bytes (5 MB); make the images smaller "+
				"or leave some out", ErrTooLarge, MaxTotal)
		}

		data, err := readFile(resolved, MaxTotal)
		if err != nil {
			return err
		}

		r.files = append(r.files, File{Path: p, Data: data})
	}

	return nil
}

func (r *folderReader) skip(p string, dir bool, why string) {
	if dir {
		p += "/"
	}

	r.skipped = append(r.skipped, Skipped{Path: p, Why: why})
}

// readFile reads at most limit bytes of a file and refuses a larger one.
func readFile(name string, limit int64) ([]byte, error) {
	f, err := os.Open(name) //nolint:gosec // the Bot names a file of its own directory, checked above
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", name, err)
	}
	defer f.Close() //nolint:errcheck // read only

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s is over %d bytes (5 MB)", ErrTooLarge, filepath.Base(name), limit)
	}

	return data, nil
}

// inside says whether path is root or under it.
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

// SkippedNote says in one sentence what a folder left out, as OpenMausBot's skippedNote does; "" when nothing was.
func SkippedNote(skipped []Skipped) string {
	if len(skipped) == 0 {
		return ""
	}

	const shown = 5

	parts := make([]string, 0, shown+1)
	for i, s := range skipped {
		if i == shown {
			parts = append(parts, fmt.Sprintf("%d more", len(skipped)-shown))

			break
		}

		parts = append(parts, fmt.Sprintf("%s (%s)", s.Path, s.Why))
	}

	return "Left out " + strings.Join(parts, ", ") + "."
}
