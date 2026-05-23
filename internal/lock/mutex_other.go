//go:build !unix

package lock

// withMutex on platforms without flock (e.g. Windows): clio's runtime is
// macOS/Linux. Here the lease relies on its nonce+heartbeat content-lease,
// which is bounded and idempotent (at worst a brief two-watcher window, never
// data corruption). True cross-process mutual exclusion on Windows would
// require LockFileEx via golang.org/x/sys/windows; not pulled in for a
// non-runtime build target.
func withMutex(_ string, fn func() error) error {
	return fn()
}
