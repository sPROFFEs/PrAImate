package studio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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
