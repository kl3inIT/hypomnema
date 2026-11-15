package browser

// The two programs the engine runs, each pinned to one version with the
// checksum of its release file, as a node's tools are
// (internal/shared/agenttool/catalog.go).

// release is one system's release file of a program.
type release struct {
	URL    string
	SHA256 string
	// Bytes is the file's size, checked before its checksum.
	Bytes int64
	// Binary is the path of the command inside the unpacked file; empty when
	// the file is the command itself.
	Binary string
}

// program is a pinned program with a release file for each system, by
// "os/arch".
type program struct {
	Name     string
	Version  string
	Releases map[string]release
}

// agentBrowser is agent-browser (github.com/vercel-labs/agent-browser,
// Apache-2.0), the engine OpenMausBot chose for its bots
// (docs/plans/browser-engine.md, server/browser-engine.ts): one executable
// that drives Chrome over CDP. The sizes and checksums are the ones GitHub
// publishes for the release's files, read on 2026-11-12; the linux/amd64 file
// was run on zm that day.
func agentBrowser() program {
	const (
		version = "0.42.0"
		base    = "https://github.com/vercel-labs/agent-browser/releases/download/v" + version + "/"
	)

	return program{
		Name: "agent-browser", Version: version,
		Releases: map[string]release{
			"linux/amd64": {
				URL:    base + "agent-browser-linux-x64",
				SHA256: "b69e0f5ea76dcb51f8f75da6bcb87c7f4d81f79b5738f1f08bd121b51cb3dc57",
				Bytes:  20_373_120,
			},
			"linux/arm64": {
				URL:    base + "agent-browser-linux-arm64",
				SHA256: "a219b10251ecd85e5787d1126dc969d926ac8d2dea325f9d94413907a8856f40",
				Bytes:  17_751_832,
			},
		},
	}
}

// chromeForTesting is Chrome for Testing's full browser, which agent-browser
// runs with `--headless=new`, pinned as OpenMausBot pins it
// (server/browser/chrome/install.ts): a version with the size and SHA-256 of
// each system's .zip, checked before it is unpacked. Not its headless shell,
// which "cannot be made to look like Chrome" (server/browser/chrome/resolve.ts,
// isHeadlessShell; issue #540).
//
// Google publishes no checksum for these files. The version is the Stable
// channel's of 2026-11-15
// (googlechromelabs.github.io/chrome-for-testing/last-known-good-versions.json),
// and the sizes and checksums are of the files as fetched from Google's
// storage that day. To move the pin: fetch each .zip, and write its size and
// `sha256sum` here. T3 Code says never to pin below 156.0.8078.70
// (apps/server/src/preview/PreviewBrowser.ts): older builds trust compromised
// intermediates. agent-browser warns in a tool's answer when the Chrome it
// drives is behind the stable release.
func chromeForTesting() program {
	const (
		version = "157.0.8103.78"
		base    = "https://storage.googleapis.com/chrome-for-testing-public/" + version + "/"
	)

	return program{
		Name: "chrome", Version: version,
		Releases: map[string]release{
			"linux/amd64": {
				URL:    base + "linux64/chrome-linux64.zip",
				SHA256: "9223832977a13db3646201b1341c1f9cf08afa3ceae92540116d7b0c062364fb",
				Bytes:  203_266_906,
				Binary: "chrome-linux64/chrome",
			},
			"linux/arm64": {
				URL:    base + "linux-arm64/chrome-linux-arm64.zip",
				SHA256: "f354d666822a11d8fe99e923c44be77dd226fab2750c5b5947e6318818bf10f1",
				Bytes:  203_144_198,
				Binary: "chrome-linux-arm64/chrome",
			},
		},
	}
}
