//go:build windows

package gitutil

import (
	"os/exec"
	"syscall"
)

// The origin probe runs before backup and installer Git commands, so it needs
// its own console suppression even when the calling command already has it.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
