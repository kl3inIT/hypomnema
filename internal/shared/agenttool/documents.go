package agenttool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// The documents environment (issue #401): one Python environment, with the
// libraries the Bots' document skills import, that every Bot's pi finds first
// on its PATH. It is Hermes's shared "documents" side environment
// (`hermes pm environment documents --extra documents --extra documents-pdf`;
// pm/cli.py cmd_environment, pm/operations.py ensure_environment and
// _ensure_generation), built here the way Hermes builds it:
//
//   - uv makes it, with a uv-managed Python pinned to one version and the
//     packages pinned by documents/uv.lock (`uv sync --frozen`, as Hermes
//     syncs a locked environment: pm/environment.py, sync), hashes and all;
//   - concurrent builders take turns on one lock (.install.lock), and the
//     second finds the first's work done;
//   - each build is a new generation, gen-<32 hex>, and active.json, written
//     last and atomically after the tree reached the disk, selects it. A build
//     that fails leaves the selected generation as it was, and the generation
//     a running Bot started with is never changed or removed.
//
// Its layout under a machine's home:
//
//	<home>/tools/envs/documents/active.json      {"generation": "gen-…", "inputs": "<sha256>"}
//	<home>/tools/envs/documents/gen-…/venv/bin    put first on pi's PATH by hypomnema-pi
//	<home>/tools/python                           the Pythons uv installed
//	<home>/cache/uv                               uv's cache, outside the Bots' box
const (
	// DocumentsPython is the Python the environment is built on: Hermes's
	// pinned Python (pm/lock.json, cpython 3.14.9).
	DocumentsPython = "3.14.9"
	// documentsImports is the check a generation passes before it is
	// selected, the Hermes pdf skill's own (SKILL.md, Prerequisites).
	documentsImports = "import docx, openpyxl, pptx, reportlab, pypdf, pdfplumber, pypdfium2"
)

//go:embed documents/pyproject.toml documents/uv.lock
var documentsProject embed.FS

var generationPattern = regexp.MustCompile(`^gen-[0-9a-f]{32}$`)

// DocumentsRoot is the directory of the documents environment's generations.
func DocumentsRoot(home string) string { return filepath.Join(home, "tools", "envs", "documents") }

// Documents builds the documents environment under a machine's home.
type Documents struct {
	Home string
	// UV is the path of the uv command and UVVersion its version.
	UV, UVVersion string
	// Python is the version of Python to build on; empty is DocumentsPython.
	Python string
	// run runs a command in dir with env; nil runs it for real. A test gives
	// a fake uv.
	run func(ctx context.Context, dir string, env []string, name string, args ...string) error
}

// selection is active.json, Hermes's record of the selected generation.
type selection struct {
	Generation string `json:"generation"`
	Inputs     string `json:"inputs"`
}

// Ensure has the environment of the current pins selected and returns its
// venv: it does nothing when the selected generation was built from the same
// inputs, and otherwise builds a new one and selects it.
func (d Documents) Ensure(ctx context.Context) (string, error) {
	root := DocumentsRoot(d.Home)

	inputs, err := d.inputs()
	if err != nil {
		return "", err
	}

	if venv, ok := current(root, inputs); ok {
		return venv, nil
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("making %s: %w", root, err)
	}

	unlock, err := lockFile(filepath.Join(root, ".install.lock"))
	if err != nil {
		return "", fmt.Errorf("locking the documents environment: %w", err)
	}
	defer unlock()

	// Another builder may have finished while this one waited.
	if venv, ok := current(root, inputs); ok {
		return venv, nil
	}

	generation, err := newGeneration(root)
	if err != nil {
		return "", err
	}

	venv, err := d.build(ctx, generation)
	if err == nil {
		err = selectGeneration(root, selection{Generation: filepath.Base(generation), Inputs: inputs})
	}

	if err != nil {
		_ = os.RemoveAll(generation)

		return "", fmt.Errorf("building the documents environment: %w", err)
	}

	return venv, nil
}

// inputs is what a generation is built from, as one digest: Hermes's
// identity of a side environment (its requirements, Python and interpreter).
func (d Documents) inputs() (string, error) {
	project, err := fs.ReadFile(documentsProject, "documents/pyproject.toml")
	if err != nil {
		return "", err
	}

	lock, err := fs.ReadFile(documentsProject, "documents/uv.lock")
	if err != nil {
		return "", err
	}

	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

	raw, err := json.Marshal(map[string]string{
		"uv": d.UVVersion, "python": d.python(), "pythons": d.pythons(), "target": runtime.GOOS + "/" + runtime.GOARCH,
		"pyproject.toml": sum(project), "uv.lock": sum(lock),
	})
	if err != nil {
		return "", err
	}

	return sum(raw), nil
}

func (d Documents) python() string {
	if d.Python == "" {
		return DocumentsPython
	}

	return d.Python
}

func (d Documents) pythons() string { return filepath.Join(d.Home, "tools", "python") }

// build makes the generation's venv: the project's files, the pinned
// Python, the locked packages, and the skill's import check.
func (d Documents) build(ctx context.Context, generation string) (string, error) {
	for _, name := range []string{"pyproject.toml", "uv.lock"} {
		raw, err := fs.ReadFile(documentsProject, "documents/"+name)
		if err != nil {
			return "", err
		}

		if err := os.WriteFile(filepath.Join(generation, name), raw, 0o600); err != nil {
			return "", err
		}
	}

	venv := filepath.Join(generation, "venv")
	env := append(uvEnvironment(os.Environ()),
		"UV_PYTHON_INSTALL_DIR="+d.pythons(), "UV_CACHE_DIR="+filepath.Join(d.Home, "cache", "uv"),
		"UV_PROJECT_ENVIRONMENT="+venv)

	// --no-bin: the Python stays under the tools, with no command put in ~/.local/bin. The patch version is
	// named, so the venv is bound to that Python and not to a minor version uv may move to a newer patch.
	if err := d.runCommand(ctx, generation, env, d.UV, "python", "install", "--no-config", "--no-bin",
		d.python()); err != nil {
		return "", fmt.Errorf("installing Python %s: %w", d.python(), err)
	}

	// --frozen installs uv.lock as it is, each file checked against its hash. The box is read-only, so the
	// bytecode is compiled now (Hermes's --compile-bytecode).
	if err := d.runCommand(ctx, generation, append(env, "UV_PYTHON_DOWNLOADS=never"), d.UV, "sync", "--no-config",
		"--frozen", "--managed-python", "--python", d.python(), "--compile-bytecode"); err != nil {
		return "", fmt.Errorf("installing the packages of uv.lock: %w", err)
	}

	python := filepath.Join(venv, "bin", "python")
	if err := d.runCommand(ctx, generation, env, python, "-I", "-c", documentsImports); err != nil {
		return "", fmt.Errorf("the environment does not import its packages: %w", err)
	}

	return venv, nil
}

func (d Documents) runCommand(ctx context.Context, dir string, env []string, name string, args ...string) error {
	if d.run != nil {
		return d.run(ctx, dir, env, name, args...)
	}

	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // uv and the venv's Python, of this machine's install
	cmd.Dir, cmd.Env = dir, env

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", filepath.Base(name), strings.Join(args, " "), err,
			strings.TrimSpace(lastLines(string(out), 20)))
	}

	return nil
}

// uvEnvironment is the caller's environment without what would pick another
// project, interpreter or cache for uv: Hermes's _base_environment
// (pm/environment.py) drops PYTHON*, VIRTUAL_ENV and UV_*.
func uvEnvironment(environ []string) []string {
	out := make([]string, 0, len(environ))

	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "PYTHON") || strings.HasPrefix(name, "UV_") || name == "VIRTUAL_ENV" {
			continue
		}

		out = append(out, kv)
	}

	return out
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "\n")
}

// current is the venv of the selected generation when it was built from
// inputs and is still whole.
func current(root, inputs string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(root, "active.json")) //nolint:gosec // the environment's own record
	if err != nil {
		return "", false
	}

	var sel selection
	if json.Unmarshal(raw, &sel) != nil || !generationPattern.MatchString(sel.Generation) || sel.Inputs != inputs {
		return "", false
	}

	venv := filepath.Join(root, sel.Generation, "venv")
	for _, file := range []string{filepath.Join(venv, "pyvenv.cfg"), filepath.Join(venv, "bin", "python")} {
		if _, err := os.Stat(file); err != nil {
			return "", false
		}
	}

	return venv, true
}

func newGeneration(root string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}

	generation := filepath.Join(root, "gen-"+hex.EncodeToString(b[:]))
	if err := os.Mkdir(generation, 0o700); err != nil {
		return "", fmt.Errorf("making a generation of the documents environment: %w", err)
	}

	return generation, nil
}

// selectGeneration writes active.json last: every file of the generation is
// flushed to the disk first, then the record is written beside its place,
// flushed, and renamed over the old one (Hermes's flush_before_selecting and
// _write).
func selectGeneration(root string, sel selection) error {
	syncDisk()

	raw, err := json.Marshal(sel)
	if err != nil {
		return err
	}

	target := filepath.Join(root, "active.json")
	tmp := target + ".tmp"

	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // under root
	if err != nil {
		return err
	}

	_, err = file.Write(append(raw, '\n'))
	if err == nil {
		err = file.Sync()
	}

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err == nil {
		err = os.Rename(tmp, target)
	}

	if err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("selecting the new generation: %w", err)
	}

	return nil
}
