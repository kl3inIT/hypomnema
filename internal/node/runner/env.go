package runner

import (
	"os"
	"path/filepath"
	"strings"
)

// The worker's PATH (issue #718). The node runs as a systemd user service,
// which reads no shell profile, so its PATH lacks the user's own
// ~/.local/bin, where a repository's setup script puts the tools it
// installs without sudo (deploy/worker/setup.sh: Go, Node, pnpm). A worker
// gets that directory first on its PATH when it exists, as a login shell
// gets it from ~/.profile on Debian and Ubuntu (/etc/skel/.profile: "set PATH
// so it includes user's private bin if it exists"), so the version the
// repository asked for wins over an older one of the machine's. Orca adds the
// same directory to the PATH it gives agents
// (src/main/startup/configure-process.ts, posix-version-manager-bin-dirs.ts).

// userBin is the user's own directory of commands, under home.
func userBin(home string) string { return filepath.Join(home, ".local", "bin") }

// WorkerPath is path with home's ~/.local/bin first, when that directory
// exists and is not first already.
func WorkerPath(home, path string) string {
	if home == "" {
		return path
	}

	bin := userBin(home)
	if info, err := os.Stat(bin); err != nil || !info.IsDir() {
		return path
	}

	if path == "" {
		return bin
	}

	if first, _, _ := strings.Cut(path, string(os.PathListSeparator)); first == bin {
		return path
	}

	return bin + string(os.PathListSeparator) + path
}

// Path is the worker's PATH: the node's, as WorkerPath makes it.
func Path() string { return WorkerPath(os.Getenv("HOME"), os.Getenv("PATH")) }

// HostEnv is what a worker's clean environment takes from the node's: its
// PATH, as Path makes it, and who and where the user is (durable-harness
// env.ts). Keep the user's XDG_RUNTIME_DIR so pnpm can coordinate access to
// the shared store there instead of falling back to /tmp (issue #895).
func HostEnv() []string {
	var out []string

	for _, name := range []string{
		"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "SystemRoot", "TEMP", "TMP", "GOCACHE", "XDG_RUNTIME_DIR",
	} {
		v, ok := os.LookupEnv(name)
		if name == "PATH" {
			v = Path()
			ok = v != ""
		}

		if ok {
			out = append(out, name+"="+v)
		}
	}

	return out
}

// PathOf is the PATH an environment of the worker's sets last, which a host's environment may set first (the
// owner's GitHub App's commands, issue #1262); Path when it sets none.
func PathOf(env []string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if path, ok := strings.CutPrefix(env[i], "PATH="); ok {
			return path
		}
	}

	return Path()
}
