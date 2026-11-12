// Package workspacefs reads a working folder for the owner (issue #255): a
// directory's entries, a file, what git says changed, and a changed file
// before and after. The node reads a Task's worktree with it, and the Bot's
// helper on the control plane's machine reads a Bot's folder.
//
// It follows Omnigent's runner (omnigent/runner/environment_filesystem.py and
// omnigent/runtime/filesystem_registry.py, Apache License 2.0; read for how it
// behaves, not copied): pages of at most 1000 entries, a file read up to
// 10 MiB and sniffed for binary in its first 8 KiB, git status without
// optional locks, and every path kept inside the folder after its links are
// followed.
package workspacefs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Limits (Omnigent: list_dir's limit, _MAX_READ_BYTES, _BINARY_SNIFF_BYTES,
// the changed files' limit and _DEFAULT_GIT_TIMEOUT_SECONDS).
const (
	PageEntries = 1000
	MaxRead     = 10 << 20
	sniffBytes  = 8 << 10
	MaxChanges  = 10_000
	GitTimeout  = 30 * time.Second
)

// Op is what a request asks for.
type Op string

// The operations.
const (
	OpList    Op = "list"
	OpRead    Op = "read"
	OpChanges Op = "changes"
	OpDiff    Op = "diff"
)

// Request is one question about a folder. Path is relative to the folder,
// with forward slashes; empty is the folder itself.
type Request struct {
	Op    Op     `json:"op"`
	Path  string `json:"path,omitempty"`
	After string `json:"after,omitempty"`
}

// Entry is one name in a directory. A link is listed as what it points to.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Type is "file" or "directory".
	Type       string `json:"type"`
	Bytes      int64  `json:"bytes"`
	ModifiedAt int64  `json:"modified_at"`
}

// Listing is a page of a directory, by name.
type Listing struct {
	Entries []Entry `json:"entries"`
	HasMore bool    `json:"has_more"`
}

// File is a file's text. A binary file has no content.
type File struct {
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

// Change is one file git says changed since HEAD.
type Change struct {
	Path string `json:"path"`
	// Status is "created", "modified" or "deleted".
	Status string `json:"status"`
	// LinesAdded and LinesRemoved are absent when git does not count them: a
	// file git does not track yet, or a binary one.
	LinesAdded   *int  `json:"lines_added,omitempty"`
	LinesRemoved *int  `json:"lines_removed,omitempty"`
	ModifiedAt   int64 `json:"modified_at"`
}

// Changes is what changed in a folder. Git is false for a folder that is
// not in a git repository, which has no changes to show.
type Changes struct {
	Git       bool     `json:"git"`
	Changes   []Change `json:"changes"`
	Truncated bool     `json:"truncated"`
}

// Diff is a changed file at HEAD and now. Before is absent for a created
// file, After for a deleted one; neither is there for a binary file.
type Diff struct {
	Path   string  `json:"path"`
	Before *string `json:"before,omitempty"`
	After  *string `json:"after,omitempty"`
	Binary bool    `json:"binary"`
}

// Result answers a Request: one of its parts, or Error.
type Result struct {
	Error   *Error   `json:"error,omitempty"`
	Listing *Listing `json:"listing,omitempty"`
	File    *File    `json:"file,omitempty"`
	Changes *Changes `json:"changes,omitempty"`
	Diff    *Diff    `json:"diff,omitempty"`
}

// Error is why a request was not answered, with a code the caller maps.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Code names a kind of failure.
type Code string

// The codes.
const (
	// CodeInvalidPath: the path is not one inside the folder.
	CodeInvalidPath Code = "invalid_path"
	// CodeNotFound: nothing is at the path.
	CodeNotFound Code = "not_found"
	// CodeGone: the folder itself is not there any more.
	CodeGone Code = "gone"
	// CodeNotGit: the folder is not in a git repository.
	CodeNotGit Code = "not_git"
	// CodeTooLarge: a file is larger than can be shown.
	CodeTooLarge Code = "too_large"
	// CodeGitFailed: git did not answer, or failed; the message says how.
	CodeGitFailed Code = "git_failed"
	// CodeFailed: anything else.
	CodeFailed Code = "failed"
)

func failure(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Serve answers a request about the folder at root.
func Serve(ctx context.Context, root string, req Request) Result {
	var (
		res Result
		err error
	)

	switch req.Op {
	case OpList:
		res.Listing, err = List(root, req.Path, req.After)
	case OpRead:
		res.File, err = Read(root, req.Path)
	case OpChanges:
		res.Changes, err = Status(ctx, root)
	case OpDiff:
		res.Diff, err = DiffOf(ctx, root, req.Path)
	default:
		err = failure(CodeFailed, "unknown operation %q", req.Op)
	}

	if err != nil {
		var known *Error
		if !errors.As(err, &known) {
			known = failure(CodeFailed, "%s", err.Error())
		}

		return Result{Error: known}
	}

	return res
}
