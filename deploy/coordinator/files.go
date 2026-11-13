// Package coordinator holds the files that set up pi, the coordinator, on the control plane's machine
// (setup-coordinator.sh and what it installs). They are built into the control plane's binary, so that an update
// installs the launchers of the same release as the binary it puts in place (issue #279; Coder ships its unit and
// binary in one package, OpenClaw checks its launcher against the release).
package coordinator

import "embed"

// Files are this folder's files, at their names.
//
//go:embed AGENTS.md hypomnema-pi hypomnema-routine package.json package-lock.json setup-box.sh setup-coordinator.sh
var Files embed.FS
