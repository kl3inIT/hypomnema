package device

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// The components are installed under the device's directory, with no root,
// as the shared browser installs its programs
// (internal/controlplane/browser/install.go), but in the layout the emulator
// reads an SDK from, so a component has one fixed place rather than a folder
// per version:
//
//	<dir>/sdk/platform-tools, <dir>/sdk/emulator,
//	<dir>/sdk/system-images/android-34/google_apis_playstore/x86_64,
//	<dir>/bin/android, <dir>/bin/scrcpy-server
//
// A file is downloaded beside its place, checked against its size and
// checksum, and only then unpacked and renamed into place: a file that does
// not match is deleted and nothing is installed. What was installed is
// recorded in installed.json, one entry a component, written after each:
// an install that stopped half-way takes up at the component it stopped in,
// and a component whose pin is unchanged is not fetched again.

// downloadTimeout bounds one component's download: the phone image is 1.5 GB.
const downloadTimeout = time.Hour

const (
	installedFile = "installed.json"
	downloadsDir  = "downloads"
)

// Installed is one component the node has, as it reports it.
type Installed struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// installer fetches the pinned components into dir.
type installer struct {
	dir    string
	client *http.Client
}

// installed reads what installed.json records, by name; nothing for a node
// that installed nothing yet.
func (i installer) installed() (map[string]Installed, error) {
	raw, err := os.ReadFile(filepath.Join(i.dir, installedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Installed{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading what is installed: %w", err)
	}

	var list []Installed
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", installedFile, err)
	}

	out := make(map[string]Installed, len(list))
	for _, c := range list {
		out[c.Name] = c
	}

	return out, nil
}

// complete says whether every component is installed at its pin.
func (i installer) complete(comps []component) bool {
	have, err := i.installed()
	if err != nil {
		return false
	}

	for _, c := range comps {
		if !i.has(have, c) {
			return false
		}
	}

	return true
}

// has says whether c is installed at its pin and still in its place.
func (i installer) has(have map[string]Installed, c component) bool {
	if have[c.Name].SHA256 != c.SHA256 {
		return false
	}

	_, err := os.Stat(filepath.Join(i.dir, filepath.FromSlash(c.Place)))

	return err == nil
}

// install has every component at its pin, in order, and stops at the first
// that cannot be.
func (i installer) install(ctx context.Context, comps []component) error {
	downloads := filepath.Join(i.dir, downloadsDir)

	// What an earlier try left half-way is not resumed: the file is fetched again whole.
	if err := os.RemoveAll(downloads); err != nil {
		return fmt.Errorf("clearing an earlier try: %w", err)
	}

	if err := os.MkdirAll(downloads, 0o700); err != nil {
		return fmt.Errorf("making the device's directory: %w", err)
	}

	defer os.RemoveAll(downloads) //nolint:errcheck // A leftover is cleared by the next try.

	for _, c := range comps {
		have, err := i.installed()
		if err != nil {
			return err
		}

		if i.has(have, c) {
			continue
		}

		if err := i.installOne(ctx, c, downloads); err != nil {
			return err
		}

		have[c.Name] = Installed{Name: c.Name, Version: c.Version, SHA256: c.SHA256}
		if err := i.record(comps, have); err != nil {
			return err
		}
	}

	return nil
}

func (i installer) installOne(ctx context.Context, c component, downloads string) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	file, err := i.download(ctx, c, downloads)
	if err != nil {
		return err
	}
	defer os.Remove(file) //nolint:errcheck // The downloads folder goes with the install.

	place := filepath.Join(i.dir, filepath.FromSlash(c.Place))
	if err := os.MkdirAll(filepath.Dir(place), 0o700); err != nil {
		return fmt.Errorf("making the place of %s: %w", c.Name, err)
	}

	// Unpacked beside its place and renamed into it, so a crash never leaves half a component there.
	tmp := filepath.Join(downloads, c.Name+".tmp")
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("clearing an earlier try at %s: %w", c.Name, err)
	}

	from, err := unpack(file, c, tmp)
	if err != nil {
		_ = os.RemoveAll(tmp)

		return fmt.Errorf("unpacking %s %s: %w", c.Name, c.Version, err)
	}

	if err := os.RemoveAll(place); err != nil {
		return fmt.Errorf("installing %s %s: %w", c.Name, c.Version, err)
	}

	if err := os.Rename(from, place); err != nil {
		return fmt.Errorf("installing %s %s: %w", c.Name, c.Version, err)
	}

	return os.RemoveAll(tmp)
}

// record writes what is installed, whole, and renames it into place.
func (i installer) record(comps []component, have map[string]Installed) error {
	list := make([]Installed, 0, len(have))

	for _, c := range comps {
		if entry, ok := have[c.Name]; ok {
			list = append(list, entry)
		}
	}

	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(i.dir, installedFile)
	if err := os.WriteFile(path+".tmp", append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("recording what is installed: %w", err)
	}

	if err := os.Rename(path+".tmp", path); err != nil {
		return fmt.Errorf("recording what is installed: %w", err)
	}

	return nil
}

// download fetches a component's file into dir and returns its path once its
// size and checksum matched, as the browser's installer does.
func (i installer) download(ctx context.Context, c component, dir string) (string, error) {
	client := i.client
	if client == nil {
		client = &http.Client{}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return "", fmt.Errorf("%s: building the request: %w", c.Name, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s %s: %w", c.Name, c.Version, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The body was read or the read failed; closing adds nothing.

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s %s: status %d", c.Name, c.Version, resp.StatusCode)
	}

	file, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", fmt.Errorf("%s: making a file for the download: %w", c.Name, err)
	}

	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, sum), io.LimitReader(resp.Body, c.Bytes+1))

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	switch got := hex.EncodeToString(sum.Sum(nil)); {
	case err != nil:
		err = fmt.Errorf("fetching %s %s: %w", c.Name, c.Version, err)
	case n != c.Bytes:
		err = fmt.Errorf("%s %s: the file is not the pinned %d bytes; nothing was installed",
			c.Name, c.Version, c.Bytes)
	case got != c.SHA256:
		err = fmt.Errorf("%s %s: the file's checksum is %s, not the pinned %s; nothing was installed",
			c.Name, c.Version, got, c.SHA256)
	}

	if err != nil {
		_ = os.Remove(file.Name())

		return "", err
	}

	return file.Name(), nil
}

// unpack puts a checked file under tmp and returns what is to be renamed
// into the component's place: the file itself, or the .zip's root folder.
func unpack(file string, c component, tmp string) (string, error) {
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", err
	}

	if c.Root == "" {
		mode := os.FileMode(0o600)
		if c.Executable {
			mode = 0o700
		}

		to := filepath.Join(tmp, filepath.Base(filepath.FromSlash(c.Place)))

		return to, copyFile(file, to, mode)
	}

	if err := unzip(file, tmp, c.MaxUnpacked); err != nil {
		return "", err
	}

	root := filepath.Join(tmp, c.Root)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", fmt.Errorf("the .zip has no %s/ folder", c.Root)
	}

	return root, nil
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from) //nolint:gosec // The node's own download.
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // Read only.

	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) //nolint:gosec // Run by its owner.
	if err != nil {
		return err
	}

	_, err = io.Copy(out, in)

	if closeErr := out.Close(); err == nil {
		err = closeErr
	}

	return err
}

// unzip writes every member of the archive under home, as the browser's
// installer does. A member that would land outside home stops it; so does a
// link whose target is outside home, the one kind of member beside files and
// folders the SDK's archives may hold.
func unzip(archive, home string, maxBytes int64) error {
	members, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("the file is not a .zip: %w", err)
	}
	defer members.Close() //nolint:errcheck // Read only.

	var written int64

	for _, member := range members.File {
		name := filepath.FromSlash(member.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%q is not a path inside the .zip", member.Name)
		}

		target := filepath.Join(home, name)
		mode := member.Mode()

		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}

			continue
		case mode&fs.ModeSymlink != 0:
			if err := unzipLink(member, name, target); err != nil {
				return fmt.Errorf("%s: %w", member.Name, err)
			}

			continue
		case !mode.IsRegular():
			return fmt.Errorf("%q is neither a file, a folder nor a link", member.Name)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}

		n, err := unzipMember(member, target, maxBytes-written, maxBytes)
		if err != nil {
			return fmt.Errorf("%s: %w", member.Name, err)
		}

		written += n
	}

	return nil
}

// unzipLink makes a link a member stands for, when what it points at is
// inside the archive's own tree.
func unzipLink(member *zip.File, name, target string) error {
	in, err := member.Open()
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // Read only.

	raw, err := io.ReadAll(io.LimitReader(in, 4<<10))
	if err != nil {
		return err
	}

	to := filepath.FromSlash(string(raw))
	if filepath.IsAbs(to) || !filepath.IsLocal(filepath.Join(filepath.Dir(name), to)) {
		return fmt.Errorf("the link points outside the .zip, at %q", string(raw))
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}

	return os.Symlink(to, target)
}

func unzipMember(member *zip.File, target string, room, maxBytes int64) (int64, error) {
	in, err := member.Open()
	if err != nil {
		return 0, err
	}
	defer in.Close() //nolint:errcheck // Read only.

	// Only the node's user reads or runs what it installs.
	mode := os.FileMode(0o600)
	if member.Mode()&0o100 != 0 {
		mode = 0o700
	}

	//nolint:gosec // Inside the device's directory, checked by the caller.
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}

	n, err := io.Copy(out, io.LimitReader(in, room+1))

	if closeErr := out.Close(); err == nil {
		err = closeErr
	}

	if err == nil && n > room {
		err = fmt.Errorf("the .zip unpacks to over %d MB", maxBytes>>20)
	}

	return n, err
}
