package studio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

type terminalCLI struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type terminalPlan struct {
	UsageID string            `json:"usageId,omitempty"`
	CLI     string            `json:"cli"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Cwd     string            `json:"cwd"`
	Env     map[string]string `json:"env"`
}

func terminalCLIs() []terminalCLI {
	result := []terminalCLI{}
	for _, cli := range launcher.KnownAgents() {
		entry := terminalCLI{ID: string(cli.ID), Label: cli.Label}
		_, err := core.ResolveInteractiveCLIBinary(entry.ID)
		entry.Available = err == nil
		if err != nil {
			entry.Reason = "Executable not found; install it from Desktop → CLIs, then reopen Studio."
		}
		result = append(result, entry)
	}
	return result
}

// prepareTerminal never spawns a process or writes project configuration.
// PrAImate CLI gets a core chat snapshot; other CLIs retain their own sessions.
func (s *Server) prepareTerminal(body []byte) (*terminalPlan, error) {
	var request struct {
		CLI string `json:"cli"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	config := s.sessionSnapshot()
	if config.Workspace == "" {
		return nil, fmt.Errorf("open a workspace folder before launching a terminal")
	}
	info, err := os.Stat(config.Workspace)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("terminal workspace is not an accessible directory")
	}
	cli, model := request.CLI, ""
	if cli == "" {
		cli = config.CLI
	}
	if cli == config.CLI {
		model = config.Model
	}
	_, args, err := core.InteractiveCLICommand(cli, model)
	if err != nil {
		return nil, err
	}
	command, err := core.ResolveInteractiveCLIBinary(cli)
	if err != nil {
		return nil, fmt.Errorf("%s executable not found; install it from Desktop → CLIs, then reopen Studio", cli)
	}
	if cli == "praimate-cli" {
		ctx := context.Background()
		if cli != config.CLI {
			config = sessionConfig{CLI: cli, Workspace: config.Workspace}
		}
		settings, err := s.settings(ctx, config)
		if err != nil {
			return nil, err
		}
		chat, err := s.core.CreateChat(ctx, core.CreateChatRequest{Title: "Studio native terminal", CLIAgent: cli, AgentID: config.AgentID, WorkspacePath: config.Workspace, Settings: settings})
		if err != nil {
			return nil, err
		}
		args = []string{"--chat", chat.ID}
	}
	// Export only PATH, not Desktop's credential-bearing environment. This lets
	// installed CLI wrappers find node/bun and other managed runtime tools.
	plan := &terminalPlan{CLI: cli, Command: command, Args: args, Cwd: config.Workspace, Env: map[string]string{"PATH": os.Getenv("PATH")}}
	usage, err := s.core.BeginTerminalUsage(s.ctx, cli, model, plan.Env)
	if err != nil {
		return nil, fmt.Errorf("prepare terminal usage: %w", err)
	}
	if usage != nil {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			usage.Close()
			return nil, err
		}
		plan.UsageID = hex.EncodeToString(id[:])
		plan.Args = append(plan.Args, usage.Args...)
		for key, value := range usage.Env {
			plan.Env[key] = value
		}
		s.mu.Lock()
		if s.terminalUsage == nil {
			s.terminalUsage = map[string]*core.TerminalUsage{}
		}
		s.terminalUsage[plan.UsageID] = usage
		s.mu.Unlock()
	}
	return plan, nil
}
