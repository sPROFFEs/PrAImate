//go:build !windows

package studio

import "os/exec"

func hideEditorCLIConsole(cmd *exec.Cmd) {}
