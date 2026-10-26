package studio

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Opt-in: opens an isolated editor profile with the registered bundled
// extension and a fixture Core. Never touches the user's database or profile.
func TestRealEditorSmoke(t *testing.T) {
	bin := os.Getenv("PRAIMATE_TEST_CODIUM")
	if bin == "" {
		t.Skip("set PRAIMATE_TEST_CODIUM to run the real editor smoke test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("native Windows CLI fixture executables are not supplied by this Linux smoke test")
	}
	profile := t.TempDir()
	t.Setenv("HOME", profile)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(profile, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(profile, "cache"))
	s, _ := fixture(t)
	endpoint, token, err := s.StartLocal()
	if err != nil {
		t.Fatal(err)
	}
	extension, err := filepath.Abs("extension")
	if err != nil {
		t.Fatal(err)
	}
	extensionsDir, _ := ExtensionsDir()
	dataDir, _ := UserDataDir()
	if err := os.MkdirAll(extensionsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extensionsDir, "extensions.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := prepareExtensions(ctx, bin); err != nil {
		t.Fatal(err)
	}
	installed, registered, err := registeredExtension(extensionsDir)
	if err != nil || !registered {
		t.Fatalf("bundled extension not registered: %v", err)
	}
	// The test runner is a separate development extension. PrAImate itself
	// must be discovered from the installed registry, exactly as in Studio.
	helper := filepath.Join(profile, "test-runner")
	if err := os.MkdirAll(helper, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helper, "package.json"), []byte(`{"name":"praimate-test-runner","publisher":"PrAImateTests","version":"1.0.0","engines":{"vscode":"^1.96.0"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	resultFile := filepath.Join(profile, "result.json")
	fixtureDir := filepath.Join(profile, "cli tools with spaces")
	if err := os.MkdirAll(fixtureDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, cli := range []string{"claude", "openclaude", "codex", "opencode", "praimate-code"} {
		script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then exit 0; fi\nif [ -n \"$PRAIMATE_STUDIO_TOKEN\" ]; then exit 9; fi\nprintf '%s\\n' \"$PWD\" \"$@\" > \"$PRAIMATE_TEST_TERMINAL_DIR/" + cli + ".txt\"\n"
		if err := os.WriteFile(filepath.Join(fixtureDir, cli), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", fixtureDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	config, _ := json.Marshal(sessionConfig{CLI: "claude", Model: "fixture-model", Tools: "safe", Workspace: s.session.Workspace})
	cmd := exec.CommandContext(ctx, bin, "--user-data-dir", dataDir, "--extensions-dir", extensionsDir,
		"--extensionDevelopmentPath="+helper, "--extensionTestsPath="+filepath.Join(extension, "editor-smoke.cjs"),
		"--disable-workspace-trust", "--skip-welcome", "--skip-release-notes", "--disable-updates", "--disable-gpu", s.session.Workspace)
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = studioEnvironment(os.Environ(), map[string]string{"PRAIMATE_SOCK": endpoint, "PRAIMATE_STUDIO_TOKEN": token, "PRAIMATE_STUDIO_CONFIG": string(config), "VSCODE_IPC_HOOK_CLI": "", "ELECTRON_RUN_AS_NODE": ""})
	cmd.Env = append(cmd.Env, "PRAIMATE_TEST_RESULT="+resultFile, "PRAIMATE_TEST_TERMINAL_DIR="+profile, "PRAIMATE_TEST_EXTENSION_PATH="+installed)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real editor smoke: %v\n%s", err, output)
	}
	// Some editor launchers detach. An exit code alone is not evidence that
	// the extension-host tests ran; require their explicit completion marker.
	var result []byte
	for len(result) == 0 && ctx.Err() == nil {
		result, _ = os.ReadFile(resultFile)
		if len(result) == 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if len(result) == 0 {
		t.Fatalf("editor exited without test completion marker\n%s", output)
	}
	var summary struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(result, &summary) != nil || !summary.OK {
		t.Fatalf("editor smoke failed: %s", result)
	}
	t.Logf("extension-host verification: %s", result)
	t.Logf("real editor smoke completed:\n%s", output)
}
