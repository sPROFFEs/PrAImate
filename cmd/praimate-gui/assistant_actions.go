package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/assistant"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/orchestrator"
	"github.com/sPROFFEs/PrAImate/internal/studio"
)

func textArg(args map[string]any, key string) string { s, _ := args[key].(string); return s }
func boolArg(args map[string]any, key string) bool   { b, _ := args[key].(bool); return b }
func chatSummary(ch core.Chat) map[string]any {
	return map[string]any{"id": ch.ID, "title": ch.Title, "cli": ch.CLIAgent, "agent_id": ch.AgentID, "workspace": ch.WorkspacePath, "archived": ch.EndedAt != nil}
}

// Actions call the same application services as the UI. Credentials, endpoint
// authentication and internal configuration documents are never tool results.
func (a *App) assistantActions() *assistant.Registry {
	r := assistant.NewRegistry()
	field := func(description string, required bool) assistant.Field {
		return assistant.Field{Type: "string", Description: description, Required: required}
	}
	register := func(name, description, cap string, fields map[string]assistant.Field, fn func(context.Context, map[string]any) (any, error)) {
		r.Register(assistant.Action{Name: name, Description: description, Capability: cap, Fields: fields, Execute: fn})
	}
	ok := func(err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return map[string]any{"updated": true}, nil
	}
	register("actions.search", "Find other application actions using Spanish or English keywords", "read", map[string]assistant.Field{"query": field("Short task keywords", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		return r.Search(textArg(args, "query"), 6), nil
	})
	register("app.search", "Search chats/messages, agents, workers, skills, MCP, projects and terminal sessions on demand", "read", map[string]assistant.Field{"query": field("Search text", true), "types": {Type: "array", Description: "Optional chat, agent, worker, skill, mcp, project, session", Required: false}}, a.assistantSearch)
	register("ui.navigate", "Open an application page. Optional chat ID opens a conversation.", "navigate", map[string]assistant.Field{"page": {Type: "string", Description: "Application page", Required: true, Enum: []string{"dashboard", "chats", "workers", "agents", "skills", "mcp", "settings", "code", "studio", "documents"}}, "chat_id": field("Chat ID", false), "agent_id": field("Agent ID to open in Studio", false), "worker_id": field("Worker execution ID", false)}, func(ctx context.Context, args map[string]any) (any, error) {
		page := textArg(args, "page")
		valid := false
		for _, p := range []string{"dashboard", "chats", "workers", "agents", "skills", "mcp", "settings", "code", "studio", "documents"} {
			if p == page {
				valid = true
			}
		}
		if !valid {
			return nil, errors.New("unknown application page")
		}
		if id := textArg(args, "chat_id"); id != "" {
			if _, err := a.core.GetChat(ctx, id); err != nil {
				return nil, err
			}
		}
		if id := textArg(args, "agent_id"); id != "" {
			if _, err := a.core.GetAgent(ctx, id); err != nil {
				return nil, err
			}
		}
		if id := textArg(args, "worker_id"); id != "" {
			if _, err := a.WorkerRunSnapshot(id); err != nil {
				return nil, err
			}
		}
		if a.ctx == nil {
			return nil, errors.New("application UI is unavailable")
		}
		a.emitUIEvent("assistant:navigate", args)
		studio.PublishDesktopAssistant(a.core, "assistant.navigate", args)
		return map[string]any{"opened": page}, nil
	})
	register("chats.list", "List recent conversations and IDs", "read", nil, func(ctx context.Context, args map[string]any) (any, error) {
		chats, err := a.ListChats()
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, ch := range chats {
			if len(out) >= 20 {
				break
			}
			out = append(out, chatSummary(ch))
		}
		return out, nil
	})
	register("chats.read", "Read a conversation's latest messages", "read", map[string]assistant.Field{"id": field("Chat ID", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		ch, err := a.core.GetChat(ctx, textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		messages, err := a.core.RecentChatMessages(ctx, ch.ID, 8)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, message := range messages {
			out = append(out, map[string]any{"role": message.Role, "text": message.Content, "at": message.TS})
		}
		return map[string]any{"chat": chatSummary(*ch), "messages": out}, nil
	})
	register("chats.create", "Create a clean conversation with selected CLI, model and workspace", "chats", map[string]assistant.Field{"cli": field("Installed CLI identifier", true), "model": field("Model identifier", false), "workspace": field("Absolute workspace path", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		ch, err := a.StartCleanChat(textArg(args, "cli"), textArg(args, "model"), textArg(args, "workspace"))
		if err != nil {
			return nil, err
		}
		return chatSummary(*ch), nil
	})
	register("chats.rename", "Rename an existing conversation", "chats", map[string]assistant.Field{"id": field("Chat ID", true), "title": field("New title", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		return ok(a.RenameChat(textArg(args, "id"), textArg(args, "title")))
	})
	register("chats.delete", "Permanently delete a conversation and its messages", "chats", map[string]assistant.Field{"id": field("Chat ID", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		return ok(a.DeleteChat(textArg(args, "id")))
	})
	register("agents.list", "List agents by ID, name and supported CLIs", "read", nil, func(ctx context.Context, args map[string]any) (any, error) {
		agents, err := a.ListAgents()
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, agent := range agents {
			out = append(out, map[string]any{"id": agent.ID, "name": agent.Name, "description": agent.Description, "supports": agent.Supports})
		}
		return out, nil
	})

	register("agents.read", "Inspect an agent persona, supported CLIs, tools and MCP selections", "read", map[string]assistant.Field{"id": field("Agent ID", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		agent, err := a.core.GetAgent(ctx, textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": agent.ID, "name": agent.Name, "description": agent.Description, "instructions": agent.Instructions, "supports": agent.Supports, "tools": agent.Tools, "mcp_servers": agent.MCPServers}, nil
	})
	register("agents.create", "Create a saved agent persona by name. Optional fields have defaults; no tools, MCP, scripts or autonomous runtime are enabled.", "agents", map[string]assistant.Field{"id": field("New identifier; defaults to a slug of name", false), "name": field("New agent display name", true), "description": field("Optional short description", false), "instructions": field("Persona instructions; defaults to a helpful assistant", false), "cli": field("Supported CLI; defaults to praimate-cli", false)}, func(ctx context.Context, args map[string]any) (any, error) {
		id := textArg(args, "id")
		if id == "" {
			id = core.MCPSlug(textArg(args, "name"))
		}
		if _, err := a.core.GetAgent(ctx, id); err == nil {
			return nil, errors.New("agent already exists")
		} else if !errors.Is(err, core.ErrAgentNotFound) {
			return nil, err
		}
		instructions, cli := textArg(args, "instructions"), textArg(args, "cli")
		if instructions == "" {
			instructions = "You are a helpful assistant. Follow the user's instructions and ask for clarification when needed."
		}
		if cli == "" {
			cli = "praimate-cli"
		}
		agent := &core.Agent{Schema: "praimate.agent/v1", ID: id, Name: textArg(args, "name"), Description: textArg(args, "description"), Instructions: instructions, Supports: []string{cli}, Tools: []string{}, MCPServers: []string{}}
		raw, err := core.MarshalAgentYAML(agent)
		if err != nil {
			return nil, err
		}
		created, err := a.core.ImportAgentYAML(ctx, raw, "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": created.ID, "name": created.Name}, nil
	})
	register("agents.set_instructions", "Update an existing agent's persona instructions, preserving its other configuration", "agents", map[string]assistant.Field{"id": field("Existing agent ID", true), "instructions": field("Complete replacement instructions", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		agent, err := a.core.GetAgent(ctx, textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		agent.Instructions = textArg(args, "instructions")
		raw, err := core.MarshalAgentYAML(agent)
		if err != nil {
			return nil, err
		}
		_, err = a.core.ImportAgentYAML(ctx, raw, agent.SourcePath)
		return ok(err)
	})
	register("skills.set_chat", "Select only already installed and approved immutable skills for a chat. Existing session resync rules still apply.", "skills", map[string]assistant.Field{"chat_id": field("Chat ID", true), "choices": {Type: "array", Description: "Objects containing ref, digest and activation (auto, pinned, manual or off)", Required: true}}, func(ctx context.Context, args map[string]any) (any, error) {
		raw, err := json.Marshal(args["choices"])
		if err != nil {
			return nil, err
		}
		var choices []core.InstalledSkillChoice
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&choices); err != nil {
			return nil, err
		}
		selection, err := a.core.BuildInstalledSkillSelection(ctx, choices)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(selection)
		if err != nil {
			return nil, err
		}
		return ok(a.SetChatSkillsV2(textArg(args, "chat_id"), string(encoded)))
	})
	register("agents.delete", "Delete an existing agent", "agents", map[string]assistant.Field{"id": field("Agent ID", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		return ok(a.DeleteAgent(textArg(args, "id")))
	})
	register("delegate.agent", "Create an agent conversation and send a concrete task. Return the chat ID to inspect its output.", "delegate", map[string]assistant.Field{"agent_id": field("Existing agent ID", true), "cli": field("Installed CLI identifier", true), "workspace": field("Absolute workspace", true), "task": field("Concrete goal and expected output", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		ch, err := a.StartChat(textArg(args, "agent_id"), textArg(args, "cli"), textArg(args, "workspace"))
		if err != nil {
			return nil, err
		}
		agent, err := a.core.GetAgent(ctx, ch.AgentID)
		if err != nil {
			return nil, err
		}
		_, err = a.core.ContinueChat(ctx, ch.ID, textArg(args, "task"), ch.WorkspacePath, core.AgentSystemPrompt(agent))
		if err != nil {
			return map[string]any{"chat_id": ch.ID}, err
		}
		return map[string]any{"chat_id": ch.ID, "run_id": ch.ID, "status": "completed", "hint": "Use chats.read to inspect the actual answer."}, nil
	})
	register("workers.list", "List saved worker executions and task statuses", "read", nil, func(ctx context.Context, args map[string]any) (any, error) {
		runs, err := a.WorkerRuns()
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, run := range runs {
			if len(out) >= 20 {
				break
			}
			out = append(out, map[string]any{"id": run.ID, "title": run.Title, "status": run.Status, "workspace": run.Workspace})
		}
		return out, nil
	})
	register("workers.read", "Inspect progress and output of a worker task", "read", map[string]assistant.Field{"id": field("Worker execution ID", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		run, err := a.WorkerRunSnapshot(textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": run.ID, "status": run.Status, "task": run.CurrentTask, "result": run.Result, "error": run.Error}, nil
	})
	register("workers.config", "Inspect configured reasoner, middle and fast worker models", "read", nil, func(ctx context.Context, args map[string]any) (any, error) {
		config, err := a.WorkerConfig()
		if err != nil {
			return nil, err
		}
		profiles := []map[string]any{}
		for _, p := range config.Profiles {
			profiles = append(profiles, map[string]any{"tier": p.Tier, "runtime": p.Runtime, "cli": p.CLI, "model": p.Model, "allow_commands": p.AllowCommands, "allow_edits": p.AllowEdits})
		}
		return map[string]any{"workspace": config.Workspace, "profiles": profiles}, nil
	})
	register("workers.set_model", "Change the model for an existing worker tier, preserving its CLI and permissions", "workers", map[string]assistant.Field{"tier": {Type: "string", Description: "primary=reasoner; middle=intermediate; fast=small worker", Required: true, Enum: []string{"primary", "middle", "fast"}}, "model": field("Exact model identifier from the user or configured models", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		config, err := a.WorkerConfig()
		if err != nil {
			return nil, err
		}
		found := false
		for i := range config.Profiles {
			if string(config.Profiles[i].Tier) == textArg(args, "tier") {
				config.Profiles[i].Model = textArg(args, "model")
				found = true
			}
		}
		if !found {
			return nil, errors.New("unknown worker tier")
		}
		raw, err := json.Marshal(config)
		if err != nil {
			return nil, err
		}
		if err := a.SaveWorkerConfig(string(raw)); err != nil {
			return nil, err
		}
		return map[string]any{"updated": true, "tier": textArg(args, "tier"), "model": textArg(args, "model")}, nil
	})
	register("delegate.workers", "Start a background task through the configured reasoner, middle and fast workers; task must specify goal, constraints and expected output. Inspect workers.read, never assume completion.", "delegate", map[string]assistant.Field{"task": field("Concrete delegated goal and expected output", true), "tier": field("primary (deep reasoning), middle (implementation) or fast (simple scoped work)", false)}, func(ctx context.Context, args map[string]any) (any, error) {
		tier := orchestrator.Tier(textArg(args, "tier"))
		if tier == "" {
			tier = orchestrator.Primary
		}
		if a.workers == nil {
			return nil, errors.New("worker runtime unavailable")
		}
		id, err := a.workers.StartAtTier(textArg(args, "task"), tier, a.approvalProvider)
		if err != nil {
			return nil, err
		}
		return map[string]any{"run_id": id, "status": "started"}, nil
	})
	register("workers.continue", "Resume a saved worker execution with a follow-up task", "delegate", map[string]assistant.Field{"id": field("Worker ID", true), "task": field("Follow-up goal", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		return ok(a.ContinueWorkerRun(textArg(args, "id"), textArg(args, "task")))
	})
	register("workers.cancel", "Cancel a running worker task", "tasks", map[string]assistant.Field{"id": field("Worker ID", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		return ok(a.CancelWorkerRun(textArg(args, "id")))
	})
	register("skills.list", "Inspect installed skills and their approval status; use the Skills UI to review or import new packages", "read", nil, func(ctx context.Context, args map[string]any) (any, error) { return a.InstalledSkillVersionsV2() })
	register("mcp.list", "List MCP servers without exposing credentials or authentication settings", "read", nil, func(ctx context.Context, args map[string]any) (any, error) {
		servers, err := a.MCPServers()
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, server := range servers {
			out = append(out, map[string]any{"id": server.ID, "name": server.Name, "transport": server.Transport, "enabled": server.Enabled})
		}
		return out, nil
	})
	register("mcp.set_enabled", "Enable or disable an existing MCP server", "mcp", map[string]assistant.Field{"id": field("MCP server ID", true), "enabled": {Type: "boolean", Description: "Enable server", Required: true}}, func(ctx context.Context, args map[string]any) (any, error) {
		return ok(a.SetMCPEnabled(textArg(args, "id"), boolArg(args, "enabled")))
	})
	register("settings.appearance", "Change the appearance theme", "settings", map[string]assistant.Field{"theme": {Type: "string", Description: "light=claro; dark=oscuro; system=follow OS", Required: true, Enum: []string{"light", "dark", "system"}}}, func(ctx context.Context, args map[string]any) (any, error) {
		theme := textArg(args, "theme")
		if theme != "light" && theme != "dark" && theme != "system" {
			return nil, errors.New("invalid theme")
		}
		if a.ctx == nil {
			return nil, errors.New("UI is unavailable")
		}
		a.emitUIEvent("assistant:appearance", theme)
		return map[string]any{"theme": theme}, nil
	})
	register("system.command", "Execute one command in a project. Requires separate system permission. No shell expansion; provide an executable and argument array.", "system", map[string]assistant.Field{"workspace": field("Absolute project directory", true), "command": field("Executable", true), "args": {Type: "array", Description: "String arguments", Required: true}}, func(ctx context.Context, args map[string]any) (any, error) {
		config, err := a.AssistantConfig()
		if err != nil {
			return nil, err
		}
		return a.core.ExecuteApplicationTool(ctx, textArg(args, "workspace"), "command.run", map[string]any{"command": args["command"], "args": args["args"], "timeout_seconds": config.CommandSeconds})
	})
	register("project.read", "Read a bounded file within a project; never application storage", "filesystem", map[string]assistant.Field{"workspace": field("Absolute project directory", true), "path": field("Relative file path", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		return a.core.ExecuteApplicationTool(ctx, textArg(args, "workspace"), "project.read", map[string]any{"path": args["path"], "limit": 4096})
	})
	register("project.write", "Write a project file; requires separate filesystem permission", "filesystem", map[string]assistant.Field{"workspace": field("Absolute project directory", true), "path": field("Relative file path", true), "content": field("File content", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		return a.core.ExecuteApplicationTool(ctx, textArg(args, "workspace"), "project.write", map[string]any{"path": args["path"], "content": args["content"]})
	})
	register("network.get", "Fetch an HTTP resource with separate network permission", "network", map[string]assistant.Field{"workspace": field("Absolute project directory", true), "url": field("HTTP(S) URL", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		url := textArg(args, "url")
		if strings.TrimSpace(url) == "" {
			return nil, errors.New("URL is required")
		}
		return a.core.ExecuteApplicationTool(ctx, textArg(args, "workspace"), "network.get", map[string]any{"url": url})
	})
	a.registerAssistantExtras(r)
	a.registerAssistantCode(r)
	a.registerAssistantTasks(r)
	return r
}
