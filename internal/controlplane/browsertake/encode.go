package browsertake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/takes"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// segmentFile is a browser take's one segment, named as a phone take's are.
const segmentFile = "seg-000.mp4"

// frameFile is a frame written of a take: its file, and when it was drawn on the take's timeline.
type frameFile struct {
	file string
	atMs int64
}

// encode makes the frames into the take's segment in dir, of width by height,
// lasting until stopMs (row 2), and says what it is.
func encode(ctx context.Context, tools ffmpeg.Tools, dir string, frames []frameFile, stopMs int64, width, height int,
) (string, devicetool.TakeSegment, error) {
	files := make([]string, len(frames))
	at := make([]int64, len(frames))

	for i, f := range frames {
		files[i], at[i] = f.file, f.atMs
	}

	content, err := framesConcatContent(files, frameDurations(at, stopMs))
	if err != nil {
		return "", devicetool.TakeSegment{}, err
	}

	list := filepath.Join(dir, "frames.txt")
	if err := os.WriteFile(list, []byte(content), 0o600); err != nil {
		return "", devicetool.TakeSegment{}, err
	}

	out := filepath.Join(dir, segmentFile)
	if _, err := run(ctx, tools.FFmpeg, framesEncodeArgs(list, out, width, height, stopMs)...); err != nil {
		return "", devicetool.TakeSegment{}, fmt.Errorf("encoding the frames: %w", err)
	}

	probed, err := run(ctx, tools.FFprobe, "-v", "error", "-select_streams", "v:0", "-count_packets",
		"-show_entries", "stream=nb_read_packets", "-of", "csv=p=0", out)
	if err != nil {
		return "", devicetool.TakeSegment{}, fmt.Errorf("reading the segment back: %w", err)
	}

	n, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(probed), ","), 10, 64)
	if err != nil || n <= 0 {
		return "", devicetool.TakeSegment{}, fmt.Errorf("the segment has no frame: %q", probed)
	}

	info, err := os.Stat(out)
	if err != nil {
		return "", devicetool.TakeSegment{}, err
	}

	if info.Size() > takes.MaxSegmentBytes {
		return "", devicetool.TakeSegment{}, fmt.Errorf("the segment is %d MB, more than a take's %d MB",
			info.Size()>>20, takes.MaxSegmentBytes>>20)
	}

	sum, err := sumOf(out)
	if err != nil {
		return "", devicetool.TakeSegment{}, err
	}

	// A frame every 1/FPS s: the last starts one frame before the end.
	return out, devicetool.TakeSegment{
		File: segmentFile, LengthMs: n * 1000 / FPS, LastFrameMs: (n - 1) * 1000 / FPS,
		Width: width, Height: height, Bytes: info.Size(), SHA256: sum,
	}, nil
}

// run runs one of ffmpeg's programs and returns what it wrote.
func run(ctx context.Context, bin string, args ...string) (string, error) {
	if bin == "" {
		return "", errors.New("ffmpeg is not installed")
	}

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // The pinned ffmpeg, with arguments written here.
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", filepath.Base(bin), err, lastLine(stderr.String()))
	}

	return stdout.String(), nil
}

// lastLine is the last line a program wrote, cut to a length fit for an error.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")

	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 300 {
		line = line[:300]
	}

	return line
}

// sumOf is a file's SHA-256 in hex.
func sumOf(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // The take's own file.
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // Read only.

	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
