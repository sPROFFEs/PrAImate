package core

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestAntigravityMCPLaunchIsScopedAndCleaned(t *testing.T) {
	if !CapabilitiesForCLI("antigravity").MCP {
		t.Fatal("MCP support is not advertised to Desktop/Studio")
	}
	workspace := t.TempDir()
	servers := []MCPServer{{ID: "praimate_skills", Transport: MCPTransportStdio, Command: "fixture-shim", Args: []string{"-mcp-skills", "http://localhost/skills"}}, {ID: "remote", Transport: MCPTransportHTTP, URL: "https://fixture.invalid/mcp", Auth: map[string]string{"token": "fixture-only"}}}
	env, err := writeMCPConfigForRun("antigravity", workspace, servers)
	if err != nil {
		t.Fatal(err)
	}
	args, cleanup, err := PrepareInteractiveCLIConfig("antigravity", env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(args) != 2 || args[0] != "--agent" {
		t.Fatalf("MCP config was not passed to Antigravity: %v", args)
	}
	body, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "---", 3)
	if len(parts) != 3 {
		t.Fatal("missing launch agent metadata")
	}
	var meta struct {
		Plugins []string `yaml:"plugins"`
	}
	if err := yaml.Unmarshal([]byte(parts[1]), &meta); err != nil || len(meta.Plugins) != 1 {
		t.Fatalf("missing MCP plugin: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(meta.Plugins[0], "mcp_config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			URL     string            `json:"serverUrl"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.Servers["praimate_skills"].Command != "fixture-shim" || config.Servers["remote"].URL != "https://fixture.invalid/mcp" || config.Servers["remote"].Headers["Authorization"] != "Bearer fixture-only" {
		t.Fatal("wrong native MCP schema")
	}
	entries, _ := os.ReadDir(workspace)
	if len(entries) != 0 {
		t.Fatal("modified project files")
	}
	cleanup()
	if _, err := os.Stat(args[1]); !os.IsNotExist(err) {
		t.Fatal("launch MCP files retained after exit")
	}
}

func TestAntigravityInstalledSessionMCPConfig(t *testing.T) {
	bin := os.Getenv("PRAIMATE_TEST_ANTIGRAVITY_BINARY")
	if bin == "" {
		t.Skip("set PRAIMATE_TEST_ANTIGRAVITY_BINARY to validate the native CLI contract")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("native CLI path must be absolute")
	}
	root := t.TempDir()
	for k, v := range map[string]string{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data")} {
		t.Setenv(k, v)
	}
	shim, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required for the native MCP fixture")
	}
	env, err := antigravityMCPConfig([]MCPServer{{ID: "praimate_skills", Transport: MCPTransportStdio, Command: shim, Args: []string{"--version"}}})
	if err != nil {
		t.Fatal(err)
	}
	args, cleanup, err := PrepareInteractiveCLIConfig("antigravity", env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	plugin := filepath.Join(filepath.Dir(args[1]), "plugin")
	validate := exec.CommandContext(ctx, bin, "plugin", "validate", plugin)
	validate.Dir = root
	if output, err := validate.CombinedOutput(); err != nil || !strings.Contains(string(output), "1 processed") {
		t.Fatalf("native CLI rejected MCP plugin: %s %v", output, err)
	}
	// Empty JSONL input exercises CLI setup without sending a model request.
	launch := exec.CommandContext(ctx, bin, append([]string{"--input-format", "stream-json", "--output-format", "stream-json"}, args...)...)
	launch.Dir = root
	output, err := launch.Output()
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Event string `json:"event"`
		Init  struct {
			Agent string `json:"agent"`
		} `json:"init"`
	}
	if err := json.Unmarshal(output, &event); err != nil || event.Event != "init" || event.Init.Agent != args[1] {
		t.Fatalf("native CLI did not select launch MCP agent: %s %v", output, err)
	}
}
