package device

import "time"

// A tiny H.264 encoder for the tests: a stream any decoder reads, made of
// what the standard allows without any compression (ITU-T H.264, 7.3): a
// Baseline SPS and PPS, key frames (IDR) whose every macroblock is I_PCM, its
// pixels as they are, and frames after them (P) whose every macroblock is
// skipped, the picture before repeated. It stands for scrcpy's encoder: the
// node only moves its packets, and ffprobe, ffmpeg and a browser decode them.

type bitWriter struct {
	out  []byte
	cur  byte
	bits int
}

func (w *bitWriter) bit(b uint32) {
	w.cur = w.cur<<1 | byte(b&1)
	w.bits++

	if w.bits == 8 {
		w.out = append(w.out, w.cur)
		w.cur, w.bits = 0, 0
	}
}

func (w *bitWriter) u(n int, v uint32) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

// ue writes an Exp-Golomb ue(v).
func (w *bitWriter) ue(v uint32) {
	v++
	n := 0

	for x := v; x > 1; x >>= 1 {
		n++
	}

	w.u(n, 0)
	w.u(n+1, v)
}

func (w *bitWriter) se(v int32) {
	if v <= 0 {
		w.ue(uint32(-2 * v)) //nolint:gosec // Small test values.
	} else {
		w.ue(uint32(2*v - 1)) //nolint:gosec // Small test values.
	}
}

// align writes zero bits to the next byte.
func (w *bitWriter) align() {
	for w.bits != 0 {
		w.bit(0)
	}
}

// trailing writes rbsp_trailing_bits.
func (w *bitWriter) trailing() []byte {
	w.bit(1)
	w.align()

	return w.out
}

// nal is a NAL unit in Annex B, with emulation prevention.
func nal(header byte, rbsp []byte) []byte {
	out := []byte{0, 0, 0, 1, header}
	zeros := 0

	for _, b := range rbsp {
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}

		out = append(out, b)

		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}

	return out
}

// testVideo encodes pictures of one size.
type testVideo struct {
	width, height int
	frame         uint32
	idr           uint32
}

func (v *testVideo) mbs() int { return (v.width / 16) * (v.height / 16) }

// config is the SPS and PPS, as scrcpy sends a configuration packet.
func (v *testVideo) config() []byte {
	var sps bitWriter

	sps.u(8, 66)                    // profile_idc: Baseline
	sps.u(8, 0)                     // constraint flags
	sps.u(8, 30)                    // level_idc
	sps.ue(0)                       // seq_parameter_set_id
	sps.ue(0)                       // log2_max_frame_num_minus4: frame_num in 4 bits
	sps.ue(2)                       // pic_order_cnt_type: output in decoding order
	sps.ue(1)                       // max_num_ref_frames
	sps.u(1, 0)                     // gaps_in_frame_num_value_allowed_flag
	sps.ue(uint32(v.width/16 - 1))  //nolint:gosec // A test size.
	sps.ue(uint32(v.height/16 - 1)) //nolint:gosec // A test size.
	sps.u(1, 1)                     // frame_mbs_only_flag
	sps.u(1, 1)                     // direct_8x8_inference_flag
	sps.u(1, 0)                     // frame_cropping_flag
	sps.u(1, 0)                     // vui_parameters_present_flag

	var pps bitWriter

	pps.ue(0)   // pic_parameter_set_id
	pps.ue(0)   // seq_parameter_set_id
	pps.u(1, 0) // entropy_coding_mode_flag: CAVLC
	pps.u(1, 0) // bottom_field_pic_order_in_frame_present_flag
	pps.ue(0)   // num_slice_groups_minus1
	pps.ue(0)   // num_ref_idx_l0_default_active_minus1
	pps.ue(0)   // num_ref_idx_l1_default_active_minus1
	pps.u(1, 0) // weighted_pred_flag
	pps.u(2, 0) // weighted_bipred_idc
	pps.se(0)   // pic_init_qp_minus26
	pps.se(0)   // pic_init_qs_minus26
	pps.se(0)   // chroma_qp_index_offset
	pps.u(1, 1) // deblocking_filter_control_present_flag
	pps.u(1, 0) // constrained_intra_pred_flag
	pps.u(1, 0) // redundant_pic_cnt_present_flag

	return append(nal(0x67, sps.trailing()), nal(0x68, pps.trailing())...)
}

// key is a key frame of one shade: every macroblock I_PCM.
func (v *testVideo) key(shade byte) []byte {
	var w bitWriter

	w.ue(0)                // first_mb_in_slice
	w.ue(7)                // slice_type: I, every slice
	w.ue(0)                // pic_parameter_set_id
	w.u(4, 0)              // frame_num
	w.ue(v.idr % 2)        // idr_pic_id: two IDRs in a row differ
	w.u(1, 0)              // no_output_of_prior_pics_flag
	w.u(1, 0)              // long_term_reference_flag
	w.se(0)                // slice_qp_delta
	w.ue(1)                // disable_deblocking_filter_idc
	luma := max(shade, 16) // never 0, which PCM samples once could not be
	pixels := make([]byte, 256+128)

	for i := range pixels {
		pixels[i] = 128
	}

	for i := range 256 {
		pixels[i] = luma
	}

	for range v.mbs() {
		w.ue(25) // mb_type: I_PCM
		w.align()

		for _, b := range pixels {
			w.u(8, uint32(b))
		}
	}

	v.idr++
	v.frame = 0

	return nal(0x65, w.trailing())
}

// delta is a frame after a key frame: every macroblock skipped.
func (v *testVideo) delta() []byte {
	v.frame++

	var w bitWriter

	w.ue(0)               // first_mb_in_slice
	w.ue(5)               // slice_type: P, every slice
	w.ue(0)               // pic_parameter_set_id
	w.u(4, v.frame%16)    // frame_num
	w.u(1, 0)             // num_ref_idx_active_override_flag
	w.u(1, 0)             // ref_pic_list_modification_flag_l0
	w.u(1, 0)             // adaptive_ref_pic_marking_mode_flag
	w.se(0)               // slice_qp_delta
	w.ue(1)               // disable_deblocking_filter_idc
	w.ue(uint32(v.mbs())) //nolint:gosec // mb_skip_run: every macroblock.

	return nal(0x41, w.trailing())
}

// The packets of a test stream, as scrcpy sends them: PTS in microseconds.
func configPacket(v *testVideo) packet { return packet{Config: true, Data: v.config()} }

func keyPacket(v *testVideo, pts time.Duration) packet {
	return packet{Key: true, PTS: uint64(pts.Microseconds()), Data: v.key(200)} //nolint:gosec // Never negative.
}

func deltaPacket(v *testVideo, pts time.Duration) packet {
	return packet{PTS: uint64(pts.Microseconds()), Data: v.delta()} //nolint:gosec // Never negative.
}
