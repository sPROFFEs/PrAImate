package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sPROFFEs/PrAImate/internal/assistant"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"strings"
)

func (a *App) registerAssistantExtras(r *assistant.Registry) {
	f := func(description string, required bool) assistant.Field {
		return assistant.Field{Type: "string", Description: description, Required: required}
	}
	register := func(name, desc, cap string, fields map[string]assistant.Field, fn func(context.Context, map[string]any) (any, error)) {
		r.Register(assistant.Action{Name: name, Description: desc, Capability: cap, Fields: fields, Execute: fn})
	}
	r.Register(assistant.Action{Name: "chats.send", Description: "Send a concrete message to an existing conversation using its configured CLI, model and agent. Inspect chats.read for the actual reply. Use workers.continue for worker executions.", Capability: "delegate", ExtraCapabilities: func(map[string]any) []string { return []string{"chats"} }, Fields: map[string]assistant.Field{"id": f("Chat ID", true), "message": f("Concrete message and expected output", true)}, Execute: func(ctx context.Context, args map[string]any) (any, error) {
		ch, err := a.core.GetChat(ctx, textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		if ch.CLIAgent == "workers" {
			return nil, errors.New("use workers.continue for worker executions")
		}
		prompt := ""
		if ch.AgentID != "" {
			agent, err := a.core.GetAgent(ctx, ch.AgentID)
			if err != nil {
				return nil, err
			}
			prompt = core.AgentSystemPrompt(agent)
		}
		_, err = a.core.ContinueChat(ctx, ch.ID, textArg(args, "message"), ch.WorkspacePath, prompt)
		if err != nil {
			return map[string]any{"chat_id": ch.ID}, err
		}
		return map[string]any{"chat_id": ch.ID, "run_id": ch.ID, "status": "completed", "hint": "Use chats.read to inspect the actual answer."}, nil
	}})
	register("chats.clone", "Create a fresh chat with the same configuration as an existing one; history and CLI session are not copied", "chats", map[string]assistant.Field{"id": f("Source chat ID", true), "title": f("New title", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		ch, err := a.core.GetChat(ctx, textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		settings := ch.Settings
		if ch.CLIAgent == "workers" {
			return nil, errors.New("create a new worker execution using delegate.workers")
		}
		settings.SkillRuntime = nil
		settings.Surface = ""
		created, err := a.core.CreateChat(ctx, core.CreateChatRequest{Title: textArg(args, "title"), AgentID: ch.AgentID, CLIAgent: ch.CLIAgent, WorkspacePath: ch.WorkspacePath, Settings: settings})
		if err != nil {
			return nil, err
		}
		return chatSummary(*created), nil
	})
	register("agents.clone", "Clone an agent definition and runtime configuration. Knowledge files and setup scripts are not copied. read_only removes commands, edits, network, MCP and scripted workflows.", "agents", map[string]assistant.Field{"id": f("Source agent ID", true), "new_id": f("Unused new identifier", true), "name": f("New display name", true), "read_only": {Type: "boolean", Description: "Remove mutating runtime capabilities", Required: false}}, func(ctx context.Context, args map[string]any) (any, error) {
		agent, err := a.core.GetAgent(ctx, textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		id := textArg(args, "new_id")
		if _, err := a.core.GetAgent(ctx, id); err == nil {
			return nil, errors.New("destination agent already exists")
		} else if !errors.Is(err, core.ErrAgentNotFound) {
			return nil, err
		}
		manifest, err := core.LoadAgentRuntime(agent.ID)
		if err != nil {
			return nil, err
		}
		agent.ID = id
		agent.Name = textArg(args, "name")
		agent.SourcePath = ""
		agent.Knowledge = ""
		agent.Requirements = nil
		if boolArg(args, "read_only") {
			agent.Tools = nil
			agent.MCPServers = nil
			agent.Workflows = nil
			agent.DefaultWorkflow = ""
			if manifest == nil {
				manifest = &core.AgentRuntimeManifest{Schema: core.AgentRuntimeSchema, Mode: core.RuntimeNative, PresetOrigin: core.PresetCustom}
			}
			manifest.Capabilities = core.AgentCapabilities{ReadProject: true, AnalyzeCode: true}
			manifest.Permissions.DefaultTools = "plan"
		}
		raw, err := core.MarshalAgentYAML(agent)
		if err != nil {
			return nil, err
		}
		created, err := a.core.ImportAgentYAML(ctx, raw, "")
		if err != nil {
			return nil, err
		}
		if manifest != nil {
			if err := core.SaveAgentRuntime(id, manifest); err != nil {
				return nil, fmt.Errorf("agent %s created but runtime configuration failed: %w", id, err)
			}
		}
		return map[string]any{"id": created.ID, "name": created.Name, "read_only": boolArg(args, "read_only")}, nil
	})
	register("agents.configure_mcp", "Attach existing MCP servers to an agent without exposing their credentials", "agents", map[string]assistant.Field{"id": f("Agent ID", true), "mcp_servers": {Type: "array", Description: "Existing server ID strings", Required: true}}, func(ctx context.Context, args map[string]any) (any, error) {
		agent, err := a.core.GetAgent(ctx, textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		servers := []string{}
		for _, v := range args["mcp_servers"].([]any) {
			id, ok := v.(string)
			if !ok {
				return nil, errors.New("MCP IDs must be strings")
			}
			if _, err := a.core.GetMCPServer(ctx, id); err != nil {
				return nil, err
			}
			servers = append(servers, id)
		}
		agent.MCPServers = servers
		raw, err := core.MarshalAgentYAML(agent)
		if err != nil {
			return nil, err
		}
		_, err = a.core.ImportAgentYAML(ctx, raw, agent.SourcePath)
		return map[string]any{"id": agent.ID, "mcp_servers": servers}, err
	})
	register("agents.validate", "Validate an existing agent definition and its effective runtime capabilities", "read", map[string]assistant.Field{"id": f("Agent ID", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		agent, err := a.core.GetAgent(ctx, textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		if err := agent.Validate(); err != nil {
			return nil, err
		}
		effective, err := a.core.ResolveEffectiveAgentConfig(ctx, agent)
		if err != nil {
			return nil, err
		}
		return map[string]any{"valid": true, "mode": effective.Mode, "capabilities": func() any {
			if effective.Manifest != nil {
				return effective.Manifest.Capabilities
			}
			return nil
		}()}, nil
	})
	register("skills.set_agent", "Enable, disable or replace an agent's selections using already installed and approved immutable skills. Empty choices disables all skills; approvals are never changed.", "skills", map[string]assistant.Field{"agent_id": f("Agent ID", true), "choices": {Type: "array", Description: "Objects containing ref, digest and activation (auto, pinned, manual or off)", Required: true}}, func(ctx context.Context, args map[string]any) (any, error) {
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
		agent, err := a.core.GetAgent(ctx, textArg(args, "agent_id"))
		if err != nil {
			return nil, err
		}
		agent.Schema = core.AgentSchemaV2
		agent.Skills = selection.Config
		agent.SkillsLock = selection.Lock
		encoded, err := core.MarshalAgentYAML(agent)
		if err != nil {
			return nil, err
		}
		_, err = a.core.ImportAgentYAML(ctx, encoded, agent.SourcePath)
		return map[string]any{"updated": err == nil, "agent_id": agent.ID}, err
	})
	register("skills.inspect", "Read one installed immutable skill file or list its files", "read", map[string]assistant.Field{"ref": f("Skill reference", true), "digest": f("Exact installed digest", true), "path": f("Optional package file path", false)}, func(ctx context.Context, args map[string]any) (any, error) {
		result, err := a.core.SkillLibrary(ctx, core.SkillLibraryRequest{Action: "read", Ref: textArg(args, "ref"), Digest: textArg(args, "digest")})
		if err != nil {
			return nil, err
		}
		files := []map[string]any{}
		path := textArg(args, "path")
		for _, file := range result.Files {
			if path == "" {
				files = append(files, map[string]any{"path": file.Path, "bytes": len(file.Content)})
			} else if path == file.Path {
				content := string(file.Content)
				truncated := len(content) > 4096
				if truncated {
					content = content[:4096]
				}
				return map[string]any{"ref": textArg(args, "ref"), "path": path, "content": content, "truncated": truncated, "approved": result.Approved}, nil
			}
		}
		if path != "" {
			return nil, errors.New("skill file not found")
		}
		return map[string]any{"files": files, "approved": result.Approved}, nil
	})
	register("skills.create", "Create and validate a skill draft from SKILL.md content; no approval or activation is granted", "skills", map[string]assistant.Field{"ref": f("New skill reference", true), "content": f("Complete SKILL.md including YAML front matter", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		return a.assistantSkillDraft(ctx, "", "", textArg(args, "ref"), textArg(args, "content"))
	})
	register("skills.update", "Fork an installed skill into a new draft, preserving its resources and local immutable version; replace only SKILL.md", "skills", map[string]assistant.Field{"ref": f("Existing reference", true), "digest": f("Exact source digest", true), "new_ref": f("Draft reference", true), "content": f("Updated SKILL.md", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		return a.assistantSkillDraft(ctx, textArg(args, "ref"), textArg(args, "digest"), textArg(args, "new_ref"), textArg(args, "content"))
	})
	register("skills.validate", "Validate a saved skill draft and show changes against an optional installed version", "read", map[string]assistant.Field{"draft_key": f("Draft key", true), "ref": f("Optional source reference", false), "digest": f("Optional source digest", false)}, func(ctx context.Context, args map[string]any) (any, error) {
		result, err := a.core.SkillLibrary(ctx, core.SkillLibraryRequest{Action: "draft-preview", Key: textArg(args, "draft_key"), Ref: textArg(args, "ref"), Digest: textArg(args, "digest")})
		if err != nil {
			return nil, err
		}
		return map[string]any{"valid": true, "review": result.Review, "changes": result.Changes}, nil
	})
	register("skills.publish", "Publish an exact reviewed draft as an immutable unapproved version; pinned versions and selections are preserved", "skills", map[string]assistant.Field{"draft_key": f("Draft key", true), "review": f("Exact digest from skills.validate", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		result, err := a.assistantSkillMutation(ctx, core.SkillLibraryRequest{Action: "publish", Key: textArg(args, "draft_key"), Review: textArg(args, "review")})
		if err != nil {
			return nil, err
		}
		return map[string]any{"version": result.Version, "approved": false}, nil
	})
	r.Register(assistant.Action{Name: "skills.install_url", Description: "Inspect or install immutable skills from a GitHub repository. Provide review only after inspection. Never approves or activates packages.", Capability: "skills", ExtraCapabilities: func(map[string]any) []string { return []string{"network"} }, Fields: map[string]assistant.Field{"url": f("GitHub repository URL", true), "git_ref": f("Revision or branch", false), "subpath": f("Optional subdirectory", false), "review": f("Exact inspection digest to install", false)}, Execute: func(ctx context.Context, args map[string]any) (any, error) {
		return a.assistantSkillInstall(ctx, textArg(args, "url"), "github", textArg(args, "git_ref"), textArg(args, "subpath"), textArg(args, "review"))
	}})
	r.Register(assistant.Action{Name: "skills.import", Description: "Inspect or import skills from a local directory or ZIP. Requires filesystem permission. No automatic approval or activation.", Capability: "skills", ExtraCapabilities: func(map[string]any) []string { return []string{"filesystem"} }, Fields: map[string]assistant.Field{"path": f("Local package path", true), "kind": f("directory or zip", true), "review": f("Exact inspection digest to install", false)}, Execute: func(ctx context.Context, args map[string]any) (any, error) {
		kind := textArg(args, "kind")
		if kind != "directory" && kind != "zip" {
			return nil, errors.New("import kind must be directory or zip")
		}
		return a.assistantSkillInstall(ctx, textArg(args, "path"), kind, "", "", textArg(args, "review"))
	}})
	register("mcp.catalogue", "Inspect available MCP providers and installation metadata", "read", nil, func(ctx context.Context, args map[string]any) (any, error) { return core.ListMCPCatalogue(), nil })
	register("mcp.configure", "Register a custom MCP without credentials. Server is initially disabled; enabling and testing are separate actions. Installation may require system/network permissions.", "mcp", map[string]assistant.Field{"id": f("New server identifier", true), "name": f("Display name", true), "transport": f("stdio, http or sse", true), "command": f("Local executable and args for stdio", false), "url": f("HTTP/SSE endpoint", false)}, func(ctx context.Context, args map[string]any) (any, error) {
		id := textArg(args, "id")
		if _, err := a.core.GetMCPServer(ctx, id); err == nil {
			return nil, errors.New("MCP server exists; edit it in MCP settings to preserve credentials")
		} else if !errors.Is(err, core.ErrMCPServerNotFound) {
			return nil, err
		}
		enabled := false
		server, err := a.core.AddCustomMCP(ctx, core.AddCustomMCPRequest{ID: id, Name: textArg(args, "name"), Transport: textArg(args, "transport"), Command: textArg(args, "command"), URL: textArg(args, "url"), Enabled: &enabled})
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": server.ID, "name": server.Name, "enabled": server.Enabled}, nil
	})
	r.Register(assistant.Action{Name: "mcp.test", Description: "Run a real MCP handshake and list tools. Match transport from mcp.list. stdio requires system permission; http/sse requires network permission.", Capability: "mcp", Fields: map[string]assistant.Field{"id": f("Server ID", true), "transport": f("Current stdio, http or sse transport", true)}, ExtraCapabilities: func(args map[string]any) []string {
		if textArg(args, "transport") == "stdio" {
			return []string{"system"}
		}
		return []string{"network"}
	}, Execute: func(ctx context.Context, args map[string]any) (any, error) {
		server, err := a.core.GetMCPServer(ctx, textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		if string(server.Transport) != textArg(args, "transport") {
			return nil, errors.New("MCP transport changed; inspect configuration before testing")
		}
		return a.core.InspectApplicationMCP(ctx, server.ID)
	}})
	register("mcp.delete", "Delete an existing MCP registration", "mcp", map[string]assistant.Field{"id": f("Server ID", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		err := a.core.DeleteMCPServer(ctx, textArg(args, "id"))
		return map[string]any{"deleted": err == nil}, err
	})
	register("models.list", "List configured local model profiles without exposing credentials", "read", nil, func(ctx context.Context, args map[string]any) (any, error) { return a.core.ListLocalHosts(ctx) })
	register("chats.set_model", "Change a chat's model, preserving its CLI, endpoint and permissions", "chats", map[string]assistant.Field{"id": f("Chat ID", true), "model": f("Exact model identifier", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		ch, err := a.core.GetChat(ctx, textArg(args, "id"))
		if err != nil {
			return nil, err
		}
		if ch.CLIAgent == "workers" {
			return nil, errors.New("use workers.set_model for worker executions")
		}
		model := textArg(args, "model")
		err = a.core.UpdateChatConfig(ctx, ch.ID, ch.CLIAgent, model, ch.Settings.Tools)
		if err == nil && ch.Settings.Local != nil {
			err = a.core.UpdateChatSettings(ctx, ch.ID, func(s *core.ChatSettings) {
				if s.Local != nil {
					s.Local.Model = model
				}
			})
		}
		return map[string]any{"updated": err == nil}, err
	})
	register("chats.archive", "Archive a saved conversation without deleting its history", "chats", map[string]assistant.Field{"id": f("Chat ID", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		err := a.core.EndChat(ctx, textArg(args, "id"), "archived")
		return map[string]any{"archived": err == nil}, err
	})
	register("skills.search", "Search installed skill names and descriptions", "read", map[string]assistant.Field{"query": f("Search text", true)}, func(ctx context.Context, args map[string]any) (any, error) {
		args = map[string]any{"query": textArg(args, "query"), "types": []any{"skill"}}
		return a.assistantSearch(ctx, args)
	})
}
