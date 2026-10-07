package core

import (
	"context"
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

// ProbeMCPServer performs the same initialize/tools-list handshake as the native
// runtime, including legacy SSE negotiation, without returning credentials.
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
	client, err := newManagedMCPClient(ctx, "", *server)
	if err != nil {
		return fail(err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx)
	if err != nil {
		return fail(err)
	}
	info := make([]MCPToolInfo, 0, len(tools))
	for _, tool := range tools {
		info = append(info, MCPToolInfo{Name: tool.Name, Description: tool.Description})
	}
	return MCPProbeResult{OK: true, Tools: info, Latency: time.Since(started).Round(time.Millisecond).String()}, nil
}
