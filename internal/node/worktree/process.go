package worktree

import "time"

const (
	gitStalledExit = 124
	gitFailedExit  = 125
)

func (git mirrorGit) bounds() (time.Duration, time.Duration) {
	stall, grace := git.stall, git.grace
	if stall <= 0 {
		stall = mirrorGitStall
	}
	if grace <= 0 {
		grace = mirrorGitGrace
	}
	return stall, grace
}

type outputTail []byte

func (tail *outputTail) Write(data []byte) (int, error) {
	size := len(data)
	if size >= setupOutputKept {
		*tail = append((*tail)[:0], data[size-setupOutputKept:]...)
	} else {
		if overflow := len(*tail) + size - setupOutputKept; overflow > 0 {
			*tail = (*tail)[:copy(*tail, (*tail)[overflow:])]
		}
		*tail = append(*tail, data...)
	}
	return size, nil
}
