package core

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestCopilotMCPLaunchConfigIsPrivateAndTemporary(t *testing.T) {
	env, err := copilotMCPConfig([]MCPServer{
		{ID: "local", Transport: MCPTransportStdio, Command: "test-mcp", Args: []string{"serve"}, Env: map[string]string{"TEST_MCP_KEY": "secret-fixture"}},
		{ID: "remote", Transport: MCPTransportHTTP, URL: "https://example.invalid/mcp", Auth: map[string]string{"token": "bearer-fixture"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	args, cleanup, err := PrepareInteractiveCLIConfig("copilot", env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(args) != 2 || args[0] != "--additional-mcp-config" || !strings.HasPrefix(args[1], "@") {
		t.Fatal(args)
	}
	path := strings.TrimPrefix(args[1], "@")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-fixture") || strings.Contains(string(raw), "bearer-fixture") {
		t.Fatal("credential written to config")
	}
	var config struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.Servers["local"]["type"] != "local" || config.Servers["remote"]["type"] != "http" {
		t.Fatal(config)
	}
	if env["TEST_MCP_KEY"] != "secret-fixture" {
		t.Fatal("missing environment credential")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatal("config permissions", info.Mode())
		}
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("temporary config retained", err)
	}
}
