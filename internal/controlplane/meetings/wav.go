package meetings

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
)

// A live meeting's recording is one WAV, PCM16 24 kHz mono, written here without a dependency (issue #820, row 10):
// the tracks' audio, each at its place on the meeting's clock, summed and clipped, as a mixer adds two sources. It is
// served as an upload's recording is, so the player seeks it with ranges.

const (
	wavHeader  = 44
	mixChunk   = 64 << 10
	sampleSize = 2
)

// mix writes the tracks' audio as one WAV at dst and returns its size and length. A track missing is silence; the
// recording is as long as the longest track.
func mix(dst string, tracks []string) (int64, time.Duration, error) {
	var (
		files  []*os.File
		length int64
	)

	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	for _, p := range tracks {
		f, err := os.Open(p) //nolint:gosec // a track of the meeting in the recordings' folder
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			return 0, 0, err
		}

		files = append(files, f)

		info, err := f.Stat()
		if err != nil {
			return 0, 0, err
		}

		length = max(length, info.Size()&^1)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), "mix.*.part")
	if err != nil {
		return 0, 0, err
	}

	done := false

	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err := tmp.Write(wavHead(length)); err != nil {
		return 0, 0, err
	}

	sum := make([]int32, mixChunk/sampleSize)
	buf := make([]byte, mixChunk)
	out := make([]byte, mixChunk)

	for at := int64(0); at < length; at += mixChunk {
		n := min(int64(mixChunk), length-at)
		clear(sum)

		for _, f := range files {
			read, err := f.ReadAt(buf[:n], at)
			if err != nil && !errors.Is(err, io.EOF) {
				return 0, 0, err
			}

			for i := 0; i+1 < read; i += sampleSize {
				sum[i/sampleSize] += int32(int16(binary.LittleEndian.Uint16(buf[i:]))) //nolint:gosec // PCM16's bits
			}
		}

		for i := range n / sampleSize {
			v := min(max(sum[i], math.MinInt16), math.MaxInt16)
			binary.LittleEndian.PutUint16(out[i*sampleSize:], uint16(int16(v))) //nolint:gosec // clipped to PCM16
		}

		if _, err := tmp.Write(out[:n]); err != nil {
			return 0, 0, err
		}
	}

	if err := tmp.Close(); err != nil {
		return 0, 0, err
	}

	if err := os.Rename(tmp.Name(), dst); err != nil {
		return 0, 0, fmt.Errorf("keeping the mixed recording: %w", err)
	}

	done = true

	return wavHeader + length, time.Duration(length/sampleSize) * time.Second / speech.LiveSampleRate, nil
}

// wavHead is the RIFF header of PCM16 mono audio of n bytes.
func wavHead(n int64) []byte {
	h := make([]byte, wavHeader)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(min(n+wavHeader-8, math.MaxUint32))) //nolint:gosec // bounded
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], speech.LiveSampleRate)
	binary.LittleEndian.PutUint32(h[28:], speech.LiveBytesPerSecond)
	binary.LittleEndian.PutUint16(h[32:], sampleSize)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(min(n, math.MaxUint32))) //nolint:gosec // bounded

	return h
}
