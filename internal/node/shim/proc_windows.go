//go:build windows

package shim

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// Alive reports whether the process runs.
func Alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	defer func() { _ = p.Release() }()
	alive := false
	_ = p.WithHandle(func(handle uintptr) {
		status, err := windows.WaitForSingleObject(windows.Handle(handle), 0)
		alive = err == nil && status == uint32(windows.WAIT_TIMEOUT)
	})
	return alive
}

type workerStop struct{ job windows.Handle }

func startWorker(cmd *exec.Cmd) (workerStop, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return workerStop{}, err
	}
	stop := workerStop{job: job}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		stop.close()
		return workerStop{}, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err := cmd.Start(); err != nil {
		stop.close()
		return workerStop{}, err
	}
	attachErr := cmd.Process.WithHandle(func(handle uintptr) {
		err = windows.AssignProcessToJobObject(job, windows.Handle(handle))
		if err == nil {
			err = resumeWorker(uint32(cmd.Process.Pid))
		}
	})
	if attachErr != nil || err != nil {
		stop.close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if attachErr != nil {
			return workerStop{}, attachErr
		}
		return workerStop{}, err
	}
	return stop, nil
}

func resumeWorker(pid uint32) error {
	threads, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(threads) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err := windows.Thread32First(threads, &entry); err == nil; err = windows.Thread32Next(threads, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		defer func() { _ = windows.CloseHandle(thread) }()
		_, err = windows.ResumeThread(thread)
		return err
	}
	return windows.ERROR_NOT_FOUND
}

func (stop workerStop) close() { _ = windows.CloseHandle(stop.job) }

func (stop workerStop) kill() (bool, error) { return false, windows.TerminateJobObject(stop.job, 1) }

func reapOrphans(int) func() { return func() {} }

func signalOf(*os.ProcessState) string { return "" }

func killSignal() string { return "" }
