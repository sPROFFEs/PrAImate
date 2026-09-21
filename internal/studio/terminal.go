package studio

import (
	"encoding/json"
	"fmt"
	"os"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/launcher"
)

type terminalCLI struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type terminalPlan struct {
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

// prepareTerminal only resolves a launch; it never spawns a process or writes
// project configuration. The extension host launches it in Codium's real PTY.
// Native permissions/authentication remain under that CLI's own control.
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
	// Export only PATH, not Desktop's credential-bearing environment. This lets
	// installed CLI wrappers find node/bun and other managed runtime tools.
	return &terminalPlan{CLI: cli, Command: command, Args: args, Cwd: config.Workspace, Env: map[string]string{"PATH": os.Getenv("PATH")}}, nil
}
