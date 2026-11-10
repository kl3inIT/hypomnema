//go:build !windows

package terminal

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// Supported says whether this build can start a shell.
func Supported() bool { return true }

// shells are tried in this order when the user's own is not set (Nezha
// pkg/pty/pty.go tries zsh, fish, bash, sh).
var shells = []string{"bash", "zsh", "sh"}

type ptyShell struct {
	file *os.File
	cmd  *exec.Cmd
}

// StartShell starts the node user's shell on a pseudo-terminal, in the
// user's home directory, with the node's environment and a terminal type a
// browser's terminal understands (Nezha: TERM=xterm-256color).
func StartShell(rows, cols uint16) (Shell, error) {
	path, err := shellPath()
	if err != nil {
		return nil, err
	}

	// The shell lives as long as its session, not a request, so it takes no context.
	cmd := exec.Command(path, "-l") //nolint:gosec,noctx // G204: the user's own shell
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")

	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}

	file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, fmt.Errorf("starting %s on a pseudo-terminal: %w", path, err)
	}

	return &ptyShell{file: file, cmd: cmd}, nil
}

func shellPath() (string, error) {
	if own := os.Getenv("SHELL"); own != "" {
		if path, err := exec.LookPath(own); err == nil {
			return path, nil
		}
	}

	for _, name := range shells {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("no shell found: tried $SHELL and %v", shells)
}

func (s *ptyShell) Read(p []byte) (int, error)  { return s.file.Read(p) }
func (s *ptyShell) Write(p []byte) (int, error) { return s.file.Write(p) }

func (s *ptyShell) Resize(rows, cols uint16) error {
	return pty.Setsize(s.file, &pty.Winsize{Rows: rows, Cols: cols})
}

// Close ends the shell and what it started in its terminal: closing the
// terminal hangs them up, and the shell itself is killed if it stays.
func (s *ptyShell) Close() error {
	err := s.file.Close()

	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}

	return err
}
