//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"os"
)

// x/term enables VT input. The Windows console also needs VT output for the
// editor's cursor controls; preserve the original mode on exit.
func enableConsole() (func(), error) {
	handle := windows.Handle(os.Stderr.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(handle, mode) }, nil
}
