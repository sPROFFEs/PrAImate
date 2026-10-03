//go:build windows

package assistant

import (
	"fmt"
	"os/exec"
	"runtime"
	"syscall"

	"golang.org/x/sys/windows"
)

func prepareRuntimeCommand(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	dlls, err := runtimeCRTImports(cmd.Path)
	if err != nil {
		return err
	}
	for _, dll := range dlls {
		// CRT dependencies must come from the owned package or Windows, never
		// from an unrelated executable's directory or the user's PATH.
		handle, err := windows.LoadLibraryEx(dll, 0, windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
		if err != nil {
			return fmt.Errorf("managed Assistant runtime requires %s: %w; install or repair the Microsoft Visual C++ v14 Redistributable for Windows %s: https://learn.microsoft.com/en-us/cpp/windows/latest-supported-vc-redist", dll, err, runtime.GOARCH)
		}
		_ = windows.FreeLibrary(handle)
	}
	return nil
}
