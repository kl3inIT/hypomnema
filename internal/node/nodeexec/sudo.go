package nodeexec

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// sudo is given the node's password as Hermes gives it
// (tools/terminal_tool_sudo.py, the askpass form): each sudo that is the
// command of a simple command is told to ask its helper (-A), and the
// helper, named by SUDO_ASKPASS, prints the password from a file only the
// node's user reads, removed when the command ends. The model never sees it,
// it is in no command line, and a sudo in a loop or a pipeline asks the
// helper as often as it needs, which a password piped on stdin cannot do.
// A sudo the shell would not run as a word of its own (in an alias, behind
// env or in `sh -c "…"`) is left as it is, as Hermes leaves it: it asks for
// a terminal and fails, or runs when the machine needs no password.

// withAskpass adds -A after every sudo that is the command of a simple
// command in command. It says whether it found one; a command that cannot be
// parsed is left as it is.
func withAskpass(command string) (string, bool) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return command, false
	}

	var at []int

	syntax.Walk(file, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if ok && len(call.Args) > 0 && call.Args[0].Lit() == "sudo" {
			at = append(at, int(call.Args[0].End().Offset()))
		}

		return true
	})

	if len(at) == 0 {
		return command, false
	}

	slices.Sort(at)

	var b strings.Builder

	last := 0
	for _, offset := range at {
		b.WriteString(command[last:offset])
		b.WriteString(" -A")
		last = offset
	}

	b.WriteString(command[last:])

	return b.String(), true
}

// askpass is a helper that prints a password, in a folder of its own.
type askpass struct {
	dir string
}

// newAskpass writes the helper and the password beside it, readable by the
// node's user alone.
func newAskpass(password string) (*askpass, error) {
	dir, err := os.MkdirTemp("", "hypomnema-askpass-")
	if err != nil {
		return nil, err
	}

	a := &askpass{dir: dir}

	if err := os.WriteFile(filepath.Join(dir, "p"), []byte(password+"\n"), 0o600); err != nil {
		a.remove()

		return nil, err
	}

	// The folder is named in single quotes; os.MkdirTemp makes no name with one.
	script := "#!/bin/sh\nexec cat '" + filepath.Join(dir, "p") + "'\n"
	if err := os.WriteFile(a.path(), []byte(script), 0o700); err != nil { //nolint:gosec // G306: the helper runs
		a.remove()

		return nil, err
	}

	return a, nil
}

// path is the helper, for SUDO_ASKPASS.
func (a *askpass) path() string { return filepath.Join(a.dir, "askpass") }

func (a *askpass) remove() { _ = os.RemoveAll(a.dir) }
