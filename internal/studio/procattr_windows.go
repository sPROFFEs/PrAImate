//go:build windows

package studio

import (
	"os/exec"
	"syscall"
)

func hideEditorCLIConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
