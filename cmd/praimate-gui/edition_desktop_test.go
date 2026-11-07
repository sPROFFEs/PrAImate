//go:build !server

package main

import (
	"os"
	"testing"
)

func TestDesktopIgnoresRemoteConfiguration(t *testing.T) {
	t.Setenv("PRAIMATE_REMOTE_TOKEN", "synthetic-test-token")
	e := newEditionServices()
	e.start(&App{})
	e.publish("example", nil)
	e.close()
	if os.Getenv("PRAIMATE_REMOTE_TOKEN") != "synthetic-test-token" {
		t.Fatal("desktop consumed remote configuration")
	}
}
