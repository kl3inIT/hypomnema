//go:build unix && !linux

package agenttool

import "syscall"

func syncDisk() { _ = syscall.Sync() }
