//go:build !windows

package lock

import (
	"errors"
	"os"
	"syscall"
)

func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// EPERM means the process exists but we lack permission to signal it (e.g. a
	// different user) — still alive, so don't steal its lease.
	serr := signalProc(proc, syscall.Signal(0))
	return serr == nil || errors.Is(serr, syscall.EPERM)
}
