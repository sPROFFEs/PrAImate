package core

import (
	"context"
	"os"
	"time"
)

// InspectApplicationMCP performs the real MCP initialize/tools-list handshake.
// It reuses managed transports and returns no stored authentication values.
func (c *Core) InspectApplicationMCP(ctx context.Context, id string) (MCPProbeResult, error) {
	server, err := c.GetMCPServer(ctx, id)
	if err != nil {
		return MCPProbeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	started := time.Now()
	client, err := newManagedMCPClient(ctx, os.TempDir(), *server)
	if err != nil {
		return MCPProbeResult{}, err
	}
	defer client.Close()
	tools, err := client.ListTools(ctx)
	if err != nil {
		return MCPProbeResult{}, err
	}
	out := MCPProbeResult{OK: true, Tools: []MCPToolInfo{}, Latency: time.Since(started).Round(time.Millisecond).String()}
	for _, tool := range tools {
		out.Tools = append(out.Tools, MCPToolInfo{Name: tool.Name, Description: tool.Description})
	}
	return out, nil
}
