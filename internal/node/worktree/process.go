package worktree

import "time"

func (git mirrorGit) stopGrace() time.Duration {
	if git.grace <= 0 {
		return mirrorGitGrace
	}
	return git.grace
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
