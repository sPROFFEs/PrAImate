package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

type MCPToolInfo = core.MCPToolInfo
type MCPProbeResult = core.MCPProbeResult

// Connection tests use the same authenticated initialize/tools-list handshake
// as the runtime, including HTTP and legacy SSE negotiation.
func (a *App) TestMCPServer(id string) (MCPProbeResult, error) {
	c, err := a.requireCore()
	if err != nil {
		return MCPProbeResult{}, err
	}
	return c.ProbeMCPServer(a.ctx, id)
}

type importedMCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Headers map[string]string `json:"headers"`
	URL     string            `json:"url"`
	Type    string            `json:"type"`
}

func (a *App) ImportMCPServersJSON(text string) (int, error) {
	c, err := a.requireCore()
	if err != nil {
		return 0, err
	}
	var envelope struct {
		MCPServers map[string]importedMCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return 0, fmt.Errorf("invalid MCP configuration JSON")
	}
	definitions := envelope.MCPServers
	if definitions == nil {
		if err := json.Unmarshal([]byte(text), &definitions); err != nil {
			return 0, fmt.Errorf("invalid MCP configuration JSON")
		}
	}
	if len(definitions) == 0 {
		return 0, fmt.Errorf("no MCP servers found in JSON")
	}
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	imported := 0
	var failed []string
	for _, name := range names {
		item := definitions[name]
		transport := item.Type
		if transport == "" {
			transport = "stdio"
			if item.URL != "" {
				transport = "http"
			}
		}
		req := core.AddCustomMCPRequest{Name: name, Transport: transport, Command: item.Command, Args: item.Args, URL: item.URL, Env: item.Env, Headers: item.Headers}
		if transport != "stdio" {
			req.Command = ""
			req.Args = nil
		}
		if _, err := c.AddCustomMCP(a.ctx, req); err != nil {
			failed = append(failed, name)
		} else {
			imported++
		}
	}
	if len(failed) > 0 {
		return imported, fmt.Errorf("imported %d server(s); check the transport, URL and headers for: %s", imported, strings.Join(failed, ", "))
	}
	return imported, nil
}

func mcpFormWithHeaders(name, transport, command, url, envText, headersText string) (core.AddCustomMCPRequest, error) {
	headers, err := core.ParseMCPHeaderLines(headersText)
	if err != nil {
		return core.AddCustomMCPRequest{}, err
	}
	return core.AddCustomMCPRequest{Name: name, Transport: transport, Command: command, URL: url, Env: core.ParseEnvLines(envText), Headers: headers}, nil
}

func (a *App) AddCustomMCPWithHeaders(name, transport, command, url, envText, headersText string) (*core.MCPServer, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	request, err := mcpFormWithHeaders(name, transport, command, url, envText, headersText)
	if err != nil {
		return nil, err
	}
	return c.AddCustomMCP(a.ctx, request)
}

func (a *App) UpdateMCPServerWithHeaders(id, name, transport, command, url, envText, headersText string) (*core.MCPServer, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	request, err := mcpFormWithHeaders(name, transport, command, url, envText, headersText)
	if err != nil {
		return nil, err
	}
	return c.UpdateMCPServer(a.ctx, id, request)
}
