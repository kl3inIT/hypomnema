//go:build linux

package tempns

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// probeTimeout bounds one probe. Codex gives its probe of the system bwrap
// 500 ms (SYSTEM_BWRAP_PROBE_TIMEOUT); a node probes once, at its start,
// often while it is busy, so it waits longer.
const probeTimeout = 5 * time.Second

// Probe finds a bubblewrap that can make the namespace: the one on PATH,
// else the one Codex ships, found from codex, the Codex CLI the node found
// (empty: none). The first that sees the probe file in both mounts wins.
func Probe(ctx context.Context, codex string) Isolation {
	var candidates []string
	if path, err := exec.LookPath("bwrap"); err == nil {
		candidates = append(candidates, path)
	}
	candidates = append(candidates, bundled(codex)...)

	if len(candidates) == 0 {
		return Isolation{Reason: "bubblewrap is not installed, and no Codex CLI ships one"}
	}

	var reasons []string
	for _, bwrap := range candidates {
		err := probe(ctx, bwrap)
		if err == nil {
			return Isolation{Bwrap: bwrap}
		}
		reasons = append(reasons, bwrap+": "+err.Error())
	}

	return Isolation{Reason: "no bubblewrap can make the namespace (" + strings.Join(reasons, "; ") + ")"}
}

// probe checks both binds without writing to the shared temp directories
// even if the executable does not set up a namespace.
func probe(ctx context.Context, bwrap string) error {
	sh, err := exec.LookPath("sh")
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "hypomnema-tempns-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) //nolint:errcheck // a probe's own directory

	marker, err := os.CreateTemp(dir, "probe-")
	if err != nil {
		return err
	}
	if err := marker.Close(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var stderr bytes.Buffer

	argv := Isolation{Bwrap: bwrap}.Wrap([]string{
		sh, "-c", `test -f "/tmp/$1" && test -f "/var/tmp/$1"`, "sh", filepath.Base(marker.Name()),
	}, dir)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // a bwrap found on PATH or beside Codex
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second

	if err := cmd.Run(); err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if line == "" {
			line = err.Error()
		}

		return errors.New(line)
	}

	return nil
}

// codexTriple is the target triple of Codex's Linux builds for this
// machine, and codexPackage the npm package that holds it
// (codex-cli/bin/codex.js, PLATFORM_PACKAGE_BY_TARGET).
func codexTriple() (triple, pkg string) {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64-unknown-linux-musl", "codex-linux-x64"
	case "arm64":
		return "aarch64-unknown-linux-musl", "codex-linux-arm64"
	default:
		return "", ""
	}
}

// bundled is where the bwrap Codex ships may be, for the Codex CLI at
// codex, existing executables only. The npm launcher (bin/codex.js) finds
// its platform package's vendor/<triple>, nested in its own node_modules or
// beside it, else its own vendor; the native binary looks in
// codex-resources beside itself and one directory up
// (codex-rs/linux-sandbox/src/bundled_bwrap.rs, legacy_candidates_for_exe).
func bundled(codex string) []string {
	if codex == "" {
		return nil
	}

	resolved, err := filepath.EvalSymlinks(codex)
	if err != nil {
		return nil
	}

	exeDir := filepath.Dir(resolved)
	paths := []string{
		filepath.Join(exeDir, "codex-resources", "bwrap"),
		filepath.Join(filepath.Dir(exeDir), "codex-resources", "bwrap"),
	}

	if triple, pkg := codexTriple(); triple != "" {
		root := filepath.Dir(exeDir) // the @openai/codex package, from its bin/codex.js
		for _, vendor := range []string{
			filepath.Join(root, "node_modules", "@openai", pkg, "vendor"),
			filepath.Join(filepath.Dir(root), pkg, "vendor"),
			filepath.Join(root, "vendor"),
		} {
			paths = append(paths, filepath.Join(vendor, triple, "codex-resources", "bwrap"))
		}
	}

	var out []string
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			out = append(out, p)
		}
	}

	return out
}
