//go:build unix

package lock

import (
	"os"
	"syscall"
)

// mutexPath returns the sidecar lockfile path for a given lease path.
func mutexPath(leasePath string) string { return leasePath + ".lk" }

// withMutex runs fn while holding an exclusive OS advisory lock (flock) on the
// lease's sidecar .lk file, serializing lease read-modify-write across
// processes. The kernel releases the lock when the fd is closed, including on
// process crash, so no stale-break is needed (this is why flock is used instead
// of a hand-rolled O_EXCL lockfile). The .lk file is intentionally not removed.
func withMutex(leasePath string, fn func() error) error {
	f, err := os.OpenFile(mutexPath(leasePath), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close() // closing the fd also releases the flock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
