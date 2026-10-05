package studio

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEditorEnvironmentUnsetsElectronAndStaleCLIFlags(t *testing.T) {
	for _, base := range [][]string{
		{"Path=C:\\Tools", "electron_run_as_node=1", "VSCODE_IPC_HOOK_CLI=stale"},
		{"PATH=/usr/bin", "ELECTRON_RUN_AS_NODE="},
	} {
		out := studioEnvironment(base, map[string]string{"ELECTRON_RUN_AS_NODE": "", "VSCODE_IPC_HOOK_CLI": "", "PRAIMATE_STUDIO_CONFIG": "fixture"})
		for _, item := range out {
			key, _, _ := strings.Cut(item, "=")
			if strings.EqualFold(key, "ELECTRON_RUN_AS_NODE") || strings.EqualFold(key, "VSCODE_IPC_HOOK_CLI") {
				t.Fatalf("GUI startup flag must be absent, not empty: %q", item)
			}
		}
	}
}

func TestEditorStartupReportsFailuresAndAcceptsDetachedLaunchers(t *testing.T) {
	for _, mode := range []string{"failed", "detached", "running", "large-error"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestEditorStartupHelper$")
			cmd.Env = studioEnvironment(os.Environ(), map[string]string{"PRAIMATE_TEST_EDITOR_HELPER": mode, "GORACE": "atexit_sleep_ms=0"})
			err := startEditor(cmd)
			if mode == "running" {
				t.Cleanup(func() { _ = cmd.Process.Kill() })
			}
			if mode == "failed" || mode == "large-error" {
				if err == nil || !strings.Contains(err.Error(), "editor exited during startup") {
					t.Fatalf("early editor failure was hidden: %v", err)
				}
				if mode == "failed" && !strings.Contains(err.Error(), "fixture startup error") {
					t.Fatalf("startup diagnostic lost: %v", err)
				}
				if len(err.Error()) > 2200 {
					t.Fatal("unbounded editor startup diagnostics")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEditorStartupHelper(t *testing.T) {
	switch os.Getenv("PRAIMATE_TEST_EDITOR_HELPER") {
	case "failed":
		_, _ = os.Stderr.WriteString("fixture startup error\n")
		os.Exit(7)
	case "large-error":
		_, _ = os.Stderr.WriteString(strings.Repeat("x", 64<<10))
		os.Exit(9)
	case "detached":
		os.Exit(0)
	case "running":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
}

func TestUpgradeIncludesLegacyBuiltinExtension(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	app, _ := AppDir()
	legacy := filepath.Join(app, "resources", "app", "extensions", "praimate")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "package.json"), []byte(`{"name":"praimate-studio","version":"0.2.0"}`), 0644); err != nil {
		t.Fatal(err)
	}
	ext, _ := ExtensionsDir()
	if err := provisionExtension(filepath.Join(ext, "praimate")); err != nil {
		t.Fatal(err)
	}
	name := "codium"
	if runtime.GOOS == "windows" {
		name = "VSCodium.exe"
	}
	if err := os.WriteFile(filepath.Join(app, name), []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	status, err := NewManager(nil).GetStatus(context.Background())
	if err != nil || status.State != StateUpdateAvailable {
		t.Fatalf("stale builtin was missed: %v %v", status, err)
	}
	if err := provisionExtensions(); err != nil {
		t.Fatal(err)
	}
	if !extensionCurrent(legacy) || !extensionCurrent(filepath.Join(ext, "praimate")) {
		t.Fatal("both extension copies must be upgraded")
	}
	status, err = NewManager(nil).GetStatus(context.Background())
	if err != nil || status.State != StateInstalled {
		t.Fatalf("upgrade not reflected: %v %v", status, err)
	}
	// Replacing the descriptor is required when Desktop restarts. Credentials
	// must remain private and never appear in public StudioStatus.
	for _, endpoint := range []string{"tcp://127.0.0.1:1234", "tcp://127.0.0.1:5678"} {
		if err := publishConnection(endpoint, "test-only-token", "backend with spaces", `{}`); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{legacy, filepath.Join(ext, "praimate")} {
		file := filepath.Join(dir, "connection.json")
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var descriptor struct{ Endpoint, Token, Backend, LaunchID string }
		if err := json.Unmarshal(body, &descriptor); err != nil {
			t.Fatal(err)
		}
		if descriptor.Endpoint != "tcp://127.0.0.1:5678" || descriptor.Token == "" || descriptor.LaunchID == "" {
			t.Fatal("stale or incomplete connection descriptor")
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("connection descriptor is not private")
		}
	}
}
