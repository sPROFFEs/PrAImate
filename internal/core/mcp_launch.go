package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// prepareMCPForRun resolves the agent's declared MCP servers, writes the
// selected CLI's project-scoped config file, and returns environment
// variables that must be present when that CLI launches.
func (c *Core) prepareMCPForRun(ctx context.Context, agent *Agent, cli, cwd string) (map[string]string, error) {
	if c.store == nil || agent == nil || len(agent.MCPServers) == 0 {
		return nil, nil
	}
	if cwd == "" {
		return nil, fmt.Errorf("MCP config: empty cwd")
	}
	servers, err := c.resolveAgentMCPServers(ctx, agent)
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, nil
	}
	return writeMCPConfigForRun(cli, cwd, servers)
}

// PrepareMCPForRun is the GUI/terminal-facing wrapper around the
// launch-time MCP config emission used by workflow runs.
func (c *Core) PrepareMCPForRun(ctx context.Context, agent *Agent, cli, cwd string) (map[string]string, error) {
	return c.PrepareMCPForRunWithExtra(ctx, agent, nil, cli, cwd)
}

// PrepareMCPForRunWithExtra prepares declared MCP servers plus internal system servers.
func (c *Core) PrepareMCPForRunWithExtra(ctx context.Context, agent *Agent, extra []MCPServer, cli, cwd string) (map[string]string, error) {
	if cwd == "" {
		return nil, fmt.Errorf("MCP config: empty cwd")
	}
	var servers []MCPServer
	if c.store != nil && agent != nil && len(agent.MCPServers) > 0 {
		resolved, err := c.resolveAgentMCPServers(ctx, agent)
		if err != nil {
			return nil, err
		}
		servers = resolved
	}
	servers = append(servers, extra...)
	if len(servers) == 0 {
		return nil, nil
	}
	return writeMCPConfigForRun(cli, cwd, servers)
}

// PrepareEnabledMCPForRun writes config for all globally enabled MCP
// servers. This is used for clean live-terminal sessions, where there
// is no PrAImate agent YAML to declare a narrower mcp_servers list.
func (c *Core) PrepareEnabledMCPForRun(ctx context.Context, cli, cwd string) (map[string]string, error) {
	return c.PrepareEnabledMCPForRunWithExtra(ctx, nil, cli, cwd)
}

// PrepareEnabledMCPForRunWithExtra prepares enabled MCP servers plus internal system servers.
func (c *Core) PrepareEnabledMCPForRunWithExtra(ctx context.Context, extra []MCPServer, cli, cwd string) (map[string]string, error) {
	var servers []MCPServer
	if c.store != nil {
		enabled, err := c.ListMCPServers(ctx, true)
		if err == nil {
			servers = enabled
		}
	}
	servers = append(servers, extra...)
	if len(servers) == 0 {
		return nil, nil
	}
	return writeMCPConfigForRun(cli, cwd, servers)
}

// PrepareSelectedMCPForRun writes only the MCP servers selected for a chat.
// Disabled servers are skipped even when their ID remains in an older chat.
// Calling this with an empty selection deliberately writes an empty generated
// config, so removing the final MCP from a chat takes effect on its next turn.
func (c *Core) PrepareSelectedMCPForRun(ctx context.Context, ids []string, cli, cwd string) (map[string]string, error) {
	return c.PrepareSelectedMCPForRunWithExtra(ctx, ids, nil, cli, cwd)
}

// PrepareSelectedMCPForRunWithExtra prepares selected MCP servers plus internal system servers.
func (c *Core) PrepareSelectedMCPForRunWithExtra(ctx context.Context, ids []string, extra []MCPServer, cli, cwd string) (map[string]string, error) {
	if cwd == "" {
		return nil, fmt.Errorf("MCP config: empty cwd")
	}
	var servers []MCPServer
	if c.store != nil && len(ids) > 0 {
		resolved, err := c.resolveMCPServerIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		servers = resolved
	}
	servers = append(servers, extra...)
	return writeSelectedMCPConfigForRun(cli, cwd, servers)
}

func (c *Core) resolveMCPServerIDs(ctx context.Context, ids []string) ([]MCPServer, error) {
	seen := make(map[string]bool, len(ids))
	out := make([]MCPServer, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		s, err := c.GetMCPServer(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("chat MCP %s: %w", id, err)
		}
		if s.Enabled {
			out = append(out, *s)
		}
	}
	return out, nil
}

func writeMCPConfigForRun(cli, cwd string, servers []MCPServer) (map[string]string, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	if cwd == "" {
		return nil, fmt.Errorf("MCP config: empty cwd")
	}
	env := map[string]string{}
	var err error
	switch cli {
	case "claude", "openclaude":
		err = writeClaudeMCPConfig(cwd, servers, env)
	case "codex":
		err = writeCodexMCPConfig(cwd, servers, env)
	case "opencode", "praimate-code":
		err = writeOpenCodeMCPConfig(cwd, servers, env)
	default:
		// Gemini/DeepSeek MCP wiring is intentionally absent in 1.0a;
		// silently skip so an agent can still run on those CLIs.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return env, nil
}

func writeSelectedMCPConfigForRun(cli, cwd string, servers []MCPServer) (map[string]string, error) {
	if cwd == "" {
		return nil, fmt.Errorf("MCP config: empty cwd")
	}
	env := map[string]string{}
	var err error
	switch cli {
	case "claude", "openclaude":
		err = writeClaudeMCPConfig(cwd, servers, env)
	case "codex":
		err = writeCodexMCPConfig(cwd, servers, env)
	case "opencode", "praimate-code":
		err = writeOpenCodeMCPConfig(cwd, servers, env)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return env, nil
}

func (c *Core) resolveAgentMCPServers(ctx context.Context, agent *Agent) ([]MCPServer, error) {
	out, err := c.resolveMCPServerIDs(ctx, agent.MCPServers)
	if err != nil {
		return nil, fmt.Errorf("agent %s: %w", agent.ID, err)
	}
	return out, nil
}

func writeClaudeMCPConfig(cwd string, servers []MCPServer, env map[string]string) error {
	type claudeServer map[string]any
	body := map[string]any{"mcpServers": map[string]claudeServer{}}
	dst := body["mcpServers"].(map[string]claudeServer)
	for _, s := range servers {
		entry := claudeServer{"type": claudeType(s.Transport)}
		switch s.Transport {
		case MCPTransportStdio:
			command, args := resolvedStdioMCPCommand(s)
			entry["command"] = command
			entry["args"] = orEmpty(args)
			if len(s.Env) > 0 {
				entry["env"] = envRefs(s, env, "${%s}")
			}
		case MCPTransportHTTP, MCPTransportSSE:
			entry["url"] = s.URL
			if headers := headerRefs(s, env, "Bearer ${%s}", "${%s}"); len(headers) > 0 {
				entry["headers"] = headers
			}
			if s.Auth["type"] == string(MCPAuthOAuth) {
				entry["oauth"] = oauthConfig(s, env, "${%s}")
			}
		}
		dst[s.ID] = entry
	}
	path := filepath.Join(cwd, ".mcp.json")
	return writeJSONFile(path, body)
}

func writeCodexMCPConfig(cwd string, servers []MCPServer, env map[string]string) error {
	var b bytes.Buffer
	b.WriteString("# Generated by PrAImate. Do not put secrets in this file.\n")
	for _, s := range servers {
		b.WriteString("\n[mcp_servers.")
		b.WriteString(tomlKey(s.ID))
		b.WriteString("]\n")
		b.WriteString("enabled = true\n")
		switch s.Transport {
		case MCPTransportStdio:
			command, args := resolvedStdioMCPCommand(s)
			b.WriteString("command = ")
			b.WriteString(tomlString(command))
			b.WriteByte('\n')
			if len(args) > 0 {
				b.WriteString("args = ")
				b.WriteString(tomlArray(args))
				b.WriteByte('\n')
			}
			if len(s.Env) > 0 {
				keys := mapKeys(s.Env)
				for _, key := range keys {
					env[key] = s.Env[key]
				}
				b.WriteString("env_vars = ")
				b.WriteString(tomlArray(keys))
				b.WriteByte('\n')
			}
		case MCPTransportHTTP, MCPTransportSSE:
			b.WriteString("url = ")
			b.WriteString(tomlString(s.URL))
			b.WriteByte('\n')
			if s.Auth["type"] == string(MCPAuthOAuth) {
				scopes := strings.Fields(s.Auth["scopes"])
				if len(scopes) > 0 {
					b.WriteString("scopes = ")
					b.WriteString(tomlArray(scopes))
					b.WriteByte('\n')
				}
			}
			if headers := codexHeaderEnvRefs(s, env); len(headers) > 0 {
				b.WriteString("env_http_headers = { ")
				i := 0
				for _, key := range mapKeys(headers) {
					if i > 0 {
						b.WriteString(", ")
					}
					b.WriteString(tomlBareOrQuotedKey(key))
					b.WriteString(" = ")
					b.WriteString(tomlString(headers[key]))
					i++
				}
				b.WriteString(" }\n")
			}
		}
	}
	path := filepath.Join(cwd, ".codex", "config.toml")
	return writeTextFile(path, b.Bytes())
}

func writeOpenCodeMCPConfig(cwd string, servers []MCPServer, env map[string]string) error {
	path := filepath.Join(cwd, "opencode.json")
	cfg := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}
	if cfg["$schema"] == nil {
		cfg["$schema"] = "https://opencode.ai/config.json"
	}
	mcp, _ := cfg["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
		cfg["mcp"] = mcp
	}
	// Remove entries generated by the previous PrAImate selection before
	// adding the current one. The sidecar lets us avoid touching MCP entries
	// the user authored directly in opencode.json.
	managedPath := filepath.Join(cwd, ".praimate", "mcp-opencode.json")
	var previous []string
	if raw, err := os.ReadFile(managedPath); err == nil {
		_ = json.Unmarshal(raw, &previous)
	}
	for _, id := range previous {
		delete(mcp, id)
	}
	managed := make([]string, 0, len(servers))
	for _, s := range servers {
		entry := map[string]any{"enabled": true}
		switch s.Transport {
		case MCPTransportStdio:
			command, args := resolvedStdioMCPCommand(s)
			entry["type"] = "local"
			entry["command"] = append([]string{command}, args...)
			if len(s.Env) > 0 {
				entry["environment"] = envRefs(s, env, "{env:%s}")
			}
		case MCPTransportHTTP, MCPTransportSSE:
			entry["type"] = "remote"
			entry["url"] = s.URL
			if headers := headerRefs(s, env, "Bearer {env:%s}", "{env:%s}"); len(headers) > 0 {
				entry["headers"] = headers
				if s.Auth["type"] != string(MCPAuthOAuth) {
					entry["oauth"] = false
				}
			}
			if s.Auth["type"] == string(MCPAuthOAuth) {
				entry["oauth"] = oauthConfig(s, env, "{env:%s}")
			}
		}
		mcp[s.ID] = entry
		managed = append(managed, s.ID)
	}
	if err := writeJSONFile(path, cfg); err != nil {
		return err
	}
	return writeJSONFile(managedPath, managed)
}

func ResolveStdioMCPCommand(s MCPServer) (string, []string) {
	return resolvedStdioMCPCommand(s)
}

func resolvedStdioMCPCommand(s MCPServer) (string, []string) {
	return expandMCPProcessArg(s.Command), expandMCPProcessArgs(s.Args)
}

func writeJSONFile(path string, body any) error {
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeTextFile(path, raw)
}

func writeTextFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func claudeType(t MCPTransport) string {
	switch t {
	case MCPTransportHTTP:
		return "http"
	case MCPTransportSSE:
		return "sse"
	default:
		return "stdio"
	}
}

func envRefs(s MCPServer, launchEnv map[string]string, pattern string) map[string]string {
	out := map[string]string{}
	for _, key := range mapKeys(s.Env) {
		launchEnv[key] = s.Env[key]
		out[key] = fmt.Sprintf(pattern, key)
	}
	return out
}

func headerRefs(s MCPServer, launchEnv map[string]string, bearerPattern, rawPattern string) map[string]string {
	header, token, ok := headerAndToken(s)
	if !ok {
		return nil
	}
	envName := mcpSecretEnvName(s.ID, header)
	launchEnv[envName] = token
	if strings.EqualFold(header, "Authorization") && !strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return map[string]string{header: fmt.Sprintf(bearerPattern, envName)}
	}
	return map[string]string{header: fmt.Sprintf(rawPattern, envName)}
}

func codexHeaderEnvRefs(s MCPServer, launchEnv map[string]string) map[string]string {
	header, token, ok := headerAndToken(s)
	if !ok {
		return nil
	}
	envName := mcpSecretEnvName(s.ID, header)
	if strings.EqualFold(header, "Authorization") && !strings.HasPrefix(strings.ToLower(token), "bearer ") {
		launchEnv[envName] = "Bearer " + token
	} else {
		launchEnv[envName] = token
	}
	return map[string]string{header: envName}
}

func headerAndToken(s MCPServer) (string, string, bool) {
	header := s.Auth["header"]
	token := s.Auth["token"]
	if header == "" && token != "" {
		header = "Authorization"
	}
	if token == "" {
		return "", "", false
	}
	return header, token, true
}

func oauthConfig(s MCPServer, launchEnv map[string]string, secretPattern string) map[string]any {
	out := map[string]any{}
	if s.Auth["issuer"] != "" {
		out["issuer"] = s.Auth["issuer"]
	}
	if s.Auth["scopes"] != "" {
		out["scope"] = s.Auth["scopes"]
		out["scopes"] = strings.Fields(s.Auth["scopes"])
	}
	if s.Auth["client_id"] != "" {
		out["clientId"] = s.Auth["client_id"]
	}
	if s.Auth["client_secret"] != "" {
		envName := mcpSecretEnvName(s.ID, "oauth_client_secret")
		launchEnv[envName] = s.Auth["client_secret"]
		out["clientSecret"] = fmt.Sprintf(secretPattern, envName)
	}
	return out
}

var envSafe = regexp.MustCompile(`[^A-Z0-9_]`)

func mcpSecretEnvName(serverID, key string) string {
	raw := strings.ToUpper(serverID + "_" + key)
	raw = strings.ReplaceAll(raw, "-", "_")
	raw = envSafe.ReplaceAllString(raw, "_")
	return "PRAIMATE_MCP_" + strings.Trim(raw, "_")
}

func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func tomlArray(xs []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range xs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(tomlString(x))
	}
	b.WriteByte(']')
	return b.String()
}

func tomlKey(s string) string {
	if isBareTomlKey(s) {
		return s
	}
	return tomlString(s)
}

func tomlBareOrQuotedKey(s string) string {
	if isBareTomlKey(s) {
		return s
	}
	return tomlString(s)
}

func isBareTomlKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
