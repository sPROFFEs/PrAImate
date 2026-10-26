package installer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStudioInstallUsesNativeBackendInsteadOfPackageManager(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture shell scripts run on Unix")
	}
	dir := t.TempDir()
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	t.Setenv("STUDIO_INSTALL_RECORD", filepath.Join(dir, "record"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	for name, body := range map[string]string{
		"praimate": "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$STUDIO_INSTALL_RECORD\"\n",
		"npm":      "#!/bin/sh\nexit 74\n",
		"pnpm":     "#!/bin/sh\nexit 74\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	methods := ToolMethods(ToolPraimateStudio, ActionInstall, DetectOS())
	if len(methods) != 1 {
		t.Fatal("missing Studio installer")
	}
	if err := Run(context.Background(), methods[0], io.Discard, io.Discard); err != nil {
		t.Fatalf("Studio installer did not invoke its native backend: %v", err)
	}
	body, err := os.ReadFile(os.Getenv("STUDIO_INSTALL_RECORD"))
	if err != nil || strings.TrimSpace(string(body)) != "studio\ninstall" {
		t.Fatalf("unexpected native install arguments: %q %v", body, err)
	}
}
