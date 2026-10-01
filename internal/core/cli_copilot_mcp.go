package core

import (
	"encoding/json"
	"os"
)

// Session-only MCP configuration. Credential values stay in environment
// variables; no global Copilot configuration or project file is replaced.
func copilotMCPConfig(servers []MCPServer) (map[string]string, error) {
	env := map[string]string{}
	entries := map[string]any{}
	for _, s := range servers {
		entry := map[string]any{"tools": []string{"*"}}
		switch s.Transport {
		case MCPTransportStdio:
			command, args := resolvedStdioMCPCommand(s)
			entry["type"], entry["command"], entry["args"] = "local", command, orEmpty(args)
			if len(s.Env) > 0 {
				entry["env"] = envRefs(s, env, "${%s}")
			}
		case MCPTransportHTTP, MCPTransportSSE:
			entry["type"], entry["url"] = string(s.Transport), s.URL
			if headers := headerRefs(s, env, "Bearer ${%s}", "${%s}"); len(headers) > 0 {
				entry["headers"] = headers
			}
		}
		entries[s.ID] = entry
	}
	raw, err := json.Marshal(map[string]any{"mcpServers": entries})
	env["PRAIMATE_COPILOT_MCP_CONFIG"] = string(raw)
	return env, err
}

// PrepareInteractiveCLIConfig adds the same per-launch MCP configuration as the
// headless adapter without changing native terminal permissions.
func PrepareInteractiveCLIConfig(cli string, env map[string]string) ([]string, func(), error) {
	noop := func() {}
	if cli != "copilot" || env["PRAIMATE_COPILOT_MCP_CONFIG"] == "" {
		return nil, noop, nil
	}
	f, err := os.CreateTemp("", "praimate-copilot-mcp-*.json")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	_, writeErr := f.WriteString(env["PRAIMATE_COPILOT_MCP_CONFIG"])
	closeErr := f.Close()
	if writeErr != nil {
		cleanup()
		return nil, noop, writeErr
	}
	if closeErr != nil {
		cleanup()
		return nil, noop, closeErr
	}
	return []string{"--additional-mcp-config", "@" + f.Name()}, cleanup, nil
}
