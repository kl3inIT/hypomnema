package device

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	amp4 "github.com/abema/go-mp4"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
)

// One segment of a take (issue #817, rows 1 to 3): a fragmented MP4 file of
// its own, an init with the segment's one configuration (SPS and PPS) and
// fragments after it, written with bluenviron/mediacommon's fmp4 package
// (MIT; notice in third_party/mediacommon) as MediaMTX's recorder writes its
// segments (internal/recorder/format_fmp4*.go, MIT; notice in
// third_party/mediamtx): Annex B made AVCC (format_fmp4.go:451-465), a
// timescale of 90 kHz, DTS equal to PTS since scrcpy's encoder writes no
// B-frames (scrcpy app/src/recorder.c:385-386), each sample's duration the
// next one's PTS less its own (recorder.c:388-391), a PTS that does not grow
// moved one tick past the one before (recorder.c:106-113), and the movie's
// duration written into mvhd as the segment closes (format_fmp4_segment.go,
// writeDuration). The last frame lasts until the segment closes, so a still
// screen at its end counts (agent-demo-video src/capture.ts:321-335).

const (
	// segmentTimescale is the video track's timescale, MediaMTX's for video.
	segmentTimescale = 90_000
	// partBytes is how many bytes of samples a fragment holds before it is written: fragments are flushed by size,
	// as MediaMTX bounds its parts (format_fmp4_part.go:60-64), so a take holds at most this much in memory.
	partBytes = 1 << 20
)

// segmentInfo is what a closed segment is: its file's name, length and size, and its sum.
type segmentInfo struct {
	File          string
	Length        time.Duration
	Width, Height int
	Bytes         int64
	SHA256        string
}

// sample is a frame waiting for its duration: the next frame's PTS, or the segment's close.
type sample struct {
	ticks uint64
	key   bool
	data  []byte
}

// segmentWriter writes one segment's file.
type segmentWriter struct {
	f      *os.File
	name   string
	sps    []byte
	pps    []byte
	size   screenSize
	config []byte

	firstPTS uint64
	lastPTS  uint64
	started  bool
	pending  *sample
	part     []*fmp4.Sample
	partBase uint64
	partSize int
	seq      uint32
	written  int64
}

// splitConfig reads the SPS and PPS of a configuration packet, in Annex B.
func splitConfig(config []byte) (sps, pps []byte, err error) {
	var units h264.AnnexB
	if err := units.Unmarshal(config); err != nil {
		return nil, nil, fmt.Errorf("the configuration is not Annex B: %w", err)
	}

	for _, unit := range units {
		if len(unit) == 0 {
			continue
		}

		switch h264.NALUType(unit[0] & 0x1f) {
		case h264.NALUTypeSPS:
			sps = unit
		case h264.NALUTypePPS:
			pps = unit
		default:
		}
	}

	if sps == nil || pps == nil {
		return nil, nil, errors.New("the configuration has no SPS or no PPS")
	}

	return sps, pps, nil
}

// createSegment creates a segment's file, private to the node's user, and
// writes its init with the configuration.
func createSegment(path, name string, config []byte) (*segmentWriter, error) {
	sps, pps, err := splitConfig(config)
	if err != nil {
		return nil, err
	}

	size, _ := spsSize(config)

	init := fmp4.Init{Tracks: []*fmp4.InitTrack{{
		ID: 1, TimeScale: segmentTimescale, Codec: &codecs.H264{SPS: sps, PPS: pps},
	}}}

	var buf seekablebuffer.Buffer
	if err := init.Marshal(&buf); err != nil {
		return nil, fmt.Errorf("encoding the segment's init: %w", err)
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // The take's own folder.
	if err != nil {
		return nil, err
	}

	w := &segmentWriter{f: f, name: name, sps: sps, pps: pps, size: size, config: config}

	if err := w.out(buf.Bytes()); err != nil {
		_ = f.Close()

		return nil, err
	}

	return w, nil
}

func (w *segmentWriter) out(b []byte) error {
	n, err := w.f.Write(b)
	w.written += int64(n)

	return err
}

// ticksOf is a PTS in microseconds as ticks of the segment from its first frame.
func (w *segmentWriter) ticksOf(pts uint64) uint64 {
	return (pts - w.firstPTS) * segmentTimescale / 1_000_000
}

// write adds a frame, in Annex B with its PTS in microseconds. The first is
// a key frame, with the configuration put before it, as scrcpy's demuxer
// merges a configuration into the packet after it (app/src/demuxer.c:277-285,
// 316-326).
func (w *segmentWriter) write(p packet) error {
	data := p.Data

	if !w.started {
		if !p.Key {
			return errors.New("a segment must start on a key frame")
		}

		w.started, w.firstPTS, w.lastPTS = true, p.PTS, p.PTS
		data = append(append([]byte(nil), w.config...), p.Data...)
	} else if p.PTS <= w.lastPTS {
		p.PTS = w.lastPTS + 1
	}

	w.lastPTS = p.PTS

	var units h264.AnnexB
	if err := units.Unmarshal(data); err != nil {
		return fmt.Errorf("a frame is not Annex B: %w", err)
	}

	avcc, err := h264.AVCC(units).Marshal()
	if err != nil {
		return err
	}

	next := &sample{ticks: w.ticksOf(p.PTS), key: p.Key, data: avcc}
	if w.pending != nil {
		if err := w.add(w.pending, next.ticks); err != nil {
			return err
		}
	}

	w.pending = next

	return nil
}

// add puts a frame whose end is known into the fragment, and writes the
// fragment once it holds partBytes.
func (w *segmentWriter) add(s *sample, end uint64) error {
	duration := max(end-s.ticks, 1)

	if len(w.part) == 0 {
		w.partBase = s.ticks
	}

	w.part = append(w.part, &fmp4.Sample{
		Duration: uint32(min(duration, uint64(^uint32(0)))), IsNonSyncSample: !s.key, Payload: s.data,
	})
	w.partSize += len(s.data)

	if w.partSize >= partBytes {
		return w.flush()
	}

	return nil
}

// flush writes the fragment held.
func (w *segmentWriter) flush() error {
	if len(w.part) == 0 {
		return nil
	}

	w.seq++

	part := fmp4.Part{SequenceNumber: w.seq, Tracks: []*fmp4.PartTrack{{ID: 1, BaseTime: w.partBase, Samples: w.part}}}

	var buf seekablebuffer.Buffer
	if err := part.Marshal(&buf); err != nil {
		return fmt.Errorf("encoding a fragment: %w", err)
	}

	w.part, w.partSize = nil, 0

	return w.out(buf.Bytes())
}

// close ends the segment at length from its first frame: the last frame
// lasts until then. It writes the movie's duration, syncs the file and sums
// it. A segment that got no frame is removed and is no segment.
func (w *segmentWriter) close(length time.Duration) (segmentInfo, bool, error) {
	if !w.started {
		_ = w.f.Close()
		_ = os.Remove(w.f.Name())

		return segmentInfo{}, false, nil
	}

	end := uint64(max(length, 0)) * segmentTimescale / uint64(time.Second)

	if w.pending != nil {
		if end <= w.pending.ticks {
			end = w.pending.ticks + 1
		}

		if err := w.add(w.pending, end); err != nil {
			return segmentInfo{}, true, w.fail(err)
		}

		w.pending = nil
	}

	if err := w.flush(); err != nil {
		return segmentInfo{}, true, w.fail(err)
	}

	total := time.Duration(end) * time.Second / segmentTimescale //nolint:gosec // Ticks of a take, far below 2^63.

	if err := writeDuration(w.f, total); err != nil {
		return segmentInfo{}, true, w.fail(fmt.Errorf("writing the segment's duration: %w", err))
	}

	if err := w.f.Sync(); err != nil {
		return segmentInfo{}, true, w.fail(err)
	}

	sum := sha256.New()
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return segmentInfo{}, true, w.fail(err)
	}

	if _, err := io.Copy(sum, w.f); err != nil {
		return segmentInfo{}, true, w.fail(err)
	}

	if err := w.f.Close(); err != nil {
		return segmentInfo{}, true, err
	}

	return segmentInfo{
		File: w.name, Length: total, Width: w.size.Width, Height: w.size.Height, Bytes: w.written,
		SHA256: hex.EncodeToString(sum.Sum(nil)),
	}, true, nil
}

// fail closes the file after an error, which it returns.
func (w *segmentWriter) fail(err error) error {
	_ = w.f.Close()

	return err
}

// writeDuration writes a movie's duration into its mvhd, after ftyp and at
// the start of moov, as MediaMTX's writeDuration does
// (internal/recorder/format_fmp4_segment.go:56-118, MIT): mediacommon's init
// says no duration, which a player then finds only by reading every fragment.
func writeDuration(f io.ReadWriteSeeker, d time.Duration) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	head := make([]byte, 8)
	if _, err := io.ReadFull(f, head); err != nil {
		return err
	}

	if !bytes.Equal(head[4:], []byte("ftyp")) {
		return errors.New("ftyp box not found")
	}

	ftypSize := uint32(head[0])<<24 | uint32(head[1])<<16 | uint32(head[2])<<8 | uint32(head[3])
	if _, err := f.Seek(int64(ftypSize), io.SeekStart); err != nil {
		return err
	}

	if _, err := io.ReadFull(f, head); err != nil {
		return err
	}

	if !bytes.Equal(head[4:], []byte("moov")) {
		return errors.New("moov box not found")
	}

	moovSize := uint32(head[0])<<24 | uint32(head[1])<<16 | uint32(head[2])<<8 | uint32(head[3])

	mvhdAt, err := f.Seek(8, io.SeekCurrent)
	if err != nil {
		return err
	}

	var mvhd amp4.Mvhd
	if _, err := amp4.Unmarshal(f, uint64(moovSize-8), &mvhd, amp4.Context{}); err != nil {
		return err
	}

	mvhd.DurationV0 = uint32(min(d/time.Millisecond, time.Duration(^uint32(0)))) //nolint:gosec // Bounded above.

	if _, err := f.Seek(mvhdAt, io.SeekStart); err != nil {
		return err
	}

	_, err = amp4.Marshal(f, &mvhd, amp4.Context{})

	return err
}
