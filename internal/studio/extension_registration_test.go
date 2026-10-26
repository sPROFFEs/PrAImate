package studio

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRegisteredExtensionUsesPrivateRegistryAndAcceptsEditorMetadata(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "praimate.praimate-studio-"+embeddedExtensionVersion())
	if err := provisionExtension(installed); err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(ExtensionPackageJSON), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["__metadata"] = map[string]any{"installedTimestamp": 123}
	body, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(installed, "package.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	if !extensionCurrent(installed) {
		t.Fatal("editor installation metadata made the bundled extension appear outdated")
	}
	registry := filepath.Join(root, "extensions.json")
	for _, tc := range []struct {
		name, location, version string
		registered              bool
	}{
		{"installed", filepath.Base(installed), embeddedExtensionVersion(), true},
		{"outdated", filepath.Base(installed), "0.0.1", false},
		{"missing files", "missing", embeddedExtensionVersion(), false},
		{"outside profile", "../outside", embeddedExtensionVersion(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal([]any{map[string]any{
				"identifier": map[string]string{"id": extensionID}, "version": tc.version, "relativeLocation": tc.location,
			}})
			if err := os.WriteFile(registry, body, 0600); err != nil {
				t.Fatal(err)
			}
			dir, current, err := registeredExtension(root)
			if err != nil || current != tc.registered || (current && dir != installed) {
				t.Fatalf("unexpected registration: %s %v %v", dir, current, err)
			}
		})
	}
	if err := os.WriteFile(registry, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, current, err := registeredExtension(root); err != nil || current {
		t.Fatalf("empty editor registry must require registration: %v %v", current, err)
	}
}

func TestEditorExtensionCommandUsesCLIForNativeExecutables(t *testing.T) {
	t.Setenv("ELECTRON_RUN_AS_NODE", "inherited")
	t.Setenv("VSCODE_IPC_HOOK_CLI", "unrelated-editor")
	for _, name := range []string{"codium", "Code.exe", filepath.Join("VSCodium.app", "Contents", "MacOS", "Electron")} {
		t.Run(name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "editor with spaces", name)
			script := filepath.Join(filepath.Dir(bin), "resources", "app", "out", "cli.js")
			if filepath.Base(bin) == "Electron" {
				script = filepath.Join(filepath.Dir(bin), "..", "Resources", "app", "out", "cli.js")
			}
			if err := os.MkdirAll(filepath.Dir(script), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(script, []byte("fixture"), 0644); err != nil {
				t.Fatal(err)
			}
			cmd, err := editorExtensionCommand(context.Background(), bin, "--install-extension", "extension with spaces.vsix", "--force")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cmd.Args, []string{bin, script, "--install-extension", "extension with spaces.vsix", "--force"}) {
				t.Fatalf("native executable would launch the GUI or reinterpret arguments: %q", cmd.Args)
			}
			if !slices.Contains(cmd.Env, "ELECTRON_RUN_AS_NODE=1") {
				t.Fatal("native extension CLI must run as Node")
			}
			for _, item := range cmd.Env {
				if strings.HasPrefix(item, "VSCODE_IPC_HOOK_CLI=") || item == "ELECTRON_RUN_AS_NODE=inherited" {
					t.Fatalf("extension installer inherited another editor instance's startup flag: %s", item)
				}
			}
		})
	}
}

func TestDetectBinarySkipsManagedWrapperWithMissingRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses Unix launch scripts")
	}
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	app, _ := AppDir()
	if err := os.MkdirAll(filepath.Join(app, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(app, "bin", "codium")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 127\n"), 0755); err != nil {
		t.Fatal(err)
	}
	system := t.TempDir()
	working := filepath.Join(system, "codium")
	if err := os.WriteFile(working, []byte("#!/bin/sh\necho fixture\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", system)
	if bin, _ := DetectBinary(); bin != working {
		t.Fatalf("broken managed wrapper won over installed editor: %s", bin)
	}
}

// Exercises only the installed editor's extension CLI with isolated data, not
// its GUI, the user's profile, project files or the application's database.
func TestInstalledEditorFindsProvisionedExtension(t *testing.T) {
	bin := os.Getenv("PRAIMATE_TEST_CODIUM")
	if bin == "" {
		t.Skip("set PRAIMATE_TEST_CODIUM to verify native extension discovery")
	}
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	ext, _ := ExtensionsDir()
	data, _ := UserDataDir()
	if err := provisionExtensions(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list := func() string {
		t.Helper()
		cmd := exec.CommandContext(ctx, bin, "--extensions-dir", ext, "--user-data-dir", data, "--list-extensions", "--show-versions")
		cmd.Env = studioEnvironment(os.Environ(), map[string]string{"ELECTRON_RUN_AS_NODE": "", "VSCODE_IPC_HOOK_CLI": ""})
		body, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("native editor extension listing: %v: %s", err, body)
		}
		return string(body)
	}
	if !strings.Contains(strings.ToLower(list()), "praimate.praimate-studio@") {
		t.Fatal("freshly provisioned extension was not discovered")
	}
	// Simulate an editor profile whose installed-extension registry no longer
	// contains our extension although its files remain on disk.
	if err := os.WriteFile(filepath.Join(ext, "extensions.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	listed := list()
	t.Logf("Extension remains discoverable with a stale registry: %v", strings.Contains(strings.ToLower(listed), "praimate.praimate-studio@"))
	status, err := NewManager(nil).GetStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(listed), "praimate.praimate-studio@") && status.State == StateInstalled {
		t.Fatal("Studio reports ready although the editor cannot find the extension")
	}
	if _, err := NewManager(nil).Repair(ctx); err != nil {
		dir, registered, registryErr := registeredExtension(ext)
		t.Logf("Repaired registry: directory=%s current=%v error=%v", filepath.Base(dir), registered, registryErr)
		for name, want := range extensionFiles() {
			body, readErr := os.ReadFile(filepath.Join(dir, name))
			if readErr != nil || string(body) != want {
				t.Logf("Editor-managed file differs: %s", name)
			}
		}
		t.Fatalf("repair failed: %v", err)
	}
	if !strings.Contains(strings.ToLower(list()), "praimate.praimate-studio@") {
		t.Fatal("repair did not register the extension with the editor")
	}
	status, err = NewManager(nil).GetStatus(ctx)
	if err != nil || status.State != StateInstalled {
		t.Fatalf("repaired extension is not ready: %+v %v", status, err)
	}
}
