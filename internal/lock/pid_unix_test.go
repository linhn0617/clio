//go:build !windows

package lock

import (
	"os"
	"syscall"
	"testing"
)

// Unix only: pid_windows.go asks the OS directly and never goes through signalProc.
// pidAlive must treat EPERM (process exists but we may not signal it) as alive, so a
// lease owned by a live process we cannot signal is not wrongly stolen.
func TestPidAliveTreatsEPERMAsAlive(t *testing.T) {
	orig := signalProc
	t.Cleanup(func() { signalProc = orig })

	signalProc = func(*os.Process, os.Signal) error { return syscall.EPERM }
	if !pidAlive(os.Getpid()) {
		t.Error("EPERM should be treated as alive")
	}
	signalProc = func(*os.Process, os.Signal) error { return nil }
	if !pidAlive(os.Getpid()) {
		t.Error("nil signal error should be alive")
	}
	signalProc = func(*os.Process, os.Signal) error { return syscall.ESRCH }
	if pidAlive(os.Getpid()) {
		t.Error("ESRCH (no such process) should be dead")
	}
}
