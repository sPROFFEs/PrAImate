package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/studio"
)

func TestManagedStudioInstallationUsesStudioManager(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	appDir, _ := studio.AppDir()
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	name := "codium"
	if runtime.GOOS == "windows" {
		name = "VSCodium.exe"
	}
	if err := os.WriteFile(filepath.Join(appDir, name), []byte("#!/bin/sh\necho fixture\n"), 0755); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	a.ctx = context.Background()
	methods, err := a.ListToolInstallMethods("praimate-studio")
	if err != nil || len(methods) != 1 || methods[0].ID != "studio" {
		t.Fatalf("native Studio installer missing from CLIs: %+v %v", methods, err)
	}
	if err := a.InstallManagedTool("praimate-studio", "studio"); err != nil {
		t.Fatal(err)
	}
	status, err := a.StudioGetStatus()
	if err != nil || status.State != studio.StateInstalled {
		t.Fatalf("Studio setup was not reflected in status: %+v %v", status, err)
	}
	tools, err := a.ListManagedTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.ID == "praimate-studio" {
			if !tool.Installed || tool.Binary != status.BinaryPath {
				t.Fatalf("CLIs and Studio disagree about the installation: %+v %+v", tool, status)
			}
			return
		}
	}
	t.Fatal("Studio missing from managed tools")
}

func TestListCLIBackendsRefreshesUserPATHBeforeDetection(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
		t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
		t.Setenv("PATHEXT", ".BAT;.CMD;.EXE")
		t.Setenv("PATH", os.Getenv("SystemRoot")+`\System32;`+os.Getenv("SystemRoot"))
	} else {
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		t.Setenv("PATH", "/usr/bin:/bin")
	}

	binDir := filepath.Join(home, ".bun", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "codex"
	body := "#!/bin/sh\necho codex 9.9.9\n"
	if runtime.GOOS == "windows" {
		name = "codex.bat"
		body = "@echo off\r\necho codex 9.9.9\r\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.ctx = context.Background()
	backends, err := app.ListCLIBackends()
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range backends {
		if backend.ID == "codex" {
			if !backend.Installed {
				t.Fatalf("Codex in a newly-created user bin dir was not detected: %+v", backend)
			}
			return
		}
	}
	t.Fatal("Codex missing from CLI catalogue")
}

func TestListCLIBackendsFindsUserNodeManagersInServiceEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixtures")
	}
	for _, manager := range []string{"nvm", "fnm"} {
		t.Run(manager, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PRAIMATE_HOME", filepath.Join(home, "praimate"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
			for _, key := range []string{"NVM_DIR", "NVM_BIN", "FNM_DIR", "FNM_MULTISHELL_PATH", "NVM_HOME", "NVM_SYMLINK", "PRAIMATE_NODE_BIN"} {
				t.Setenv(key, "")
			}
			bin := filepath.Join(home, ".config", "nvm", "versions", "node", "v24.1.0", "bin")
			if manager == "fnm" {
				bin = filepath.Join(home, ".local", "share", "fnm", "node-versions", "v24.1.0", "installation", "bin")
			}
			if err := os.MkdirAll(bin, 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"node", "npm", "codex"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho fixture-cli 9.9.9\n"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			system := filepath.Join(home, "system-bin")
			if err := os.MkdirAll(system, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(system, "codex"), []byte("#!/bin/sh\necho fixture-cli 1.0.0\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", system+string(os.PathListSeparator)+"/usr/bin:/bin")
			a := NewApp()
			a.ctx = context.Background()
			backends, err := a.ListCLIBackends()
			if err != nil {
				t.Fatal(err)
			}
			for _, backend := range backends {
				if backend.ID == "codex" {
					if !backend.Installed || backend.Binary != filepath.Join(bin, "codex") || backend.Version != "fixture-cli 9.9.9" {
						t.Fatalf("service detected another Codex: %+v", backend)
					}
					return
				}
			}
			t.Fatal("Codex is missing from backends")
		})
	}
}

func TestListCLIsDetectsManagedOpenClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PRAIMATE_HOME", filepath.Join(home, "praimate"))
	t.Setenv("PATH", "")
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".BAT;.CMD;.EXE")
	}

	binDir := filepath.Join(home, "praimate", "agents", "openclaude", "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "openclaude"
	body := "#!/bin/sh\necho openclaude 9.9.9\n"
	if runtime.GOOS == "windows" {
		name = "openclaude.bat"
		body = "@echo off\r\necho openclaude 9.9.9\r\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	app.ctx = context.Background()
	core.RegisterAllCLIAdapters()
	for _, cli := range app.ListCLIs() {
		if cli.ID == "openclaude" {
			if !cli.Available {
				t.Fatalf("managed OpenClaude was not available in launch pickers: %+v", cli)
			}
			return
		}
	}
	t.Fatal("OpenClaude missing from launch picker catalogue")
}
