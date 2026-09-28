//go:build windows

package lock

import (
	"os"

	"golang.org/x/sys/windows"
)

// syscallSig0 is unused on Windows (processAlive is overridden below) but
// kept to satisfy the shared var.
func syscallSig0() os.Signal { return os.Interrupt }

// processAlive reports whether a pid exists (OpenProcess probe).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}
