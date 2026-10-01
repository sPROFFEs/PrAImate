package main

import (
	"context"
	"strings"
)

func (a *App) assistantSearch(ctx context.Context, args map[string]any) (any, error) {
	query := strings.ToLower(strings.TrimSpace(textArg(args, "query")))
	types := map[string]bool{}
	if supplied, ok := args["types"].([]any); ok {
		for _, v := range supplied {
			if name, ok := v.(string); ok {
				types[name] = true
			}
		}
	}
	include := func(name string) bool { return len(types) == 0 || types[name] }
	match := func(values ...string) bool {
		return strings.Contains(strings.ToLower(strings.Join(values, " ")), query)
	}
	out := []map[string]any{}
	add := func(item map[string]any) {
		if len(out) < 30 {
			out = append(out, item)
		}
	}
	if include("chat") || include("project") {
		chats, err := a.core.SearchChats(ctx, query, 12)
		if err != nil {
			return nil, err
		}
		for _, ch := range chats {
			if include("chat") {
				item := chatSummary(ch)
				item["type"] = "chat"
				add(item)
			}
		}
		if include("project") {
			chats, err := a.core.ListChats(ctx, 100)
			if err != nil {
				return nil, err
			}
			seen := map[string]bool{}
			for _, ch := range chats {
				if ch.WorkspacePath != "" && !seen[ch.WorkspacePath] && match(ch.WorkspacePath) {
					seen[ch.WorkspacePath] = true
					add(map[string]any{"type": "project", "path": ch.WorkspacePath})
				}
			}
		}
	}
	if include("agent") {
		agents, err := a.ListAgents()
		if err != nil {
			return nil, err
		}
		for _, agent := range agents {
			if match(agent.ID, agent.Name, agent.Description) {
				add(map[string]any{"type": "agent", "id": agent.ID, "name": agent.Name, "description": agent.Description})
			}
		}
	}
	if include("worker") && a.workers != nil {
		runs, err := a.WorkerRuns()
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if match(run.ID, run.Title, run.Task) {
				add(map[string]any{"type": "worker", "id": run.ID, "title": run.Title, "status": run.Status})
			}
		}
	}
	if include("mcp") {
		servers, err := a.MCPServers()
		if err != nil {
			return nil, err
		}
		for _, server := range servers {
			if match(server.ID, server.Name) {
				add(map[string]any{"type": "mcp", "id": server.ID, "name": server.Name, "enabled": server.Enabled})
			}
		}
	}
	if include("skill") {
		if skills, err := a.InstalledSkillVersionsV2(); err == nil {
			for _, skill := range skills {
				if match(skill.Ref, skill.Name, skill.Description) {
					add(map[string]any{"type": "skill", "ref": skill.Ref, "digest": skill.Digest, "name": skill.Name, "approved": skill.Approved})
				}
			}
		}
	}
	if include("session") {
		for _, session := range a.ListTerminalSessions() {
			if match(session.ID, session.Name, session.Cwd) {
				add(map[string]any{"type": "session", "id": session.ID, "name": session.Name, "workspace": session.Cwd})
			}
		}
	}
	if include("task") {
		a.assistantTasksMu.Lock()
		tasks, err := a.loadApplicationTasks(ctx)
		a.assistantTasksMu.Unlock()
		if err != nil {
			return nil, err
		}
		for _, task := range tasks {
			if match(task.ID, task.Goal) {
				add(map[string]any{"type": "task", "id": task.ID, "goal": task.Goal, "status": task.Status, "run_id": task.RunID})
			}
		}
	}
	return out, nil
}
