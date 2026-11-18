package device

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"
)

// One segment of a take (issue #817, rows 1 to 3), written with
// bluenviron/mediacommon (MIT; notice in third_party/mediacommon) as
// MediaMTX's recorder and playback write theirs (MIT; notice in
// third_party/mediamtx). While it is filmed it is a fragmented MP4, an init
// with the segment's one configuration (SPS and PPS) and fragments flushed
// by size (internal/recorder/format_fmp4*.go): Annex B made AVCC
// (format_fmp4.go:451-465), a timescale of 90 kHz, DTS equal to PTS since
// scrcpy's encoder writes no B-frames (scrcpy app/src/recorder.c:385-386),
// each sample's duration the next one's PTS less its own (recorder.c:388-391),
// a PTS that does not grow moved one tick past the one before
// (recorder.c:106-113). The last frame lasts until the segment closes, so a
// still screen at its end counts (agent-demo-video src/capture.ts:321-335).
//
// As it closes, the fragments are muxed into a regular MP4 whose sample table
// holds every sample's duration, as MediaMTX's playback serves its fMP4
// recordings as MP4 (internal/playback/muxer_mp4.go, with mediacommon's
// pmp4): Chromium could not seek into the last sample of the fragmented file,
// whose durations its demuxer does not read, and so not into a still tail
// (tried with Playwright's Chromium on 2026-11-18). The fragments' file goes
// once the segment is written; a node that ends meanwhile leaves it, and the
// next node removes it (take.go, recoverTakes).

const (
	// segmentTimescale is the video track's timescale, MediaMTX's for video.
	segmentTimescale = 90_000
	// partBytes is how many bytes of samples a fragment holds before it is written: fragments are flushed by size,
	// as MediaMTX bounds its parts (format_fmp4_part.go:60-64), so a take holds at most this much in memory.
	partBytes = 1 << 20
	// maxSegmentBytes bounds a segment: an MP4's chunk offsets are 32 bits.
	maxSegmentBytes = 1<<32 - 1<<20
	// fragmentsSuffix names the file of a segment's fragments beside it.
	fragmentsSuffix = ".fmp4"
)

// segmentInfo is what a closed segment is: its file's name, length and size, and its sum.
type segmentInfo struct {
	File          string
	Length        time.Duration
	LastFrame     time.Duration
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

// written is where a sample is in the fragments' file, for the MP4 made from them.
type written struct {
	offset   int64
	size     uint32
	duration uint32
	key      bool
}

// segmentWriter writes one segment's file.
type segmentWriter struct {
	f      *os.File
	path   string
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
	at       int64
	samples  []written
	total    int64
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

// createSegment creates the file of a segment's fragments, private to the
// node's user, and writes its init with the configuration. The segment's MP4
// is path once it closes.
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

	//nolint:gosec // The take's own folder.
	f, err := os.OpenFile(path+fragmentsSuffix, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}

	w := &segmentWriter{f: f, path: path, name: name, sps: sps, pps: pps, size: size, config: config}

	if err := w.out(buf.Bytes()); err != nil {
		_ = f.Close()

		return nil, err
	}

	return w, nil
}

func (w *segmentWriter) out(b []byte) error {
	n, err := w.f.Write(b)
	w.at += int64(n)

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

	w.total += int64(len(avcc))
	if w.total > maxSegmentBytes {
		return fmt.Errorf("a segment holds at most %d MB", maxSegmentBytes>>20)
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
	duration := uint32(min(max(end-s.ticks, 1), uint64(^uint32(0))))

	if len(w.part) == 0 {
		w.partBase = s.ticks
	}

	w.part = append(w.part, &fmp4.Sample{Duration: duration, IsNonSyncSample: !s.key, Payload: s.data})
	w.partSize += len(s.data)

	if w.partSize >= partBytes {
		return w.flush()
	}

	return nil
}

// flush writes the fragment held, and where its samples are.
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

	// The samples' payloads end the fragment, in order, in its mdat.
	offset := w.at + int64(len(buf.Bytes())-w.partSize)
	for _, s := range w.part {
		w.samples = append(w.samples, written{
			offset: offset, size: uint32(len(s.Payload)), //nolint:gosec // A frame is at most 16 MiB.
			duration: s.Duration, key: !s.IsNonSyncSample,
		})
		offset += int64(len(s.Payload))
	}

	w.part, w.partSize = nil, 0

	return w.out(buf.Bytes())
}

// close ends the segment at length from its first frame: the last frame
// lasts until then. It writes the segment's MP4 from the fragments, syncs it
// and sums it. A segment that got no frame is removed and is no segment.
func (w *segmentWriter) close(length time.Duration) (segmentInfo, bool, error) {
	defer func() { _ = os.Remove(w.f.Name()) }() //nolint:gosec // The take's own folder.

	if !w.started {
		_ = w.f.Close()

		return segmentInfo{}, false, nil
	}

	end := uint64(max(length, 0)) * segmentTimescale / uint64(time.Second)

	var last uint64

	if w.pending != nil {
		if end <= w.pending.ticks {
			end = w.pending.ticks + 1
		}

		last = w.pending.ticks

		if err := w.add(w.pending, end); err != nil {
			return segmentInfo{}, true, w.fail(err)
		}

		w.pending = nil
	}

	if err := w.flush(); err != nil {
		return segmentInfo{}, true, w.fail(err)
	}

	info, err := w.mux()
	if closeErr := w.f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		_ = os.Remove(w.path)

		return segmentInfo{}, true, err
	}

	// Ticks of a take, far below 2^63.
	info.Length = time.Duration(end) * time.Second / segmentTimescale //nolint:gosec // See above.
	info.LastFrame = time.Duration(last) * time.Second / segmentTimescale

	return info, true, nil
}

// mux writes the segment's MP4 from its fragments, as MediaMTX's playback
// muxes a recording (muxer_mp4.go): one track, every sample read from the
// fragments' file as it is written.
func (w *segmentWriter) mux() (segmentInfo, error) {
	samples := make([]*pmp4.Sample, 0, len(w.samples))

	for _, s := range w.samples {
		samples = append(samples, &pmp4.Sample{
			Duration: s.duration, IsNonSyncSample: !s.key, PayloadSize: s.size,
			GetPayload: func() ([]byte, error) {
				buf := make([]byte, s.size)
				_, err := w.f.ReadAt(buf, s.offset)

				return buf, err
			},
		})
	}

	out, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return segmentInfo{}, err
	}

	sum := sha256.New()
	counted := &countingWriter{w: io.MultiWriter(out, sum)}

	presentation := pmp4.Presentation{Tracks: []*pmp4.Track{{
		ID: 1, TimeScale: segmentTimescale, Codec: &codecs.H264{SPS: w.sps, PPS: w.pps}, Samples: samples,
	}}}

	err = presentation.Marshal(counted)
	if err == nil {
		err = out.Sync()
	}

	if closeErr := out.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return segmentInfo{}, fmt.Errorf("writing the segment's MP4: %w", err)
	}

	return segmentInfo{
		File: w.name, Width: w.size.Width, Height: w.size.Height, Bytes: counted.n,
		SHA256: hex.EncodeToString(sum.Sum(nil)),
	}, nil
}

// fail closes the fragments' file after an error, which it returns.
func (w *segmentWriter) fail(err error) error {
	_ = w.f.Close()

	return err
}

// countingWriter counts what it writes.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)

	return n, err
}
