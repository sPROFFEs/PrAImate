//go:build windows

package gitutil

import (
	"context"
	"testing"
)

func TestOriginProbeDoesNotCreateConsole(t *testing.T) {
	cmd := originCommand(context.Background(), t.TempDir())
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("background origin probe can open a visible console")
	}
}
