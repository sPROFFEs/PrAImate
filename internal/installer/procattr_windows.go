//go:build windows

package installer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// createNoWindow stops Windows from allocating a console for the child.
// Without it, every per-turn CLI spawn from a GUI process flashes a
// black console window. HideWindow alone is not enough for console-
// subsystem children; CREATE_NO_WINDOW suppresses the console entirely.
const createNoWindow = 0x08000000

// hideConsole configures cmd so the child process never opens a visible
// console window. Must be called before cmd.Start/Run.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}

// Batch shims need cmd.exe. Pass the path through an environment variable so
// metacharacters in a profile directory are not interpolated into shell code.
func toolVersionCommand(ctx context.Context, path string, args []string) *exec.Cmd {
	extension := strings.ToLower(filepath.Ext(path))
	if len(args) == 0 && (extension == ".cmd" || extension == ".bat") {
		cmd := exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
		hideConsole(cmd)
		cmd.SysProcAttr.CmdLine = `cmd.exe /d /v:off /s /c ""%PRAIMATE_TOOL_PROBE%" --version"`
		cmd.Env = append(os.Environ(), "PRAIMATE_TOOL_PROBE="+path)
		return cmd
	}
	cmd := exec.CommandContext(ctx, path, append(args, "--version")...)
	hideConsole(cmd)
	return cmd
}
