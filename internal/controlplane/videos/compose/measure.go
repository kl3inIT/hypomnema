package compose

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// Measurement is a file's first audio stream as Measure decoded it (issue #910, row 6c).
type Measurement struct {
	// LUFS is the integrated loudness, ebur128's I; -70 for silence.
	LUFS float64
	// TruePeak is the largest true peak of the stream, as an amplitude (ebur128 writes it linear: f_ebur128.c's
	// true_peak, a 0.461 probe being -6.73 dBTP).
	TruePeak float64
	// LengthMs is the decoded length: ffmpeg's last out_time_us, not the duration ffprobe estimates.
	LengthMs int64
}

// SilentLUFS is the integrated loudness at or under which a stream is silence: ebur128's floor.
const SilentLUFS = -70.0

// TPdB is the true peak in dBTP: 20·log10 of the amplitude, -∞ for a peak of 0.
func (m Measurement) TPdB() float64 {
	if m.TruePeak <= 0 {
		return math.Inf(-1)
	}

	return 20 * math.Log10(m.TruePeak)
}

// Silent says whether the stream is silence: I at ebur128's floor, or no peak at all.
func (m Measurement) Silent() bool { return m.LUFS <= SilentLUFS || m.TruePeak <= 0 }

// maxMeasureOutput bounds what Measure reads of ffmpeg's output: a 20-minute track prints about 3.5 MB.
const maxMeasureOutput = 32 << 20

// measureWaitDelay is how long Measure waits, once ffmpeg is killed, for its stderr to close.
const measureWaitDelay = 500 * time.Millisecond

// errMeasureCut is a decode whose output ended before ffmpeg said progress=end.
var errMeasureCut = errors.New("ffmpeg's output ended before the decode's end")

// Measure decodes the first audio stream of file, strictly (-xerror: a corrupt packet is an error), at most limitSec
// seconds of it when limitSec is positive, and measures its loudness, true peak and decoded length through ffmpeg's
// stdout, with no file written (issue #910, row 6c: ffmpeg ignores the errors of writing a metadata file,
// f_metadata.c:196-205, while a pipe has no such failure). The output must end with progress=end, so a decode that
// stops early is never taken for a short file.
func Measure(parent context.Context, tools ffmpeg.Tools, file string, limitSec float64) (Measurement, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	args := []string{"-nostats", "-v", "error", "-xerror", "-progress", "pipe:1"}
	if limitSec > 0 {
		args = append(args, "-t", strconv.FormatFloat(limitSec, 'f', -1, 64))
	}

	args = append(args, "-i", file, "-map", "0:a:0",
		"-af", "ebur128=peak=true:metadata=1,ametadata=mode=print:file=-", "-f", "null", "-")

	var stderr bytes.Buffer

	cmd := tools.Command(ctx, args...)
	cmd.Stderr = &stderr
	// At the deadline the whole process group is killed, so nothing ffmpeg started outlives it holding its pipes.
	ownGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	// Once ffmpeg is killed, Wait closes its stderr after this long even if something it started still holds it.
	cmd.WaitDelay = measureWaitDelay

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Measurement{}, err
	}

	if err := cmd.Start(); err != nil {
		return Measurement{}, err
	}

	// The group is killed at the deadline until both pipes are done with, not only while ffmpeg runs: exec stops
	// calling Cancel once ffmpeg has exited, while something it started may still hold its stderr.
	stopGroup := context.AfterFunc(ctx, func() { _ = killGroup(cmd) })
	defer stopGroup()

	// At the deadline the read end is closed, so reading stops even while something ffmpeg started still holds
	// its stdout (it would keep the read waiting for an end of file that never comes).
	stop := context.AfterFunc(ctx, func() { _ = stdout.Close() })

	m, readErr := readMeasure(stdout)
	stop()

	if readErr != nil {
		// Stop ffmpeg before waiting: it may still be writing to the pipe no one reads.
		cancel()
	}

	waitErr := cmd.Wait()
	// Whatever Wait returned, what ffmpeg started may still be running, holding its stderr: its group is killed.
	// An empty group is no error to Measure.
	_ = killGroup(cmd)

	switch {
	case parent.Err() != nil:
		return Measurement{}, parent.Err()
	case readErr != nil:
		return Measurement{}, fmt.Errorf("measuring %s: %w", file, readErr)
	case waitErr != nil:
		return Measurement{}, fmt.Errorf("measuring %s: %s: %w\n%s", file, tools.FFmpeg, waitErr,
			tail(stderr.String(), 25))
	}

	return m, nil
}

// readMeasure reads what Measure's ffmpeg prints, keeping the last lavfi.r128.I, the largest
// lavfi.r128.true_peak and the last out_time_us, up to maxMeasureOutput bytes; the last progress= must be end.
func readMeasure(r io.Reader) (Measurement, error) {
	limited := &io.LimitedReader{R: r, N: maxMeasureOutput + 1}
	lines := bufio.NewScanner(limited)
	lines.Buffer(make([]byte, 0, 4096), 64<<10)

	var (
		m                      Measurement
		haveI, haveTime, ended bool
	)

	for lines.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(lines.Text()), "=")
		if !ok {
			continue
		}

		switch key {
		case "lavfi.r128.I":
			if v, err := strconv.ParseFloat(value, 64); err == nil {
				m.LUFS, haveI = v, true
			}
		case "lavfi.r128.true_peak":
			if v, err := strconv.ParseFloat(value, 64); err == nil {
				m.TruePeak = max(m.TruePeak, v)
			}
		case "out_time_us":
			if v, err := strconv.ParseInt(value, 10, 64); err == nil {
				m.LengthMs, haveTime = (v+500)/1000, true
			}
		case "progress":
			ended = value == "end"
		}
	}

	if limited.N <= 0 {
		return Measurement{}, fmt.Errorf("ffmpeg printed more than %d MB", maxMeasureOutput>>20)
	}

	if err := lines.Err(); err != nil {
		return Measurement{}, err
	}

	switch {
	case !ended:
		return Measurement{}, errMeasureCut
	case !haveI:
		return Measurement{}, errors.New("ffmpeg measured no loudness: the file has no audio")
	case !haveTime:
		return Measurement{}, errors.New("ffmpeg said no length")
	}

	return m, nil
}

// tail is the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")

	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
