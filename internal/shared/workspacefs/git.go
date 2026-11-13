package workspacefs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// skipped are the directories whose files are never listed as changes
// (Omnigent's _SKIP_DIRS), and ephemeral are the endings of an editor's or a
// tool's scratch files (its ephemeral patterns).
var (
	skipped = []string{
		".git", "__pycache__", ".venv", "venv", "node_modules", ".tox", ".mypy_cache", ".pytest_cache",
		".ruff_cache", ".eggs",
	}
	ephemeral = []string{".tmp", "~", ".swp", ".swo", ".swx"}
)

// git runs git in a folder, without its optional locks and prompts, so that
// reading never waits on or blocks the agent working there.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{ //nolint:gosec // git with its own arguments, no shell
		"-C", dir, "--no-optional-locks", "-c", "core.quotepath=off", "-c", "diff.autoRefreshIndex=false",
	}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
	}

	return out, err
}

// repo is the folder's place in its git repository: its real path and its
// path from the repository's top ("" at the top, else ending in /). ok is
// false when the folder is in no repository.
func repo(ctx context.Context, root string) (base, prefix string, ok bool, err error) {
	base, err = filepath.EvalSymlinks(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", false, failure(CodeGone, "the folder %s is not there any more", root)
		}

		return "", "", false, failure(CodeFailed, "reading the folder: %s", err)
	}

	out, err := git(ctx, base, "rev-parse", "--show-prefix")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return base, "", false, nil
		}

		return "", "", false, gitFailed(ctx, err)
	}

	return base, strings.TrimSpace(string(out)), true, nil
}

func gitFailed(ctx context.Context, err error) *Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return failure(CodeGitFailed, "git did not answer within %s", GitTimeout)
	}

	return failure(CodeGitFailed, "git failed: %s", err)
}

// Status is what git says changed in the folder since HEAD, newest first: a
// file git does not track yet or that was added is created, a deleted one
// deleted, any other modified, a renamed one too, under its new path only
// (Omnigent's porcelain mapping).
func Status(ctx context.Context, root string) (*Changes, error) {
	ctx, cancel := context.WithTimeout(ctx, GitTimeout)
	defer cancel()

	base, prefix, ok, err := repo(ctx, root)
	if err != nil {
		return nil, err
	}

	if !ok {
		return &Changes{Changes: []Change{}}, nil
	}

	spec := []string{"--", "."}
	for _, dir := range skipped {
		spec = append(spec, ":(exclude,glob)**/"+dir+"/**")
	}

	out, err := git(ctx, base, append([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all"}, spec...)...)
	if err != nil {
		return nil, gitFailed(ctx, err)
	}

	changes := parseStatus(out, prefix)

	// Line counts are a help: without them, as in a repository with no commit yet, the changes still show.
	if counts, err := git(ctx, base, "diff", "--numstat", "-z", "--no-renames", "HEAD", "--", "."); err == nil {
		lines := parseNumstat(counts, prefix)
		for i := range changes {
			if n, ok := lines[changes[i].Path]; ok {
				changes[i].LinesAdded, changes[i].LinesRemoved = n[0], n[1]
			}
		}
	}

	for i := range changes {
		if info, err := os.Stat(filepath.Join(base, filepath.FromSlash(changes[i].Path))); err == nil {
			changes[i].ModifiedAt = info.ModTime().Unix()
		}
	}

	slices.SortStableFunc(changes, func(a, b Change) int {
		if a.ModifiedAt != b.ModifiedAt {
			return int(b.ModifiedAt - a.ModifiedAt)
		}

		return strings.Compare(a.Path, b.Path)
	})

	result := &Changes{Git: true, Changes: changes}
	if len(changes) > MaxChanges {
		result.Changes, result.Truncated = changes[:MaxChanges], true
	}

	return result, nil
}

// parseStatus reads `git status --porcelain=v1 -z`, whose paths are from the
// repository's top, into changes with paths from the folder.
func parseStatus(out []byte, prefix string) []Change {
	changes := []Change{}
	fields := strings.Split(string(out), "\x00")

	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if len(field) < 4 {
			continue
		}

		xy, name := field[:2], field[3:]
		// A rename or a copy is followed by its old path, which is not shown.
		if xy[0] == 'R' || xy[0] == 'C' {
			i++
		}

		rel, ok := strings.CutPrefix(name, prefix)
		if !ok || rel == "" || slices.ContainsFunc(ephemeral, func(e string) bool { return strings.HasSuffix(rel, e) }) {
			continue
		}

		status := "modified"

		switch {
		case xy == "??", xy[0] == 'A', xy[1] == 'A':
			status = "created"
		case xy[0] == 'D', xy[1] == 'D':
			status = "deleted"
		}

		changes = append(changes, Change{Path: rel, Status: status})
	}

	return changes
}

// parseNumstat reads `git diff --numstat -z`: lines added and removed by path
// from the folder; a binary file, which git counts as "-", has none.
func parseNumstat(out []byte, prefix string) map[string][2]*int {
	lines := map[string][2]*int{}

	for _, field := range strings.Split(string(out), "\x00") {
		parts := strings.SplitN(field, "\t", 3)
		if len(parts) != 3 {
			continue
		}

		added, errA := strconv.Atoi(parts[0])
		removed, errR := strconv.Atoi(parts[1])

		rel, ok := strings.CutPrefix(parts[2], prefix)
		if !ok || errA != nil || errR != nil {
			continue
		}

		lines[rel] = [2]*int{&added, &removed}
	}

	return lines
}

// DiffOf is a file at HEAD and now, for the browser to draw (Omnigent's diff
// route: before from `git show HEAD:<path>`, after the file read now).
func DiffOf(ctx context.Context, root, rel string) (*Diff, error) {
	ctx, cancel := context.WithTimeout(ctx, GitTimeout)
	defer cancel()

	if rel == "" {
		return nil, failure(CodeInvalidPath, "a diff is of a file")
	}

	base, prefix, ok, err := repo(ctx, root)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, failure(CodeNotGit, "this folder is not in a git repository")
	}

	out := &Diff{Path: path.Clean(rel)}

	now, err := Read(root, rel, false)

	var known *Error

	switch {
	case err == nil && now.Truncated:
		return nil, failure(CodeTooLarge, "%s is larger than %d MiB", now.Path, MaxRead>>20)
	case err == nil && now.Binary:
		out.Binary = true
	case err == nil:
		out.After = &now.Content
	case errors.As(err, &known) && known.Code == CodeNotFound:
	default:
		return nil, err
	}

	then, err := git(ctx, base, "show", "HEAD:"+prefix+out.Path)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, gitFailed(ctx, err)
	case err != nil:
		// Not at HEAD: the file is new.
	case len(then) > MaxRead:
		return nil, failure(CodeTooLarge, "%s at HEAD is larger than %d MiB", out.Path, MaxRead>>20)
	case binary(then, false):
		out.Binary = true
	default:
		text := string(then)
		out.Before = &text
	}

	if out.Binary {
		out.Before, out.After = nil, nil
	}

	if out.Before == nil && out.After == nil && !out.Binary {
		return nil, failure(CodeNotFound, "%s is neither at HEAD nor in the folder", out.Path)
	}

	return out, nil
}
