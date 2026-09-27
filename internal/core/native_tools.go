package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func nativeToolDef(name, description, properties string, required ...string) nativeTool {
	var props map[string]any
	if err := json.Unmarshal([]byte(properties), &props); err != nil {
		panic(err)
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false})
	return nativeTool{Type: "function", Function: nativeToolDefinition{Name: name, Description: description, Parameters: schema}}
}

func (run *nativeExecution) nativeTools(ctx context.Context, root, level string, approval *ApprovalConfig) (*managedToolBroker, []nativeTool, error) {
	if run.modelOnly {
		return nil, nil, nil
	}
	caps := AgentCapabilities{ReadProject: true, AnalyzeCode: true, UseGit: true, ModifyFiles: level != "", ExecuteCommands: level != "", Network: level == "ask" || level == "full", ExternalServices: level == "ask" || level == "full"}
	if run.agent != nil {
		effective, err := run.core.ResolveEffectiveAgentConfig(ctx, run.agent)
		if err != nil {
			return nil, nil, err
		}
		if effective.Manifest != nil {
			limit := effective.Manifest.Capabilities
			caps.ReadProject = caps.ReadProject && limit.ReadProject
			caps.AnalyzeCode = caps.AnalyzeCode && limit.AnalyzeCode
			caps.UseGit = caps.UseGit && limit.UseGit
			caps.ModifyFiles = caps.ModifyFiles && limit.ModifyFiles
			caps.ExecuteCommands = caps.ExecuteCommands && limit.ExecuteCommands
			caps.Network = caps.Network && limit.Network
			caps.ExternalServices = caps.ExternalServices && limit.ExternalServices
		}
	}
	policy := &ApprovalConfig{Request: func(ctx context.Context, tool string, input map[string]any) (bool, error) {
		if level == "full" {
			return true, nil
		}
		if level == "edits" && tool == "project.write" {
			return true, nil
		}
		if (level == "ask" || level == "edits") && approval != nil && approval.Request != nil {
			return approval.Request(ctx, tool, input)
		}
		return false, nil
	}}
	var servers []MCPServer
	if len(run.servers) > 0 {
		if !caps.ExternalServices {
			return nil, nil, errors.New("selected MCP servers require Ask or Full tools and the external-services capability")
		}
		for _, s := range run.servers {
			allowed, err := policy.Request(ctx, "mcp.connect."+s.ID, map[string]any{"server": s.ID, "transport": s.Transport, "command": s.Command, "url": s.URL})
			if err != nil {
				return nil, nil, err
			}
			if !allowed {
				return nil, nil, fmt.Errorf("MCP connection %q denied", s.ID)
			}
			servers = append(servers, s)
		}
	}
	broker, err := newManagedToolBroker(ctx, run.agent, caps, root, policy, servers)
	if err != nil {
		return nil, nil, err
	}
	var defs []nativeTool
	if caps.UseGit {
		defs = append(defs, nativeToolDef("git_inspect", "Read repository status, diff or recent log without external diff/textconv/fsmonitor commands. Mutations require run_command and its approval policy.", `{"operation":{"type":"string","enum":["status","diff","log"]},"staged":{"type":"boolean"},"limit":{"type":"integer"}}`, "operation"))
	}
	if broker.canReadProject() {
		defs = append(defs,
			nativeToolDef("read_file", "Read a workspace-relative file. offset and limit are bytes.", `{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}}`, "path"),
			nativeToolDef("list_files", "List a workspace-relative directory.", `{"path":{"type":"string"}}`, "path"),
			nativeToolDef("search_files", "Search literal text in the workspace.", `{"query":{"type":"string"},"path":{"type":"string"},"max_results":{"type":"integer"}}`, "query"))
	}
	if caps.ModifyFiles {
		defs = append(defs,
			nativeToolDef("write_file", "Write a complete workspace-relative file under the selected approval policy.", `{"path":{"type":"string"},"content":{"type":"string"}}`, "path", "content"),
			nativeToolDef("edit_file", "Replace an exact, unique string in a workspace-relative file. Read it first.", `{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"}}`, "path", "old_string", "new_string"))
	}
	if caps.ExecuteCommands {
		defs = append(defs, nativeToolDef("run_command", "Run an executable and arguments in the workspace after permission approval. Shell syntax requires explicitly invoking the platform shell.", `{"command":{"type":"string"},"args":{"type":"array","items":{"type":"string"}},"timeout_seconds":{"type":"integer"}}`, "command", "args"))
	}
	if caps.Network {
		defs = append(defs, nativeToolDef("fetch_url", "Fetch a URL through the core network policy.", `{"url":{"type":"string"}}`, "url"))
	}
	if broker.canReadKnowledge() {
		defs = append(defs,
			nativeToolDef("knowledge_read", "Read an agent knowledge file.", `{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}}`, "path"),
			nativeToolDef("knowledge_search", "Search the selected agent knowledge.", `{"query":{"type":"string"},"path":{"type":"string"},"max_results":{"type":"integer"}}`, "query"))
	}
	if broker.canQueryKnowledge() {
		defs = append(defs, nativeToolDef("knowledge_query", "Query the agent knowledge index.", `{"question":{"type":"string"},"budget":{"type":"integer"}}`, "question"))
	}
	if broker.mcp != nil {
		for _, id := range sortedMCPIDs(broker.mcp) {
			s := broker.mcp.servers[id]
			for _, t := range s.tools {
				defs = append(defs, nativeTool{Type: "function", Function: nativeToolDefinition{Name: nativeMCPName(id, t.Name), Description: "MCP " + s.server.Name + ": " + t.Name + " — " + truncate(t.Description, 500), Parameters: json.RawMessage(safeMCPToolSchema(t.InputSchema))}})
			}
		}
	}
	return broker, defs, nil
}
func sortedMCPIDs(m *managedMCPSet) []string {
	var ids []string
	for id := range m.servers {
		ids = append(ids, id)
	}
	return sortedStrings(ids)
}
func nativeMCPName(server, tool string) string {
	sum := sha256.Sum256([]byte(server + "\x00" + tool))
	return "mcp_" + hex.EncodeToString(sum[:16])
}

func executeNativeTool(ctx context.Context, b *managedToolBroker, defs []nativeTool, call nativeToolCall) (string, error) {
	allowed := false
	for _, d := range defs {
		if d.Function.Name == call.Function.Name {
			allowed = true
			break
		}
	}
	if !allowed || b == nil {
		return "", errors.New("tool is not enabled by the current core policy")
	}
	raw := json.RawMessage(call.Function.Arguments)
	if call.Function.Name == "git_inspect" {
		if !b.capabilities.UseGit {
			return "", errors.New("Git capability is disabled")
		}
		var args struct {
			Operation string `json:"operation"`
			Staged    bool   `json:"staged"`
			Limit     int    `json:"limit"`
		}
		if err := decodeManagedArgs(raw, &args); err != nil {
			return "", err
		}
		argv := []string{"--no-pager", "--no-optional-locks", "-c", "core.fsmonitor=false", "-C", b.root}
		switch args.Operation {
		case "status":
			argv = append(argv, "status", "--porcelain=v1", "--untracked-files=normal")
		case "diff":
			argv = append(argv, "diff", "--no-ext-diff", "--no-textconv")
			if args.Staged {
				argv = append(argv, "--cached")
			}
		case "log":
			limit := args.Limit
			if limit <= 0 {
				limit = 10
			}
			if limit > 50 {
				return "", errors.New("Git log limit must be at most 50")
			}
			argv = append(argv, "log", "--no-decorate", "-n", strconv.Itoa(limit), "--format=%h %s")
		default:
			return "", errors.New("git_inspect only supports status, diff and log")
		}
		return b.execBounded(ctx, 30*time.Second, "git", argv...)
	}
	mapping := map[string]string{"read_file": "project.read", "list_files": "project.list", "search_files": "project.search", "write_file": "project.write", "run_command": "command.run", "fetch_url": "network.get", "knowledge_read": "knowledge.read", "knowledge_search": "knowledge.search", "knowledge_query": "knowledge.query"}
	if name := mapping[call.Function.Name]; name != "" {
		return b.ExecuteTool(ctx, name, raw)
	}
	if call.Function.Name == "edit_file" {
		var p struct {
			Path string `json:"path"`
			Old  string `json:"old_string"`
			New  string `json:"new_string"`
		}
		if err := decodeManagedArgs(raw, &p); err != nil {
			return "", err
		}
		if p.Old == "" {
			return "", errors.New("old_string cannot be empty")
		}
		path, err := b.resolveProjectPath(p.Path, false)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Size() > managedWriteLimit {
			return "", errors.New("edit target exceeds file size limit")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if strings.Count(string(body), p.Old) != 1 {
			return "", errors.New("old_string must match exactly once")
		}
		args, _ := json.Marshal(map[string]any{"path": p.Path, "content": strings.Replace(string(body), p.Old, p.New, 1)})
		return b.ExecuteTool(ctx, "project.write", args)
	}
	if b.mcp != nil {
		for id, s := range b.mcp.servers {
			for _, t := range s.tools {
				if nativeMCPName(id, t.Name) == call.Function.Name {
					args, _ := json.Marshal(map[string]any{"server": id, "tool": t.Name, "arguments": raw})
					return b.ExecuteTool(ctx, "mcp.call", args)
				}
			}
		}
	}
	return "", errors.New("unknown native tool")
}
