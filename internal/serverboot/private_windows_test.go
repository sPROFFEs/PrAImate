package serverboot

import (
	"bytes"
	"os/exec"
	"testing"
)

func TestWindowsStartupCredentialUsesDPAPI(t *testing.T) {
	plain := []byte("synthetic-private-startup-password")
	protected, err := protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(protected)
	if bytes.Contains(protected, plain) {
		t.Fatal("DPAPI output retained the plaintext credential")
	}
	opened, err := unprotect(protected)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(opened)
	if !bytes.Equal(opened, plain) {
		t.Fatal("startup credential did not round trip")
	}
}
func TestWindowsStartupCommandsStayHidden(t *testing.T) {
	cmd := exec.Command("fixture.exe")
	hideWindow(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("startup polling can create visible terminal windows")
	}
}
