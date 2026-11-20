package worktree

import (
	"errors"

	"golang.org/x/sys/unix"
)

func gitExited(pid int) error {
	queue, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(queue) }()
	change := []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}}
	var event [1]unix.Kevent_t
	for {
		_, err := unix.Kevent(queue, change, event[:], nil)
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
