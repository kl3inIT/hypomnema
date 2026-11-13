// Package workspacefs reads and changes a working folder for the owner (issues #255 and #297): a
// directory's entries, a file, what git says changed, and a changed file
// before and after; a file written only over the text it was read as, a file
// or folder made, renamed or deleted. The node reads a Task's worktree with it, and the Bot's
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
	// MaxWrite is the largest text written: what fits the message that carries it to a node (Orca caps a remote
	// write per request, staged-file-save.ts).
	MaxWrite = 1 << 20
	// MaxSend is the largest file a raw read gives when it asks for more than MaxRead: a file a Bot sends into a
	// Lark chat, which takes at most 30 MB (OpenClaw extensions/feishu/src/media.ts, FEISHU_MAX_FILE_UPLOAD_BYTES).
	MaxSend = 30 << 20
)

// Op is what a request asks for.
type Op string

// The operations.
const (
	OpList    Op = "list"
	OpRead    Op = "read"
	OpChanges Op = "changes"
	OpDiff    Op = "diff"
	// OpWrite replaces a file's text, only when the file is still what Expect's SHA-256 says (Orca's
	// writeIfUnchanged), or makes it when Expect is empty and nothing is there.
	OpWrite Op = "write"
	// OpMkdir makes a folder where nothing is.
	OpMkdir Op = "mkdir"
	// OpRename moves Path to To, where nothing is.
	OpRename Op = "rename"
	// OpDelete deletes a file, or a folder with what it holds.
	OpDelete Op = "delete"
)

// Request is one question about a folder. Path is relative to the folder,
// with forward slashes; empty is the folder itself.
type Request struct {
	Op    Op     `json:"op"`
	Path  string `json:"path,omitempty"`
	After string `json:"after,omitempty"`
	// Raw asks a read for the file's bytes, whatever they are, up to MaxRead (an image, a download).
	Raw bool `json:"raw,omitempty"`
	// Limit is the largest file a raw read gives, MaxRead when it is not set, at most MaxSend.
	Limit int64 `json:"limit,omitempty"`
	// Content is the text a write puts in the file; Expect the SHA-256 of the file it replaces, empty for a new
	// file.
	Content string `json:"content,omitempty"`
	Expect  string `json:"expect,omitempty"`
	// To is where a rename moves the path.
	To string `json:"to,omitempty"`
	// Upload names an upload; Offset is where its piece Data goes.
	Upload string `json:"upload,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Data   []byte `json:"data,omitempty"`
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

// File is a file's text. A binary file has no content. SHA256 is the whole file's, which a write names as the
// text it replaces; Data is its bytes, when they were asked for.
type File struct {
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
	SHA256    string `json:"sha256,omitempty"`
	Data      []byte `json:"data,omitempty"`
}

// Done says what a change left: the path, a written file's SHA-256, and an upload begun.
type Done struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Upload string `json:"upload,omitempty"`
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
	Done    *Done    `json:"done,omitempty"`
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
	// CodeChanged: the file is not the text the write was based on any more.
	CodeChanged Code = "changed"
	// CodeExists: something is already where a file or folder was to be made.
	CodeExists Code = "exists"
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
		res.File, err = readUpTo(root, req.Path, req.Raw, req.Limit)
	case OpChanges:
		res.Changes, err = Status(ctx, root)
	case OpDiff:
		res.Diff, err = DiffOf(ctx, root, req.Path)
	case OpWrite:
		res.Done, err = Write(root, req.Path, req.Content, req.Expect)
	case OpMkdir:
		res.Done, err = Mkdir(root, req.Path)
	case OpRename:
		res.Done, err = Rename(root, req.Path, req.To)
	case OpDelete:
		res.Done, err = Delete(root, req.Path)
	case OpUploadBegin:
		res.Done, err = UploadBegin(root, req.Path)
	case OpUploadChunk:
		res.Done, err = UploadChunk(root, req.Path, req.Upload, req.Offset, req.Data)
	case OpUploadCommit:
		res.Done, err = UploadCommit(root, req.Path, req.Upload, req.Expect)
	case OpUploadAbort:
		res.Done, err = UploadAbort(root, req.Path, req.Upload)
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
