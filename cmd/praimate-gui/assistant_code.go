package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/sPROFFEs/PrAImate/internal/assistant"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

func (a *App) registerAssistantCode(r *assistant.Registry) {
	cliIDs := []string{}
	for _, cli := range launcher.KnownAgents() {
		cliIDs = append(cliIDs, string(cli.ID))
	}
	r.Register(assistant.Action{Name: "code.options", Description: "List installed CLIs for opening a new Code terminal session. Ask the user for their project folder if it is unknown.", Capability: "read", Execute: func(ctx context.Context, _ map[string]any) (any, error) {
		out := []map[string]any{}
		for _, cli := range launcher.KnownAgents() {
			_, err := core.ResolveInteractiveCLIBinary(string(cli.ID))
			out = append(out, map[string]any{"cli": string(cli.ID), "installed": err == nil})
		}
		return out, nil
	}})
	r.Register(assistant.Action{Name: "code.start", Description: "Open a new terminal in PrAImate Desktop's Code tab with an installed CLI and the user's project folder. ui.navigate only changes pages; it does not create terminals.", Capability: "chats", Fields: map[string]assistant.Field{
		"cli":       {Type: "string", Description: "Installed CLI identifier chosen by the user; inspect code.options if unknown", Required: true, Enum: cliIDs, InputSource: "cli"},
		"workspace": {Type: "string", Description: "Complete absolute project folder from the user or UI context; ask if missing", Required: true, InputSource: "project"},
		"model":     {Type: "string", Description: "Optional CLI model identifier"},
	}, Execute: func(ctx context.Context, args map[string]any) (any, error) {
		workspace := textArg(args, "workspace")
		if !filepath.IsAbs(workspace) {
			return nil, errors.New("an absolute project folder is required")
		}
		info, err := os.Stat(workspace)
		if err != nil || !info.IsDir() {
			return nil, errors.New("project folder does not exist or is not a directory")
		}
		if a.ctx == nil || a.terms == nil {
			return nil, errors.New("desktop terminal service is unavailable")
		}
		cli, model := textArg(args, "cli"), textArg(args, "model")
		if _, _, err := core.InteractiveCLICommand(cli, model); err != nil {
			return nil, err
		}
		started, err := a.StartCodeSessionWithSkills("", cli, model, workspace, "", "", "")
		if err != nil {
			return nil, err
		}
		// Attach to this exact PTY. Opening the Code page without the pending
		// session only shows its list and leaves the terminal invisible.
		a.emitUIEvent("assistant:navigate", map[string]any{"page": "code", "chat_id": started.ChatID, "term_id": started.TermID, "cli": cli, "workspace": workspace, "model": model})
		return map[string]any{"started": true, "surface": "desktop", "chat_id": started.ChatID, "terminal_id": started.TermID, "cli": cli, "workspace": workspace}, nil
	}})
}
