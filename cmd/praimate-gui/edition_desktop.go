//go:build !server

package main

import (
	"fmt"
	"os"
)

// Desktop builds do not import the remote adapter or read its configuration.
type editionServices struct{}

func newEditionServices() editionServices                    { return editionServices{} }
func (*editionServices) start(*App)                          {}
func (*editionServices) close()                              {}
func (*editionServices) publish(string, any)                 {}
func (*editionServices) localTerminalInput(string)           {}
func (*editionServices) skipLocalTerminalResize(string) bool { return false }
func (*editionServices) openOwner(string) error              { return nil }
func (*editionServices) releaseOwner()                       {}
func (*editionServices) passwordChanged(*App, string) error  { return nil }
func (*editionServices) connectManaged(*App) bool            { return false }
func (*editionServices) isClient() bool                      { return false }
func editionCommand(args []string) (bool, int) {
	if len(args) > 0 && args[0] == "--server" {
		fmt.Fprintln(os.Stderr, "Headless remote service requires the server edition")
		return true, 2
	}
	return false, 0
}
