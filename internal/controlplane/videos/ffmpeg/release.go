package ffmpeg

// The ffmpeg the control plane makes videos with (issue #818, row 9), pinned
// to one build with the size and SHA-256 of each system's file, as a node pins
// scrcpy-server (internal/node/device/release.go) and the shared browser its
// programs (internal/controlplane/browser/release.go).
//
// The build is BtbN/FFmpeg-Builds' (github.com/BtbN/FFmpeg-Builds), the
// static builds FFmpeg's download page links for Linux: its "gpl" variant has
// libx264 and libass, and links nothing but glibc. FFmpeg built so is under
// the GPL version 3 (the file's LICENSE.txt), which allows it to be
// redistributed; the control plane fetches it from its publisher itself and
// keeps the license beside the programs. The release branch is 9.0; GitHub
// keeps a daily build about two years (the oldest listed on 2026-11-18 was of
// 2024-12-31).
//
// The sizes and checksums are the ones GitHub publishes for the files (the
// assets' digest), read on 2026-11-18; the linux/amd64 file was fetched and
// matched that day. To move the pin: pick a newer autobuild's
// ffmpeg-n<version>-linux64-gpl-<branch>.tar.xz and linuxarm64 file, and write
// their size and digest here
// (`gh api repos/BtbN/FFmpeg-Builds/releases/tags/<tag> --jq '.assets[]'`).

// release is one system's file of the build.
type release struct {
	URL    string
	SHA256 string
	// Bytes is the file's size, checked before its checksum.
	Bytes int64
	// Root is the folder the .tar.xz holds everything under.
	Root string
}

// build is the pinned build, with a file for each system, by "os/arch".
type build struct {
	Version  string
	Releases map[string]release
}

// maxUnpackedBytes bounds what is taken from the file: ffmpeg and ffprobe are
// about 176 MB each on linux/amd64.
const maxUnpackedBytes = 1 << 30

func pinned() build {
	const (
		version = "n9.0.2-57-gd838f9f9ae"
		base    = "https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-11-18-12-39/"
		amd64   = "ffmpeg-" + version + "-linux64-gpl-9.0"
		arm64   = "ffmpeg-" + version + "-linuxarm64-gpl-9.0"
	)

	return build{
		Version: version,
		Releases: map[string]release{
			"linux/amd64": {
				URL:    base + amd64 + ".tar.xz",
				SHA256: "8c408fd01c775bcddea564a03a4de2ac12724b568f3308d391f0a65675c7527a",
				Bytes:  153_904_648,
				Root:   amd64,
			},
			"linux/arm64": {
				URL:    base + arm64 + ".tar.xz",
				SHA256: "5ecf7612b57fe4621cd6d583d3153bfd1a8dce1011d5b358dbb104febbc611b6",
				Bytes:  129_382_848,
				Root:   arm64,
			},
		},
	}
}
