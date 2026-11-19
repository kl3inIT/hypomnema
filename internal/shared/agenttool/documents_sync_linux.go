//go:build linux

package agenttool

import "syscall"

// syncDisk flushes every file to disk before a selection is written, as Hermes does in pm/environments.py.
func syncDisk() { syscall.Sync() }
