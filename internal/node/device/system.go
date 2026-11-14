package device

import (
	"context"
	"errors"
)

// Command is one run of a program.
type Command struct {
	Path string
	Args []string
	// Env is the whole environment; a later entry of a name wins.
	Env []string
	// Log is the file a long-lived program's output goes to. A file rather
	// than a pipe, so the program outlives the node that started it: a write
	// to a pipe nobody reads any more would end it.
	Log string
}

// Output is what a run wrote and how it ended.
type Output struct {
	Stdout string
	Stderr string
	Code   int
}

// Process is a long-lived program the device started, in a process group
// of its own whose id is its pid.
type Process interface {
	Pid() int
	// Done is closed once the program ended.
	Done() <-chan struct{}
}

// Signal is what ends a process group.
type Signal int

// The signals the device sends.
const (
	Terminate Signal = iota + 1
	Kill
)

// System runs programs and answers about them: the machine's own on Linux
// (system_linux.go), a fake in the tests, as the terminal's shell is
// injected (internal/node/terminal, Options.Start).
type System interface {
	// Run runs a program to its end.
	Run(ctx context.Context, c Command) (Output, error)
	// Start starts a long-lived program in a process group of its own.
	Start(c Command) (Process, error)
	// Signal sends sig to every process of the group pgid.
	Signal(pgid int, sig Signal) error
	// Alive says whether a live process of the group pgid has needle in its
	// command line, so that a group id reused by another program is not
	// taken for the device's.
	Alive(pgid int, needle string) bool
	// DisplayFree says no X server holds display n; DisplayReady that one
	// listens on it.
	DisplayFree(n int) bool
	DisplayReady(n int) bool
}

// ErrUnsupported says this machine cannot run the emulator.
var ErrUnsupported = errors.New("the Android emulator runs only on Linux x86_64")
