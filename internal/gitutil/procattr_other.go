//go:build !windows

package gitutil

import "os/exec"

func hideConsole(*exec.Cmd) {}
