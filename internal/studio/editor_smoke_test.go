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

// Opt-in: opens an isolated, temporary editor profile with the development
// extension and a fixture Core. Never touches the user's database or profile.
func TestRealEditorSmoke(t *testing.T) {
	bin := os.Getenv("PRAIMATE_TEST_CODIUM")
	if bin == "" {
		t.Skip("set PRAIMATE_TEST_CODIUM to run the real editor smoke test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("native Windows CLI fixture executables are not supplied by this Linux smoke test")
	}
	s, _ := fixture(t)
	endpoint, token, err := s.StartLocal()
	if err != nil {
		t.Fatal(err)
	}
	extension, err := filepath.Abs("extension")
	if err != nil {
		t.Fatal(err)
	}
	profile := t.TempDir()
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--user-data-dir", filepath.Join(profile, "data"), "--extensions-dir", filepath.Join(profile, "extensions"),
		"--extensionDevelopmentPath="+extension, "--extensionTestsPath="+filepath.Join(extension, "editor-smoke.cjs"),
		"--disable-workspace-trust", "--skip-welcome", "--skip-release-notes", "--disable-updates", "--disable-gpu", s.session.Workspace)
	cmd.Env = studioEnvironment(os.Environ(), map[string]string{"PRAIMATE_SOCK": endpoint, "PRAIMATE_STUDIO_TOKEN": token, "PRAIMATE_STUDIO_CONFIG": string(config), "VSCODE_IPC_HOOK_CLI": "", "ELECTRON_RUN_AS_NODE": ""})
	cmd.Env = append(cmd.Env, "PRAIMATE_TEST_RESULT="+resultFile, "PRAIMATE_TEST_TERMINAL_DIR="+profile)
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
