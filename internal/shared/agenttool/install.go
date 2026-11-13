package agenttool

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Tools the control plane tells the node to have (issue #103). A tool is one
// release file pinned by its checksum, as the node's own release is
// (deploy/release/release-node.sh: version, url, sha256, after Omnara's
// per-platform manifests). The node installs it under its home, with no root:
//
//	<home>/tools/<name>/<version>/<name>
//
// A worker never installs one, and a version is never replaced in place: a
// new pin is a new directory. A tool that is a directory of files and not one
// command (the AWS CLI, issue #331) comes as a .zip: the directory its command
// is in is unpacked whole as <version>, so that the command is still at
// <home>/tools/<name>/<version>/<name>.

const (
	// maxToolDownloadBytes bounds a release file; gws' is 7 MB.
	maxToolDownloadBytes = 256 << 20
	// maxToolBinaryBytes bounds the command unpacked from it; gws is 18 MB.
	// A directory unpacked from a .zip is bounded by it too, all its files
	// together (the AWS CLI's are 249 MB), and by maxToolFiles (8735).
	maxToolBinaryBytes  = 512 << 20
	maxToolFiles        = 20000
	toolDownloadTimeout = 10 * time.Minute
)

var (
	toolNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	toolVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ToolInstaller installs tools under a node's home.
type ToolInstaller struct {
	Home string
	// Client fetches release files; nil is a client with a download's timeout.
	Client *http.Client
	// allowHTTP lets a test serve a release file without TLS.
	allowHTTP bool
}

// ToolPath is where a tool's command is, installed or not.
func ToolPath(home, name, version string) string {
	return filepath.Join(home, "tools", name, version, name)
}

// Install has the tool on this machine: it does nothing when that version is
// there, and otherwise fetches the release file, checks its checksum, and
// unpacks the command. A file that does not match its checksum is deleted and
// nothing is installed.
func (t ToolInstaller) Install(ctx context.Context, tool nodeproto.Tool) error {
	if err := t.valid(tool); err != nil {
		return err
	}

	target := ToolPath(t.Home, tool.Name, tool.Version)
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
		return nil
	}

	dir := filepath.Dir(target)

	if isZip(tool.URL) {
		return t.installDirectory(ctx, tool, dir)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making the directory of tool %s: %w", tool.Name, err)
	}

	archive, err := t.download(ctx, tool, dir)
	if err != nil {
		return err
	}
	defer os.Remove(archive) //nolint:errcheck // A leftover download is replaced by the next try.

	// Unpacked beside its place and renamed into it, so a crash never leaves half a command there.
	tmp := target + ".tmp"
	if err := unpackTool(archive, tool.Binary, tmp); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("unpacking tool %s %s: %w", tool.Name, tool.Version, err)
	}

	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("installing tool %s %s: %w", tool.Name, tool.Version, err)
	}

	return nil
}

func (t ToolInstaller) valid(tool nodeproto.Tool) error {
	switch {
	case !toolNamePattern.MatchString(tool.Name):
		return fmt.Errorf("tool name %q is not a command's name", tool.Name)
	case !toolVersionPattern.MatchString(tool.Version):
		return fmt.Errorf("tool %s: version %q is not a version", tool.Name, tool.Version)
	case !sha256Pattern.MatchString(tool.SHA256):
		return fmt.Errorf("tool %s: no SHA-256 checksum, so it is not installed", tool.Name)
	case !strings.HasPrefix(tool.URL, "https://") && (!t.allowHTTP || !strings.HasPrefix(tool.URL, "http://")):
		return fmt.Errorf("tool %s: its release file is not fetched over HTTPS", tool.Name)
	case tool.Binary == "" || path.Clean(tool.Binary) != tool.Binary || path.IsAbs(tool.Binary) ||
		strings.HasPrefix(tool.Binary, ".."):
		return fmt.Errorf("tool %s: %q is not a path inside its release file", tool.Name, tool.Binary)
	}

	return nil
}

// download fetches the release file into dir and returns its path once its
// checksum matched.
func (t ToolInstaller) download(ctx context.Context, tool nodeproto.Tool, dir string) (string, error) {
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: toolDownloadTimeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tool.URL, nil)
	if err != nil {
		return "", fmt.Errorf("tool %s: building the request: %w", tool.Name, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching tool %s %s: %w", tool.Name, tool.Version, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The body was read or the read failed; closing adds nothing.

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching tool %s %s: status %d", tool.Name, tool.Version, resp.StatusCode)
	}

	file, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", fmt.Errorf("tool %s: making a file for the download: %w", tool.Name, err)
	}

	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, sum), io.LimitReader(resp.Body, maxToolDownloadBytes+1))

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	switch got := hex.EncodeToString(sum.Sum(nil)); {
	case err != nil:
		err = fmt.Errorf("fetching tool %s %s: %w", tool.Name, tool.Version, err)
	case n > maxToolDownloadBytes:
		err = fmt.Errorf("tool %s %s: the release file is over %d MB", tool.Name, tool.Version, maxToolDownloadBytes>>20)
	case got != tool.SHA256:
		err = fmt.Errorf("tool %s %s: the release file's checksum is %s, not the pinned %s; nothing was installed",
			tool.Name, tool.Version, got, tool.SHA256)
	}

	if err != nil {
		_ = os.Remove(file.Name())

		return "", err
	}

	return file.Name(), nil
}

// unpackTool writes the member of a .tar.gz named binary to target, as a
// command its owner can run.
func unpackTool(archive, binary, target string) error {
	file, err := os.Open(archive) //nolint:gosec // the node's own download
	if err != nil {
		return err
	}
	defer file.Close() //nolint:errcheck // Read only.

	zipped, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("the release file is not a .tar.gz: %w", err)
	}

	for members := tar.NewReader(zipped); ; {
		header, err := members.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("the release file has no %s", binary)
		}

		if err != nil {
			return fmt.Errorf("reading the release file: %w", err)
		}

		if header.Typeflag != tar.TypeReg || path.Clean(strings.TrimPrefix(header.Name, "./")) != binary {
			continue
		}

		//nolint:gosec // A command, run by its owner.
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if err != nil {
			return err
		}

		n, err := io.Copy(out, io.LimitReader(members, maxToolBinaryBytes+1))

		if closeErr := out.Close(); err == nil {
			err = closeErr
		}

		if err == nil && n > maxToolBinaryBytes {
			err = fmt.Errorf("%s is over %d MB", binary, maxToolBinaryBytes>>20)
		}

		return err
	}
}

func isZip(url string) bool { return strings.HasSuffix(url, ".zip") }

// installDirectory installs a tool that is a directory: the release file is
// fetched beside the version's directory, the directory of the command in it
// is unpacked beside it too, and only then renamed into place. Whatever an
// earlier try left there is removed first: the command is not there, or this
// would not run.
func (t ToolInstaller) installDirectory(ctx context.Context, tool nodeproto.Tool, dir string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("making the directory of tool %s: %w", tool.Name, err)
	}

	archive, err := t.download(ctx, tool, parent)
	if err != nil {
		return err
	}
	defer os.Remove(archive) //nolint:errcheck // A leftover download is replaced by the next try.

	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)

	if err := unpackDirectory(archive, path.Dir(tool.Binary), tmp); err != nil {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("unpacking tool %s %s: %w", tool.Name, tool.Version, err)
	}

	if info, err := os.Stat(filepath.Join(tmp, path.Base(tool.Binary))); err != nil || !info.Mode().IsRegular() {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("unpacking tool %s %s: the release file has no %s", tool.Name, tool.Version, tool.Binary)
	}

	if path.Base(tool.Binary) != tool.Name {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("tool %s: its command in a directory must be named %s, not %s", tool.Name, tool.Name,
			path.Base(tool.Binary))
	}

	if err := os.RemoveAll(dir); err != nil {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("installing tool %s %s: %w", tool.Name, tool.Version, err)
	}

	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("installing tool %s %s: %w", tool.Name, tool.Version, err)
	}

	return nil
}

// unpackDirectory writes the members of a .zip under from to target, keeping
// which may run: files and directories only, none outside target.
func unpackDirectory(archive, from, target string) error {
	zipped, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("the release file is not a .zip: %w", err)
	}
	defer zipped.Close() //nolint:errcheck // Read only.

	prefix := from + "/"

	var (
		total int64
		files int
	)

	for _, member := range zipped.File {
		name := path.Clean(member.Name)
		if !strings.HasPrefix(name+"/", prefix) || name == from {
			continue
		}

		rel := strings.TrimPrefix(name, prefix)
		if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "\\") {
			return fmt.Errorf("the release file names %s, outside its directory", member.Name)
		}

		if files++; files > maxToolFiles {
			return fmt.Errorf("the release file has more than %d files", maxToolFiles)
		}

		out := filepath.Join(target, filepath.FromSlash(rel))
		mode := member.Mode()

		switch {
		case mode.IsDir():
			if err := os.MkdirAll(out, 0o700); err != nil {
				return err
			}

			continue
		case !mode.IsRegular():
			return fmt.Errorf("the release file has %s, which is not a file", member.Name)
		}

		perm := os.FileMode(0o600)
		if mode.Perm()&0o111 != 0 {
			perm = 0o700
		}

		n, err := unpackMember(member, out, perm, maxToolBinaryBytes-total)
		if err != nil {
			return err
		}

		total += n
	}

	return nil
}

// unpackMember writes one member to out, no more than left bytes.
func unpackMember(member *zip.File, out string, perm os.FileMode, left int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return 0, err
	}

	in, err := member.Open()
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", member.Name, err)
	}
	defer in.Close() //nolint:errcheck // Read only.

	file, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm) //nolint:gosec // under target
	if err != nil {
		return 0, err
	}

	n, err := io.Copy(file, io.LimitReader(in, left+1))

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err == nil && n > left {
		err = fmt.Errorf("the tool is over %d MB", maxToolBinaryBytes>>20)
	}

	return n, err
}
