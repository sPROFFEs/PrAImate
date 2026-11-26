package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/sPROFFEs/PrAImate/internal/installer"
)

const managedREAMCPID = "praimate-rea"

// RegisterManagedREA exposes the installed artifact-analysis profile through the
// same connection catalogue used by chats, agents and Studio.
func (c *Core) RegisterManagedREA(ctx context.Context) error {
	node, launcher, err := installer.REAMCPCommand()
	if err != nil {
		return err
	}
	req := ConnectMCPRequest{
		ID: managedREAMCPID, Name: "REA (local artifact analysis)",
		Transport: MCPTransportStdio, Command: node, Args: []string{launcher},
	}
	current, err := c.GetMCPServer(ctx, req.ID)
	switch {
	case err == nil:
		// An install or repair must never replace a user-defined server.
		if current.Transport != req.Transport || len(current.Args) != 1 || current.Args[0] != launcher {
			return fmt.Errorf("MCP ID %q is already used by another server", req.ID)
		}
		req.Enabled = &current.Enabled
		req.Name, req.Env = current.Name, current.Env
	case !errors.Is(err, ErrMCPServerNotFound):
		return err
	}
	// Verify the process before publishing a connection to every CLI.
	probe := probeMCPDefinition(ctx, MCPServer{Transport: req.Transport, Command: req.Command, Args: req.Args, Env: req.Env})
	if !probe.OK || len(probe.Tools) == 0 {
		return fmt.Errorf("REA MCP did not advertise tools: %s", probe.Error)
	}

	_, err = c.ConnectMCP(ctx, req)
	return err
}
