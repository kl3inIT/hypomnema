package device

// The programs and the phone image the node downloads, each pinned to one
// version with the size and checksum of its file, as the shared browser's
// are (internal/controlplane/browser/release.go). They are fetched from
// their publishers by the node itself and never redistributed: the Android
// SDK's license (developer.android.com/studio/terms) applies to the SDK
// parts, and scrcpy's Apache-2.0 to scrcpy-server.
//
// The sizes and checksums are of the files as fetched on 2026-11-14 on the
// trial machine of issue #417; for the SDK parts their SHA-1 matched the one
// Google's repository XML gives (dl.google.com/android/repository/repository2-3.xml
// and its system images' list), which publishes no SHA-256.
// To move a pin: fetch the file, check its SHA-1 against that XML, and write
// its size and `sha256sum` here.
//
// cmdline-tools are not installed: the emulator boots from these folders
// alone with an AVD written by hand (avd.go), with no package.xml, sdkmanager,
// avdmanager or Java (tried on 2026-11-14).

// component is one pinned file and where it goes under the device's
// directory.
type component struct {
	Name    string
	Version string
	URL     string
	SHA256  string
	// Bytes is the file's size, checked before its checksum.
	Bytes int64
	// Root is the folder the .zip holds everything under; empty when the
	// file is not a .zip and is put in place as it is.
	Root string
	// Place is where the component goes, relative to the device's directory:
	// the folder Root becomes, or the file itself.
	Place string
	// Executable marks a file put in place as it is that is a command.
	Executable bool
	// MaxUnpacked bounds what the .zip unpacks to.
	MaxUnpacked int64
}

// The components' names, as the node reports their versions.
const (
	namePlatformTools = "platform-tools"
	nameEmulator      = "emulator"
	nameSystemImage   = "system-image"
	nameAndroidCLI    = "android-cli"
	nameScrcpyServer  = "scrcpy-server"
)

// The places that other parts of the package run things from.
const (
	sdkDir         = "sdk"
	systemImageDir = sdkDir + "/system-images/android-34/google_apis_playstore/x86_64"
	adbPath        = sdkDir + "/platform-tools/adb"
	emulatorPath   = sdkDir + "/emulator/emulator"
	androidCLIPath = "bin/android"
	// ScrcpyServerPath is where scrcpy-server is kept, for the owner's view
	// (issue #417, step 3).
	ScrcpyServerPath = "bin/scrcpy-server"
)

// components are what a device of this release has, in the order they are
// installed: the small ones first, so that a failure shows soon.
func components() []component {
	return []component{
		{
			// Stable channel.
			Name: namePlatformTools, Version: "37.0.1",
			URL:    "https://dl.google.com/android/repository/platform-tools_r37.0.1-linux.zip",
			SHA256: "d230f13842f60f782a8645f9c813f8f845bf36089ea7289f28c48f17979313f1", Bytes: 9_054_187,
			Root: "platform-tools", Place: sdkDir + "/platform-tools", MaxUnpacked: 256 << 20,
		},
		{
			// Android CLI (developer.android.com/tools/agents/android-cli), which agents drive the phone
			// with in step 2. Google publishes only a "latest" address and no checksum for it: the pin is the
			// build tried on 2026-11-14, and a newer build behind the same address is refused until the pin is
			// moved.
			Name: nameAndroidCLI, Version: "1.1.16919734",
			URL:    "https://dl.google.com/android/cli/latest/linux_x86_64/android",
			SHA256: "2aabb936c5e67d580ba164a9ee6c01e5142db55e2cd567b5c57a25d92b1b84cf", Bytes: 5_042_944,
			Place: androidCLIPath, Executable: true,
		},
		{
			// The server half of scrcpy (github.com/Genymobile/scrcpy, Apache-2.0), which the owner's view
			// pushes to the phone in step 3; Orca pins it the same way (scrcpy-server-deploy.ts).
			Name: nameScrcpyServer, Version: "3.3.4",
			URL:    "https://github.com/Genymobile/scrcpy/releases/download/v3.3.4/scrcpy-server-v3.3.4",
			SHA256: "8588238c9a5a00aa542906b6ec7e6d5541d9ffb9b5d0f6e1bc0e365e2303079e", Bytes: 90_980,
			Place: ScrcpyServerPath,
		},
		{
			// The beta channel's 37.3.9: the stable 37.2.13 crashed as the owner's map app opened its tabs
			// (docs/research/2026-11-14-android-device-for-agents.md, section 2).
			Name: nameEmulator, Version: "37.3.9",
			URL:    "https://dl.google.com/android/repository/emulator-linux_x64-16863232.zip",
			SHA256: "887266c9e0a4b6e3449a57ae5392a9e18cec74f05d4d201c01d19aba82492e5c", Bytes: 350_070_672,
			Root: "emulator", Place: sdkDir + "/emulator", MaxUnpacked: 2 << 30,
		},
		{
			// Android 14 (API 34) with Google Play, x86_64, revision 14: Play and a signed-in Google account
			// are what the owner's apps need (issue #417).
			Name: nameSystemImage, Version: "android-34-google_apis_playstore-x86_64-r14",
			URL:    "https://dl.google.com/android/repository/sys-img/google_apis_playstore/x86_64-34_r14.zip",
			SHA256: "22e7797a7e14d346bc6ed867d84ed423beaf4364db567edaa97f85a99558cf80", Bytes: 1_510_752_654,
			Root: "x86_64", Place: systemImageDir, MaxUnpacked: 12 << 30,
		},
	}
}
