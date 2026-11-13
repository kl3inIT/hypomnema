package workspacefs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()

	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // its own git
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t")

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func codeOf(err error) Code {
	var known *Error
	if errors.As(err, &known) {
		return known.Code
	}

	return ""
}

func TestPathsStayInsideTheFolder(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/a.txt", "a")

	for _, rel := range []string{"../x", "src/../../x", "/etc/passwd", `src\a.txt`, "a\x00b"} {
		if _, err := Read(root, rel, false); codeOf(err) != CodeInvalidPath {
			t.Errorf("Read(%q) = %v, want invalid_path", rel, err)
		}
	}

	if _, err := Read(root, "src/missing.txt", false); codeOf(err) != CodeNotFound {
		t.Errorf("a missing file: %v", err)
	}

	if _, err := List(filepath.Join(root, "gone"), "", ""); codeOf(err) != CodeGone {
		t.Errorf("a folder that is gone: %v", err)
	}

	if runtime.GOOS == "windows" {
		t.Skip("links need privileges on Windows")
	}

	outside := t.TempDir()
	write(t, outside, "secret", "s")

	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}

	if _, err := Read(root, "out/secret", false); codeOf(err) != CodeInvalidPath {
		t.Errorf("a link out of the folder: %v", err)
	}

	if err := os.Symlink("src/a.txt", filepath.Join(root, "in")); err != nil {
		t.Fatal(err)
	}

	if f, err := Read(root, "in", false); err != nil || f.Content != "a" {
		t.Errorf("a link inside the folder: %v, %v", f, err)
	}
}

func TestListPagesByName(t *testing.T) {
	root := t.TempDir()
	write(t, root, "b.txt", "bb")
	write(t, root, "a/x.txt", "x")
	write(t, root, "c.txt", "c")

	got, err := List(root, "", "")
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, e := range got.Entries {
		names = append(names, e.Path+":"+e.Type)
	}

	if strings.Join(names, " ") != "a:directory b.txt:file c.txt:file" || got.HasMore {
		t.Fatalf("listing: %v, has more %v", names, got.HasMore)
	}

	if got.Entries[1].Bytes != 2 {
		t.Errorf("b.txt is 2 bytes, not %d", got.Entries[1].Bytes)
	}

	next, err := List(root, "", "b.txt")
	if err != nil || len(next.Entries) != 1 || next.Entries[0].Name != "c.txt" {
		t.Fatalf("after b.txt: %+v, %v", next, err)
	}

	sub, err := List(root, "a", "")
	if err != nil || len(sub.Entries) != 1 || sub.Entries[0].Path != "a/x.txt" {
		t.Fatalf("a directory's own: %+v, %v", sub, err)
	}
}

func TestReadKnowsBinaryAndCutsAtACharacter(t *testing.T) {
	root := t.TempDir()
	write(t, root, "text.md", "xin chào")
	write(t, root, "image.png", "\x89PNG\x00\x01")
	write(t, root, "latin1.txt", "caf\xe9")

	if f, err := Read(root, "text.md", false); err != nil || f.Binary || f.Content != "xin chào" {
		t.Errorf("text: %+v, %v", f, err)
	}

	for _, name := range []string{"image.png", "latin1.txt"} {
		if f, err := Read(root, name, false); err != nil || !f.Binary || f.Content != "" {
			t.Errorf("%s is binary: %+v, %v", name, f, err)
		}
	}

	if _, err := Read(root, "", false); codeOf(err) != CodeInvalidPath {
		t.Errorf("the folder is not a file: %v", err)
	}

	// "à" is two bytes: cut after its first, the end is dropped.
	if got := string(cut([]byte("xà")[:2])); got != "x" {
		t.Errorf("cut = %q", got)
	}

	if binary([]byte("xà")[:2], true) {
		t.Error("a character cut by the end is not binary")
	}
}

func TestStatusSaysWhatChangedSinceHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}

	ctx := context.Background()
	top := t.TempDir()
	run(t, top, "init", "-q", "-b", "main")
	write(t, top, "app/keep.go", "one\ntwo\n")
	write(t, top, "app/old.go", "old\n")
	write(t, top, "app/moved.go", "moved\n")
	write(t, top, "other/x.go", "x\n")
	run(t, top, "add", ".")
	run(t, top, "commit", "-q", "-m", "first")

	// The folder is a directory inside the repository, as a worktree may be its top.
	root := filepath.Join(top, "app")
	write(t, root, "keep.go", "one\nTWO\nthree\n")
	write(t, root, "new.go", "new\n")
	write(t, root, "node_modules/lib/index.js", "x")
	write(t, root, "scratch.swp", "x")
	write(t, top, "other/x.go", "changed\n")
	run(t, top, "rm", "-q", "app/old.go")
	run(t, top, "mv", "app/moved.go", "app/renamed.go")

	got, err := Status(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	by := map[string]Change{}
	for _, c := range got.Changes {
		by[c.Path] = c
	}

	want := map[string]string{"keep.go": "modified", "new.go": "created", "old.go": "deleted", "renamed.go": "modified"}
	if !got.Git || len(by) != len(want) {
		t.Fatalf("changes: %+v", got.Changes)
	}

	for p, status := range want {
		if by[p].Status != status {
			t.Errorf("%s is %q, want %q", p, by[p].Status, status)
		}
	}

	if c := by["keep.go"]; c.LinesAdded == nil || *c.LinesAdded != 2 || *c.LinesRemoved != 1 {
		t.Errorf("keep.go's lines: %+v", c)
	}

	if by["new.go"].LinesAdded != nil {
		t.Error("a file git does not track has no line counts")
	}

	if none, err := Status(ctx, t.TempDir()); err != nil || none.Git || len(none.Changes) != 0 {
		t.Errorf("a folder in no repository: %+v, %v", none, err)
	}
}

func TestDiffIsAFileAtHeadAndNow(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}

	ctx := context.Background()
	root := t.TempDir()
	run(t, root, "init", "-q", "-b", "main")
	write(t, root, "a.go", "before\n")
	write(t, root, "gone.go", "bye\n")
	run(t, root, "add", ".")
	run(t, root, "commit", "-q", "-m", "first")
	write(t, root, "a.go", "after\n")
	write(t, root, "b.go", "new\n")
	run(t, root, "rm", "-q", "gone.go")

	d, err := DiffOf(ctx, root, "a.go")
	if err != nil || *d.Before != "before\n" || *d.After != "after\n" {
		t.Fatalf("modified: %+v, %v", d, err)
	}

	if d, err := DiffOf(ctx, root, "b.go"); err != nil || d.Before != nil || *d.After != "new\n" {
		t.Errorf("created: %+v, %v", d, err)
	}

	if d, err := DiffOf(ctx, root, "gone.go"); err != nil || *d.Before != "bye\n" || d.After != nil {
		t.Errorf("deleted: %+v, %v", d, err)
	}

	if _, err := DiffOf(ctx, root, "../a.go"); codeOf(err) != CodeInvalidPath {
		t.Errorf("out of the folder: %v", err)
	}

	if _, err := DiffOf(ctx, t.TempDir(), "a.go"); codeOf(err) != CodeNotGit {
		t.Errorf("no repository: %v", err)
	}
}

func TestServeAnswersWithACode(t *testing.T) {
	res := Serve(context.Background(), t.TempDir(), Request{Op: OpRead, Path: "../x"})
	if res.Error == nil || res.Error.Code != CodeInvalidPath || res.File != nil {
		t.Fatalf("%+v", res)
	}

	if res := Serve(context.Background(), t.TempDir(), Request{Op: "write"}); res.Error == nil {
		t.Fatal("an unknown operation is refused")
	}
}
