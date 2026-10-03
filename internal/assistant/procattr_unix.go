//go:build !windows

package assistant

import "os/exec"

func prepareRuntimeCommand(*exec.Cmd) error { return nil }
