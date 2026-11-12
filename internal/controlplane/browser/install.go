package browser

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// A program is installed under the engine's directory, with no root, as a
// node installs a tool (internal/shared/agenttool/install.go):
//
//	<dir>/tools/<name>/<version>/...
//
// A version is never replaced in place: a new pin is a new directory. A file
// that does not match its size or checksum is deleted and nothing is
// installed.

const (
	downloadTimeout = 15 * time.Minute
	// maxUnpackedBytes bounds what a release file unpacks to; the headless
	// shell is about 300 MB.
	maxUnpackedBytes = 2 << 30
)

// installer fetches the pinned programs.
type installer struct {
	dir    string
	client *http.Client
}

// errNoRelease reports a system a pinned program has no release file for.
var errNoRelease = errors.New("no release for this system")

// install has the program on this machine and returns its command's path.
func (i installer) install(ctx context.Context, p program) (string, error) {
	system := runtime.GOOS + "/" + runtime.GOARCH

	rel, ok := p.Releases[system]
	if !ok {
		return "", fmt.Errorf("%s %s on %s: %w", p.Name, p.Version, system, errNoRelease)
	}

	home := filepath.Join(i.dir, "tools", p.Name, p.Version)

	command := filepath.Join(home, p.Name)
	if rel.Binary != "" {
		command = filepath.Join(home, filepath.FromSlash(rel.Binary))
	}

	if info, err := os.Stat(command); err == nil && info.Mode().IsRegular() {
		return command, nil
	}

	if err := os.MkdirAll(filepath.Dir(home), 0o700); err != nil {
		return "", fmt.Errorf("making the directory of %s: %w", p.Name, err)
	}

	file, err := i.download(ctx, p, rel, filepath.Dir(home))
	if err != nil {
		return "", err
	}
	defer os.Remove(file) //nolint:errcheck // A leftover download is replaced by the next try.

	// Unpacked beside its place and renamed into it, so a crash never leaves half a program there.
	tmp := home + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return "", fmt.Errorf("clearing an earlier try at %s: %w", p.Name, err)
	}

	if err := place(file, rel, p.Name, tmp); err != nil {
		_ = os.RemoveAll(tmp)

		return "", fmt.Errorf("unpacking %s %s: %w", p.Name, p.Version, err)
	}

	if err := os.RemoveAll(home); err != nil {
		return "", fmt.Errorf("installing %s %s: %w", p.Name, p.Version, err)
	}

	if err := os.Rename(tmp, home); err != nil {
		_ = os.RemoveAll(tmp)

		return "", fmt.Errorf("installing %s %s: %w", p.Name, p.Version, err)
	}

	return command, nil
}

// download fetches the release file into dir and returns its path once its
// size and checksum matched.
func (i installer) download(ctx context.Context, p program, rel release, dir string) (string, error) {
	client := i.client
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return "", fmt.Errorf("%s: building the request: %w", p.Name, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s %s: %w", p.Name, p.Version, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The body was read or the read failed; closing adds nothing.

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s %s: status %d", p.Name, p.Version, resp.StatusCode)
	}

	file, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", fmt.Errorf("%s: making a file for the download: %w", p.Name, err)
	}

	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, sum), io.LimitReader(resp.Body, rel.Bytes+1))

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	switch got := hex.EncodeToString(sum.Sum(nil)); {
	case err != nil:
		err = fmt.Errorf("fetching %s %s: %w", p.Name, p.Version, err)
	case n != rel.Bytes:
		err = fmt.Errorf("%s %s: the release file is not the pinned %d bytes; nothing was installed",
			p.Name, p.Version, rel.Bytes)
	case got != rel.SHA256:
		err = fmt.Errorf("%s %s: the release file's checksum is %s, not the pinned %s; nothing was installed",
			p.Name, p.Version, got, rel.SHA256)
	}

	if err != nil {
		_ = os.Remove(file.Name())

		return "", err
	}

	return file.Name(), nil
}

// place puts a checked release file under home: the file itself as the
// command, or the tree a .zip holds.
func place(file string, rel release, name, home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}

	if rel.Binary == "" {
		return copyFile(file, filepath.Join(home, name), 0o700)
	}

	return unzip(file, home)
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from) //nolint:gosec // The engine's own download.
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // Read only.

	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) //nolint:gosec // A command, run by its owner.
	if err != nil {
		return err
	}

	_, err = io.Copy(out, in)

	if closeErr := out.Close(); err == nil {
		err = closeErr
	}

	return err
}

// unzip writes every file of the archive under home. A member that would land
// outside home, or is not a plain file or a directory, stops it.
func unzip(archive, home string) error {
	members, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("the release file is not a .zip: %w", err)
	}
	defer members.Close() //nolint:errcheck // Read only.

	var written int64

	for _, member := range members.File {
		if !filepath.IsLocal(filepath.FromSlash(member.Name)) {
			return fmt.Errorf("%q is not a path inside the release file", member.Name)
		}

		target := filepath.Join(home, filepath.FromSlash(member.Name))
		mode := member.Mode()

		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}

			continue
		case !mode.IsRegular():
			return fmt.Errorf("%q is neither a file nor a directory", member.Name)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}

		n, err := unzipMember(member, target, maxUnpackedBytes-written)
		if err != nil {
			return fmt.Errorf("%s: %w", member.Name, err)
		}

		written += n
	}

	return nil
}

func unzipMember(member *zip.File, target string, room int64) (int64, error) {
	in, err := member.Open()
	if err != nil {
		return 0, err
	}
	defer in.Close() //nolint:errcheck // Read only.

	// Only its owner reads or runs what the engine installs.
	mode := os.FileMode(0o600)
	if member.Mode()&0o100 != 0 {
		mode = 0o700
	}

	//nolint:gosec // Inside the engine's directory, checked by the caller.
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}

	n, err := io.Copy(out, io.LimitReader(in, room+1))

	if closeErr := out.Close(); err == nil {
		err = closeErr
	}

	if err == nil && n > room {
		err = fmt.Errorf("the release file unpacks to over %d MB", maxUnpackedBytes>>20)
	}

	return n, err
}

// sandboxHint turns Chrome's own failure into what to do about it, as T3
// Code's server does (apps/server/src/preview/PreviewBrowserHost.ts): Ubuntu
// 23.10 and later refuse the sandbox Chrome runs in until an AppArmor profile
// allows it.
func sandboxHint(output string) string {
	if !strings.Contains(output, "No usable sandbox") {
		return ""
	}

	return "this machine blocks the sandbox Chrome runs in (AppArmor on Ubuntu 23.10 and later). " +
		"Install the AppArmor profile of deploy/browser/setup-browser.sh once, with sudo"
}
