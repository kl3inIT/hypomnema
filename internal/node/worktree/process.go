package worktree

import (
	"bytes"
	"errors"
	"time"
)

func (git mirrorGit) stopGrace() time.Duration {
	if git.grace <= 0 {
		return mirrorGitGrace
	}
	return git.grace
}

// refLockMarker is what Git prints when a ref it updates moved under it (issue #868).
var refLockMarker = []byte("error: cannot lock ref '")

// outputTail keeps the end of a command's output, and notes whether Git reported a ref lock failure
// anywhere in it, before the start is cut away.
type outputTail struct {
	kept    []byte
	carry   []byte
	refLock bool
}

func (tail *outputTail) Write(data []byte) (int, error) {
	if !tail.refLock {
		scan := append(tail.carry, data...)
		tail.refLock = bytes.Contains(scan, refLockMarker)
		tail.carry = append(tail.carry[:0], scan[max(0, len(scan)-len(refLockMarker)+1):]...)
	}

	size := len(data)
	if size >= setupOutputKept {
		tail.kept = append(tail.kept[:0], data[size-setupOutputKept:]...)
	} else {
		if overflow := len(tail.kept) + size - setupOutputKept; overflow > 0 {
			tail.kept = tail.kept[:copy(tail.kept, tail.kept[overflow:])]
		}
		tail.kept = append(tail.kept, data...)
	}
	return size, nil
}

func (tail *outputTail) String() string {
	if tail == nil {
		return ""
	}
	return string(tail.kept)
}

// errRefLock marks a Git command that printed refLockMarker, wherever in its output.
var errRefLock = errors.New("git reported: cannot lock ref")
