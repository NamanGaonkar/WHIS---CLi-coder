//go:build windows

package mcp

import (
	"os/exec"
	"syscall"
)

// hideWindowsWindow sets CREATE_NO_WINDOW on the child process so MCP
// servers spawn invisibly on Windows.
func hideWindowsWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
