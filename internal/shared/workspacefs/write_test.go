package workspacefs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func read(t *testing.T, root, rel string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // the test's folder
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// Scenario: the owner saves a file the agent has not touched since it was opened, then a second time from the old
// text after the agent changed it. The first lands; the second is refused as changed and leaves the agent's text.
func TestAWriteLandsOnlyOverTheTextItWasReadAs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/app.ts", "one\n")

	opened, err := Read(root, "src/app.ts", false)
	if err != nil {
		t.Fatal(err)
	}

	done, err := Write(root, "src/app.ts", "two\n", opened.SHA256)
	if err != nil {
		t.Fatalf("Write = %v", err)
	}

	if got := read(t, root, "src/app.ts"); got != "two\n" {
		t.Fatalf("the file is %q, want two", got)
	}

	if done.SHA256 != sha([]byte("two\n")) {
		t.Errorf("Done.SHA256 = %s, want the new text's", done.SHA256)
	}

	write(t, root, "src/app.ts", "the agent's\n")

	if _, err := Write(root, "src/app.ts", "three\n", done.SHA256); codeOf(err) != CodeChanged {
		t.Fatalf("Write over a changed file = %v, want changed", err)
	}

	if got := read(t, root, "src/app.ts"); got != "the agent's\n" {
		t.Errorf("the refused write changed the file to %q", got)
	}

	entries, _ := os.ReadDir(filepath.Join(root, "src"))
	if len(entries) != 1 {
		t.Errorf("the folder holds %d names, want the file alone (no temporary file left)", len(entries))
	}
}

// Scenario: a new file is made where nothing is, and not over one; a write to a file that was deleted meanwhile is
// refused; a write larger than a node's message is refused.
func TestNewFilesAndFilesThatWent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "a")

	if _, err := Write(root, "b.txt", "b", ""); err != nil {
		t.Fatalf("a new file: %v", err)
	}

	if _, err := Write(root, "a.txt", "x", ""); codeOf(err) != CodeExists {
		t.Errorf("a new file over one = %v, want exists", err)
	}

	if _, err := Write(root, "gone.txt", "x", sha([]byte("x"))); codeOf(err) != CodeNotFound {
		t.Errorf("a write to a deleted file = %v, want not_found", err)
	}

	big := make([]byte, MaxWrite+1)
	if _, err := Write(root, "big.txt", string(big), ""); codeOf(err) != CodeTooLarge {
		t.Errorf("a write over MaxWrite = %v, want too_large", err)
	}
}

// Scenario: folders are made, a file renamed into one, and a folder deleted with what it holds. Nothing is made over
// what is there, nothing moved into itself, the folder itself never changed, and no path leaves the folder.
func TestFoldersRenamesAndDeletes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "a")

	if _, err := Mkdir(root, "docs"); err != nil {
		t.Fatal(err)
	}

	if _, err := Mkdir(root, "docs"); codeOf(err) != CodeExists {
		t.Errorf("Mkdir over a folder = %v, want exists", err)
	}

	if done, err := Rename(root, "a.txt", "docs/a.md"); err != nil || done.Path != "docs/a.md" {
		t.Fatalf("Rename = %v, %v", done, err)
	}

	if got := read(t, root, "docs/a.md"); got != "a" {
		t.Errorf("the moved file holds %q", got)
	}

	if _, err := Rename(root, "docs", "docs/inner"); codeOf(err) != CodeInvalidPath {
		t.Errorf("a folder moved into itself = %v, want invalid_path", err)
	}

	write(t, root, "b.txt", "b")

	if _, err := Rename(root, "b.txt", "docs/a.md"); codeOf(err) != CodeExists {
		t.Errorf("a rename over a file = %v, want exists", err)
	}

	if _, err := Delete(root, "docs"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "docs")); !os.IsNotExist(err) {
		t.Errorf("the folder is still there: %v", err)
	}

	for _, rel := range []string{"", ".", "../x", "/etc/passwd", "a/../../x"} {
		if _, err := Delete(root, rel); codeOf(err) != CodeInvalidPath {
			t.Errorf("Delete(%q) = %v, want invalid_path", rel, err)
		}

		if _, err := Write(root, rel, "x", ""); codeOf(err) != CodeInvalidPath {
			t.Errorf("Write(%q) = %v, want invalid_path", rel, err)
		}
	}
}

// Scenario: a link inside the folder points out of it. A write through it is refused and the file it points to is
// left; deleting it deletes the link, not what it points to.
func TestALinkIsNeverWrittenThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need privileges on Windows")
	}

	root, outside := t.TempDir(), t.TempDir()
	write(t, outside, "secret", "keep")

	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := Write(root, "link", "x", sha([]byte("keep"))); codeOf(err) != CodeInvalidPath {
		t.Errorf("a write through a link = %v, want invalid_path", err)
	}

	if _, err := Delete(root, "link"); err != nil {
		t.Fatalf("deleting the link: %v", err)
	}

	if got := read(t, outside, "secret"); got != "keep" {
		t.Errorf("the file the link pointed to holds %q", got)
	}
}

// Scenario: an image is read for its bytes, and its SHA-256 with them.
func TestARawReadSendsTheBytes(t *testing.T) {
	root := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00")

	if err := os.WriteFile(filepath.Join(root, "logo.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Read(root, "logo.png", true)
	if err != nil {
		t.Fatal(err)
	}

	if string(f.Data) != string(png) || !f.Binary || f.SHA256 != sha(png) {
		t.Errorf("Read raw = %+v", f)
	}
}
