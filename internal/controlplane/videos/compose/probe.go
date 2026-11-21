package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

// Probe is what ffprobe says of a file's container and streams: the format's names, as ffprobe's format_name lists
// them (mp3; mov,mp4,m4a,3gp,3g2,mj2; wav; flac; ogg), and each stream's type and codec.
type Probe struct {
	Format string
	// Brand is an ISO base media file's major brand, as ffprobe reads its ftyp (M4A, isom, mp42, qt for QuickTime):
	// the containers ffprobe names alike (mov,mp4,m4a,…) are told apart by it.
	Brand   string
	Streams []Stream
}

// Stream is one stream of a probed file.
type Stream struct {
	// Type is ffprobe's codec_type: audio, video, subtitle, data.
	Type string
	// Codec is ffprobe's codec_name, such as mp3, aac, pcm_s16le, flac, vorbis, opus or alac.
	Codec string
	// AttachedPicture is a cover picture (disposition attached_pic), a video stream of one frame that is no video.
	AttachedPicture bool
}

// ProbeFile asks ffprobe what a file holds (issue #910, row 6b: the music library types a track by ffprobe, since
// Go's sniffing misses m4a, flac and an mp3 without ID3, as meetings found).
func ProbeFile(ctx context.Context, tools ffmpeg.Tools, file string) (Probe, error) {
	out, err := runner{tools: tools}.run(ctx, tools.FFprobe, "-v", "error", "-print_format", "json",
		"-show_entries", "format=format_name:format_tags=major_brand:stream=codec_type,codec_name:"+
			"stream_disposition=attached_pic", file)
	if err != nil {
		return Probe{}, err
	}

	var raw struct {
		Format struct {
			FormatName string `json:"format_name"`
			Tags       struct {
				MajorBrand string `json:"major_brand"`
			} `json:"tags"`
		} `json:"format"`
		Streams []struct {
			CodecType   string `json:"codec_type"`
			CodecName   string `json:"codec_name"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(out, &raw); err != nil {
		return Probe{}, fmt.Errorf("reading what ffprobe says of %s: %w", file, err)
	}

	p := Probe{
		Format: raw.Format.FormatName, Brand: strings.TrimSpace(raw.Format.Tags.MajorBrand),
		Streams: make([]Stream, 0, len(raw.Streams)),
	}
	for _, s := range raw.Streams {
		p.Streams = append(p.Streams, Stream{
			Type: s.CodecType, Codec: s.CodecName, AttachedPicture: s.Disposition.AttachedPic == 1,
		})
	}

	return p, nil
}
