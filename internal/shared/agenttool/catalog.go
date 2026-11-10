// Package agenttool is what both binaries share about the command-line tools
// agents call Integrations with (issue #103): which tool an Integration has
// and its pinned releases, how a machine installs one, and the launcher that
// runs one with a fresh credential.
//
// A tool is run the same way for every agent: the coordinator on the control
// plane's machine and a worker on a node both find a small script named after
// the tool on their PATH; it runs this package's launcher, which asks the
// control plane for a short-lived token and then runs the real tool. Only who
// the agent is differs (a coordinator's token, or a Dispatch's own key), and
// the control plane decides from that which accounts it may use.
package agenttool

import "github.com/kl3inIT/hypomnema/internal/shared/nodeproto"

// Def is the command-line tool an Integration's skill calls, pinned to one
// version with a release file for each system a machine can be. OpenClaw's
// skills declare a tool the same way (requires.bins with an install recipe);
// the pin and the checksum follow this repository's node release manifest
// (version, url, sha256).
type Def struct {
	// Integration is the id of the Integration the tool is for: google.
	Integration string
	// Name is the command: gws.
	Name    string
	Version string
	// TokenEnv is the variable the tool reads a ready access token from.
	TokenEnv string
	// ClientEnv is the variable the tool reads the id of the Integration's
	// client from, when it will not run without it; empty for a tool that
	// needs the token alone. The id is not a secret.
	ClientEnv string
	// Env are the variables the tool is always run with, as "NAME=value".
	Env []string
	// Releases is the release file of each system, by "os/arch".
	Releases map[string]Release
}

// Release is one system's release file of a tool.
type Release struct {
	URL string
	// SHA256 is the checksum the tool's publisher gives for the file.
	SHA256 string
	// Binary is the path of the command inside the file.
	Binary string
}

// For returns the release of the tool for a system as the message a machine
// installs it from, and false when the tool has none for that system.
func (d Def) For(os, arch string) (nodeproto.Tool, bool) {
	release, ok := d.Releases[os+"/"+arch]
	if !ok {
		return nodeproto.Tool{}, false
	}

	return nodeproto.Tool{
		Name: d.Name, Version: d.Version, URL: release.URL, SHA256: release.SHA256, Binary: release.Binary,
	}, true
}

// Catalog is every tool, one an Integration.
func Catalog() []Def { return []Def{GoogleWorkspaceCLI(), LarkCLI()} }

// Of returns the tool of an Integration.
func Of(integration string) (Def, bool) {
	for _, d := range Catalog() {
		if d.Integration == integration {
			return d, true
		}
	}

	return Def{}, false
}

// Named returns the tool with a command's name.
func Named(name string) (Def, bool) {
	for _, d := range Catalog() {
		if d.Name == name {
			return d, true
		}
	}

	return Def{}, false
}

// GoogleWorkspaceCLI is the Google Workspace CLI (github.com/googleworkspace/cli,
// Apache-2.0), which Hermes' google-workspace skill uses. It is one static
// binary; it reads a ready access token from GOOGLE_WORKSPACE_CLI_TOKEN on
// every call (crates/google-workspace-cli/src/auth.rs, get_token). The
// checksums are the ones published beside each release file (.sha256), read
// on 2026-11-10.
func GoogleWorkspaceCLI() Def {
	const (
		version = "0.22.5"
		release = "https://github.com/googleworkspace/cli/releases/download/v" + version + "/"
	)

	return Def{
		Integration: "google", Name: "gws", Version: version,
		TokenEnv: "GOOGLE_WORKSPACE_CLI_TOKEN",
		Releases: map[string]Release{
			"linux/amd64": {
				URL:    release + "google-workspace-cli-x86_64-unknown-linux-musl.tar.gz",
				SHA256: "4db473dde4b1ab872e4ff35d769b0d4af1f1a6441a605e79d5cf8ada9c87e920",
				Binary: "gws",
			},
			"linux/arm64": {
				URL:    release + "google-workspace-cli-aarch64-unknown-linux-musl.tar.gz",
				SHA256: "e700fe63524932b10ec2130b47ece90aa850e66005fe52ccfc4cf8767bf9919a",
				Binary: "gws",
			},
		},
	}
}

// LarkCLI is Lark's own command-line tool (github.com/larksuite/cli, MIT). It
// reads a ready token from LARKSUITE_CLI_TENANT_ACCESS_TOKEN on every call
// (internal/envvars/envvars.go), refuses to run with one unless the app's id
// is in LARKSUITE_CLI_APP_ID beside it, and then writes nothing to its
// configuration directory (tried on 2026-11-10, issue #135). The brand names
// Lark international, the only one the Integration declares; the two notifier
// variables keep its notices about updates out of what an agent reads. The
// checksums are the ones of the release's checksums.txt, read the same day
// and matched against the files.
func LarkCLI() Def {
	const (
		version = "1.0.100"
		release = "https://github.com/larksuite/cli/releases/download/v" + version + "/lark-cli-" + version
	)

	//nolint:gosec // The names of variables and public addresses; no credential is here.
	return Def{
		Integration: "lark", Name: "lark-cli", Version: version,
		TokenEnv:  "LARKSUITE_CLI_TENANT_ACCESS_TOKEN",
		ClientEnv: "LARKSUITE_CLI_APP_ID",
		Env: []string{
			"LARKSUITE_CLI_BRAND=lark", "LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
		},
		Releases: map[string]Release{
			"linux/amd64": {
				URL:    release + "-linux-amd64.tar.gz",
				SHA256: "cd7363f731c9e5dd2becd909837922cffea8aad229d581a84eb13c6aa0ce6808",
				Binary: "lark-cli",
			},
			"linux/arm64": {
				URL:    release + "-linux-arm64.tar.gz",
				SHA256: "166e95ffaec34f82123f50316480250329884c09e71a6f92313047e2c5c3fa5d",
				Binary: "lark-cli",
			},
		},
	}
}
