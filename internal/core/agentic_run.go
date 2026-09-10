package core

// Managed single-agent runtime integration. The runtime engine is a leaf
// package; this file adapts PrAImate's existing CLI, routing, privacy, and data
// root boundaries without changing the native workflow runner.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/agentic"
	"git.jtsec.local/lab/PrAImate/internal/appdata"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

type ManagedRunEvent struct {
	RunID   string         `json:"runId"`
	AgentID string         `json:"agentId"`
	Type    string         `json:"type"`
	Turn    int            `json:"turn,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	Detail  string         `json:"detail,omitempty"`
	OK      bool           `json:"ok,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

type ManagedRunRequest struct {
	TaskBudgetID   string
	FinishEvidence []agentic.EvidenceRequirement
	SkillSettings  *ChatSettings
	Surface        ExecutionSurface
	Agent          *Agent
	CLI            string
	Cwd            string
	Model          string
	Local          *ChatLocalEndpoint
	Task           string
	Instructions   string
	Env            map[string]string
	Approval       *ApprovalConfig
	ApprovalScope  string
	ResumeRunID    string
	OnEvent        func(ManagedRunEvent)
}

type managedRunSpec struct {
	TaskBudgetID   string                        `json:"task_budget_id,omitempty"`
	FinishEvidence []agentic.EvidenceRequirement `json:"finish_evidence,omitempty"`
	SkillsV2       *skills.SkillConfig           `json:"skills_v2,omitempty"`
	SkillsLock     *skills.SkillVersionLock      `json:"skills_lock,omitempty"`
	Schema         string                        `json:"schema"`
	AgentID        string                        `json:"agentId"`
	Surface        ExecutionSurface              `json:"surface"`
	CLI            string                        `json:"cli"`
	Cwd            string                        `json:"cwd"`
	Model          string                        `json:"model,omitempty"`
	Local          *ChatLocalEndpoint            `json:"local,omitempty"`
	Task           string                        `json:"task"`
	Instructions   string                        `json:"instructions,omitempty"`
}

type ManagedRun struct {
	InputBytes       int64                  `json:"inputBytes,omitempty"`
	SkillBytes       int64                  `json:"skillBytes,omitempty"`
	InputLimitBytes  int64                  `json:"inputLimitBytes,omitempty"`
	EvidenceVerified bool                   `json:"evidenceVerified,omitempty"`
	ID               string                 `json:"id"`
	AgentID          string                 `json:"agentId"`
	AgentName        string                 `json:"agentName"`
	State            string                 `json:"state"`
	Turns            int                    `json:"turns"`
	SessionID        string                 `json:"sessionId,omitempty"`
	StartedAt        string                 `json:"startedAt"`
	UpdatedAt        string                 `json:"updatedAt"`
	CompletedAt      string                 `json:"completedAt,omitempty"`
	Error            string                 `json:"error,omitempty"`
	Final            string                 `json:"final,omitempty"`
	Artifacts        []ManagedRunArtifact   `json:"artifacts"`
	Memory           []ManagedRunMemoryItem `json:"memory,omitempty"`
	CanResume        bool                   `json:"canResume,omitempty"`
}

type ManagedRunArtifact struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"createdAt"`
}

type ManagedRunMemoryItem struct {
	Kind      string `json:"kind"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"createdAt"`
}

func (c *Core) RunManagedAgent(ctx context.Context, req ManagedRunRequest) (*ManagedRun, error) {
	if req.Agent == nil {
		return nil, errors.New("managed run requires an agent")
	}
	skillSettings := ChatSettings{}
	if req.SkillSettings != nil {
		skillSettings = *req.SkillSettings
	} else {
		var err error
		skillSettings, err = freezeBoundSettings(req.Agent, nil, skillSettings)
		if err != nil {
			return nil, err
		}
		snapshot := CreateChatRequest{Settings: skillSettings}
		if err := c.snapshotChatSkills(ctx, &snapshot); err != nil {
			return nil, err
		}
		skillSettings = snapshot.Settings
	}
	if err := c.guardBoundSkills(ctx, nil, nil, skillSettings); err != nil {
		return nil, err
	}
	effectiveAgent, err := c.ResolveEffectiveAgentConfig(ctx, req.Agent)
	if err != nil {
		return nil, err
	}
	if effectiveAgent.Mode != RuntimeAgentic {
		return nil, fmt.Errorf("agent %q uses the native runtime", req.Agent.Name)
	}
	if !effectiveAgent.AgenticCompatible {
		return nil, fmt.Errorf("agent %q requires unavailable managed features: %s", req.Agent.Name, strings.Join(effectiveAgent.UnsupportedFeatures, ", "))
	}
	if err := validateManagedKnowledge(req.Agent); err != nil {
		return nil, err
	}
	if !contains(req.Agent.Supports, req.CLI) {
		return nil, fmt.Errorf("agent %q does not support CLI %q", req.Agent.ID, req.CLI)
	}
	gate := string(req.Surface)
	if req.Surface == SurfaceStudio {
		gate = "editor"
	}
	if (gate == "chat" || gate == "terminal" || gate == "editor") && !req.Agent.AllowsSurface(gate) {
		return nil, fmt.Errorf("agent %q is not allowed on the %s surface", req.Agent.Name, req.Surface)
	}
	if req.Surface == SurfaceTerminal {
		return nil, errors.New("managed Autonomous runs are unavailable in the interactive Terminal surface")
	}
	adapter, err := GetCLIAdapter(strings.TrimSpace(req.CLI))
	if err != nil {
		return nil, err
	}
	if !supportsManagedSafeMode(adapter) {
		return nil, fmt.Errorf("CLI %q cannot enforce managed safe mode and is blocked for Autonomous runs", req.CLI)
	}
	if err := adapter.Available(ctx); err != nil {
		return nil, err
	}
	// Resolve routing without passing the agent: ResolveExecutionConfig is the
	// native-run contract and intentionally rejects agentic manifests. Managed
	// tools are brokered by internal/agentic; the underlying CLI is always safe.
	execution, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{
		Surface: req.Surface, CLI: req.CLI, Cwd: req.Cwd,
		Model: req.Model, Tools: "", Local: req.Local,
		SkillSettings: &skillSettings, skillSnapshot: true,
	})
	if err != nil {
		return nil, err
	}
	if err := c.PrepareExecution(ctx, execution); err != nil {
		return nil, err
	}
	root, err := managedRunsRoot()
	if err != nil {
		return nil, err
	}
	runID := strings.TrimSpace(req.ResumeRunID)
	if runID == "" {
		runID, err = agentic.NewRunID()
		if err != nil {
			return nil, err
		}
		local := req.Local
		if local != nil {
			copyLocal := *local
			copyLocal.APIKey = ""
			local = &copyLocal
		}
		spec := managedRunSpec{
			TaskBudgetID:   skillTaskBudgetID(ctx, req.TaskBudgetID),
			FinishEvidence: req.FinishEvidence,
			SkillsV2:       skillSettings.SkillsV2, SkillsLock: skillSettings.SkillsLock,
			Schema: "praimate.managed-run/v1", AgentID: req.Agent.ID, Surface: req.Surface,
			CLI: req.CLI, Cwd: execution.Cwd, Model: req.Model, Local: local,
			Task: req.Task, Instructions: req.Instructions,
		}
		runDir := filepath.Join(root, runID)
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			return nil, err
		}
		if err := writeManagedRunSpec(filepath.Join(runDir, "request.json"), spec); err != nil {
			return nil, err
		}
	}
	c.managedMu.Lock()
	if c.managedActive[runID] {
		c.managedMu.Unlock()
		return nil, fmt.Errorf("managed run %q is already active", runID)
	}
	c.managedActive[runID] = true
	c.managedMu.Unlock()
	defer func() {
		c.managedMu.Lock()
		delete(c.managedActive, runID)
		c.managedMu.Unlock()
	}()
	model := &managedCLIModel{
		adapter: adapter, cwd: execution.Cwd, model: execution.Model,
		env: mergeStringMaps(req.Env, execution.Env),
	}
	if req.ResumeRunID != "" {
		previous, loadErr := agentic.LoadInstance(root, req.ResumeRunID)
		if loadErr != nil {
			return nil, loadErr
		}
		model.sessionID = previous.SessionID
	}
	manifest := effectiveAgent.Manifest
	limits := agentic.Limits{}
	if manifest != nil {
		limits.MaxTurns = manifest.Limits.MaxTurns
		limits.MaxContextChars = manifest.Limits.MaxContextChars
		limits.MaxOutputChars = manifest.Limits.MaxOutputChars
		limits.MaxTotalInputBytes = manifest.Limits.MaxTotalInputBytes
	}
	approval := req.Approval
	if approval == nil && c.approvalProvider != nil {
		scope := strings.TrimSpace(req.ApprovalScope)
		if scope == "" {
			scope = req.Agent.ID
		}
		approval = c.approvalProvider(scope)
	}
	var mcpServers []MCPServer
	if len(req.Agent.MCPServers) > 0 {
		if !manifest.Capabilities.ExternalServices {
			return nil, errors.New("managed MCP servers require the external_services capability")
		}
		mcpServers, err = c.resolveAgentMCPServers(ctx, req.Agent)
		if err != nil {
			return nil, err
		}
		for _, server := range mcpServers {
			if approval == nil || approval.Request == nil {
				return nil, fmt.Errorf("managed MCP %q requires GUI approval before connection", server.Name)
			}
			detail := map[string]any{"server": server.ID, "transport": server.Transport}
			if server.Transport == MCPTransportStdio {
				detail["command"] = server.Command
			} else {
				detail["url"] = server.URL
			}
			allowed, approvalErr := approval.Request(ctx, "mcp.connect."+server.ID, detail)
			if approvalErr != nil {
				return nil, approvalErr
			}
			if !allowed {
				return nil, fmt.Errorf("managed MCP connection to %q was denied", server.Name)
			}
		}
	}
	tools, err := newManagedToolBroker(ctx, req.Agent, manifest.Capabilities, execution.Cwd, approval, mcpServers)
	if err != nil {
		return nil, err
	}
	defer tools.Close()
	var toolExecutor agentic.ToolExecutor = tools
	if skillSettings.SkillsV2 != nil {
		model.skills = &managedSkillSession{core: c, settings: skillSettings}
		defer func() {
			if model.skills.runtime != nil {
				_ = model.skills.runtime.Close()
			}
		}()
		toolExecutor = &managedSkillTools{base: tools, skills: model.skills}
	}
	instructions := strings.TrimSpace(req.Instructions)
	if instructions == "" {
		instructions = AgentSystemPrompt(req.Agent)
	}
	result, runErr := agentic.Run(ctx, agentic.Config{
		ReserveInput: func(ctx context.Context, input, skill, limit int64) error {
			fallback := req.TaskBudgetID
			if fallback == "" {
				fallback = "managed:" + runID
			}
			return c.reserveSkillTaskInput(ctx, skillTaskBudgetID(ctx, fallback), input, skill, limit)
		},
		FinishEvidence: req.FinishEvidence,
		RootDir:        root, AgentID: req.Agent.ID, AgentName: req.Agent.Name,
		Instructions: instructions, Task: req.Task, Limits: limits, Model: model, Tools: toolExecutor,
		RunID: runID, ResumeRunID: req.ResumeRunID,
		OnEvent: func(ev agentic.Event) {
			if req.OnEvent != nil {
				req.OnEvent(ManagedRunEvent{
					RunID: ev.RunID, AgentID: ev.AgentID, Type: ev.Type, Turn: ev.Turn,
					Tool: ev.Tool, Detail: ev.Detail, OK: ev.OK, Payload: ev.Payload,
				})
			}
		},
	})
	if result == nil {
		return nil, runErr
	}
	managed := managedRunFromResult(result)
	return managed, runErr
}

func validateManagedKnowledge(agent *Agent) error {
	if agent == nil || agent.Knowledge == "" {
		return nil
	}
	dir, err := AgentKnowledgeDir(agent.ID)
	if err != nil {
		return err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("managed %s knowledge directory is unavailable; add knowledge in Agent Studio", agent.Knowledge)
	}
	if agent.Knowledge == "rag" {
		if info, err := os.Stat(filepath.Join(dir, "graphify-out", "graph.json")); err != nil || info.IsDir() {
			return errors.New("managed Graphify index is unavailable; build the RAG index in Agent Studio")
		}
	}
	return nil
}

func writeManagedRunSpec(path string, spec managedRunSpec) error {
	raw, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

func loadManagedRunSpec(root, runID string) (*managedRunSpec, error) {
	if filepath.Base(runID) != runID {
		return nil, errors.New("invalid run ID")
	}
	raw, err := os.ReadFile(filepath.Join(root, runID, "request.json"))
	if err != nil {
		return nil, err
	}
	var spec managedRunSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	if spec.Schema != "praimate.managed-run/v1" {
		return nil, errors.New("managed run request has an unsupported schema")
	}
	return &spec, nil
}

func (c *Core) ResumeManagedRun(ctx context.Context, runID string, onEvent func(ManagedRunEvent)) (*ManagedRun, error) {
	root, err := managedRunsRoot()
	if err != nil {
		return nil, err
	}
	spec, err := loadManagedRunSpec(root, strings.TrimSpace(runID))
	if err != nil {
		return nil, fmt.Errorf("resume managed run: %w", err)
	}
	agent, err := c.GetAgent(ctx, spec.AgentID)
	if err != nil {
		return nil, err
	}
	return c.RunManagedAgent(ctx, ManagedRunRequest{
		TaskBudgetID:   spec.TaskBudgetID,
		FinishEvidence: spec.FinishEvidence,
		SkillSettings:  &ChatSettings{SkillsV2: spec.SkillsV2, SkillsLock: spec.SkillsLock},
		Surface:        spec.Surface, Agent: agent, CLI: spec.CLI, Cwd: spec.Cwd, Model: spec.Model,
		Local: spec.Local, Task: spec.Task, Instructions: spec.Instructions,
		ApprovalScope: runID, ResumeRunID: runID, OnEvent: onEvent,
	})
}

type managedCLIModel struct {
	skills    *managedSkillSession
	adapter   CLIAdapter
	cwd       string
	model     string
	env       map[string]string
	sessionID string
}

func (m *managedCLIModel) PrepareInput(ctx context.Context, input agentic.ModelInput) (agentic.ModelInput, error) {
	if m.skills != nil {
		return m.skills.prepare(ctx, input)
	}
	return input, nil
}

func (m *managedCLIModel) Turn(ctx context.Context, input agentic.ModelInput, emit agentic.EventSink) (*agentic.ModelOutput, error) {
	stream := func(ev StreamEvent) {
		if emit == nil || ev.Type == "text" {
			return
		}
		emit(agentic.Event{Type: "model." + ev.Type, Tool: ev.Tool, Detail: ev.Detail, OK: ev.OK})
	}
	// V2 requests carry the complete measured payload each time. Starting a
	// fresh safe CLI call prevents private native history duplicating it.
	first := m.skills != nil || m.sessionID == "" || !m.adapter.SupportsResume()
	var reply *Reply
	var err error
	if streaming, ok := m.adapter.(streamingAdapter); ok {
		if first {
			reply, err = streaming.SingleShotStream(ctx, SingleShotOpts{
				Cwd: m.cwd, Message: input.Message, SystemPrompt: input.SystemPrompt,
				Model: m.model, Tools: "", Env: m.env,
			}, stream)
		} else {
			reply, err = streaming.ResumeStream(ctx, m.sessionID, ResumeOpts{
				Cwd: m.cwd, Message: input.Message, Model: m.model, Tools: "", Env: m.env,
			}, stream)
		}
		if errors.Is(err, ErrStreamUnsupported) {
			reply, err = nil, nil
		}
	}
	if reply == nil && err == nil {
		if first {
			reply, err = m.adapter.SingleShot(ctx, SingleShotOpts{
				Cwd: m.cwd, Message: input.Message, SystemPrompt: input.SystemPrompt,
				Model: m.model, Tools: "", Env: m.env,
			})
		} else {
			reply, err = m.adapter.Resume(ctx, m.sessionID, ResumeOpts{
				Cwd: m.cwd, Message: input.Message, Model: m.model, Tools: "", Env: m.env,
			})
		}
	}
	if err != nil {
		return nil, err
	}
	if reply.ExitCode != 0 {
		return nil, fmt.Errorf("%s exited with code %d: %s", m.adapter.Name(), reply.ExitCode, reply.Text)
	}
	if reply.SessionID != "" {
		m.sessionID = reply.SessionID
	}
	if m.skills != nil && m.skills.state != nil {
		m.skills.state.Status = "delivered"
		if emit != nil {
			emit(agentic.Event{Type: "skills.delivered", OK: true, Payload: map[string]any{"receipt": m.skills.state}})
		}
	}
	return &agentic.ModelOutput{Text: reply.Text, SessionID: m.sessionID}, nil
}

func managedRunsRoot() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	root = filepath.Join(root, "runs")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

func (c *Core) ListManagedRuns(agentID string) ([]ManagedRun, error) {
	root, err := managedRunsRoot()
	if err != nil {
		return nil, err
	}
	instances, err := agentic.ListInstances(root, agentID)
	if err != nil {
		return nil, err
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].StartedAt.After(instances[j].StartedAt) })
	out := make([]ManagedRun, 0, len(instances))
	for i := range instances {
		run := managedRunFromInstance(&instances[i], nil)
		run.CanResume = c.managedRunCanResume(root, instances[i])
		out = append(out, *run)
	}
	return out, nil
}

func (c *Core) GetManagedRun(runID string) (*ManagedRun, error) {
	root, err := managedRunsRoot()
	if err != nil {
		return nil, err
	}
	instance, err := agentic.LoadInstance(root, runID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, runID, "memory.json"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var memory agentic.Memory
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &memory); err != nil {
			return nil, err
		}
	}
	out := managedRunFromInstance(instance, &memory)
	out.CanResume = c.managedRunCanResume(root, *instance)
	return out, nil
}

func (c *Core) managedRunCanResume(root string, instance agentic.Instance) bool {
	if instance.State == agentic.StateCompleted {
		return false
	}
	c.managedMu.Lock()
	active := c.managedActive[instance.ID]
	c.managedMu.Unlock()
	if active {
		return false
	}
	for _, name := range []string{"request.json", "checkpoint.json", "memory.json"} {
		if info, err := os.Stat(filepath.Join(root, instance.ID, name)); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func (c *Core) ReadManagedArtifact(runID, name string) ([]byte, error) {
	root, err := managedRunsRoot()
	if err != nil {
		return nil, err
	}
	return agentic.ReadArtifact(root, runID, name)
}

func managedRunFromResult(result *agentic.Result) *ManagedRun {
	return managedRunFromInstance(result.Instance, &result.Memory)
}

func managedRunFromInstance(instance *agentic.Instance, memory *agentic.Memory) *ManagedRun {
	if instance == nil {
		return nil
	}
	out := &ManagedRun{
		InputBytes: instance.InputBytes, SkillBytes: instance.SkillBytes, InputLimitBytes: instance.InputLimitBytes, EvidenceVerified: instance.EvidenceVerified,
		ID: instance.ID, AgentID: instance.AgentID, AgentName: instance.AgentName,
		State: string(instance.State), Turns: instance.Turns, SessionID: instance.SessionID,
		StartedAt: instance.StartedAt.Format(time.RFC3339Nano), UpdatedAt: instance.UpdatedAt.Format(time.RFC3339Nano),
		Error: instance.Error, Final: instance.Final,
		Artifacts: make([]ManagedRunArtifact, 0, len(instance.Artifacts)),
	}
	if instance.CompletedAt != nil {
		out.CompletedAt = instance.CompletedAt.Format(time.RFC3339Nano)
	}
	for _, artifact := range instance.Artifacts {
		out.Artifacts = append(out.Artifacts, ManagedRunArtifact{Name: artifact.Name, Size: artifact.Size, CreatedAt: artifact.CreatedAt.Format(time.RFC3339Nano)})
	}
	if memory != nil {
		for _, item := range memory.Items {
			out.Memory = append(out.Memory, ManagedRunMemoryItem{Kind: item.Kind, Title: item.Title, Content: item.Content, Status: item.Status, CreatedAt: item.CreatedAt.Format(time.RFC3339Nano)})
		}
	}
	return out
}
