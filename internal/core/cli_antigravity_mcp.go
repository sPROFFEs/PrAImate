package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const antigravityMCPConfigEnv = "PRAIMATE_ANTIGRAVITY_MCP_CONFIG"

// Antigravity loads MCP configurations from globals or agent plugins.
// Carry the selected servers into a private launch plugin and a uniquely named
// agent, preserving existing configuration and keeping credentials out of projects.
func antigravityMCPConfig(servers []MCPServer) (map[string]string, error) {
	entries := make(map[string]any, len(servers))
	for _, server := range servers {
		entry := map[string]any{}
		switch server.Transport {
		case MCPTransportStdio:
			command, args := resolvedStdioMCPCommand(server)
			entry["command"], entry["args"] = command, orEmpty(args)
			if len(server.Env) > 0 {
				entry["env"] = server.Env
			}
		case MCPTransportHTTP, MCPTransportSSE:
			entry["serverUrl"] = server.URL
			if headers := MCPHTTPHeaders(server); len(headers) > 0 {
				entry["headers"] = headers
			}
			if server.Auth["type"] == string(MCPAuthOAuth) {
				entry["oauth"] = map[string]string{"clientId": server.Auth["client_id"], "clientSecret": server.Auth["client_secret"]}
			}
		default:
			return nil, fmt.Errorf("unsupported Antigravity MCP transport %q", server.Transport)
		}
		entries[server.ID] = entry
	}
	raw, err := json.Marshal(map[string]any{"mcpServers": entries})
	return map[string]string{antigravityMCPConfigEnv: string(raw)}, err
}

func prepareAntigravityMCPConfig(raw string) ([]string, func(), error) {
	noop := func() {}
	var config struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil || config.Servers == nil {
		return nil, noop, fmt.Errorf("invalid Antigravity launch MCP configuration")
	}
	dir, err := os.MkdirTemp("", "praimate-agy-")
	if err != nil {
		return nil, noop, err
	}
	var agentDir string
	cleanup := func() {
		_ = os.RemoveAll(dir)
		if agentDir != "" {
			_ = os.RemoveAll(agentDir)
		}
	}
	plugin := filepath.Join(dir, "plugin")
	manifest := map[string]string{"name": "praimate-session", "version": "1.0.0", "description": "PrAImate session MCP tools"}
	if err := writeJSONFile(filepath.Join(plugin, ".antigravity-plugin", "plugin.json"), manifest); err != nil {
		cleanup()
		return nil, noop, err
	}
	if err := os.WriteFile(filepath.Join(plugin, "mcp_config.json"), []byte(raw), 0600); err != nil {
		cleanup()
		return nil, noop, err
	}
	// 1.4 selects discovered agents by name and no longer accepts an arbitrary
	// Markdown path. Register a uniquely named, private launch agent without
	// editing project instructions or any existing user configuration. This
	// discovery form is also understood by 1.3. Remove only our own directory.
	geminiDir := strings.TrimSpace(os.Getenv("ANTIGRAVITY_GEMINI_DIR"))
	if geminiDir == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			cleanup()
			return nil, noop, homeErr
		}
		geminiDir = filepath.Join(home, ".gemini")
	}
	agents := filepath.Join(geminiDir, "config", "agents")
	if err := os.MkdirAll(agents, 0700); err != nil {
		cleanup()
		return nil, noop, err
	}
	agentDir, err = os.MkdirTemp(agents, "praimate-session-")
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	name := filepath.Base(agentDir)
	metadata, err := yaml.Marshal(map[string]any{"name": name, "description": "PrAImate session with its selected MCP tools", "plugins": []string{plugin}, "enable_mcp_tools": true, "subagent": false, "mainAgent": true})
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	body := "---\n" + string(metadata) + "---\n\nFollow the user's task and session instructions. Use the attached MCP tools for on-demand skill loading. Native execution permissions remain those selected for this CLI session.\n"
	path := filepath.Join(agentDir, "agent.md")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		cleanup()
		return nil, noop, err
	}
	return []string{"--agent", name}, cleanup, nil
}
