package core

// Unified execution configuration and preflight. Every GUI surface resolves
// its CLI, model, local route, permissions, and MCP selection through this
// package before starting a child process. Native execution leaves the loop to
// the CLI; agentic execution wraps model turns in PrAImate's managed runtime.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/ollama"
)

type ExecutionSurface string

const (
	SurfaceChat     ExecutionSurface = "chat"
	SurfaceStudio   ExecutionSurface = "studio"
	SurfaceWorkflow ExecutionSurface = "workflow"
	SurfaceTerminal ExecutionSurface = "terminal"
)

// CLICapabilities is the behaviour PrAImate can actually enforce for a CLI.
// It is intentionally conservative: a feature is true only when the current
// adapter path implements it, not merely when an interactive CLI may offer it.
type CLICapabilities struct {
	CLI             string   `json:"cli"`
	Streaming       bool     `json:"streaming"`
	Resume          bool     `json:"resume"`
	MCP             bool     `json:"mcp"`
	LocalRouting    bool     `json:"localRouting"`
	ManagedApproval bool     `json:"managedApproval"`
	ToolLevels      []string `json:"toolLevels"`
}

type PreflightIssue struct {
	Severity string `json:"severity"` // error | warning | info
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type ExecutionPreflight struct {
	OK           bool             `json:"ok"`
	CLI          string           `json:"cli"`
	Surface      ExecutionSurface `json:"surface"`
	Model        string           `json:"model,omitempty"`
	Tools        string           `json:"tools,omitempty"`
	Capabilities CLICapabilities  `json:"capabilities"`
	Issues       []PreflightIssue `json:"issues"`
}

// ExecutionRequest is the unresolved launch contract. Local credentials are
// never accepted from the renderer; an empty Local.APIKey is filled from the
// encrypted store.
type ExecutionRequest struct {
	Workflow      *Workflow
	SkillSettings *ChatSettings
	// skillSnapshot prevents current defaults from changing a persisted run.
	skillSnapshot bool
	Surface       ExecutionSurface
	Agent         *Agent
	ChatID        string
	CLI           string
	Cwd           string
	Model         string
	Tools         string
	// ToolsConfigured prevents an explicit empty/safe policy from being
	// replaced by the agent runtime's default tool level.
	ToolsConfigured bool
	Local           *ChatLocalEndpoint

	// MCP selection precedence: ExplicitMCP, then Agent.MCPServers, then
	// AllEnabledMCP for clean terminal sessions.
	MCPServers    []string
	ExplicitMCP   bool
	AllEnabledMCP bool
	Approval      *ApprovalConfig
}

// EffectiveExecutionConfig is consumed only inside the Go backend. Env may
// contain credentials and therefore must never be returned through Wails.
type EffectiveExecutionConfig struct {
	Surface            ExecutionSurface
	Agent              *Agent
	ChatID             string
	CLI                string
	Cwd                string
	Model              string
	Tools              string
	Local              *ChatLocalEndpoint
	Env                map[string]string
	Approval           *ApprovalConfig
	InternalMCPServers []MCPServer
	Capabilities       CLICapabilities
	Issues             []PreflightIssue

	mcpServers    []string
	explicitMCP   bool
	allEnabledMCP bool
}

func CapabilitiesForCLI(cli string) CLICapabilities {
	cap := CLICapabilities{CLI: cli, ToolLevels: []string{""}}
	switch cli {
	case "claude":
		cap.Streaming = true
		cap.Resume = true
		cap.MCP = true
		cap.ManagedApproval = true
		cap.ToolLevels = []string{"", "ask", "edits", "full"}
	case "openclaude":
		cap.Streaming = true
		cap.Resume = true
		cap.MCP = true
		cap.LocalRouting = true
		cap.ManagedApproval = true
		cap.ToolLevels = []string{"", "ask", "edits", "full"}
	case "codex":
		cap.Streaming = true
		cap.Resume = true
		cap.MCP = true
		cap.ToolLevels = []string{"", "edits", "full"}
	case "opencode", "praimate-code":
		cap.Streaming = true
		cap.Resume = true
		cap.MCP = true
		cap.LocalRouting = true
		// Headless OpenCode auto-rejects requested permissions unless its
		// all-permissions flag is used. It has no edits-only mode.
		cap.ToolLevels = []string{"", "plan", "full"}
	default:
		if a, err := GetCLIAdapter(cli); err == nil {
			cap.Resume = a.SupportsResume()
			_, cap.Streaming = a.(streamingAdapter)
		}
	}
	return cap
}

// ValidateLocalRoutingCLI enforces the product support boundary before any
// chat or project state is written. Claude Code deliberately stays on its
// supported Anthropic transport; OpenClaude owns Claude-style local routing.
func ValidateLocalRoutingCLI(cli string) error {
	cli = strings.TrimSpace(cli)
	if CapabilitiesForCLI(cli).LocalRouting {
		return nil
	}
	if cli == "claude" {
		return errors.New("Claude Code local-LLM routing is not supported; choose OpenClaude for local OpenAI-compatible models, or clear the local endpoint to use Claude Code with Anthropic")
	}
	return fmt.Errorf("%s local-LLM routing is not supported by PrAImate", cli)
}

func (c *Core) ResolveExecutionConfig(ctx context.Context, req ExecutionRequest) (*EffectiveExecutionConfig, error) {
	if req.ChatID != "" && c.store != nil {
		chat, err := c.GetChat(ctx, req.ChatID)
		if err != nil {
			return nil, err
		}
		if err := c.guardBoundSkills(ctx, nil, nil, chat.Settings); err != nil {
			return nil, err
		}
	} else {
		state := ChatSettings{}
		if req.SkillSettings != nil {
			state = *req.SkillSettings
		}
		var err error
		if req.skillSnapshot {
			err = c.guardBoundSkills(ctx, nil, nil, state)
		} else {
			err = c.guardNewBoundSkills(ctx, req.Agent, req.Workflow, state)
		}
		if err != nil {
			return nil, err
		}
	}
	req.CLI = strings.TrimSpace(req.CLI)
	req.Cwd = strings.TrimSpace(req.Cwd)
	req.Model = strings.TrimSpace(req.Model)
	req.Tools = strings.TrimSpace(req.Tools)
	if req.CLI == "" {
		return nil, errors.New("execution preflight: no CLI selected")
	}
	if req.Cwd == "" {
		return nil, errors.New("execution preflight: a working folder is required")
	}
	if !validToolLevel(req.Tools) {
		return nil, fmt.Errorf("execution preflight: unknown tools level %q", req.Tools)
	}
	if req.Agent != nil {
		if !contains(req.Agent.Supports, req.CLI) {
			return nil, fmt.Errorf("agent %q does not support CLI %q", req.Agent.ID, req.CLI)
		}
		gate := string(req.Surface)
		if req.Surface == SurfaceStudio {
			gate = "editor"
		}
		if gate == "chat" || gate == "terminal" || gate == "editor" {
			if !req.Agent.AllowsSurface(gate) {
				return nil, fmt.Errorf("agent %q is not allowed on the %s surface", req.Agent.Name, req.Surface)
			}
		}
		agentConfig, err := c.ResolveEffectiveAgentConfig(ctx, req.Agent)
		if err != nil {
			return nil, err
		}
		if !agentConfig.NativeCompatible {
			return nil, fmt.Errorf("agent %q requires the %s runtime (%s); this PrAImate build only supports native execution",
				req.Agent.Name, agentConfig.Mode, strings.Join(agentConfig.RequiredFeatures, ", "))
		}
		if req.Tools == "" && !req.ToolsConfigured {
			req.Tools = agentConfig.DefaultTools
		}
	}

	cap := CapabilitiesForCLI(req.CLI)
	out := &EffectiveExecutionConfig{
		Surface: req.Surface, Agent: req.Agent, ChatID: req.ChatID,
		CLI: req.CLI, Cwd: req.Cwd, Model: req.Model, Tools: req.Tools,
		Approval: req.Approval, Capabilities: cap,
		mcpServers:  append([]string(nil), req.MCPServers...),
		explicitMCP: req.ExplicitMCP, allEnabledMCP: req.AllEnabledMCP,
	}
	if out.Tools != "" && !slices.Contains(cap.ToolLevels, out.Tools) {
		out.Issues = append(out.Issues, PreflightIssue{
			Severity: "warning", Code: "tools_degraded",
			Message: fmt.Sprintf("%s cannot enforce the %q permission level on this surface; PrAImate will use safe mode", req.CLI, out.Tools),
		})
		out.Tools = ""
	}
	if out.Tools == "ask" && req.Approval == nil && req.Surface != SurfaceTerminal {
		out.Issues = append(out.Issues, PreflightIssue{
			Severity: "warning", Code: "approval_unavailable",
			Message: "Managed approval is unavailable for this run; PrAImate will use safe mode",
		})
		out.Tools = ""
	}

	if req.Local != nil && strings.TrimSpace(req.Local.Endpoint) != "" {
		if err := ValidateLocalRoutingCLI(req.CLI); err != nil {
			return nil, err
		}
		local := *req.Local
		local.Endpoint = ollama.NormalizeEndpoint(local.Endpoint)
		local.Model = strings.TrimSpace(local.Model)
		local.APIKey = strings.TrimSpace(local.APIKey)
		if local.Model == "" {
			local.Model = out.Model
		}
		if local.Model == "" {
			return nil, errors.New("execution preflight: local routing requires a model")
		}
		if local.APIKey == "" {
			key, err := c.localLLMAPIKey(ctx)
			if err != nil {
				return nil, err
			}
			local.APIKey = key
		}
		settings := ollama.Settings{
			Endpoint: local.Endpoint, Model: local.Model, APIKey: local.APIKey,
			ContextTokens: local.ContextTokens, OutputTokens: local.OutputTokens,
		}
		// Older GUI chats predate per-chat token hints. For the saved endpoint,
		// inherit the global defaults without rewriting the chat row.
		if req.CLI == "openclaude" && settings.ContextTokens == 0 && settings.OutputTokens == 0 {
			global, err := launcher.LoadConfig()
			if err != nil {
				return nil, fmt.Errorf("load local LLM token limits: %w", err)
			}
			if global != nil && ollama.NormalizeEndpoint(global.DefaultLocalEndpoint) == local.Endpoint {
				settings.ContextTokens = global.DefaultLocalContextTokens
				settings.OutputTokens = global.DefaultLocalOutputTokens
				local.ContextTokens = settings.ContextTokens
				local.OutputTokens = settings.OutputTokens
			}
		}
		switch req.CLI {
		case "openclaude":
			out.Model = local.Model
			out.Env = ollama.OpenClaudeEnv(settings)
		case "opencode", "praimate-code":
			out.Model = "praimate-local/" + local.Model
			out.Env = ollama.OpenAIEnv(settings)
		}
		out.Local = &local
		if insecureRemoteEndpoint(local.Endpoint) {
			out.Issues = append(out.Issues, PreflightIssue{
				Severity: "warning", Code: "plaintext_remote_llm",
				Message: "The selected remote LLM endpoint uses plaintext HTTP; prompts, files, and credentials may be exposed in transit",
			})
		}
	} else if req.CLI == "opencode" || req.CLI == "praimate-code" {
		managed, err := ollama.OpenCodeUsesManagedLocalRoute(out.Model)
		if err != nil {
			return nil, fmt.Errorf("resolve OpenCode local route: %w", err)
		}
		if managed {
			key, err := c.localLLMAPIKey(ctx)
			if err != nil {
				return nil, err
			}
			if key != "" {
				out.Env = map[string]string{"OPENAI_API_KEY": key}
			}
		}
	}
	return out, nil
}

// PrepareExecution applies project-scoped configuration immediately before a
// child process starts. It is the only resolver step with filesystem writes.
func (c *Core) PrepareExecution(ctx context.Context, cfg *EffectiveExecutionConfig) error {
	if cfg == nil {
		return errors.New("PrepareExecution: nil config")
	}
	if cfg.Local != nil {
		if cfg.CLI == "opencode" || cfg.CLI == "praimate-code" {
			if err := writeOpenCodeLocalRoute(cfg.Cwd, *cfg.Local); err != nil {
				return fmt.Errorf("prepare local OpenCode route: %w", err)
			}
		} else if cfg.CLI == "openclaude" {
			settings := ollama.Settings{
				Endpoint:      cfg.Local.Endpoint,
				Model:         cfg.Local.Model,
				APIKey:        cfg.Local.APIKey,
				ContextTokens: cfg.Local.ContextTokens,
				OutputTokens:  cfg.Local.OutputTokens,
			}
			ls := launcher.OllamaSettings{
				Endpoint:      settings.Endpoint,
				Model:         settings.Model,
				APIKey:        settings.APIKey,
				ContextTokens: settings.ContextTokens,
				OutputTokens:  settings.OutputTokens,
			}
			if err := launcher.WriteOpenClaudeLocalProfile(ls, settings.APIKey); err != nil {
				return fmt.Errorf("prepare local OpenClaude route: %w", err)
			}
		}
	} else if cfg.CLI == "openclaude" {
		if !launcher.IsOpenClaudeConfigured() {
			_ = launcher.BackupOpenClaudeLocalProfileIfPresent()
		}
	}

	var (
		mcpEnv map[string]string
		err    error
	)
	switch {
	case cfg.explicitMCP:
		mcpEnv, err = c.PrepareSelectedMCPForRunWithExtra(ctx, cfg.mcpServers, cfg.InternalMCPServers, cfg.CLI, cfg.Cwd)
	case cfg.Agent != nil:
		mcpEnv, err = c.PrepareMCPForRunWithExtra(ctx, cfg.Agent, cfg.InternalMCPServers, cfg.CLI, cfg.Cwd)
	case cfg.allEnabledMCP || len(cfg.InternalMCPServers) > 0:
		mcpEnv, err = c.PrepareEnabledMCPForRunWithExtra(ctx, cfg.InternalMCPServers, cfg.CLI, cfg.Cwd)
	}
	if err != nil {
		return err
	}
	cfg.Env = mergeStringMaps(cfg.Env, mcpEnv)
	return nil
}

func (c *Core) PreflightExecution(ctx context.Context, req ExecutionRequest, checkAvailable bool) ExecutionPreflight {
	result := ExecutionPreflight{CLI: strings.TrimSpace(req.CLI), Surface: req.Surface}
	if req.Agent != nil {
		if agentConfig, err := c.ResolveEffectiveAgentConfig(ctx, req.Agent); err != nil {
			result.Issues = []PreflightIssue{{Severity: "error", Code: "invalid_runtime", Message: err.Error()}}
			return result
		} else if agentConfig.Mode == RuntimeAgentic {
			return c.preflightManagedExecution(ctx, req, checkAvailable, agentConfig)
		}
	}
	cfg, err := c.ResolveExecutionConfig(ctx, req)
	if err != nil {
		result.Issues = []PreflightIssue{{Severity: "error", Code: "invalid_config", Message: err.Error()}}
		return result
	}
	result.Model = cfg.Model
	result.Tools = cfg.Tools
	result.Capabilities = cfg.Capabilities
	result.Issues = append(result.Issues, cfg.Issues...)
	if info, err := os.Stat(cfg.Cwd); err != nil {
		result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "working_folder", Message: "Working folder is unavailable: " + err.Error()})
	} else if !info.IsDir() {
		result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "working_folder", Message: "Working folder is not a directory"})
	}
	if checkAvailable {
		if adapter, err := GetCLIAdapter(cfg.CLI); err != nil {
			result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "cli_unavailable", Message: err.Error()})
		} else if err := adapter.Available(ctx); err != nil {
			result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "cli_unavailable", Message: err.Error()})
		}
	}
	ids := cfg.mcpServers
	if !cfg.explicitMCP && cfg.Agent != nil {
		ids = cfg.Agent.MCPServers
	}
	for _, id := range ids {
		s, err := c.GetMCPServer(ctx, id)
		if err != nil {
			result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "mcp_missing", Message: fmt.Sprintf("MCP server %q is unavailable: %v", id, err)})
			continue
		}
		if !s.Enabled {
			result.Issues = append(result.Issues, PreflightIssue{Severity: "warning", Code: "mcp_disabled", Message: fmt.Sprintf("MCP server %q is disabled and will not be exposed", s.Name)})
		}
	}
	result.OK = true
	for _, issue := range result.Issues {
		if issue.Severity == "error" {
			result.OK = false
			break
		}
	}
	return result
}

func (c *Core) preflightManagedExecution(ctx context.Context, req ExecutionRequest, checkAvailable bool, agentConfig *EffectiveAgentConfig) ExecutionPreflight {
	result := ExecutionPreflight{
		CLI: strings.TrimSpace(req.CLI), Surface: req.Surface,
		Capabilities: CapabilitiesForCLI(strings.TrimSpace(req.CLI)),
	}
	if !agentConfig.AgenticCompatible {
		result.Issues = append(result.Issues, PreflightIssue{
			Severity: "error", Code: "managed_features_unavailable",
			Message: "This agent requires unavailable managed features: " + strings.Join(agentConfig.UnsupportedFeatures, ", "),
		})
	}
	if err := validateManagedKnowledge(req.Agent); err != nil {
		result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "managed_knowledge", Message: err.Error()})
	}
	if req.Surface == SurfaceTerminal {
		result.Issues = append(result.Issues, PreflightIssue{
			Severity: "error", Code: "managed_terminal_unavailable",
			Message: "Managed Autonomous runs are available in Chats, Studio, and Workflows; interactive Terminal execution remains native",
		})
	}
	if len(req.Agent.MCPServers) > 0 && (agentConfig.Manifest == nil || !agentConfig.Manifest.Capabilities.ExternalServices) {
		result.Issues = append(result.Issues, PreflightIssue{
			Severity: "error", Code: "managed_mcp_capability",
			Message: "Managed MCP servers require the external_services capability",
		})
	} else {
		for _, id := range req.Agent.MCPServers {
			s, err := c.GetMCPServer(ctx, id)
			if err != nil {
				result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "mcp_missing", Message: fmt.Sprintf("MCP server %q is unavailable: %v", id, err)})
			} else if !s.Enabled {
				result.Issues = append(result.Issues, PreflightIssue{Severity: "warning", Code: "mcp_disabled", Message: fmt.Sprintf("MCP server %q is disabled and will not be exposed", s.Name)})
			}
		}
	}
	if req.CLI == "" {
		result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "cli", Message: "No CLI selected"})
	} else if !contains(req.Agent.Supports, req.CLI) {
		result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "cli", Message: fmt.Sprintf("agent %q does not support CLI %q", req.Agent.ID, req.CLI)})
	} else if adapter, err := GetCLIAdapter(req.CLI); err == nil && !supportsManagedSafeMode(adapter) {
		result.Issues = append(result.Issues, PreflightIssue{
			Severity: "error", Code: "managed_safe_mode_unavailable",
			Message: fmt.Sprintf("CLI %q cannot enforce managed safe mode and is blocked for Autonomous runs", req.CLI),
		})
	}
	gate := string(req.Surface)
	if req.Surface == SurfaceStudio {
		gate = "editor"
	}
	if (gate == "chat" || gate == "terminal" || gate == "editor") && !req.Agent.AllowsSurface(gate) {
		result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "surface", Message: fmt.Sprintf("agent %q is not allowed on the %s surface", req.Agent.Name, req.Surface)})
	}
	if strings.TrimSpace(req.CLI) != "" && strings.TrimSpace(req.Cwd) != "" {
		baseReq := req
		baseReq.Agent = nil
		baseReq.Tools = ""
		if cfg, err := c.ResolveExecutionConfig(ctx, baseReq); err != nil {
			result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "invalid_config", Message: err.Error()})
		} else {
			result.Model = cfg.Model
			result.Issues = append(result.Issues, cfg.Issues...)
		}
	}
	if info, err := os.Stat(strings.TrimSpace(req.Cwd)); err != nil {
		result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "working_folder", Message: "Working folder is unavailable: " + err.Error()})
	} else if !info.IsDir() {
		result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "working_folder", Message: "Working folder is not a directory"})
	}
	if checkAvailable && req.CLI != "" {
		if adapter, err := GetCLIAdapter(req.CLI); err != nil {
			result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "cli_unavailable", Message: err.Error()})
		} else if err := adapter.Available(ctx); err != nil {
			result.Issues = append(result.Issues, PreflightIssue{Severity: "error", Code: "cli_unavailable", Message: err.Error()})
		}
	}
	result.Issues = append(result.Issues, PreflightIssue{
		Severity: "info", Code: "managed_policy_broker",
		Message: "This run uses PrAImate's managed single-agent lifecycle; declared project, command, knowledge, and MCP tools are capability-gated and mutations require approval",
	})
	result.OK = true
	for _, issue := range result.Issues {
		if issue.Severity == "error" {
			result.OK = false
			break
		}
	}
	return result
}

func validToolLevel(level string) bool {
	switch level {
	case "", "ask", "edits", "plan", "full":
		return true
	}
	return false
}

func insecureRemoteEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func writeOpenCodeLocalRoute(cwd string, local ChatLocalEndpoint) error {
	path := filepath.Join(cwd, "opencode.json")
	cfg := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if cfg["$schema"] == nil {
		cfg["$schema"] = "https://opencode.ai/config.json"
	}
	providers, _ := cfg["provider"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
		cfg["provider"] = providers
	}
	baseURL := strings.TrimRight(ollama.NormalizeEndpoint(local.Endpoint), "/")
	if !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1"
	}
	providers["praimate-local"] = map[string]any{
		"npm":  "@ai-sdk/openai-compatible",
		"name": "PrAImate local endpoint",
		"options": map[string]any{
			"baseURL": baseURL,
			"apiKey":  "{env:OPENAI_API_KEY}",
		},
		"models": map[string]any{local.Model: map[string]any{"name": local.Model}},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeTextFile(path, raw)
}
