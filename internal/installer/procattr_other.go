//go:build !windows

package installer

import (
	"context"
	"os/exec"
)

// hideConsole is a no-op on non-Windows platforms — children inherit
// the parent's stdio without spawning a window. See procattr_windows.go.
func hideConsole(_ *exec.Cmd) {}

func toolVersionCommand(ctx context.Context, path string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, path, append(args, "--version")...)
}
