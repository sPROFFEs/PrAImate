package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
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

// ProbeMCPServer checks connectivity without returning stored credentials.
func (c *Core) ProbeMCPServer(ctx context.Context, id string) (MCPProbeResult, error) {
	server, err := c.GetMCPServer(ctx, id)
	if err != nil {
		return MCPProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	started := time.Now()
	fail := func(err error) (MCPProbeResult, error) {
		return MCPProbeResult{Error: err.Error(), Latency: time.Since(started).Round(time.Millisecond).String()}, nil
	}
	if server.Transport == MCPTransportStdio {
		command, args := ResolveStdioMCPCommand(*server)
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Env = os.Environ()
		for key, value := range server.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fail(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fail(err)
		}
		if err := cmd.Start(); err != nil {
			return fail(err)
		}
		defer func() {
			_ = stdin.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}()
		if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"praimate-studio","version":"1"}}}`+"\n"); err != nil {
			return fail(err)
		}
		reader := bufio.NewReader(stdout)
		if _, err := readMCPResponse(reader); err != nil {
			return fail(err)
		}
		if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`+"\n"); err != nil {
			return fail(err)
		}
		raw, err := readMCPResponse(reader)
		if err != nil {
			return fail(err)
		}
		var response struct {
			Result struct {
				Tools []MCPToolInfo `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return fail(err)
		}
		return MCPProbeResult{OK: true, Tools: response.Result.Tools, Latency: time.Since(started).Round(time.Millisecond).String()}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		return fail(err)
	}
	if header, token := server.Auth["header"], server.Auth["token"]; header != "" && token != "" {
		req.Header.Set(header, token)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return fail(err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return fail(fmt.Errorf("endpoint returned status %d", response.StatusCode))
	}
	return MCPProbeResult{OK: true, Latency: time.Since(started).Round(time.Millisecond).String()}, nil
}

func readMCPResponse(reader *bufio.Reader) ([]byte, error) {
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		line = []byte(strings.TrimSpace(string(line)))
		if len(line) > 0 && line[0] == '{' {
			return line, nil
		}
	}
}
