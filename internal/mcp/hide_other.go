//go:build !windows

package mcp

import "os/exec"

// hideWindowsWindow is a no-op off Windows.
func hideWindowsWindow(_ *exec.Cmd) {}
