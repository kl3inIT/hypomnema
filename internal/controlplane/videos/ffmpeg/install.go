package ffmpeg

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ulikunitz/xz"
)

// The build is installed under the control plane's own folder, with no root,
// as a node installs scrcpy-server (internal/node/device/install.go) and the
// shared browser its programs (internal/controlplane/browser/install.go):
//
//	<dir>/<version>/ffmpeg, <dir>/<version>/ffprobe, <dir>/<version>/LICENSE.txt
//	<dir>/fonts/BeVietnamPro-*.ttf, <dir>/fonts/OFL.txt (theirs)
//	<dir>/fonts/JetBrainsMono-SemiBold.ttf, <dir>/fonts/JetBrainsMono-OFL.txt
//	<dir>/current   the version in use
//
// The file is downloaded beside its place, checked against its size and
// checksum, unpacked into <version>.tmp, and the programs are run there
// (check): only then is the folder renamed into place and `current` moved to
// it. The build `current` named before stays in use, and on disk, until the
// new one passed; then the folders of other versions are removed. Programs
// are called by these absolute paths, never through PATH.

const (
	downloadTimeout = 15 * time.Minute
	checkTimeout    = 30 * time.Second
	currentFile     = "current"
	downloadsDir    = "downloads"
	fontsDir        = "fonts"
)

// fonts are the fonts captions are drawn with, each with every Vietnamese
// letter, and their licenses (SIL Open Font License 1.1), each family's next
// to it:
//
//   - Be Vietnam Pro SemiBold, ExtraBold, Bold and Medium, from
//     github.com/google/fonts at c133902 (ofl/bevietnampro), license OFL.txt;
//   - JetBrains Mono SemiBold, from github.com/JetBrains/JetBrainsMono at
//     19371302 (fonts/ttf), the commit google/fonts takes it from (ofl/jetbrainsmono), license
//     JetBrainsMono-OFL.txt.
//
// The caption presets map demotape's macOS fonts onto them (issue #908, row 7).
//
//go:embed fonts/BeVietnamPro-SemiBold.ttf fonts/OFL.txt
//go:embed fonts/BeVietnamPro-ExtraBold.ttf fonts/BeVietnamPro-Bold.ttf fonts/BeVietnamPro-Medium.ttf
//go:embed fonts/JetBrainsMono-SemiBold.ttf fonts/JetBrainsMono-OFL.txt
var fonts embed.FS

// FontFile is one of the fonts installed with ffmpeg, by its file's name, as
// the captions measure their text with it (issue #908, row 8).
func FontFile(name string) ([]byte, error) {
	return fonts.ReadFile(fontsDir + "/" + name)
}

// Tools are the installed programs, by absolute path, and the folder of the
// fonts ffmpeg's subtitles filter is given (fontsdir).
type Tools struct {
	FFmpeg   string
	FFprobe  string
	FontsDir string
	Version  string
}

// Config is what an Installer needs.
type Config struct {
	// Dir is the folder the build goes in, the control plane's own.
	Dir    string
	Logger *slog.Logger
	// Client fetches the file; nil is a client with the download's time limit.
	Client *http.Client
}

// Installer has the pinned build installed and says where it is.
type Installer struct {
	cfg   Config
	build build
	// turn is held by the one install running; others wait for it or their context.
	turn chan struct{}

	mu    sync.Mutex
	tools Tools
}

// New returns an Installer. Nothing is fetched until Start or Ensure.
func New(cfg Config) *Installer {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	return &Installer{cfg: cfg, build: pinned(), turn: make(chan struct{}, 1)}
}

// Start installs the build in the background, so that serving does not wait
// for it; what fails is logged, and the next Ensure tries again.
func (i *Installer) Start(ctx context.Context) {
	go func() {
		tools, err := i.Ensure(ctx)
		if err != nil {
			if ctx.Err() == nil {
				i.cfg.Logger.WarnContext(ctx, "ffmpeg could not be installed",
					slog.String("event", "videos.ffmpeg_install_failed"), slog.String("error", err.Error()))
			}

			return
		}

		i.cfg.Logger.InfoContext(ctx, "ffmpeg is installed",
			slog.String("event", "videos.ffmpeg_installed"), slog.String("version", tools.Version))
	}()
}

// Ensure has the pinned build installed and checked and returns its
// programs. When the pinned build cannot be installed but an earlier one is
// in place, the earlier one is returned and the failure logged.
func (i *Installer) Ensure(ctx context.Context) (Tools, error) {
	select {
	case i.turn <- struct{}{}:
	case <-ctx.Done():
		return Tools{}, ctx.Err()
	}
	defer func() { <-i.turn }()

	i.mu.Lock()
	have := i.tools
	i.mu.Unlock()

	if have.Version == i.build.Version {
		return have, nil
	}

	tools, err := i.install(ctx)
	if err != nil {
		previous, ok := i.current()
		if !ok {
			return Tools{}, err
		}

		i.cfg.Logger.WarnContext(ctx, "the pinned ffmpeg could not be installed; the one before stays in use",
			slog.String("event", "videos.ffmpeg_install_failed"), slog.String("version", previous.Version),
			slog.String("error", err.Error()))

		tools = previous
	}

	i.mu.Lock()
	i.tools = tools
	i.mu.Unlock()

	return tools, nil
}

// install has the pinned build in place, fetching it unless `current` already names it.
func (i *Installer) install(ctx context.Context) (Tools, error) {
	if err := os.MkdirAll(i.cfg.Dir, 0o700); err != nil {
		return Tools{}, fmt.Errorf("making the folder of ffmpeg: %w", err)
	}

	if err := i.writeFonts(); err != nil {
		return Tools{}, err
	}

	if tools, ok := i.current(); ok && tools.Version == i.build.Version {
		return tools, nil
	}

	system := runtime.GOOS + "/" + runtime.GOARCH

	rel, ok := i.build.Releases[system]
	if !ok {
		return Tools{}, fmt.Errorf("ffmpeg %s has no build for %s", i.build.Version, system)
	}

	downloads := filepath.Join(i.cfg.Dir, downloadsDir)

	// What an earlier try left half-way is not resumed: the file is fetched again whole.
	if err := os.RemoveAll(downloads); err != nil {
		return Tools{}, fmt.Errorf("clearing an earlier try: %w", err)
	}

	if err := os.MkdirAll(downloads, 0o700); err != nil {
		return Tools{}, err
	}
	defer os.RemoveAll(downloads) //nolint:errcheck // A leftover is cleared by the next try.

	file, err := i.download(ctx, rel, downloads)
	if err != nil {
		return Tools{}, err
	}

	home := filepath.Join(i.cfg.Dir, i.build.Version)
	tmp := home + ".tmp"

	if err := os.RemoveAll(tmp); err != nil {
		return Tools{}, fmt.Errorf("clearing an earlier try: %w", err)
	}

	if err := untar(ctx, file, rel.Root, tmp); err != nil {
		_ = os.RemoveAll(tmp)

		return Tools{}, fmt.Errorf("unpacking ffmpeg %s: %w", i.build.Version, err)
	}

	// Checked where it was unpacked, before anything in use changes.
	if err := Check(ctx, i.toolsIn(tmp)); err != nil {
		_ = os.RemoveAll(tmp)

		return Tools{}, fmt.Errorf("ffmpeg %s: %w", i.build.Version, err)
	}

	if err := os.RemoveAll(home); err != nil {
		return Tools{}, err
	}

	if err := os.Rename(tmp, home); err != nil {
		_ = os.RemoveAll(tmp)

		return Tools{}, fmt.Errorf("installing ffmpeg %s: %w", i.build.Version, err)
	}

	if err := writeFile(filepath.Join(i.cfg.Dir, currentFile), []byte(i.build.Version+"\n")); err != nil {
		return Tools{}, fmt.Errorf("recording ffmpeg %s: %w", i.build.Version, err)
	}

	i.removeOthers()

	return i.toolsIn(home), nil
}

// Installed is the build installed in dir, when there is one: what a test
// reads after `go run ./internal/controlplane/videos/ffmpeg/installffmpeg`.
func Installed(dir string) (Tools, bool) {
	return New(Config{Dir: dir}).current()
}

// current is the build `current` names, when its programs are in place.
func (i *Installer) current() (Tools, bool) {
	raw, err := os.ReadFile(filepath.Join(i.cfg.Dir, currentFile))
	if err != nil {
		return Tools{}, false
	}

	version := strings.TrimSpace(string(raw))
	if version == "" || !filepath.IsLocal(version) || strings.ContainsRune(version, filepath.Separator) {
		return Tools{}, false
	}

	tools := i.toolsIn(filepath.Join(i.cfg.Dir, version))
	tools.Version = version

	for _, path := range []string{tools.FFmpeg, tools.FFprobe} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return Tools{}, false
		}
	}

	return tools, true
}

func (i *Installer) toolsIn(home string) Tools {
	return Tools{
		FFmpeg: filepath.Join(home, "ffmpeg"), FFprobe: filepath.Join(home, "ffprobe"),
		FontsDir: filepath.Join(i.cfg.Dir, fontsDir), Version: i.build.Version,
	}
}

// removeOthers removes the folders of builds other than the pinned one, once it is in use.
func (i *Installer) removeOthers() {
	entries, err := os.ReadDir(i.cfg.Dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == i.build.Version || name == fontsDir || name == downloadsDir {
			continue
		}

		if err := os.RemoveAll(filepath.Join(i.cfg.Dir, name)); err != nil {
			i.cfg.Logger.Warn("an earlier ffmpeg could not be removed",
				slog.String("event", "videos.ffmpeg_cleanup_failed"), slog.String("error", err.Error()))
		}
	}
}

// writeFonts puts the embedded fonts in their folder, each written whole and renamed when it differs.
func (i *Installer) writeFonts() error {
	dir := filepath.Join(i.cfg.Dir, fontsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making the folder of the fonts: %w", err)
	}

	return fs.WalkDir(fonts, fontsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		want, err := fonts.ReadFile(path)
		if err != nil {
			return err
		}

		to := filepath.Join(dir, entry.Name())
		if have, err := os.ReadFile(to); err == nil && bytes.Equal(have, want) { //nolint:gosec // Its own folder.
			return nil
		}

		if err := writeFile(to, want); err != nil {
			return fmt.Errorf("writing the font %s: %w", entry.Name(), err)
		}

		return nil
	})
}

// writeFile writes a file whole beside its place and renames it there.
func writeFile(path string, content []byte) error {
	if err := os.WriteFile(path+".tmp", content, 0o600); err != nil {
		return err
	}

	return os.Rename(path+".tmp", path)
}

// download fetches the build's file into dir and returns its path once its
// size and checksum matched, as the node's installer does.
func (i *Installer) download(ctx context.Context, rel release, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	client := i.cfg.Client
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return "", fmt.Errorf("ffmpeg: building the request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching ffmpeg %s: %w", i.build.Version, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The body was read or the read failed; closing adds nothing.

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching ffmpeg %s: status %d", i.build.Version, resp.StatusCode)
	}

	file, err := os.CreateTemp(dir, "download-*")
	if err != nil {
		return "", fmt.Errorf("ffmpeg: making a file for the download: %w", err)
	}

	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, sum), io.LimitReader(resp.Body, rel.Bytes+1))

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	switch got := hex.EncodeToString(sum.Sum(nil)); {
	case err != nil:
		err = fmt.Errorf("fetching ffmpeg %s: %w", i.build.Version, err)
	case n != rel.Bytes:
		err = fmt.Errorf("ffmpeg %s: the file is not the pinned %d bytes; nothing was installed",
			i.build.Version, rel.Bytes)
	case got != rel.SHA256:
		err = fmt.Errorf("ffmpeg %s: the file's checksum is %s, not the pinned %s; nothing was installed",
			i.build.Version, got, rel.SHA256)
	}

	if err != nil {
		_ = os.Remove(file.Name())

		return "", err
	}

	return file.Name(), nil
}

// untar takes ffmpeg, ffprobe and the license out of the checked .tar.xz
// into home; the rest of it (ffplay, documents) is not kept. A canceled ctx
// stops it between members.
func untar(ctx context.Context, archive, root, home string) error {
	in, err := os.Open(archive) //nolint:gosec // The control plane's own download.
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck // Read only.

	unxz, err := xz.NewReader(bufio.NewReader(in))
	if err != nil {
		return fmt.Errorf("the file is not a .tar.xz: %w", err)
	}

	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}

	wanted := map[string]os.FileMode{
		root + "/bin/ffmpeg": 0o700, root + "/bin/ffprobe": 0o700, root + "/LICENSE.txt": 0o600,
	}

	members := tar.NewReader(unxz)

	var written int64

	for len(wanted) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}

		header, err := members.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return err
		}

		mode, ok := wanted[header.Name]
		if !ok {
			continue
		}

		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("%s is not a file", header.Name)
		}

		n, err := extract(members, filepath.Join(home, filepath.Base(header.Name)), mode, maxUnpackedBytes-written)
		if err != nil {
			return fmt.Errorf("%s: %w", header.Name, err)
		}

		written += n

		delete(wanted, header.Name)
	}

	if len(wanted) > 0 {
		return fmt.Errorf("the file has no %s", strings.Join(slices.Sorted(maps.Keys(wanted)), ", "))
	}

	return nil
}

func extract(in io.Reader, to string, mode os.FileMode, room int64) (int64, error) {
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) //nolint:gosec // Its own folder.
	if err != nil {
		return 0, err
	}

	n, err := io.Copy(out, io.LimitReader(in, room+1))

	if closeErr := out.Close(); err == nil {
		err = closeErr
	}

	if err == nil && n > room {
		err = fmt.Errorf("the file unpacks to over %d MB", maxUnpackedBytes>>20)
	}

	return n, err
}

// The filters and encoders the videos are made with (issue #818, rows 4, 6, 8).
var (
	neededFilters  = []string{"freezedetect", "subtitles", "ass"}
	neededEncoders = []string{"libx264", "aac"}
)

// Check runs the programs and says what a video needs that they lack.
func Check(ctx context.Context, tools Tools) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	if _, err := run(ctx, tools.FFprobe, "-hide_banner", "-version"); err != nil {
		return fmt.Errorf("ffprobe does not run: %w", err)
	}

	for _, list := range []struct {
		flag  string
		names []string
	}{{"-filters", neededFilters}, {"-encoders", neededEncoders}} {
		out, err := run(ctx, tools.FFmpeg, "-hide_banner", list.flag)
		if err != nil {
			return fmt.Errorf("ffmpeg does not run: %w", err)
		}

		have := map[string]bool{}

		for line := range strings.Lines(out) {
			if fields := strings.Fields(line); len(fields) >= 2 {
				have[fields[1]] = true
			}
		}

		for _, name := range list.names {
			if !have[name] {
				return fmt.Errorf("ffmpeg has no %s %s", strings.TrimSuffix(list.flag[1:], "s"), name)
			}
		}
	}

	return nil
}

func run(ctx context.Context, path string, args ...string) (string, error) {
	for try := 0; ; try++ {
		var stderr bytes.Buffer

		cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // The installed program, by its absolute path.
		cmd.Stderr = &stderr

		out, err := cmd.Output()

		// A program just written may still be open in a process another goroutine forked meanwhile
		// (golang/go#22315): it runs once that process has exec'd.
		if errors.Is(err, syscall.ETXTBSY) && try < 5 && ctx.Err() == nil {
			time.Sleep(100 * time.Millisecond)

			continue
		}

		if err != nil {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}

		return string(out), nil
	}
}
