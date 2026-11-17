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
	// TokenEnv is the variable the tool reads a ready access token from. It is
	// given a placeholder: the tool's requests go through the control plane's
	// service proxy, which puts the token in (issues #154, #175).
	TokenEnv string
	// ClientEnv is the variable the tool reads the id of the account's app
	// from, when it will not run without it; empty for a tool that needs the
	// token alone. The id is not a secret.
	ClientEnv string
	// UserTokenEnv is the variable the tool reads a person's token from, for
	// an account that is the owner's own sign-in (issue #468); it is given
	// the placeholder in place of TokenEnv. IdentityEnv then names the
	// variable that holds the tool to one identity, set to UserIdentity for
	// such an account and to AppIdentity for an app's, so that a call never
	// changes whom it acts as on its own. Empty for a tool with one kind of
	// account.
	UserTokenEnv string
	IdentityEnv  string
	UserIdentity string
	AppIdentity  string
	// CAEnv is the variable the tool reads the certificates it trusts from,
	// when it does not read SSL_CERT_FILE: the AWS CLI reads AWS_CA_BUNDLE.
	CAEnv string
	// Env are the variables the tool is always run with, as "NAME=value".
	Env []string
	// Unset are the variables the tool is run without, whatever the agent's
	// environment says: the AWS CLI refuses an empty AWS_PROFILE ("The config
	// profile () could not be found", tried with 2.38.6 on 2026-11-13), so a
	// profile is taken away, not emptied.
	Unset []string
	// Releases is the release file of each system, by "os/arch".
	Releases map[string]Release
	// BotsOnly says the tool is installed and run only where Bots run: a worker reaches the same account with its
	// Dispatch's own token (gh, issue #685), so a node is never told to have it and a worker is not given its
	// script, which would stand before the worker's own gh.
	BotsOnly bool
	// HostKind is the kind of git host the tool is for (githost's gitlab, issue #740), when it is for every host of
	// that kind rather than for one Integration: each host is an Integration of its own, named by the host, and
	// Integration names the kind. An account is a host's Connection, host:label; HostEnv is the variable the tool
	// reads the host it acts on from.
	HostKind string
	HostEnv  string
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
func Catalog() []Def {
	return []Def{GoogleWorkspaceCLI(), LarkCLI(), AWSCLI(), GitHubCLI(), GitLabCLI()}
}

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

// UV is Astral's uv (github.com/astral-sh/uv, MIT or Apache-2.0), which
// builds the documents environment the Bots' document skills run in (issue
// #401) and is for no Integration. OpenClaw's installer specs install a
// skill's Python dependencies with uv and a downloaded tool with its sha256
// under its tools directory (docs/tools/skills.md, "Installer specs"), and
// Hermes's package manager pins its uv by version and SHA-256 (pm/lock.json). It is installed beside the tools, not given to an
// agent: no script calls it. Its pin also pins the Python it installs, since
// uv checks each Python it downloads against the SHA-256 its own release
// lists. The gnu builds are the ones uv's installer picks on a glibc system;
// the checksums are the ones published beside each release file (.sha256),
// read on 2026-11-14 and matched against the files.
func UV() Def {
	const (
		version = "0.13.8"
		release = "https://github.com/astral-sh/uv/releases/download/" + version + "/"
	)

	return Def{
		Name: "uv", Version: version,
		Releases: map[string]Release{
			"linux/amd64": {
				URL:    release + "uv-x86_64-unknown-linux-gnu.tar.gz",
				SHA256: "5acfd9177cfecf0bf87578f059728215ccc884388988f55f7a65041b57387d91",
				Binary: "uv-x86_64-unknown-linux-gnu/uv",
			},
			"linux/arm64": {
				URL:    release + "uv-aarch64-unknown-linux-gnu.tar.gz",
				SHA256: "74f2f5c6024d397481db228db8fc25eed420db192243f88438c118e7e013e585",
				Binary: "uv-aarch64-unknown-linux-gnu/uv",
			},
		},
	}
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
		// gws takes a proxy and a CA file from the environment and speaks HTTP/1.1 (tried on 2026-11-11).
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
//
// The owner's own sign-in (issue #468) is given to it as a user's token in
// LARKSUITE_CLI_USER_ACCESS_TOKEN. Its environment provider acts as the user
// whenever that one is set, and LARKSUITE_CLI_STRICT_MODE holds it to one
// identity (extension/credential/env/env.go): user for the owner's account,
// bot for an app's. The variables that would choose otherwise are taken away.
func LarkCLI() Def {
	const (
		version = "1.0.100"
		release = "https://github.com/larksuite/cli/releases/download/v" + version + "/lark-cli-" + version
	)

	//nolint:gosec // The names of variables and public addresses; no credential is here.
	return Def{
		Integration: "lark", Name: "lark-cli", Version: version,
		TokenEnv:     "LARKSUITE_CLI_TENANT_ACCESS_TOKEN",
		ClientEnv:    "LARKSUITE_CLI_APP_ID",
		UserTokenEnv: "LARKSUITE_CLI_USER_ACCESS_TOKEN",
		IdentityEnv:  "LARKSUITE_CLI_STRICT_MODE", UserIdentity: "user", AppIdentity: "bot",
		Env: []string{
			"LARKSUITE_CLI_BRAND=lark", "LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
		},
		Unset: []string{
			"LARKSUITE_CLI_TENANT_ACCESS_TOKEN", "LARKSUITE_CLI_USER_ACCESS_TOKEN", "LARKSUITE_CLI_APP_SECRET",
			"LARKSUITE_CLI_STRICT_MODE", "LARKSUITE_CLI_DEFAULT_AS", "LARKSUITE_CLI_TENANT_ACCESS_TOKEN_SOURCE",
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

// AWSCLI is AWS's own command-line tool, version 2 (github.com/aws/aws-cli,
// Apache-2.0), for the AWS Integration (issue #331). It is a directory of
// files, shipped as a .zip whose aws/dist holds the command. It is given
// placeholders for a key and signs every request with them; the service proxy
// signs each one again with the session of the role the agent was given. It
// reads its proxy from HTTPS_PROXY, to which it sends a CONNECT for each
// service's host (tried with 2.38.6 on 2026-11-13), and the certificates it
// trusts from AWS_CA_BUNDLE. No file of the machine's own configuration or credentials is
// read, and no instance's metadata is asked, so that only the proxy's
// credentials are ever used; no pager waits for a terminal.
//
// AWS publishes a PGP signature for each file, not a checksum: the files of
// 2.38.6 (released 2026-10-26) were checked against the AWS CLI Team's key
// (fingerprint FB5D B77F D5C1 18B8 0511 ADA8 A631 0ACC 4672 475C) on
// 2026-11-13, and these are their SHA-256.
func AWSCLI() Def {
	const (
		version = "2.38.6"
		release = "https://awscli.amazonaws.com/awscli-exe-linux-"
	)

	//nolint:gosec // The names of variables, placeholders and public addresses; no credential is here.
	return Def{
		Integration: "aws", Name: "aws", Version: version,
		TokenEnv: "AWS_ACCESS_KEY_ID",
		CAEnv:    "AWS_CA_BUNDLE",
		Env: []string{
			"AWS_SECRET_ACCESS_KEY=" + Placeholder, "AWS_SESSION_TOKEN=", "AWS_CONFIG_FILE=/dev/null", "AWS_SHARED_CREDENTIALS_FILE=/dev/null", "AWS_EC2_METADATA_DISABLED=true",
			"AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off",
		},
		Unset: []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
			"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN"},
		Releases: map[string]Release{
			"linux/amd64": {
				URL:    release + "x86_64-" + version + ".zip",
				SHA256: "66974301c11f8116a6cb96bf72acae57453734b61fa8dd3f56422de9eaab0e42",
				Binary: "aws/dist/aws",
			},
			"linux/arm64": {
				URL:    release + "aarch64-" + version + ".zip",
				SHA256: "a9f355189a5a25834c015fc07d65e1834f0dbbee1d9bd1c418176f43a463067f",
				Binary: "aws/dist/aws",
			},
		},
	}
}

// GitHubCLI is GitHub's own command-line tool (github.com/cli/cli, MIT), for
// a Bot given a git host's Connection of github.com (issue #685), as Onyx's
// and OpenClaw's github skills use it. It reads a ready token from GH_TOKEN
// before any login it keeps, and its proxy from HTTPS_PROXY; being Go, it
// trusts what SSL_CERT_FILE names. GH_HOST holds it to github.com; prompts,
// update notices and the spinner are off, so that nothing waits for a
// terminal; its telemetry is off, as it would reach a host the proxy does not
// serve. GITHUB_TOKEN and the enterprise tokens are taken away, so that the
// placeholder is the only credential it has. The checksums are those of the
// release's gh_2.103.3_checksums.txt, read on 2026-11-17 and matched against
// the files. A worker keeps its own gh with its Dispatch's GH_TOKEN (#44).
func GitHubCLI() Def {
	const (
		version = "2.103.3"
		release = "https://github.com/cli/cli/releases/download/v" + version + "/gh_" + version + "_linux_"
	)

	return Def{
		Integration: "github.com", Name: "gh", Version: version,
		TokenEnv: "GH_TOKEN",
		Env: []string{
			"GH_HOST=github.com", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1",
			"GH_NO_EXTENSION_UPDATE_NOTIFIER=1", "GH_SPINNER_DISABLED=1", "GH_TELEMETRY=false",
		},
		Unset: []string{
			"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST",
		},
		BotsOnly: true,
		Releases: map[string]Release{
			"linux/amd64": {
				URL:    release + "amd64.tar.gz",
				SHA256: "4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d",
				Binary: "gh_" + version + "_linux_amd64/bin/gh",
			},
			"linux/arm64": {
				URL:    release + "arm64.tar.gz",
				SHA256: "ee32df2323c8847f5fac6cb6b4b69ca75828e9329a8f22f677021f7b3306ce63",
				Binary: "gh_" + version + "_linux_arm64/bin/gh",
			},
		},
	}
}

// GitLabCLI is GitLab's own command-line tool (gitlab.com/gitlab-org/cli, MIT), for a Bot given a Connection of a
// GitLab host, gitlab.com or a company's server (issue #740), as t3code drives glab for GitLab. It reads a ready
// token from GITLAB_TOKEN before any login it keeps, and the host it acts on from GITLAB_HOST, which is set to the
// account's host: glab sends an environment's token only to that host (internal/config/schema.go, "token"). Its
// proxy is HTTPS_PROXY and, being Go, it trusts what SSL_CERT_FILE names. Update checks, prompts and its telemetry,
// which would reach the host as usage_data/track_event (cmd/glab/telemetry.go), are off. GITLAB_ACCESS_TOKEN,
// OAUTH_TOKEN, a CI job's token and the other names of the host are taken away, so that the placeholder is the only
// credential it has and the account's host the only one. The checksums are those of the release's checksums.txt
// (.goreleaser.yml, checksum), read on 2026-11-17 and matched against the files. A worker keeps its own glab with
// its Dispatch's GITLAB_TOKEN (#44).
func GitLabCLI() Def {
	const (
		version = "1.130.0"
		release = "https://gitlab.com/gitlab-org/cli/-/releases/v" + version + "/downloads/glab_" + version + "_linux_"
	)

	return Def{
		Integration: "gitlab", Name: "glab", Version: version,
		HostKind: "gitlab", HostEnv: "GITLAB_HOST",
		TokenEnv: "GITLAB_TOKEN",
		Env:      []string{"GLAB_CHECK_UPDATE=false", "GLAB_SEND_TELEMETRY=false", "GLAB_NO_PROMPT=true"},
		Unset: []string{
			"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "OAUTH_TOKEN", "CI_JOB_TOKEN", "GITLAB_HOST", "GITLAB_URI",
			"GL_HOST", "GITLAB_REPO",
		},
		BotsOnly: true,
		Releases: map[string]Release{
			"linux/amd64": {
				URL:    release + "amd64.tar.gz",
				SHA256: "ce06313b2075af25180cd195ab3496d42bc5857c85a1c12ba996838dfe9d822c",
				Binary: "bin/glab",
			},
			"linux/arm64": {
				URL:    release + "arm64.tar.gz",
				SHA256: "062d53acb0f2cbcc79a014600df1aa0fb440bb3a08a5d4900d3b49b553b22767",
				Binary: "bin/glab",
			},
		},
	}
}
