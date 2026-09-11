package main

// MCP Server live probing and JSON configuration import.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

type MCPToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type MCPProbeResult struct {
	OK      bool          `json:"ok"`
	Tools   []MCPToolInfo `json:"tools"`
	Error   string        `json:"error,omitempty"`
	Latency string        `json:"latency"`
}

// TestMCPServer probes a configured MCP server (stdio or HTTP/SSE) and returns its tool list.
func (a *App) TestMCPServer(id string) (MCPProbeResult, error) {
	c, err := a.requireCore()
	if err != nil {
		return MCPProbeResult{Error: err.Error()}, err
	}
	s, err := c.GetMCPServer(a.ctx, id)
	if err != nil {
		return MCPProbeResult{Error: err.Error()}, err
	}
	return probeMCPServer(*s)
}

func probeMCPServer(s core.MCPServer) (MCPProbeResult, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()

	if s.Transport == core.MCPTransportStdio {
		command, args := core.ResolveStdioMCPCommand(s)
		cmd := exec.CommandContext(ctx, command, args...)
		for k, v := range s.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return MCPProbeResult{Error: "stdin pipe: " + err.Error()}, nil
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return MCPProbeResult{Error: "stdout pipe: " + err.Error()}, nil
		}
		if err := cmd.Start(); err != nil {
			return MCPProbeResult{Error: "start process: " + err.Error()}, nil
		}
		defer func() {
			_ = stdin.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}()

		// Send initialize
		initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"praimate-tester","version":"1.0"}}}` + "\n"
		if _, err := io.WriteString(stdin, initReq); err != nil {
			return MCPProbeResult{Error: "write init: " + err.Error()}, nil
		}
		reader := bufio.NewReader(stdout)
		_, err = readJSONRPCResponse(reader)
		if err != nil {
			return MCPProbeResult{Error: "read init: " + err.Error()}, nil
		}

		// Send tools/list
		toolsReq := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"
		if _, err := io.WriteString(stdin, toolsReq); err != nil {
			return MCPProbeResult{Error: "write tools/list: " + err.Error()}, nil
		}
		toolsResp, err := readJSONRPCResponse(reader)
		if err != nil {
			return MCPProbeResult{Error: "read tools/list: " + err.Error()}, nil
		}

		tools := parseMCPTools(toolsResp)
		return MCPProbeResult{
			OK:      true,
			Tools:   tools,
			Latency: time.Since(start).Round(time.Millisecond).String(),
		}, nil
	}

	// HTTP / SSE probe
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return MCPProbeResult{Error: err.Error()}, nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return MCPProbeResult{Error: "connect: " + err.Error()}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return MCPProbeResult{Error: fmt.Sprintf("endpoint returned status %d", resp.StatusCode)}, nil
	}
	return MCPProbeResult{
		OK:      true,
		Latency: time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

func readJSONRPCResponse(r *bufio.Reader) ([]byte, error) {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) > 0 && line[0] == '{' {
			return line, nil
		}
	}
}

func parseMCPTools(raw []byte) []MCPToolInfo {
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &resp)
	var out []MCPToolInfo
	for _, t := range resp.Result.Tools {
		out = append(out, MCPToolInfo{Name: t.Name, Description: t.Description})
	}
	return out
}

// ImportMCPServersJSON imports a JSON configuration blob containing mcpServers.
func (a *App) ImportMCPServersJSON(jsonText string) (int, error) {
	c, err := a.requireCore()
	if err != nil {
		return 0, err
	}
	var data struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Type    string            `json:"type"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
		var rootMap map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Type    string            `json:"type"`
		}
		if err2 := json.Unmarshal([]byte(jsonText), &rootMap); err2 != nil {
			return 0, fmt.Errorf("invalid JSON: %w", err)
		}
		data.MCPServers = rootMap
	}
	if len(data.MCPServers) == 0 {
		return 0, fmt.Errorf("no mcpServers found in JSON")
	}

	imported := 0
	for name, item := range data.MCPServers {
		transport := "stdio"
		if item.URL != "" || item.Type == "sse" || item.Type == "http" {
			transport = "http"
			if item.Type == "sse" {
				transport = "sse"
			}
		}
		cmdParts := []string{item.Command}
		cmdParts = append(cmdParts, item.Args...)
		fullCommand := strings.Join(cmdParts, " ")
		if transport != "stdio" {
			fullCommand = ""
		}
		req := core.AddCustomMCPRequest{
			Name:      name,
			Transport: transport,
			Command:   strings.TrimSpace(fullCommand),
			URL:       item.URL,
			Env:       item.Env,
		}
		if req.Env == nil {
			req.Env = map[string]string{}
		}
		if _, err := c.AddCustomMCP(a.ctx, req); err == nil {
			imported++
		}
	}
	return imported, nil
}
