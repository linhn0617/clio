//go:build windows

package lock

import "syscall"

// pidAlive on Windows: os.Process.Signal(0) is unsupported there, so ask the OS
// directly. A process that cannot be opened because access is denied still exists.
func pidAlive(pid int) bool {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return err == syscall.ERROR_ACCESS_DENIED
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true // opened but unqueryable: assume alive rather than steal the lease
	}
	return code == 259 // STILL_ACTIVE
}
