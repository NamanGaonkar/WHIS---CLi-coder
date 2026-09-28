//go:build !windows

package lock

import (
	"os"
	"syscall"
)

func syscallSig0() os.Signal { return syscall.Signal(0) }

// processAlive reports whether a pid exists (signal-0 probe).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}
