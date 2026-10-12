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

// Antigravity 1.3.x loads MCP configurations from globals or agent plugins.
// Carry the selected servers into a private launch plugin, rather than changing
// the user's global configuration or leaving credentials in the project.
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
			if header, token, ok := headerAndToken(server); ok {
				if strings.EqualFold(header, "Authorization") && !strings.HasPrefix(strings.ToLower(token), "bearer ") {
					token = "Bearer " + token
				}
				entry["headers"] = map[string]string{header: token}
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
	cleanup := func() { _ = os.RemoveAll(dir) }
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
	metadata, err := yaml.Marshal(map[string]any{"name": "praimate-session", "description": "PrAImate session with its selected MCP tools", "plugins": []string{plugin}, "enable_mcp_tools": true})
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	body := "---\n" + string(metadata) + "---\n\nFollow the user's task and session instructions. Use the attached MCP tools for on-demand skill loading. Native execution permissions remain those selected for this CLI session.\n"
	path := filepath.Join(dir, "agent.md")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		cleanup()
		return nil, noop, err
	}
	return []string{"--agent", path}, cleanup, nil
}
