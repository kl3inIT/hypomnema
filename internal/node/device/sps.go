package device

import "errors"

// The picture's size from the SPS of an Annex-B H.264 configuration (ITU-T
// H.264 section 7.3.2.1.1), adapted from Orca's
// src/main/emulator/android/h264-sps-picture-size.ts (MIT, Copyright (c) 2026
// Lovecast Inc.; notice in third_party/orca). scrcpy says the video's size
// once, at the stream's start; after the phone turns, the new size is only in
// the next SPS, and a touch must carry the video's size.

const nalSPS = 7

// highProfiles are the profiles whose SPS carries the chroma format, the bit
// depths and the scaling matrices.
var highProfiles = map[uint32]bool{
	100: true, 110: true, 122: true, 244: true, 44: true, 83: true, 86: true, 118: true, 128: true, 138: true,
	139: true, 134: true, 135: true,
}

var errSPSShort = errors.New("the SPS ended early")

type bitReader struct {
	b   []byte
	off int
}

func (r *bitReader) bit() (uint32, error) {
	i := r.off >> 3
	if i >= len(r.b) {
		return 0, errSPSShort
	}

	v := uint32(r.b[i]>>(7-uint(r.off&7))) & 1
	r.off++

	return v, nil
}

func (r *bitReader) bits(n int) (uint32, error) {
	var v uint32

	for range n {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}

		v = v<<1 | b
	}

	return v, nil
}

// ue is an Exp-Golomb ue(v).
func (r *bitReader) ue() (uint32, error) {
	zeros := 0

	for {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}

		if b == 1 {
			break
		}

		if zeros++; zeros > 31 {
			return 0, errors.New("an Exp-Golomb code that is not one")
		}
	}

	rest, err := r.bits(zeros)

	return (1<<zeros - 1) + rest, err
}

// se is an Exp-Golomb se(v).
func (r *bitReader) se() (int64, error) {
	v, err := r.ue()
	if v%2 == 1 {
		return int64(v+1) / 2, err
	}

	return -int64(v) / 2, err
}

// spsRBSP is the first SPS of an Annex-B stream, without its emulation
// prevention bytes.
func spsRBSP(annexB []byte) []byte {
	for i := 0; i+3 < len(annexB); i++ {
		if annexB[i] != 0 || annexB[i+1] != 0 || annexB[i+2] != 1 || annexB[i+3]&0x1f != nalSPS {
			continue
		}

		var (
			out   []byte
			zeros int
		)

		for _, b := range annexB[i+4:] {
			if zeros >= 2 && b == 1 {
				break
			}

			if zeros >= 2 && b == 3 {
				zeros = 0

				continue
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

	return nil
}

// spsSize is the cropped picture size an SPS says; false when the
// configuration has none that reads.
func spsSize(annexB []byte) (screenSize, bool) {
	rbsp := spsRBSP(annexB)
	if rbsp == nil {
		return screenSize{}, false
	}

	size, err := readPictureSize(&bitReader{b: rbsp})
	if err != nil || size.Width <= 0 || size.Height <= 0 {
		return screenSize{}, false
	}

	return size, true
}

//nolint:gocognit,gocyclo,cyclop,funlen // The SPS's fields, in their order.
func readPictureSize(r *bitReader) (screenSize, error) {
	profile, err := r.bits(8)
	if err != nil {
		return screenSize{}, err
	}

	// constraint_set flags and level_idc, then seq_parameter_set_id.
	if _, err := r.bits(16); err != nil {
		return screenSize{}, err
	}

	if _, err := r.ue(); err != nil {
		return screenSize{}, err
	}

	chroma, separate := uint32(1), uint32(0)

	if highProfiles[profile] {
		if chroma, err = r.ue(); err != nil {
			return screenSize{}, err
		}

		if chroma == 3 {
			if separate, err = r.bit(); err != nil {
				return screenSize{}, err
			}
		}

		// bit_depth_luma_minus8, bit_depth_chroma_minus8, qpprime_y_zero_transform_bypass_flag.
		for range 2 {
			if _, err := r.ue(); err != nil {
				return screenSize{}, err
			}
		}

		if _, err := r.bit(); err != nil {
			return screenSize{}, err
		}

		matrix, err := r.bit()
		if err != nil {
			return screenSize{}, err
		}

		if matrix == 1 {
			lists := 8
			if chroma == 3 {
				lists = 12
			}

			if err := skipScalingLists(r, lists); err != nil {
				return screenSize{}, err
			}
		}
	}

	// log2_max_frame_num_minus4.
	if _, err := r.ue(); err != nil {
		return screenSize{}, err
	}

	pocType, err := r.ue()
	if err != nil {
		return screenSize{}, err
	}

	switch pocType {
	case 0:
		if _, err := r.ue(); err != nil {
			return screenSize{}, err
		}
	case 1:
		if _, err := r.bit(); err != nil {
			return screenSize{}, err
		}

		for range 2 {
			if _, err := r.se(); err != nil {
				return screenSize{}, err
			}
		}

		cycle, err := r.ue()
		if err != nil {
			return screenSize{}, err
		}

		for range cycle {
			if _, err := r.se(); err != nil {
				return screenSize{}, err
			}
		}
	}

	// max_num_ref_frames, gaps_in_frame_num_value_allowed_flag.
	if _, err := r.ue(); err != nil {
		return screenSize{}, err
	}

	if _, err := r.bit(); err != nil {
		return screenSize{}, err
	}

	widthMbs, err := r.ue()
	if err != nil {
		return screenSize{}, err
	}

	heightUnits, err := r.ue()
	if err != nil {
		return screenSize{}, err
	}

	frameOnly, err := r.bit()
	if err != nil {
		return screenSize{}, err
	}

	if frameOnly == 0 {
		if _, err := r.bit(); err != nil {
			return screenSize{}, err
		}
	}

	// direct_8x8_inference_flag, then the cropping.
	if _, err := r.bit(); err != nil {
		return screenSize{}, err
	}

	crop, err := r.bit()
	if err != nil {
		return screenSize{}, err
	}

	var cropX, cropY int

	if crop == 1 {
		arrayType := chroma
		if separate == 1 {
			arrayType = 0
		}

		unitX, unitY := 1, 1
		if arrayType == 1 || arrayType == 2 {
			unitX = 2
		}

		if arrayType == 1 {
			unitY = 2
		}

		unitY *= 2 - int(frameOnly)

		var edges [4]uint32

		for i := range edges {
			if edges[i], err = r.ue(); err != nil {
				return screenSize{}, err
			}
		}

		cropX = int(edges[0]+edges[1]) * unitX
		cropY = int(edges[2]+edges[3]) * unitY
	}

	return screenSize{
		Width:  int(widthMbs+1)*16 - cropX,
		Height: int(heightUnits+1)*(2-int(frameOnly))*16 - cropY,
	}, nil
}

func skipScalingLists(r *bitReader, count int) error {
	for list := range count {
		present, err := r.bit()
		if err != nil {
			return err
		}

		if present == 0 {
			continue
		}

		size := 64
		if list < 6 {
			size = 16
		}

		last, next := int64(8), int64(8)

		for range size {
			if next != 0 {
				delta, err := r.se()
				if err != nil {
					return err
				}

				next = (last + delta + 256) % 256
			}

			if next != 0 {
				last = next
			}
		}
	}

	return nil
}
