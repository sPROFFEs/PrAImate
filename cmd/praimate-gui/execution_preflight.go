package main

import (
	"context"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// ExecutionCapabilities exposes the conservative backend capability matrix to
// launch dialogs. It contains no credentials and performs no I/O.
func (a *App) ExecutionCapabilities(cli string) core.CLICapabilities {
	return core.CapabilitiesForCLI(strings.TrimSpace(cli))
}

// PreflightExecution validates a proposed GUI launch without writing project
// configuration. The actual launch resolves the same request again and then
// calls PrepareExecution immediately before spawning the child.
func (a *App) PreflightExecution(agentID, surface, cli, model, tools, cwd, localEndpoint, localModel string) core.ExecutionPreflight {
	c, err := a.requireCore()
	if err != nil {
		return core.ExecutionPreflight{Issues: []core.PreflightIssue{{Severity: "error", Code: "core_unavailable", Message: err.Error()}}}
	}
	var agent *core.Agent
	if strings.TrimSpace(agentID) != "" {
		agent, err = c.GetAgent(a.ctx, agentID)
		if err != nil {
			return core.ExecutionPreflight{CLI: cli, Surface: core.ExecutionSurface(surface), Issues: []core.PreflightIssue{{Severity: "error", Code: "agent_unavailable", Message: err.Error()}}}
		}
	}
	var local *core.ChatLocalEndpoint
	if strings.TrimSpace(localEndpoint) != "" {
		local = &core.ChatLocalEndpoint{Endpoint: localEndpoint, Model: localModel}
	}
	ctx, cancel := context.WithTimeout(a.ctx, 12*time.Second)
	defer cancel()
	return c.PreflightExecution(ctx, core.ExecutionRequest{
		Surface: core.ExecutionSurface(surface), Agent: agent, CLI: cli, Cwd: cwd,
		Model: model, Tools: tools, Local: local, AllEnabledMCP: agent == nil && surface == string(core.SurfaceTerminal),
	}, true)
}
