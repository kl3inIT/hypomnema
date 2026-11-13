package agenttool

import "github.com/kl3inIT/hypomnema/internal/shared/nodeproto"

// The agent CLIs the node drives over ACP, put on every node by the control
// plane as it puts an Integration's tool (issue #107): pinned to one version,
// a release file per system with its SHA-256, installed under the node's home
// without root. A new version is a change here, released with the control
// plane; a node never follows a vendor's latest by itself. The versions are
// the ones that ran real Tasks through the gateway (#107, 2026-11-10).

// Runtimes are the agent CLIs every node is told to have.
func Runtimes() []Def { return []Def{Omp(), Opencode()} }

// RuntimeTools are the release files of the runtimes for a system, as a node
// installs them; a runtime with none for that system is left out.
func RuntimeTools(os, arch string) []nodeproto.Tool {
	var out []nodeproto.Tool

	for _, d := range Runtimes() {
		if tool, ok := d.For(os, arch); ok {
			out = append(out, tool)
		}
	}

	return out
}

// Omp is oh-my-pi's coding agent (github.com/can1357/oh-my-pi, MIT). Each
// release has one compiled binary per system, its linux-x64 built for CPUs
// without AVX2, and a SHA256SUMS.txt they are checked against
// (scripts/ci-release-build-binaries.ts, ci-release-checksums.ts).
func Omp() Def {
	const (
		version = "18.18.6"
		release = "https://github.com/can1357/oh-my-pi/releases/download/v" + version + "/omp-"
	)

	return Def{
		Name: "omp", Version: version,
		Releases: map[string]Release{
			"linux/amd64": {
				URL: release + "linux-x64", Binary: "omp-linux-x64",
				SHA256: "a8059b95de5eaba0b3fb6f7182842d661feae29cb2931cdbeba2986db4751ecb",
			},
			"linux/arm64": {
				URL: release + "linux-arm64", Binary: "omp-linux-arm64",
				SHA256: "aa9d69380f7ded098bcd3d8023a67edcc8fc3cc0529360a98dededc21cfa101b",
			},
			"darwin/amd64": {
				URL: release + "darwin-x64", Binary: "omp-darwin-x64",
				SHA256: "836fea57b1dfcf53c26053aef3d57b6d1f83ec034a7f8b25d789022c5adbc365",
			},
			"darwin/arm64": {
				URL: release + "darwin-arm64", Binary: "omp-darwin-arm64",
				SHA256: "ed2e63eb43888eb76cb1409b245b1db4557c94dfad52dc545c9aaca01ebce82d",
			},
		},
	}
}

// Opencode is opencode version 2 (github.com/anomalyco/opencode, MIT), from
// the npm packages its own installer fetches, @opencode/cli-<os>-<arch>
// (opencode.ai/v2/install). Linux x64 takes the baseline build, which needs
// no AVX2, as the installer does on such a CPU. npm gives no SHA-256: these
// were computed from the registry's files when pinned.
func Opencode() Def {
	const (
		version = "2.2.7"
		npm     = "https://registry.npmjs.org/@opencode/cli-"
	)

	file := func(target string) string { return npm + target + "/-/cli-" + target + "-" + version + ".tgz" }

	return Def{
		Name: "opencode", Version: version,
		Releases: map[string]Release{
			"linux/amd64": {
				URL: file("linux-x64-baseline"), Binary: "package/bin/opencode",
				SHA256: "20512b0c5c5c30c51959ce13e11b19a630ce3b5bef56b486859bf38f8b27bcae",
			},
			"linux/arm64": {
				URL: file("linux-arm64"), Binary: "package/bin/opencode",
				SHA256: "7e74ceb64fe5704ff2fdef3dee45866efcb580d71f182a6cae29606cad1f8b52",
			},
			"darwin/amd64": {
				URL: file("darwin-x64"), Binary: "package/bin/opencode",
				SHA256: "a2f1bd02c36e180d43999a70126212511ef67686345d600724dd93ea74de7601",
			},
			"darwin/arm64": {
				URL: file("darwin-arm64"), Binary: "package/bin/opencode",
				SHA256: "8d4f90c5b23faa724ba1c877d8358072bbb6565c33b8d8b5dca7c7644c4de0bf",
			},
		},
	}
}
